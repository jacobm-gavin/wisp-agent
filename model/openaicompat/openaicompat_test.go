package openaicompat

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

func TestOpenAIWireProtocolAndTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authorization")
		}
		var input struct {
			Model     string    `json:"model"`
			Messages  []message `json:"messages"`
			Tools     []tool    `json:"tools"`
			MaxTokens *int      `json:"max_tokens"`
			Reasoning any       `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Model != "gpt-test" || input.MaxTokens == nil || *input.MaxTokens != 123 || len(input.Tools) != 1 || input.Reasoning != nil {
			t.Errorf("wrong request: %+v", input)
		}
		if input.Messages[1].Content != nil || input.Messages[1].ToolCalls[0].Function.Arguments != `{"x":1}` || input.Messages[2].ToolCallID != "first" || *input.Messages[2].Content != `{"value":42}` {
			t.Errorf("tool continuation was not encoded correctly: %+v", input.Messages)
		}
		io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"fixture","arguments":"{\"x\":2}"}},{"id":"b","type":"function","function":{"name":"fixture","arguments":"{\"x\":3}"}}]}}]}`)
	}))
	defer server.Close()
	m, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "gpt-test", MaxTokens: 123})
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Generate(context.Background(), wisp.Request{
		Messages: []wisp.Message{{Role: "user", Content: "test"}, {Role: "assistant", ToolCalls: []wisp.ToolCall{{ID: "first", Name: "fixture", Arguments: json.RawMessage(`{"x":1}`)}}}, {Role: "tool", ToolCallID: "first", Content: `{"value":42}`}},
		Tools:    []wisp.ToolDefinition{{Name: "fixture", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ToolCalls) != 2 || out.ToolCalls[0].ID != "a" || string(out.ToolCalls[1].Arguments) != `{"x":3}` {
		t.Fatalf("calls = %+v", out.ToolCalls)
	}
}

func TestVLLMStyleCompletionWithoutAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("authorization = %q", got)
		}
		io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer server.Close()
	m, err := New(Config{BaseURL: server.URL + "/v1/", Model: "local-model"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Generate(context.Background(), wisp.Request{Messages: []wisp.Message{{Role: "user", Content: "hello"}}})
	if err != nil || out.Text != "done" {
		t.Fatalf("Generate = %#v, %v", out, err)
	}
}

func TestRejectsMalformedAndIncompleteResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"http error", `{"error":{"message":"bad test-key"}}`, "HTTP 429", 429},
		{"bad JSON", `{`, "decode", 200},
		{"no choices", `{"choices":[]}`, "one response", 200},
		{"truncated", `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`, "incomplete", 200},
		{"missing calls", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant"}}]}`, "without calls", 200},
		{"duplicate calls", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"x","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`, "malformed", 200},
		{"stop with calls", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`, "stop finish", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			m, err := New(Config{BaseURL: server.URL, APIKey: "test-key", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Generate(context.Background(), wisp.Request{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "test-key") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCancellationAndRedirectSafety(t *testing.T) {
	m, err := New(Config{APIKey: "test-key", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Generate(ctx, wisp.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	m, err = New(Config{BaseURL: server.URL, APIKey: "test-key", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(context.Background(), wisp.Request{}); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, cfg := range []Config{{}, {Model: "m", MaxTokens: -1}, {Model: "m", BaseURL: "relative"}, {Model: "m", BaseURL: "https://key@example.com/v1"}} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("New(%+v) succeeded", cfg)
		}
	}
	if _, err := New(Config{Model: "m", BaseURL: "http://127.0.0.1:8000/v1"}); err != nil {
		t.Fatal(err)
	}
}
