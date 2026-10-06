// Package openaicompat adapts OpenAI-compatible Chat Completions APIs to wisp.Model.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

const defaultBaseURL = "https://api.openai.com/v1"
const maxResponseBytes = 8 << 20

// Config is deployment configuration, kept outside the agent declaration.
// BaseURL defaults to OpenAI's API base URL and may instead name a compatible
// endpoint such as http://127.0.0.1:8000/v1. APIKey is optional for local
// servers; when empty, no Authorization header is sent.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// MaxTokens, when non-zero, is sent as the portable Chat Completions
	// max_tokens request field. It must not be negative.
	MaxTokens int
	// Client defaults to a client with a 90-second timeout. It is copied and
	// redirects are disabled so credentials cannot be forwarded elsewhere.
	Client *http.Client
}

// Model implements non-streaming text and function-tool turns. It is safe for
// concurrent runs and has no provider-side conversation state or retries.
type Model struct {
	key       string
	name      string
	maxTokens int
	client    *http.Client
	endpoint  string
}

var _ wisp.Model = (*Model)(nil)

// New creates a Chat Completions adapter.
func New(cfg Config) (*Model, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("openaicompat: model ID is required")
	}
	if cfg.MaxTokens < 0 {
		return nil, errors.New("openaicompat: max tokens cannot be negative")
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("openaicompat: base URL must be an absolute HTTP(S) API base without credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	client := http.Client{Timeout: 90 * time.Second}
	if cfg.Client != nil {
		client = *cfg.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Model{key: cfg.APIKey, name: cfg.Model, maxTokens: cfg.MaxTokens, client: &client, endpoint: u.String()}, nil
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type tool struct {
	Type     string              `json:"type"`
	Function wisp.ToolDefinition `json:"function"`
}

func pointer(s string) *string { return &s }

// Generate makes one Chat Completions request.
func (m *Model) Generate(ctx context.Context, req wisp.Request) (wisp.Response, error) {
	input := struct {
		Model     string    `json:"model"`
		Messages  []message `json:"messages"`
		Tools     []tool    `json:"tools,omitempty"`
		MaxTokens *int      `json:"max_tokens,omitempty"`
	}{Model: m.name, Messages: make([]message, 0, len(req.Messages))}
	if m.maxTokens != 0 {
		input.MaxTokens = &m.maxTokens
	}
	for _, msg := range req.Messages {
		converted := message{Role: msg.Role, ToolCallID: msg.ToolCallID}
		// OpenAI permits an assistant tool-call message to use null content.
		// Other Wisp messages preserve their text, including an empty string.
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 || msg.Content != "" {
			converted.Content = pointer(msg.Content)
		}
		for _, call := range msg.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || !json.Valid(call.Arguments) {
				return wisp.Response{}, errors.New("openaicompat: invalid tool call in transcript")
			}
			converted.ToolCalls = append(converted.ToolCalls, toolCall{ID: call.ID, Type: "function", Function: functionCall{Name: call.Name, Arguments: string(call.Arguments)}})
		}
		input.Messages = append(input.Messages, converted)
	}
	for _, definition := range req.Tools {
		input.Tools = append(input.Tools, tool{Type: "function", Function: definition})
	}
	body, err := json.Marshal(input)
	if err != nil {
		return wisp.Response{}, fmt.Errorf("openaicompat: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return wisp.Response{}, fmt.Errorf("openaicompat: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if m.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+m.key)
	}
	resp, err := m.client.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return wisp.Response{}, fmt.Errorf("openaicompat: request: %w", err)
		}
		return wisp.Response{}, m.errorf("request: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return wisp.Response{}, fmt.Errorf("openaicompat: read response: %w", err)
		}
		return wisp.Response{}, m.errorf("read response: %v", err)
	}
	if len(data) > maxResponseBytes {
		return wisp.Response{}, errors.New("openaicompat: response exceeds 8 MiB")
	}
	return m.decodeResponse(resp.StatusCode, data)
}

func (m *Model) decodeResponse(status int, data []byte) (wisp.Response, error) {
	var result struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Message      message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	decodeErr := json.Unmarshal(data, &result)
	if status < 200 || status >= 300 || result.Error != nil {
		detail := http.StatusText(status)
		if result.Error != nil && result.Error.Message != "" {
			detail = result.Error.Message
		}
		if decodeErr != nil && (detail == "" || detail == http.StatusText(status)) {
			detail = "invalid error response"
		}
		return wisp.Response{}, m.errorf("HTTP %d: %s", status, detail)
	}
	if decodeErr != nil {
		return wisp.Response{}, fmt.Errorf("openaicompat: decode response: %w", decodeErr)
	}
	if len(result.Choices) != 1 {
		return wisp.Response{}, errors.New("openaicompat: expected exactly one response choice")
	}
	choice := result.Choices[0]
	if choice.Message.Role != "assistant" {
		return wisp.Response{}, errors.New("openaicompat: expected an assistant response")
	}
	if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		return wisp.Response{}, m.errorf("incomplete response (finish_reason=%q)", choice.FinishReason)
	}
	output := wisp.Response{}
	if choice.Message.Content != nil {
		output.Text = *choice.Message.Content
	}
	seen := make(map[string]bool)
	for _, call := range choice.Message.ToolCalls {
		var args map[string]json.RawMessage
		if call.Type != "function" || strings.TrimSpace(call.ID) == "" || seen[call.ID] || strings.TrimSpace(call.Function.Name) == "" || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
			return wisp.Response{}, errors.New("openaicompat: malformed function call")
		}
		seen[call.ID] = true
		output.ToolCalls = append(output.ToolCalls, wisp.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments)})
	}
	if choice.FinishReason == "tool_calls" && len(output.ToolCalls) == 0 {
		return wisp.Response{}, errors.New("openaicompat: tool_calls finish reason without calls")
	}
	if choice.FinishReason == "stop" && len(output.ToolCalls) != 0 {
		return wisp.Response{}, errors.New("openaicompat: stop finish reason with function calls")
	}
	return output, nil
}

func (m *Model) errorf(format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	if m.key != "" {
		detail = strings.ReplaceAll(detail, m.key, "[redacted]")
	}
	if len(detail) > 1024 {
		detail = detail[:1024] + "…"
	}
	return errors.New("openaicompat: " + detail)
}
