package wisp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

type modelFunc func(context.Context, Request) (Response, error)

func (f modelFunc) Generate(ctx context.Context, req Request) (Response, error) { return f(ctx, req) }

type testSource struct {
	name  string
	ready chan Emit
	run   func(context.Context, Emit) error
}

func (s *testSource) Definition() EventDefinition {
	return EventDefinition{Name: s.name, Description: "Test facts"}
}
func (s *testSource) Run(ctx context.Context, emit Emit) error {
	if s.run != nil {
		return s.run(ctx, emit)
	}
	select {
	case s.ready <- emit:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}

type testTool struct {
	name    string
	execute func(context.Context, json.RawMessage) (json.RawMessage, error)
}

func (t testTool) Definition() ToolDefinition {
	return ToolDefinition{Name: t.name, Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
}
func (t testTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return t.execute(ctx, args)
}

func config(model Model, path string) Config {
	return Config{Models: map[string]Model{"test": model}, DatabasePath: path, Instructions: fstest.MapFS{
		"base.md":  &fstest.MapFile{Data: []byte("First instruction.")},
		"agent.md": &fstest.MapFile{Data: []byte("Second instruction.")},
	}}
}
func declaration(sources ...EventSource) Agent {
	return Agent{Name: "Test", Model: "test", Instructions: []string{"base.md", "agent.md"}, Events: sources}
}
func source(name string) *testSource { return &testSource{name: name, ready: make(chan Emit, 1)} }

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for test barrier")
		var zero T
		return zero
	}
}

