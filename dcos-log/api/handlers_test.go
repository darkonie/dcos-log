package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-systemd/journal"
	"github.com/dcos/dcos-log/dcos-log/config"
)

func TestGetCursor(t *testing.T) {
	req, err := http.NewRequest("GET", "/?cursor=s%3Dcea8150abb0543deaab113ed2f39b014%3Bi%3D1%3Bb%3D2c357020b6e54863a5ac9dee71d5872c%3Bm%3D33ae8a1%3Bt%3D53e52ec99a798%3Bx%3Db3fe26128f768a49", nil)
	if err != nil {
		t.Fatal(err)
	}

	c, err := getCursor(req)
	if err != nil {
		t.Fatal(err)
	}

	if c != "s=cea8150abb0543deaab113ed2f39b014;i=1;b=2c357020b6e54863a5ac9dee71d5872c;m=33ae8a1;t=53e52ec99a798;x=b3fe26128f768a49" {
		t.Fatalf("Expecting cursor 123. Got: %s", c)
	}
}

func TestGetLimit(t *testing.T) {
	limits := []struct {
		uri     string
		expect  uint64
		stream  bool
		errorOk bool
	}{
		{
			uri:    "/?limit=10",
			expect: 10,
		},
		{
			uri:     "/?limit=-10",
			errorOk: true,
		},
		{
			uri: "?limit=0",
		},
	}

	for _, limit := range limits {
		r, err := http.NewRequest("GET", limit.uri, nil)
		if err != nil {
			t.Fatal(err)
		}
		l, err := getLimit(r, limit.stream)
		if limit.errorOk {
			if err == nil {
				t.Fatalf("Expecting error on input %s but no errors", limit.uri)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}

		if l != limit.expect {
			t.Fatalf("Expecting %d. Got %d", limit.expect, l)
		}
	}
}

func TestGetSkip(t *testing.T) {
	skipValues := []struct {
		uri                string
		skipNext, skipPrev uint64
		errorOk            bool
	}{
		{
			uri:      "/?skip_next=10",
			skipNext: 10,
		},
		{
			uri:      "/?skip_prev=10",
			skipPrev: 10,
		},
		{
			uri: "/?skip_next=0",
		},
		{
			uri: "/?skip_prev=0",
		},
		{
			// max uint64 + 1
			uri:     "/?skip_next=18446744073709551616",
			errorOk: true,
		},
		{
			// max uint64 + 1
			uri:     "/?skip_prev=18446744073709551616",
			errorOk: true,
		},
	}

	for _, skip := range skipValues {
		r, err := http.NewRequest("GET", skip.uri, nil)
		if err != nil {
			t.Fatal(err)
		}

		skipNext, skipPrev, err := getSkip(r)
		if skip.errorOk {
			if err == nil {
				t.Fatalf("Expecting error on input %s but no errors", skip.uri)
			}
			continue
		}

		if err != nil {
			t.Fatal(err)
		}

		if skipNext != skip.skipNext {
			t.Fatalf("Expecting skipNext %d. Got %d", skip.skipNext, skipNext)
		}

		if skipPrev != skip.skipPrev {
			t.Fatalf("Expecting skipPrev %d. Got %d", skip.skipPrev, skipPrev)
		}
	}
}

func TestGetMatches(t *testing.T) {
	r, err := http.NewRequest("GET", "?filter=hello:world&filter=foo:bar", nil)
	if err != nil {
		t.Fatal(err)
	}

	matches, err := getMatches(r)
	if err != nil {
		t.Fatal(err)
	}

	if len(matches) != 2 {
		t.Fatalf("Must have 2 matches got %d", len(matches))
	}

	if matches[0].Field != "HELLO" || matches[0].Value != "world" {
		t.Fatalf("Expecting HELLO=world match. Got %+v", matches[0])
	}

	if matches[1].Field != "FOO" || matches[1].Value != "bar" {
		t.Fatalf("Expecting FOO=bar match. Got %+v", matches[1])
	}
}

func TestRangeServerTextHandler(t *testing.T) {
	w, err := newRequest("/range/?skip_prev=10", map[string]string{"Accept": "text/plain"}, "GET", "master")
	if err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("response code must be 200. Got %d", w.Code)
	}

	scanner := bufio.NewScanner(w.Body)
	var cnt int
	for scanner.Scan() {
		cnt++
	}

	if cnt != 10 {
		t.Fatalf("Expecting 10 last entries. Got %d", cnt)
	}
}

func TestRangeServerJSONHandler(t *testing.T) {
	w, err := newRequest("/range/?limit=10", map[string]string{"Accept": "application/json"}, "GET", "master")
	if err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("response code must be 200. Got %d", w.Code)
	}

	scanner := bufio.NewScanner(w.Body)
	for scanner.Scan() {
		var logJSON map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &logJSON); err != nil {
			t.Fatal(err)
		}

		if len(logJSON) == 0 {
			t.Fatalf("Expect at least one field. Got %d", len(logJSON))
		}
	}
}

