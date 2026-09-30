package wisp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSecondRuntimeCannotRecoverLiveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	m := modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil })
	r := newRuntime(t, declaration(), m, path)
	defer r.Close()
	if err := r.store.accept(context.Background(), "active", "event", "test", Event{Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	other, err := New(declaration(), config(m, path))
	if err == nil {
		other.Close()
		t.Fatal("second runtime acquired a live database")
	}
	h, err := r.History(context.Background(), "active")
	if err != nil || h.Run.Status != "running" {
		t.Fatalf("second runtime modified active history: %+v %v", h, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newRuntime(t, declaration(), m, path)
	defer reopened.Close()
	h, err = reopened.History(context.Background(), "active")
	if err != nil || h.Run.Status != "failed" {
		t.Fatal("released database did not recover interrupted run")
	}
}

func TestCapacityWaitCancellationAndRelease(t *testing.T) {
	s := source("test")
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	m := modelFunc(func(ctx context.Context, _ Request) (Response, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return Response{}, nil
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	})
	cfg := config(m, ":memory:")
	cfg.MaxConcurrentRuns = 2
	r, err := New(declaration(s), cfg)
	if err != nil {
		t.Fatal(err)
	}
	launch(t, r)
	emit := receive(t, s.ready)
	a := emitEvent(t, emit, `{}`)
	b := emitEvent(t, emit, `{}`)
	receive(t, entered)
	receive(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := emit(ctx, Event{Data: json.RawMessage(`{}`)}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capacity wait did not honor caller cancellation: %v", err)
	}
	runs, err := r.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 2 {
		t.Fatal("unaccepted event left a run")
	}
	close(release)
	terminal(t, r, a)
	terminal(t, r, b)
	c := emitEvent(t, emit, `{}`)
	receive(t, entered)
	terminal(t, r, c)
}

func TestRunDeadlineAndTurnLimit(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		s := source("test")
		m := modelFunc(func(ctx context.Context, _ Request) (Response, error) { <-ctx.Done(); return Response{}, ctx.Err() })
		cfg := config(m, ":memory:")
		cfg.RunTimeout = 30 * time.Millisecond
		r, err := New(declaration(s), cfg)
		if err != nil {
			t.Fatal(err)
		}
		launch(t, r)
		h := terminal(t, r, emitEvent(t, receive(t, s.ready), `{}`))
		if h.Run.Status != "failed" || h.Run.Error != "model turn 1: context deadline exceeded" {
			t.Fatalf("deadline: %+v", h.Run)
		}
	})
	t.Run("turn limit", func(t *testing.T) {
		s := source("test")
		a := declaration(s)
		a.Tools = []Tool{testTool{name: "noop", execute: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}}
		var calls atomic.Int32
		m := modelFunc(func(context.Context, Request) (Response, error) {
			id := fmt.Sprint(calls.Add(1))
			return Response{ToolCalls: []ToolCall{{ID: id, Name: "noop", Arguments: json.RawMessage(`{}`)}}}, nil
		})
		cfg := config(m, ":memory:")
		cfg.MaxModelTurns = 2
		r, err := New(a, cfg)
		if err != nil {
			t.Fatal(err)
		}
		launch(t, r)
		h := terminal(t, r, emitEvent(t, receive(t, s.ready), `{}`))
		if h.Run.Status != "failed" || calls.Load() != 2 || h.Run.Error != "model turn limit exceeded (2)" {
			t.Fatalf("turn bound: %+v", h.Run)
		}
	})
}

func TestConcurrentAcceptanceAndShutdown(t *testing.T) {
	s := source("test")
	r := newRuntime(t, declaration(s), modelFunc(func(ctx context.Context, _ Request) (Response, error) { <-ctx.Done(); return Response{}, ctx.Err() }), ":memory:")
	cancel, _ := launch(t, r)
	emit := receive(t, s.ready)
	const count = 64
	ids := make(chan string, count)
	started := make(chan struct{}, count)
	// Ensure shutdown races with actual accepted work, not just startup.
	ids <- emitEvent(t, emit, `{}`)
	ids <- emitEvent(t, emit, `{}`)
	var callers sync.WaitGroup
	for i := 2; i < count; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			started <- struct{}{}
			id, err := emit(context.Background(), Event{Data: json.RawMessage(`{}`)})
			if err == nil {
				ids <- id
			}
		}()
	}
	for i := 2; i < count; i++ {
		receive(t, started)
	}
	cancel()
	finished := make(chan struct{})
	go func() { callers.Wait(); close(finished) }()
	receive(t, finished)
	close(ids)
	accepted := 0
	for id := range ids {
		accepted++
		h := terminal(t, r, id)
		if h.Run.Status != "failed" {
			t.Fatal("accepted run did not settle")
		}
	}
	if accepted < 2 || accepted > 16 {
		t.Fatalf("unexpected admitted run count: %d", accepted)
	}
	runs, err := r.ListRuns(context.Background(), 100)
	if err != nil || len(runs) != accepted {
		t.Fatalf("accepted %d but stored %d: %v", accepted, len(runs), err)
	}
}

func TestActiveRunsOutsideRecentWindow(t *testing.T) {
	r := newRuntime(t, declaration(), modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil }), ":memory:")
	defer r.Close()
	ctx := context.Background()
	for i := 0; i < 102; i++ {
		id := fmt.Sprint(i)
		if err := r.store.accept(ctx, id, "event-"+id, "test", Event{Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			if err := r.store.finish(id, "", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	active, err := r.ActiveRuns(ctx)
	if err != nil || len(active) != 1 || active[0].ID != "0" {
		t.Fatalf("older active run disappeared: %+v %v", active, err)
	}
}

func TestSourceCallbackExpiresOnReturn(t *testing.T) {
	s := source("ephemeral")
	sourceDone := make(chan (<-chan struct{}), 1)
	s.run = func(ctx context.Context, emit Emit) error { s.ready <- emit; sourceDone <- ctx.Done(); return nil }
	r := newRuntime(t, declaration(s), modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil }), ":memory:")
	launch(t, r)
	emit := receive(t, s.ready)
	receive(t, receive(t, sourceDone))
	if _, err := emit(context.Background(), Event{Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("callback remained live after source returned")
	}
}

func TestCanceledStartupDoesNotStartSources(t *testing.T) {
	s := source("test")
	s.run = func(context.Context, Emit) error { t.Error("source started after cancellation"); return nil }
	r := newRuntime(t, declaration(s), modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil }), ":memory:")
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEmitterShutdownIsNormal(t *testing.T) {
	s := source("test")
	ready := make(chan struct{}, 1)
	s.run = func(ctx context.Context, emit Emit) error {
		ready <- struct{}{}
		<-ctx.Done()
		_, err := emit(ctx, Event{Data: json.RawMessage(`{}`)})
		return err
	}
	r := newRuntime(t, declaration(s), modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil }), ":memory:")
	cancel, _ := launch(t, r)
	receive(t, ready)
	cancel()
}