func launch(t *testing.T, r *Runtime) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := receive(t, done); err != nil {
			t.Errorf("runtime: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return cancel, done
}

func newRuntime(t *testing.T, a Agent, m Model, path string) *Runtime {
	t.Helper()
	r, err := New(a, config(m, path))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func emitEvent(t *testing.T, emit Emit, data string) string {
	t.Helper()
	id, err := emit(context.Background(), Event{Data: json.RawMessage(data)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func awaitHistory(t *testing.T, r *Runtime, id string, condition func(History) bool) History {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		h, err := r.History(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if condition(h) {
			return h
		}
		select {
		case <-deadline.C:
			t.Fatalf("run never reached expected state: %+v", h)
		case <-tick.C:
		}
	}
}
func terminal(t *testing.T, r *Runtime, id string) History {
	t.Helper()
	return awaitHistory(t, r, id, func(h History) bool { return h.Run.Status != "running" })
}

func TestFreshConcurrentRunsAndExplicitCommunication(t *testing.T) {
	user, timer := source("user.message"), source("timer.tick")
	entered := make(chan Request, 3)
	release := make(chan struct{})
	var sends atomic.Int32
	a := declaration(user, timer)
	a.Tools = []Tool{testTool{name: "respond", execute: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		sends.Add(1)
		return json.RawMessage(`{}`), nil
	}}}
	m := modelFunc(func(ctx context.Context, req Request) (Response, error) {
		entered <- req
		select {
		case <-release:
			return Response{Text: "private final text"}, nil
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	})
	r := newRuntime(t, a, m, ":memory:")
	launch(t, r)
	emitUser, emitTimer := receive(t, user.ready), receive(t, timer.ready)
	idA := emitEvent(t, emitUser, `{"message":"hello"}`)
	first := receive(t, entered)
	idB := emitEvent(t, emitTimer, `{"tick":1}`)
	second := receive(t, entered) // Must enter while first is still blocked.
	if len(first.Messages) != 4 || len(second.Messages) != 4 {
		t.Fatal("each run must start with only system, instructions, and event")
	}
	for _, req := range []Request{first, second} {
		if req.Messages[1].Content != "First instruction." || req.Messages[2].Content != "Second instruction." {
			t.Fatal("instruction order changed")
		}
		if len(req.Tools) != 1 || req.Tools[0].Name != "respond" {
			t.Fatal("declared tools missing")
		}
	}
	if strings.Contains(second.Messages[3].Content, "hello") || !strings.Contains(second.Messages[3].Content, "timer.tick") {
		t.Fatal("cross-run context contamination")
	}
	for _, id := range []string{idA, idB} {
		h, _ := r.History(context.Background(), id)
		if h.Run.Status != "running" {
			t.Fatal("runs did not overlap")
		}
	}
	close(release)
	for _, id := range []string{idA, idB} {
		h := terminal(t, r, id)
		if h.Run.Status != "completed" || h.Run.Output != "private final text" || h.Run.FinishedAt == nil {
			t.Fatalf("unexpected completion: %+v", h.Run)
		}
		if len(h.Activity) != 4 {
			t.Fatalf("unexpected timeline: %+v", h.Activity)
		}
	}
	// Identical subsequent emissions still create new runs, without history.
	idC := emitEvent(t, emitUser, `{"message":"hello"}`)
	third := receive(t, entered)
	terminal(t, r, idC)
	if len(third.Messages) != 4 || idC == idA {
		t.Fatal("emission was deduplicated or inherited context")
	}
	runs, err := r.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 3 {
		t.Fatalf("one event per run violated: %v, %d", err, len(runs))
	}
	if sends.Load() != 0 {
		t.Fatal("final model text caused external communication")
	}
}

func TestParallelToolsWaitForAllAndPreserveCallIDs(t *testing.T) {
	s := source("test")
	started := make(chan string, 2)
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	tool := func(name string, release <-chan struct{}) Tool {
		return testTool{name: name, execute: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			started <- name
			select {
			case <-release:
				return json.RawMessage(`{"from":"` + name + `"}`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}
	}
	a := declaration(s)
	a.Tools = []Tool{tool("a", releaseA), tool("b", releaseB)}
	var turns atomic.Int32
	requests := make(chan Request, 1)
	m := modelFunc(func(ctx context.Context, req Request) (Response, error) {
		if turns.Add(1) == 1 {
			return Response{Text: "reading", ToolCalls: []ToolCall{{ID: "call-a", Name: "a", Arguments: json.RawMessage(`{}`)}, {ID: "call-b", Name: "b", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		requests <- req
		return Response{Text: "done"}, nil
	})
	r := newRuntime(t, a, m, ":memory:")
	launch(t, r)
	id := emitEvent(t, receive(t, s.ready), `{}`)
	first, second := receive(t, started), receive(t, started)
	if first == second {
		t.Fatal("same tool executed twice")
	}
	close(releaseB)
	awaitHistory(t, r, id, func(h History) bool {
		for _, a := range h.Activity {
			if a.Kind == "tool.finished" && strings.Contains(string(a.Data), "call-b") {
				return true
			}
		}
		return false
	})
	if turns.Load() != 1 {
		t.Fatal("model advanced before all tools settled")
	}
	close(releaseA)
	req := receive(t, requests)
	if len(req.Messages) != 7 {
		t.Fatalf("wrong transcript length: %d", len(req.Messages))
	}
	if req.Messages[4].Role != "assistant" || len(req.Messages[4].ToolCalls) != 2 {
		t.Fatal("assistant tool request missing")
	}
	for i, want := range []string{"a", "b"} {
		msg := req.Messages[5+i]
		if msg.ToolCallID != "call-"+want || !strings.Contains(msg.Content, want) {
			t.Fatalf("misassociated result: %+v", msg)
		}
	}
	h := terminal(t, r, id)
	if h.Run.Status != "completed" || len(h.Activity) != 10 {
		t.Fatalf("unexpected history: %+v", h)
	}
}

func TestResponseToolIsExplicitAndMultiTurn(t *testing.T) {
	s := source("test")
	var sends, turns atomic.Int32
	a := declaration(s)
	a.Tools = []Tool{testTool{name: "respond", execute: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		sends.Add(1)
		return json.RawMessage(`{"sent":true}`), nil
	}}}
	m := modelFunc(func(context.Context, Request) (Response, error) {
		n := turns.Add(1)
		if n < 3 {
			return Response{ToolCalls: []ToolCall{{ID: string(rune('a' + n)), Name: "respond", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		return Response{Text: "private"}, nil
	})
	r := newRuntime(t, a, m, ":memory:")
	launch(t, r)
	h := terminal(t, r, emitEvent(t, receive(t, s.ready), `{}`))
	if h.Run.Status != "completed" || sends.Load() != 2 || turns.Load() != 3 {
		t.Fatalf("unexpected explicit communication: %+v", h.Run)
	}
}

func TestRunFailuresAreRecordedAndDoNotStopOtherRuns(t *testing.T) {
	cases := []struct {
		name       string
		response   Response
		modelErr   error
		panicModel bool
		tool       func(context.Context, json.RawMessage) (json.RawMessage, error)
		want       string
	}{
		{name: "model error", modelErr: errors.New("unavailable"), want: "unavailable"},
		{name: "model panic", panicModel: true, want: "panic"},
		{name: "unknown tool", response: Response{ToolCalls: []ToolCall{{ID: "x", Name: "hidden", Arguments: json.RawMessage(`{}`)}}}, want: "undeclared"},
		{name: "duplicate ID", response: Response{ToolCalls: []ToolCall{{ID: "x", Name: "test", Arguments: json.RawMessage(`{}`)}, {ID: "x", Name: "test", Arguments: json.RawMessage(`{}`)}}}, want: "duplicate"},
		{name: "bad arguments", response: Response{ToolCalls: []ToolCall{{ID: "x", Name: "test", Arguments: json.RawMessage(`[]`)}}}, want: "JSON object"},
		{name: "malformed JSON", response: Response{ToolCalls: []ToolCall{{ID: "x", Name: "test", Arguments: json.RawMessage(`oops`)}}}, want: "invalid model response"},
		{name: "tool error", tool: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, errors.New("tool failed") }, want: "tool failed"},
		{name: "tool panic", tool: func(context.Context, json.RawMessage) (json.RawMessage, error) { panic("broken") }, want: "panic"},
		{name: "invalid result", tool: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`nope`), nil }, want: "invalid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := source("test")
			var invoked atomic.Int32
			a := declaration(s)
			a.Tools = []Tool{testTool{name: "test", execute: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
				invoked.Add(1)
				if tc.tool != nil {
					return tc.tool(ctx, args)
				}
				return json.RawMessage(`{}`), nil
			}}}
			var calls atomic.Int32
			m := modelFunc(func(context.Context, Request) (Response, error) {
				if calls.Add(1) > 1 {
					return Response{Text: "healthy"}, nil
				}
				if tc.panicModel {
					panic("model broke")
				}
				if tc.tool != nil {
					return Response{ToolCalls: []ToolCall{{ID: "x", Name: "test", Arguments: json.RawMessage(`{}`)}}}, nil
				}
				return tc.response, tc.modelErr
			})
			r := newRuntime(t, a, m, ":memory:")
			launch(t, r)
			emit := receive(t, s.ready)
			h := terminal(t, r, emitEvent(t, emit, `{}`))
			if h.Run.Status != "failed" || !strings.Contains(h.Run.Error, tc.want) {
				t.Fatalf("failure missing: %+v", h.Run)
			}
			if tc.tool == nil && invoked.Load() != 0 {
				t.Fatal("invalid batch executed tool")
			}
			h = terminal(t, r, emitEvent(t, emit, `{}`))
			if h.Run.Status != "completed" || h.Run.Output != "healthy" {
				t.Fatal("one run failure stopped unrelated work")
			}
		})
	}
}

func TestFailedToolStillWaitsForSibling(t *testing.T) {
	s := source("test")
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	a := declaration(s)
	a.Tools = []Tool{
		testTool{name: "fail", execute: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, errors.New("failed") }},
		testTool{name: "slow", execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return json.RawMessage(`{}`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}},
	}
	var turns atomic.Int32
	m := modelFunc(func(context.Context, Request) (Response, error) {
		turns.Add(1)
		return Response{ToolCalls: []ToolCall{{ID: "a", Name: "fail", Arguments: json.RawMessage(`{}`)}, {ID: "b", Name: "slow", Arguments: json.RawMessage(`{}`)}}}, nil
	})
	r := newRuntime(t, a, m, ":memory:")
	launch(t, r)
	id := emitEvent(t, receive(t, s.ready), `{}`)
	receive(t, entered)
	h := awaitHistory(t, r, id, func(h History) bool {
		for _, a := range h.Activity {
			if a.Kind == "tool.finished" {
				return true
			}
		}
		return false
	})
	if h.Run.Status != "running" {
		t.Fatal("run terminated while sibling tool was active")
	}
	close(release)
	h = terminal(t, r, id)
	if h.Run.Status != "failed" || turns.Load() != 1 {
		t.Fatal("failed turn advanced")
	}
	var results int
	for _, a := range h.Activity {
		if a.Kind == "tool.finished" {
			results++
		}
	}
	if results != 2 {
		t.Fatal("lost sibling result")
	}
}

func TestShutdownCancelsAndSettlesRuns(t *testing.T) {
	for _, phase := range []string{"model", "tool"} {
		t.Run(phase, func(t *testing.T) {
			s := source("test")
			entered := make(chan struct{}, 1)
			a := declaration(s)
			a.Tools = []Tool{testTool{name: "wait", execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			}}}
			m := modelFunc(func(ctx context.Context, _ Request) (Response, error) {
				if phase == "model" {
					entered <- struct{}{}
					<-ctx.Done()
					return Response{}, ctx.Err()
				}
				return Response{ToolCalls: []ToolCall{{ID: "x", Name: "wait", Arguments: json.RawMessage(`{}`)}}}, nil
			})
			r := newRuntime(t, a, m, ":memory:")
			cancel, _ := launch(t, r)
			emit := receive(t, s.ready)
			id := emitEvent(t, emit, `{}`)
			receive(t, entered)
			if err := r.Close(); err == nil {
				t.Fatal("closed active database")
			}
			cancel()
			h := terminal(t, r, id)
			if h.Run.Status != "failed" || !strings.Contains(h.Run.Error, "canceled") {
				t.Fatalf("cancellation missing: %+v", h)
			}
			if _, err := emit(context.Background(), Event{Data: json.RawMessage(`{}`)}); err == nil {
				t.Fatal("accepted after shutdown")
			}
		})
	}
}

func TestPersistenceAndInterruptedRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	m := modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil })
	r := newRuntime(t, declaration(), m, path)
	if err := r.store.accept(context.Background(), "abandoned", "event-1", "test", Event{Data: json.RawMessage(`{"fact":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.accept(context.Background(), "complete", "event-2", "test", Event{Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.finish("complete", "saved", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	s := source("new")
	requests := make(chan Request, 1)
	r = newRuntime(t, declaration(s), modelFunc(func(_ context.Context, req Request) (Response, error) { requests <- req; return Response{}, nil }), path)
	h, err := r.History(context.Background(), "abandoned")
	if err != nil || h.Run.Status != "failed" || !strings.Contains(h.Run.Error, "interrupted") {
		t.Fatalf("recovery: %+v %v", h, err)
	}
	if len(h.Activity) != 2 || h.Activity[1].Kind != "run.failed" {
		t.Fatal("recovery not recorded")
	}
	h, err = r.History(context.Background(), "complete")
	if err != nil || h.Run.Output != "saved" || h.Run.Status != "completed" {
		t.Fatal("completed history lost")
	}
	launch(t, r)
	id := emitEvent(t, receive(t, s.ready), `{}`)
	req := receive(t, requests)
	terminal(t, r, id)
	if len(req.Messages) != 4 || strings.Contains(req.Messages[3].Content, "saved") {
		t.Fatal("persisted history became memory")
	}
}

func TestAcceptanceAndDeclarationValidation(t *testing.T) {
	m := modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil })
	for _, tc := range []struct {
		name   string
		modify func(*Agent, *Config)
	}{
		{"missing name", func(a *Agent, c *Config) { a.Name = "" }},
		{"missing model", func(a *Agent, c *Config) { a.Model = "missing" }},
		{"missing instructions", func(a *Agent, c *Config) { a.Instructions = nil }},
		{"unreadable instructions", func(a *Agent, c *Config) { a.Instructions = []string{"missing.md"} }},
		{"non Markdown", func(a *Agent, c *Config) { a.Instructions = []string{"base.txt"} }},
		{"duplicate events", func(a *Agent, c *Config) { a.Events = []EventSource{source("same"), source("same")} }},
		{"nil source", func(a *Agent, c *Config) { var s *testSource; a.Events = []EventSource{s} }},
		{"duplicate tools", func(a *Agent, c *Config) { a.Tools = []Tool{testTool{name: "same"}, testTool{name: "same"}} }},
		{"missing path", func(a *Agent, c *Config) { c.DatabasePath = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, c := declaration(), config(m, ":memory:")
			tc.modify(&a, &c)
			r, err := New(a, c)
			if err == nil {
				r.Close()
				t.Fatal("invalid declaration accepted")
			}
		})
	}
	s := source("test")
	r := newRuntime(t, declaration(s), m, ":memory:")
	launch(t, r)
	emit := receive(t, s.ready)
	if _, err := emit(context.Background(), Event{Data: json.RawMessage(`bad`)}); err == nil {
		t.Fatal("invalid event accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := emit(ctx, Event{Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("canceled emission accepted")
	}
	runs, err := r.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 0 {
		t.Fatal("rejected emissions left runs")
	}
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("runtime started twice")
	}
	var events int
	if err := r.store.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events); err != nil || events != 0 {
		t.Fatal("rejected emissions left events")
	}
}

func TestSourceFailureStopsRuntime(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panics], func(t *testing.T) {
			s := source("broken")
			s.run = func(context.Context, Emit) error {
				if panics {
					panic("source broke")
				}
				return errors.New("source broke")
			}
			r := newRuntime(t, declaration(s), modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil }), ":memory:")
			defer r.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.Run(ctx); err == nil || !strings.Contains(err.Error(), "source broke") {
				t.Fatalf("source failure hidden: %v", err)
			}
		})
	}
}
