package wisp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing/fstest"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

// The example's capabilities and model are deterministic test doubles. They do
// not contact a provider, listen for real events, or produce external effects.
type syntheticSource struct{ accepted chan string }

func (syntheticSource) Definition() wisp.EventDefinition {
	return wisp.EventDefinition{Name: "example.fact", Description: "A synthetic fact"}
}

func (s syntheticSource) Run(ctx context.Context, emit wisp.Emit) error {
	id, err := emit(ctx, wisp.Event{Data: json.RawMessage(`{"value":42}`)})
	if err != nil {
		return err
	}
	s.accepted <- id
	return nil
}

type scriptedModel struct{}

func (scriptedModel) Generate(_ context.Context, request wisp.Request) (wisp.Response, error) {
	if request.Messages[len(request.Messages)-1].Role == "tool" {
		return wisp.Response{Text: "Observed the synthetic result."}, nil
	}
	return wisp.Response{ToolCalls: []wisp.ToolCall{{ID: "example-call", Name: "example_value", Arguments: json.RawMessage(`{}`)}}}, nil
}

type syntheticTool struct{}

func (syntheticTool) Definition() wisp.ToolDefinition {
	return wisp.ToolDefinition{Name: "example_value", Description: "Return a synthetic value", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
}

func (syntheticTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"value":42}`), nil
}

func ExampleRuntime() {
	source := syntheticSource{accepted: make(chan string, 1)}
	agent := wisp.Agent{
		Name: "Example", Model: "scripted",
		Instructions: []string{"base.md"},
		Events:       []wisp.EventSource{source},
		Tools:        []wisp.Tool{syntheticTool{}},
	}
	runtime, err := wisp.New(agent, wisp.Config{
		Models:       map[string]wisp.Model{"scripted": scriptedModel{}},
		Instructions: fstest.MapFS{"base.md": &fstest.MapFile{Data: []byte("Inspect the provided fact.")}},
		DatabasePath: ":memory:",
	})
	if err != nil {
		panic(err)
	}
	defer runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			panic(err)
		}
	}()
	var id string
	select {
	case id = <-source.accepted:
	case <-ctx.Done():
		panic(ctx.Err())
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		history, err := runtime.History(ctx, id)
		if err != nil {
			panic(err)
		}
		if history.Run.Status != "running" {
			fmt.Println(history.Run.Status)
			fmt.Println(history.Run.Output)
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			panic(ctx.Err())
		}
	}
	// Output:
	// completed
	// Observed the synthetic result.
}
