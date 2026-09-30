package wisp

import (
	"context"
	"encoding/json"
	"io/fs"
	"time"
)

// Agent declares the complete capability surface of one agent.
type Agent struct {
	Name         string
	Model        string
	Instructions []string
	Events       []EventSource
	Tools        []Tool
}

// EventDefinition describes one distinct way the world can wake an agent.
type EventDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Event contains facts, not behavioral policy. The runtime assigns its source
// from the emitting EventSource; Data must contain valid JSON.
type Event struct {
	Data      json.RawMessage `json:"data"`
	Timestamp time.Time       `json:"timestamp"`
}

// Emit returns after the event and its run have been durably accepted. An error
// means acceptance failed. Repeated calls are distinct events, even with equal
// payloads; Wisp does not promise exactly-once external delivery.
// It is safe to call concurrently, but Data must not be mutated until Emit
// returns. Calls may block for capacity; source code must handle their errors.
type Emit func(context.Context, Event) (string, error)

// EventSource emits one declared kind of event. Run must honor cancellation and
// stop its background work before returning. Returning nil ends only this source.
// Its context and Emit callback expire when Run returns. Already accepted runs
// belong to the runtime lifetime and continue independently of the source.
type EventSource interface {
	Definition() EventDefinition
	Run(context.Context, Emit) error
}

// ToolDefinition describes a single capability and its JSON object arguments.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Tool implementations must honor cancellation and support concurrent calls (or
// synchronize internally). The runtime provides no resource isolation. Execute
// owns argument validation, including any constraints in Parameters.
type Tool interface {
	Definition() ToolDefinition
	Execute(context.Context, json.RawMessage) (json.RawMessage, error)
}

// Model implementations may be called concurrently by independent runs.
// Generate must honor cancellation. Provider-specific encoding belongs here.
type Model interface {
	Generate(context.Context, Request) (Response, error)
}

type Request struct {
	Messages []Message        `json:"messages"`
	Tools    []ToolDefinition `json:"tools"`
}

// Message is the provider-neutral transcript within one run. Tool messages
// reference the originating call ID. Event facts use the user role.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Response struct {
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Config contains runtime mechanics, separate from the capability declaration.
type Config struct {
	Models map[string]Model
	// MaxConcurrentRuns defaults to 16. Emit waits for capacity before durable
	// acceptance; cancellation while waiting accepts nothing. Negative is invalid.
	MaxConcurrentRuns int
	// MaxModelTurns defaults to 64. Exceeding it fails the run without another
	// model request. Negative is invalid; this is a bound, not a retry policy.
	MaxModelTurns int
	// RunTimeout optionally bounds each accepted run. Zero uses only the runtime
	// lifetime; implementations must cooperate with context cancellation.
	RunTimeout time.Duration
	// Instructions supplies the declared Markdown files. Files are snapshotted in
	// declaration order at startup; changing them requires a new runtime.
	Instructions fs.FS
	// DatabasePath is a SQLite file exclusively owned by this runtime. Use
	// ":memory:" for ephemeral tests. Multiple processes must not share the file.
	DatabasePath string
}

// Description is derived from the declaration, without executable objects.
type Description struct {
	Name         string            `json:"name"`
	Model        string            `json:"model"`
	Instructions []string          `json:"instructions"`
	Events       []EventDefinition `json:"events"`
	Tools        []ToolDefinition  `json:"tools"`
}
