package openrouter_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
	"github.com/jacobm-gavin/wisp-agent/model/openrouter"
)

const liveModel = "qwen/qwen3.8-27b"

// This is deliberately opt-in: ordinary tests never send data to a provider.
func TestOpenRouterLive(t *testing.T) {
	if os.Getenv("WISP_OPENROUTER_LIVE") != "1" {
		t.Skip("set WISP_OPENROUTER_LIVE=1 to make live OpenRouter requests")
	}
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Fatal("OPENROUTER_API_KEY is required")
	}
	model, err := openrouter.New(openrouter.Config{APIKey: key, Model: liveModel, MaxTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !t.Run("completion", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		response, err := model.Generate(ctx, wisp.Request{Messages: []wisp.Message{{Role: "user", Content: "Reply with exactly WISP_OK and nothing else."}}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(response.Text) != "WISP_OK" || len(response.ToolCalls) != 0 {
			t.Fatalf("unexpected response: %+v", response)
		}
		t.Logf("%s: text completion verified", liveModel)
	}) {
		return
	}
	t.Run("runtime_tool_round_trip", func(t *testing.T) {
		fixture := &fixtureTool{values: map[string]string{"left": rand.Text(), "right": rand.Text()}}
		source := fixtureSource{accepted: make(chan string, 1)}
		counted := &boundedModel{model: model}
		runtime, err := wisp.New(wisp.Agent{
			Name: "Wisp integration test", Model: "qwen-test",
			Instructions: []string{"test.md"}, Events: []wisp.EventSource{source}, Tools: []wisp.Tool{fixture},
		}, wisp.Config{
			Models: map[string]wisp.Model{"qwen-test": counted}, DatabasePath: ":memory:",
			Instructions: fstest.MapFS{"test.md": &fstest.MapFile{Data: []byte("For the fixture_lookup event, call read_fixture for left and right, once each. Request both calls in the same turn if possible. The tokens are unknown until you read the tool results. After receiving both results, return only their token values separated by a space, left then right. Do not call tools again after obtaining both tokens.")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		done := make(chan error, 1)
		go func() { done <- runtime.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		var id string
		select {
		case id = <-source.accepted:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			history, err := runtime.History(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if history.Run.Status != "running" {
				if history.Run.Status != "completed" {
					t.Fatalf("run failed: %s", history.Run.Error)
				}
				want := fixture.values["left"] + " " + fixture.values["right"]
				if strings.TrimSpace(history.Run.Output) != want {
					t.Fatalf("model did not return the synthetic tool results: %q", history.Run.Output)
				}
				if fixture.calls.Load() != 2 {
					t.Fatalf("expected two fixture calls; got %d", fixture.calls.Load())
				}
				var results int
				for _, activity := range history.Activity {
					if activity.Kind == "tool.finished" {
						results++
					}
				}
				if results != 2 {
					t.Fatal("tool results missing from history")
				}
				t.Logf("%s: completed persisted run with %d model turns and %d synthetic tool calls", liveModel, counted.calls.Load(), results)
				return
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	})
}

type boundedModel struct {
	model wisp.Model
	calls atomic.Int32
}

func (m *boundedModel) Generate(ctx context.Context, req wisp.Request) (wisp.Response, error) {
	if m.calls.Add(1) > 4 {
		return wisp.Response{}, fmt.Errorf("live test exceeded four model turns")
	}
	return m.model.Generate(ctx, req)
}

type fixtureSource struct{ accepted chan string }

func (fixtureSource) Definition() wisp.EventDefinition {
	return wisp.EventDefinition{Name: "fixture_lookup", Description: "Synthetic integration-test fact"}
}
func (s fixtureSource) Run(ctx context.Context, emit wisp.Emit) error {
	id, err := emit(ctx, wisp.Event{Data: json.RawMessage(`{"fixture_keys":["left","right"]}`)})
	if err == nil {
		s.accepted <- id
	}
	return err
}

type fixtureTool struct {
	values map[string]string
	calls  atomic.Int32
}

func (*fixtureTool) Definition() wisp.ToolDefinition {
	return wisp.ToolDefinition{Name: "read_fixture", Description: "Read the unknown token for one synthetic fixture key.", Parameters: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","enum":["left","right"]}},"required":["key"],"additionalProperties":false}`)}
}
func (f *fixtureTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return nil, err
	}
	value, ok := f.values[input.Key]
	if !ok {
		return nil, fmt.Errorf("unknown fixture key %q", input.Key)
	}
	f.calls.Add(1)
	return json.Marshal(map[string]string{"token": value})
}
