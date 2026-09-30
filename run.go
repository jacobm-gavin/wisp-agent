package wisp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

func (r *Runtime) execute(ctx context.Context, id, source string, event Event) (string, error) {
	facts, err := json.Marshal(struct {
		Source string `json:"source"`
		Event  Event  `json:"event"`
	}{source, event})
	if err != nil {
		return "", err
	}
	messages := append(cloneMessages(r.instructions), Message{Role: "user", Content: string(facts)})
	seen := make(map[string]bool)
	for turn := 1; turn <= r.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		request := Request{Messages: cloneMessages(messages), Tools: cloneTools(r.description.Tools)}
		if err := r.store.record(ctx, id, "model.started", map[string]any{"turn": turn, "model": r.description.Model, "request": request}); err != nil {
			return "", err
		}
		var response Response
		err := guarded(func() (err error) { response, err = r.model.Generate(ctx, request); return err })
		// Snapshot provider-owned slices before retaining them in the transcript.
		response.ToolCalls = cloneMessages([]Message{{ToolCalls: response.ToolCalls}})[0].ToolCalls
		// Invalid provider JSON must still be inspectable; encode its raw bytes as
		// strings if it cannot be represented as a normal response object.
		var observed any = response
		if _, encodeErr := json.Marshal(response); encodeErr != nil {
			observed = fmt.Sprintf("%+v", response)
			err = errors.Join(err, fmt.Errorf("invalid model response JSON: %w", encodeErr))
		}
		if persistErr := r.recordSettled(id, "model.finished", map[string]any{"turn": turn, "response": observed, "error": errorText(err)}); persistErr != nil {
			return "", errors.Join(err, persistErr)
		}
		if err != nil {
			return "", fmt.Errorf("model turn %d: %w", turn, err)
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(response.ToolCalls) == 0 {
			return response.Text, nil
		}
		for _, call := range response.ToolCalls {
			if call.ID == "" || seen[call.ID] {
				return "", fmt.Errorf("empty or duplicate tool call ID %q", call.ID)
			}
			seen[call.ID] = true
			if r.tools[call.Name] == nil {
				return "", fmt.Errorf("undeclared tool %q", call.Name)
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal(call.Arguments, &args); err != nil || args == nil {
				return "", fmt.Errorf("tool %s arguments must be a JSON object", call.ID)
			}
		}
		results, err := r.executeTools(ctx, id, turn, response.ToolCalls)
		if err != nil {
			return "", err
		}
		messages = append(messages, Message{Role: "assistant", Content: response.Text, ToolCalls: response.ToolCalls})
		messages = append(messages, results...)
	}
	return "", fmt.Errorf("model turn limit exceeded (%d)", r.maxTurns)
}

// recordSettled persists results even after cancellation, with a bounded write.
func (r *Runtime) recordSettled(id, kind string, data any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.store.record(ctx, id, kind, data)
}

func (r *Runtime) executeTools(ctx context.Context, id string, turn int, calls []ToolCall) ([]Message, error) {
	for _, call := range calls {
		if err := r.store.record(ctx, id, "tool.started", map[string]any{"turn": turn, "call": call}); err != nil {
			return nil, err
		}
	}
	results := make([]Message, len(calls))
	errs := make([]error, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var result json.RawMessage
			err := guarded(func() (err error) {
				if err := ctx.Err(); err != nil {
					return err
				}
				result, err = r.tools[call.Name].Execute(ctx, append(json.RawMessage(nil), call.Arguments...))
				return err
			})
			var observed any = result
			if !json.Valid(result) {
				observed = string(result)
				if err == nil {
					err = errors.New("tool returned invalid JSON")
				}
			}
			persistErr := r.recordSettled(id, "tool.finished", map[string]any{"turn": turn, "call_id": call.ID, "result": observed, "error": errorText(err)})
			if err = errors.Join(err, persistErr); err != nil {
				errs[i] = fmt.Errorf("tool %s (%s): %w", call.Name, call.ID, err)
			}
			results[i] = Message{Role: "tool", ToolCallID: call.ID, Content: string(result)}
		}()
	}
	wg.Wait()
	return results, errors.Join(errs...)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
