package wisp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAcceptanceIsAtomic(t *testing.T) {
	r := newRuntime(t, declaration(), modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil }), ":memory:")
	defer r.Close()
	ctx := context.Background()
	event := Event{Data: json.RawMessage(`{}`)}
	if err := r.store.accept(ctx, "run", "event", "test", event); err != nil {
		t.Fatal(err)
	}
	// A later failure in the same transaction must not leave an orphan event.
	if err := r.store.accept(ctx, "run", "orphan", "test", event); err == nil {
		t.Fatal("duplicate run accepted")
	}
	var count int
	if err := r.store.db.QueryRow(`SELECT count(*) FROM events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphan event: %d %v", count, err)
	}
	if err := r.store.accept(ctx, "other-run", "event", "test", event); err == nil {
		t.Fatal("one event mapped to multiple runs")
	}
	if err := r.store.db.QueryRow(`SELECT count(*) FROM runs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate event left a run")
	}
}

func TestPersistenceFailurePreventsToolEffects(t *testing.T) {
	s := source("test")
	var invoked bool
	a := declaration(s)
	a.Tools = []Tool{testTool{name: "effect", execute: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		invoked = true
		return json.RawMessage(`{}`), nil
	}}}
	r := newRuntime(t, a, modelFunc(func(context.Context, Request) (Response, error) {
		return Response{ToolCalls: []ToolCall{{ID: "x", Name: "effect", Arguments: json.RawMessage(`{}`)}}}, nil
	}), ":memory:")
	// Fault injection verifies the journal is written before invoking effects.
	_, err := r.store.db.Exec(`CREATE TRIGGER reject_tool BEFORE INSERT ON activity WHEN NEW.kind='tool.started' BEGIN SELECT RAISE(FAIL, 'journal unavailable'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	launch(t, r)
	h := terminal(t, r, emitEvent(t, receive(t, s.ready), `{}`))
	if h.Run.Status != "failed" || !strings.Contains(h.Run.Error, "journal unavailable") {
		t.Fatalf("persistence failure hidden: %+v", h.Run)
	}
	if invoked {
		t.Fatal("effect happened without durable call record")
	}
}

func TestTerminalPersistenceFailureIsReturned(t *testing.T) {
	s := source("test")
	r := newRuntime(t, declaration(s), modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil }), ":memory:")
	defer r.Close()
	_, err := r.store.db.Exec(`CREATE TRIGGER reject_finish BEFORE UPDATE ON runs BEGIN SELECT RAISE(FAIL, 'finish unavailable'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	emitEvent(t, receive(t, s.ready), `{}`)
	if err := receive(t, done); err == nil || !strings.Contains(err.Error(), "finish unavailable") {
		t.Fatalf("terminal persistence failure hidden: %v", err)
	}
}

func TestDeclarationAndRequestSnapshots(t *testing.T) {
	s := source("test")
	a := declaration(s)
	a.Tools = []Tool{testTool{name: "declared"}}
	cfg := config(modelFunc(func(_ context.Context, req Request) (Response, error) {
		if req.Messages[1].Content != "First instruction." || req.Tools[0].Name != "declared" {
			t.Error("shared request was mutated")
		}
		req.Messages[1].Content = "mutated"
		req.Tools[0].Name = "hidden"
		req.Tools[0].Parameters[0] = 'x'
		return Response{}, nil
	}), ":memory:")
	r, err := New(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Instructions[0] = "missing.md"
	a.Events[0] = source("hidden")
	a.Tools[0] = testTool{name: "hidden"}
	d := r.Describe()
	d.Tools[0].Parameters[0] = 'x'
	d.Tools[0].Name = "hidden"
	d.Events[0].Name = "hidden"
	d.Instructions[0] = "hidden"
	if r.Describe().Tools[0].Name != "declared" || r.Describe().Events[0].Name != "test" {
		t.Fatal("metadata shares mutable declaration")
	}
	launch(t, r)
	emit := receive(t, s.ready)
	for i := 0; i < 2; i++ {
		h := terminal(t, r, emitEvent(t, emit, `{}`))
		if h.Run.Status != "completed" {
			t.Fatal(h.Run.Error)
		}
		for _, activity := range h.Activity {
			if activity.Kind == "model.started" && strings.Contains(string(activity.Data), "mutated") {
				t.Fatal("model mutation changed persisted input")
			}
		}
	}
}
