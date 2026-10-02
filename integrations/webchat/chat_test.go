package webchat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

type fakeModel struct{}

func (fakeModel) Generate(_ context.Context, r wisp.Request) (wisp.Response, error) {
	if r.Messages[len(r.Messages)-1].Role == "tool" {
		return wisp.Response{Text: "PRIVATE_FINAL"}, nil
	}
	var facts struct {
		Event struct {
			Data struct {
				ID   string `json:"message_id"`
				Text string `json:"text"`
			} `json:"data"`
		} `json:"event"`
	}
	if err := json.Unmarshal([]byte(r.Messages[len(r.Messages)-1].Content), &facts); err != nil {
		return wisp.Response{}, err
	}
	if facts.Event.Data.Text == "silent" {
		return wisp.Response{Text: "PRIVATE_FINAL"}, nil
	}
	args, _ := json.Marshal(map[string]string{"message_id": facts.Event.Data.ID, "text": "Reply: " + facts.Event.Data.Text})
	return wisp.Response{ToolCalls: []wisp.ToolCall{{ID: "reply", Name: "respond_to_user", Arguments: args}}}, nil
}

func TestHTTPEventToolRoundTrip(t *testing.T) {
	c := New()
	r, err := wisp.New(wisp.Agent{Name: "chat test", Model: "test", Instructions: []string{"base.md"}, Events: []wisp.EventSource{c.Messages()}, Tools: []wisp.Tool{c.Respond()}}, wisp.Config{Models: map[string]wisp.Model{"test": fakeModel{}}, Instructions: fstest.MapFS{"base.md": &fstest.MapFile{Data: []byte("Respond explicitly.")}}, DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		r.Close()
	}()
	server := httptest.NewServer(c.Handler())
	defer server.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		ready := c.emit != nil
		c.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source not ready")
		}
		time.Sleep(time.Millisecond)
	}
	for _, text := range []string{"hello", "silent"} {
		resp, err := http.Post(server.URL+"/api/chat/messages", "application/json", strings.NewReader(`{"text":"`+text+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		var accepted Message
		err = json.NewDecoder(resp.Body).Decode(&accepted)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 202 {
			t.Fatalf("acceptance: %d %v", resp.StatusCode, err)
		}
		for {
			h, err := r.History(ctx, accepted.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if h.Run.Status != "running" {
				if h.Run.Status != "completed" || h.Run.Output != "PRIVATE_FINAL" {
					t.Fatal(h.Run)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("run did not finish")
			}
			time.Sleep(time.Millisecond)
		}
	}
	resp, err := http.Get(server.URL + "/api/chat")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Reply: hello") || strings.Contains(string(body), "PRIVATE_FINAL") {
		t.Fatalf("implicit reply or missing reply: %s", body)
	}
	c.mu.Lock()
	id := c.messages[0].ID
	c.mu.Unlock()
	args, _ := json.Marshal(map[string]string{"message_id": id, "text": "duplicate"})
	if _, err := c.Respond().Execute(ctx, args); err == nil {
		t.Fatal("duplicate reply accepted")
	}
	runs, err := r.ListRuns(ctx, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("one event per message: %v %d", err, len(runs))
	}
}

func TestValidationAndUnavailableSource(t *testing.T) {
	c := New()
	handler := c.Handler()
	for _, tc := range []struct {
		body, content, origin string
		status                int
	}{
		{`{"text":"hi"}`, "application/json", "", 503},
		{`{"text":"hi"}`, "text/plain", "", 415},
		{`{"text":"hi"}`, "application/json", "https://evil.example", 403},
		{`{"text":""}`, "application/json", "", 400},
		{`{"text":"hi","policy":"bad"}`, "application/json", "", 400},
		{`{"text":"hi"} {}`, "application/json", "", 400},
	} {
		req := httptest.NewRequest("POST", "http://127.0.0.1/api/chat/messages", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.content)
		req.Header.Set("Origin", tc.origin)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Fatalf("got %d want %d", out.Code, tc.status)
		}
	}
	if _, err := c.Respond().Execute(context.Background(), json.RawMessage(`{"message_id":"unknown","text":"hi"}`)); err == nil {
		t.Fatal("unknown target accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Respond().Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("cancellation ignored")
	}
}
