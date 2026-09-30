package wisp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInspectionHTTPAndSSE(t *testing.T) {
	s := source("facts")
	r := newRuntime(t, declaration(s), modelFunc(func(context.Context, Request) (Response, error) {
		return Response{Text: "<script>private</script>"}, nil
	}), ":memory:")
	launch(t, r)
	emit := receive(t, s.ready)
	server := httptest.NewServer(r.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	for _, tc := range []struct {
		path, contains string
		code           int
	}{
		{"/", "Active and recent runs", 200}, {"/app.js", "textContent", 200}, {"/style.css", "@media", 200},
		{"/api/active-runs", "[]", 200},
		{"/api/agent", `"name":"Test"`, 200}, {"/api/runs", "[]", 200}, {"/api/runs/missing", "404", 404}, {"/missing", "404", 404},
	} {
		resp, err := client.Get(server.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != tc.code || !strings.Contains(string(body), tc.contains) {
			t.Fatalf("%s: status %d, %s, %v", tc.path, resp.StatusCode, body, err)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("inspection may be cached")
		}
	}
	resp, err := client.Post(server.URL+"/api/runs", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatal("inspection endpoint accepted mutation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/stream", nil)
	stream, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("missing SSE content type")
	}
	scanner := bufio.NewScanner(stream.Body)
	nextRevision := func() int64 {
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				var v struct {
					Sequence int64 `json:"sequence"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &v); err != nil {
					t.Fatal(err)
				}
				return v.Sequence
			}
		}
		t.Fatalf("SSE ended: %v", scanner.Err())
		return -1
	}
	if revision := nextRevision(); revision != 0 {
		t.Fatalf("unexpected initial revision: %d", revision)
	}
	id := emitEvent(t, emit, `{"fact":true}`)
	h := terminal(t, r, id)
	if revision := nextRevision(); revision <= 0 {
		t.Fatal("SSE did not announce new history")
	}
	resp, err = client.Get(server.URL + "/api/runs/" + id)
	if err != nil {
		t.Fatal(err)
	}
	var actual History
	err = json.NewDecoder(resp.Body).Decode(&actual)
	resp.Body.Close()
	if err != nil || actual.Run.Output != h.Run.Output || len(actual.Activity) != 4 {
		t.Fatalf("incomplete API history: %+v %v", actual, err)
	}
	// Reconnection receives the current persisted revision without relying on
	// an in-process event buffer or Last-Event-ID replay.
	reconnect, err := client.Get(server.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	scanner = bufio.NewScanner(reconnect.Body)
	if revision := nextRevision(); revision != h.Activity[len(h.Activity)-1].Sequence {
		t.Fatal("reconnect lost persisted revision")
	}
	reconnect.Body.Close()
}

func TestHistoryErrors(t *testing.T) {
	r := newRuntime(t, declaration(), modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil }), ":memory:")
	if _, err := r.History(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}
	for _, limit := range []int{0, 1001} {
		if _, err := r.ListRuns(context.Background(), limit); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal("close not idempotent")
	}
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/runs", nil))
	if w.Code != 500 {
		t.Fatal("database failure hidden")
	}
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("closed runtime started")
	}
}
