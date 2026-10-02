package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

type recoverySource struct{ accepted chan string }

func (s recoverySource) Definition() wisp.EventDefinition {
	return wisp.EventDefinition{Name: "test.recovery"}
}
func (s recoverySource) Run(ctx context.Context, emit wisp.Emit) error {
	id, err := emit(ctx, wisp.Event{Data: json.RawMessage(`{}`)})
	if err != nil {
		return err
	}
	s.accepted <- id
	return nil
}

type recoveryModel struct{ turns int }

func (m *recoveryModel) Generate(_ context.Context, req wisp.Request) (wisp.Response, error) {
	m.turns++
	command := "printf 'missing dependency' >&2; exit 7"
	if m.turns > 1 {
		last := req.Messages[len(req.Messages)-1]
		var result Result
		if last.Role != "tool" {
			return wisp.Response{}, fmt.Errorf("missing tool observation")
		}
		if err := json.Unmarshal([]byte(last.Content), &result); err != nil {
			return wisp.Response{}, err
		}
		if m.turns == 2 {
			if result.ExitCode != 7 || result.Stderr != "missing dependency" || last.ToolCallID != "call-1" {
				return wisp.Response{}, fmt.Errorf("failure not surfaced: %+v", result)
			}
			command = "printf recovered"
		} else {
			if result.ExitCode != 0 || result.Stdout != "recovered" || last.ToolCallID != "call-2" {
				return wisp.Response{}, fmt.Errorf("recovery failed: %+v", result)
			}
			return wisp.Response{Text: "recovered in the same run"}, nil
		}
	}
	args, _ := json.Marshal(map[string]string{"command": command})
	return wisp.Response{ToolCalls: []wisp.ToolCall{{ID: fmt.Sprintf("call-%d", m.turns), Name: "bash", Arguments: args}}}, nil
}

func TestRunContinuesAfterNonzeroExit(t *testing.T) {
	tool, _ := newTool(t)
	source := recoverySource{accepted: make(chan string, 1)}
	model := &recoveryModel{}
	r, err := wisp.New(wisp.Agent{Name: "recovery", Model: "test", Instructions: []string{"base.md"}, Events: []wisp.EventSource{source}, Tools: []wisp.Tool{tool}}, wisp.Config{Models: map[string]wisp.Model{"test": model}, Instructions: fstest.MapFS{"base.md": &fstest.MapFile{Data: []byte("Inspect command errors before choosing recovery.")}}, DatabasePath: ":memory:", MaxModelTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		r.Close()
	}()
	var id string
	select {
	case id = <-source.accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		h, err := r.History(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if h.Run.Status != "running" {
			if h.Run.Status != "completed" || h.Run.Output != "recovered in the same run" {
				t.Fatalf("%+v", h.Run)
			}
			var observed []int
			for _, a := range h.Activity {
				if a.Kind == "tool.finished" {
					var d struct {
						Result Result `json:"result"`
						Error  string `json:"error"`
					}
					if err := json.Unmarshal(a.Data, &d); err != nil {
						t.Fatal(err)
					}
					if d.Error != "" {
						t.Fatal(d.Error)
					}
					observed = append(observed, d.Result.ExitCode)
				}
			}
			if len(observed) != 2 || observed[0] != 7 || observed[1] != 0 {
				t.Fatal("journal lost exit status", observed)
			}
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