func TestRangeServerSSEHandler(t *testing.T) {
	w, err := newRequest("/range/?limit=10", map[string]string{"Accept": "text/event-stream"}, "GET", "master")
	if err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("response code must be 200. Got %d", w.Code)
	}

	scanner := bufio.NewScanner(w.Body)
	for scanner.Scan() {
		text := scanner.Text()
		// skip \n
		if text == "" || strings.HasPrefix(text, "id:") {
			continue
		}

		if !strings.HasPrefix(text, "data: ") {
			t.Fatalf("Entry must start with `data:`. Got %s", text)
		}

		var logJSON map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "data: ")), &logJSON); err != nil {
			t.Fatal(err)
		}

		if len(logJSON) == 0 {
			t.Fatalf("Expect len fields > 0. Log %s", text)
		}
	}
}

func TestFieldsHandler(t *testing.T) {
	value := fmt.Sprintf("%d", time.Now().UnixNano())
	err := journal.Send("TEST CONTAINER_ID", journal.PriInfo, map[string]string{"CONTAINER_ID": value})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond * 50)

	w, err := newRequest("/fields/CONTAINER_ID", map[string]string{"Accept": "application/json"}, "GET", "master")
	if err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("response code must be 200. Got %d", w.Code)
	}

	if header := w.Header().Get("Content-Type"); header != "application/json" {
		t.Fatalf("Expect Content-Type: application/json. Got %s", header)
	}

	var response []string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}

	if len(response) == 0 {
		t.Fatal("Expect CONTAINER_ID field")
	}

	if !contains(response, value) {
		t.Fatalf("Expect CONTAINER_ID = %s.Got %s", value, response)
	}
}

func TestFieldNotAllowed(t *testing.T) {
	w, err := newRequest("/fields/MESSAGE", map[string]string{"Accept": "application/json"}, "GET", "master")
	if err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expect return code %d. Got %d", http.StatusBadRequest, w.Code)
	}
}

func TestContainerLogs(t *testing.T) {
	containerID := fmt.Sprintf("%d", time.Now().UnixNano())
	frameworkID := fmt.Sprintf("%d", time.Now().UnixNano())
	executorID := fmt.Sprintf("%d", time.Now().UnixNano())

	fields := map[string]string{
		"CONTAINER_ID": containerID,
		"FRAMEWORK_ID": frameworkID,
		"EXECUTOR_ID":  executorID,
	}

	err := journal.Send("TEST Task request", journal.PriInfo, fields)
	if err != nil {
		t.Fatal(err)
	}

	url := fmt.Sprintf("/range/framework/%s/executor/%s/container/%s", frameworkID, executorID, containerID)
	w, err := newRequest(url, map[string]string{"Accept": "application/json"}, "GET", "master")
	if err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("response code must be 200. Got %d", w.Code)
	}

	var r struct {
		Fields map[string]interface{} `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}

	if len(r.Fields) == 0 {
		t.Fatal("Response not received")
	}

	fID, ok := r.Fields["FRAMEWORK_ID"]
	if !ok {
		t.Fatalf("Expect `FRAMEWORK_ID`. Got %+v", r.Fields)
	}

	if fID != frameworkID {
		t.Fatalf("Expect %s. Got %s", frameworkID, fID)
	}

	eID, ok := r.Fields["EXECUTOR_ID"]
	if !ok {
		t.Fatalf("Expect `EXECUTOR_ID`. Got %+v", r.Fields)
	}

	if eID != executorID {
		t.Fatalf("Expect %s. Got %s", executorID, eID)
	}

	cID, ok := r.Fields["CONTAINER_ID"]
	if !ok {
		t.Fatalf("Expect `CONTAINER_ID`. Got %+v", r.Fields)
	}

	if cID != containerID {
		t.Fatalf("Expect %s. Got %s", containerID, cID)
	}
}

func newRequest(path string, headers map[string]string, method, role string) (*httptest.ResponseRecorder, error) {
	w := &httptest.ResponseRecorder{}

	cfg, err := config.NewConfig([]string{"dcos-log", "-role", role})
	if err != nil {
		return nil, err
	}

	r, err := newAPIRouter(cfg, nil, nil)
	if err != nil {
		return w, err
	}

	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		return w, err
	}

	for k, v := range headers {
		req.Header.Add(k, v)
	}

	w = httptest.NewRecorder()

	r.ServeHTTP(w, req)
	return w, nil
}

func contains(s []string, v string) bool {
	for _, entry := range s {
		if entry == v {
			return true
		}
	}
	return false
}

func TestDownloadHandler(t *testing.T) {
	w, err := newRequest("/range/download", map[string]string{"Accept": "application/json"}, "GET", "master")
	if err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("Expect return code 200. Got %d", w.Code)
	}

	if h := w.Header().Get("Content-disposition"); h == "" {
		t.Fatalf("Expect header `Content-disposition`")
	}
}
