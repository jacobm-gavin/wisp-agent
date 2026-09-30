package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

func TestWireProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("wrong method or authentication")
		}
		var input struct {
			Model     string    `json:"model"`
			Messages  []message `json:"messages"`
			Tools     []tool    `json:"tools"`
			Reasoning struct {
				Enabled bool `json:"enabled"`
			} `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Model != "test-model" || len(input.Tools) != 1 || input.Tools[0].Type != "function" || input.Reasoning.Enabled {
			t.Error("wrong model/tool configuration")
		}
		if input.Messages[1].ToolCalls[0].Function.Arguments != `{"x":1}` || input.Messages[2].ToolCallID != "first" {
			t.Error("tool transcript lost protocol association")
		}
		io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"fixture","arguments":"{\"x\":2}"}},{"id":"b","type":"function","function":{"name":"fixture","arguments":"{\"x\":3}"}}]}}]}`)
	}))
	defer server.Close()
	m, _ := New(Config{APIKey: "test-key", Model: "test-model"})
	m.endpoint = server.URL
	response, err := m.Generate(context.Background(), wisp.Request{
		Messages: []wisp.Message{{Role: "user", Content: "test"}, {Role: "assistant", ToolCalls: []wisp.ToolCall{{ID: "first", Name: "fixture", Arguments: json.RawMessage(`{"x":1}`)}}}, {Role: "tool", ToolCallID: "first", Content: `{"value":42}`}},
		Tools:    []wisp.ToolDefinition{{Name: "fixture", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 2 || response.ToolCalls[1].ID != "b" || string(response.ToolCalls[0].Arguments) != `{"x":2}` {
		t.Fatalf("wrong calls: %+v", response)
	}
}

func TestErrorsAndCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"completion", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`, "", 200},
		{"rate limit", `{"error":{"message":"rate limited"}}`, "429", 429},
		{"redaction", `{"error":{"message":"test-key denied"}}`, "[redacted]", 401},
		{"provider detail", `{"error":{"message":"Provider returned error","metadata":{"provider_name":"fixture-provider","raw":"upstream rate limited test-key"}}}`, "fixture-provider", 429},
		{"inline error", `{"error":{"message":"provider failed"}}`, "provider failed", 200},
		{"not JSON", `<html>unavailable</html>`, "503", 503},
		{"bad JSON", `{`, "decode", 200},
		{"empty", `{"choices":[]}`, "one response", 200},
		{"truncated", `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`, "incomplete", 200},
		{"missing calls", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null}}]}`, "without calls", 200},
		{"bad args", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"fixture","arguments":"bad"}}]}}]}`, "malformed", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			m, _ := New(Config{APIKey: "test-key", Model: "test"})
			m.endpoint = server.URL
			out, err := m.Generate(context.Background(), wisp.Request{Messages: []wisp.Message{{Role: "user", Content: "test"}}})
			if tc.want == "" {
				if err != nil || out.Text != "done" {
					t.Fatalf("completion: %+v %v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "test-key") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestCancellationAndRedirect(t *testing.T) {
	m, _ := New(Config{APIKey: "test-key", Model: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Generate(ctx, wisp.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed authenticated redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	m.endpoint = server.URL
	if _, err := m.Generate(context.Background(), wisp.Request{}); err == nil {
		t.Fatal("redirect accepted")
	}
}
