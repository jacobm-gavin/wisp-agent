package files_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
	"github.com/jacobm-gavin/wisp-agent/model/openrouter"
	"github.com/jacobm-gavin/wisp-agent/tools/files"
)

type oneEvent struct{ accepted chan string }

func (s oneEvent) Definition() wisp.EventDefinition {
	return wisp.EventDefinition{Name: "fixture.file_ready", Description: "A fixture file became available"}
}
func (s oneEvent) Run(ctx context.Context, emit wisp.Emit) error {
	id, err := emit(ctx, wisp.Event{Data: json.RawMessage(`{"path":"fixture.txt"}`)})
	if err != nil {
		return err
	}
	s.accepted <- id
	<-ctx.Done()
	return ctx.Err()
}

type modelFunc func(context.Context, wisp.Request) (wisp.Response, error)

func (f modelFunc) Generate(ctx context.Context, r wisp.Request) (wisp.Response, error) {
	return f(ctx, r)
}

func TestRuntimeFileRoundTrip(t *testing.T) {
	updated := "after-" + rand.Text() + "\n"
	var turn int
	m := modelFunc(func(_ context.Context, r wisp.Request) (wisp.Response, error) {
		turn++
		if len(r.Tools) != 2 {
			t.Error("wrong capability surface")
		}
		switch turn {
		case 1:
			return toolCall("read-1", "read_file", map[string]any{"path": "fixture.txt"}), nil
		case 2:
			var result struct {
				SHA string `json:"sha256"`
			}
			if err := json.Unmarshal([]byte(r.Messages[len(r.Messages)-1].Content), &result); err != nil {
				return wisp.Response{}, err
			}
			return toolCall("write-1", "write_file", map[string]any{"path": "fixture.txt", "content": updated, "expected_sha256": result.SHA}), nil
		case 3:
			return toolCall("read-2", "read_file", map[string]any{"path": "fixture.txt"}), nil
		default:
			var result struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(r.Messages[len(r.Messages)-1].Content), &result); err != nil {
				return wisp.Response{}, err
			}
			if result.Content != updated {
				return wisp.Response{}, fmt.Errorf("read-back mismatch")
			}
			return wisp.Response{Text: "FILES_OK"}, nil
		}
	})
	runFileRoundTrip(t, m, updated)
}

func toolCall(id, name string, args any) wisp.Response {
	raw, _ := json.Marshal(args)
	return wisp.Response{ToolCalls: []wisp.ToolCall{{ID: id, Name: name, Arguments: raw}}}
}

// Opt-in paid acceptance check, bounded to eight model calls, using only temp files.
func TestOpenRouterLiveFileTools(t *testing.T) {
	if os.Getenv("WISP_OPENROUTER_LIVE") != "1" {
		t.Skip("opt-in paid live test")
	}
	m, err := openrouter.New(openrouter.Config{APIKey: os.Getenv("OPENROUTER_API_KEY"), Model: "qwen/qwen3.8-27b", MaxTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	runFileRoundTrip(t, m, "after-"+rand.Text()+"\n")
}

func runFileRoundTrip(t *testing.T, m wisp.Model, updated string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("before-"+rand.Text()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := files.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	source := oneEvent{accepted: make(chan string, 1)}
	policy := fmt.Sprintf("For the fixture.file_ready event, read the file named in its data.path with read_file. Then replace its complete contents with this exact JSON-encoded string: %s. Use the sha256 returned by read_file as expected_sha256. Then read the file again to verify the new content. Finally output exactly FILES_OK with no more calls. Do not create other files.", jsonString(updated))
	r, err := wisp.New(wisp.Agent{Name: "File tools acceptance", Model: "test", Instructions: []string{"base.md"}, Events: []wisp.EventSource{source}, Tools: []wisp.Tool{w.ReadFiles(), w.WriteFiles()}}, wisp.Config{Models: map[string]wisp.Model{"test": m}, Instructions: fstest.MapFS{"base.md": &fstest.MapFile{Data: []byte(policy)}}, DatabasePath: filepath.Join(t.TempDir(), "history.db"), MaxModelTurns: 8, RunTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	var id string
	select {
	case id = <-source.accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		h, err := r.History(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if h.Run.Status != "running" {
			if h.Run.Status != "completed" || strings.TrimSpace(h.Run.Output) != "FILES_OK" {
				t.Fatalf("run failed: %+v", h.Run)
			}
			b, err := os.ReadFile(filepath.Join(dir, "fixture.txt"))
			if err != nil || string(b) != updated {
				t.Fatalf("disk verification failed: %q %v", b, err)
			}
			var reads, writes, results, models int
			for _, a := range h.Activity {
				switch a.Kind {
				case "model.started":
					models++
				case "tool.finished":
					results++
				case "tool.started":
					var d struct {
						Call wisp.ToolCall `json:"call"`
					}
					if err := json.Unmarshal(a.Data, &d); err != nil {
						t.Fatal(err)
					}
					if d.Call.Name == "read_file" {
						reads++
					}
					if d.Call.Name == "write_file" {
						writes++
					}
				}
			}
			if reads != 2 || writes != 1 || results != 3 {
				t.Fatalf("unexpected journal reads=%d writes=%d results=%d", reads, writes, results)
			}
			t.Logf("verified disk contents and persisted read/write/read sequence in %d model turns", models)
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }
