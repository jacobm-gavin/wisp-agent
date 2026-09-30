// Package openrouter adapts OpenRouter chat completions to wisp.Model.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

const endpoint = "https://openrouter.ai/api/v1/chat/completions"

// Config is deployment configuration, kept outside the agent declaration.
type Config struct {
	APIKey string
	Model  string
	// MaxTokens defaults to 4096. Truncated responses fail rather than appearing
	// to be successful run completions. Reasoning is disabled for this adapter.
	MaxTokens int
	// Client defaults to an HTTP client with a 90-second timeout. It is copied;
	// redirects are disabled to keep authentication at the configured endpoint.
	Client *http.Client
}

// Model implements text and function-tool turns, without streaming or retries.
// It is safe for concurrent runs. Credentials are not part of Wisp history.
type Model struct {
	key       string
	name      string
	maxTokens int
	client    *http.Client
	endpoint  string
}

var _ wisp.Model = (*Model)(nil)

func New(cfg Config) (*Model, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("openrouter: API key is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("openrouter: model ID is required")
	}
	if cfg.MaxTokens < 0 {
		return nil, errors.New("openrouter: max tokens must be positive")
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 4096
	}
	client := http.Client{Timeout: 90 * time.Second}
	if cfg.Client != nil {
		client = *cfg.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Model{key: cfg.APIKey, name: cfg.Model, maxTokens: cfg.MaxTokens, client: &client, endpoint: endpoint}, nil
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
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type tool struct {
	Type     string              `json:"type"`
	Function wisp.ToolDefinition `json:"function"`
}

// Generate makes one request. Reasoning is disabled so tool continuation needs
// only the public transcript, without hidden provider-specific reasoning state.
func (m *Model) Generate(ctx context.Context, req wisp.Request) (wisp.Response, error) {
	input := struct {
		Model     string    `json:"model"`
		Messages  []message `json:"messages"`
		Tools     []tool    `json:"tools,omitempty"`
		MaxTokens int       `json:"max_tokens"`
		Reasoning struct {
			Enabled bool `json:"enabled"`
		} `json:"reasoning"`
	}{Model: m.name, MaxTokens: m.maxTokens, Messages: make([]message, 0, len(req.Messages))}
	for _, msg := range req.Messages {
		converted := message{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID}
		for _, call := range msg.ToolCalls {
			if !json.Valid(call.Arguments) {
				return wisp.Response{}, errors.New("openrouter: invalid tool arguments in transcript")
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
		return wisp.Response{}, fmt.Errorf("openrouter: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return wisp.Response{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+m.key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(httpReq)
	if err != nil {
		return wisp.Response{}, fmt.Errorf("openrouter: request: %w", err)
	}
	defer resp.Body.Close()
	const limit = 8 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return wisp.Response{}, fmt.Errorf("openrouter: read response: %w", err)
	}
	if len(data) > limit {
		return wisp.Response{}, errors.New("openrouter: response exceeds 8 MiB")
	}
	var result struct {
		Error *struct {
			Message  string `json:"message"`
			Metadata struct {
				Provider string          `json:"provider_name"`
				Raw      json.RawMessage `json:"raw"`
			} `json:"metadata"`
		} `json:"error"`
		Choices []struct {
			Message      message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	decodeErr := json.Unmarshal(data, &result)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || result.Error != nil {
		detail := http.StatusText(resp.StatusCode)
		if result.Error != nil {
			detail = result.Error.Message
			if result.Error.Metadata.Provider != "" {
				detail += " (" + result.Error.Metadata.Provider + ")"
			}
			if raw := result.Error.Metadata.Raw; len(raw) > 0 && string(raw) != "null" {
				var upstream string
				if json.Unmarshal(raw, &upstream) != nil {
					upstream = string(raw)
				}
				detail += ": " + upstream
			}
		}
		detail = strings.ReplaceAll(detail, m.key, "[redacted]")
		if len(detail) > 1024 {
			detail = detail[:1024] + "…"
		}
		return wisp.Response{}, fmt.Errorf("openrouter: HTTP %d: %s", resp.StatusCode, detail)
	}
	if decodeErr != nil {
		return wisp.Response{}, fmt.Errorf("openrouter: decode response: %w", decodeErr)
	}
	if len(result.Choices) != 1 {
		return wisp.Response{}, errors.New("openrouter: expected exactly one response choice")
	}
	choice := result.Choices[0]
	if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		return wisp.Response{}, fmt.Errorf("openrouter: incomplete response (finish_reason=%q)", choice.FinishReason)
	}
	if choice.Message.Role != "assistant" {
		return wisp.Response{}, errors.New("openrouter: expected an assistant response")
	}
	output := wisp.Response{Text: choice.Message.Content}
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
			return wisp.Response{}, errors.New("openrouter: malformed function call")
		}
		output.ToolCalls = append(output.ToolCalls, wisp.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments)})
	}
	if choice.FinishReason == "tool_calls" && len(output.ToolCalls) == 0 {
		return wisp.Response{}, errors.New("openrouter: tool_calls finish reason without calls")
	}
	return output, nil
}
