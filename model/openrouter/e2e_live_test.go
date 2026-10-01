package openrouter_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
	"github.com/jacobm-gavin/wisp-agent/model/openrouter"
)

const acceptanceInstructions = `You are running a synthetic acceptance test. The triggering event has a data.kind field.
For kind=lookup, call read_fixture for left and right exactly once each. Both calls MUST be in the SAME response; neither depends on the other. After both results arrive, output only the two token values separated by a space, left then right. Do not call send_reply.
For kind=noop, do not call any tools. Output exactly NO_ACTION.
For kind=reply, call send_reply with message REPLY_OK exactly once. After receiving its result, output exactly PRIVATE_FINAL with no more tool calls.
For kind=failure, call fail_fixture once.
For kind=cancel, call wait_fixture once.
These are the only actions for this test. Never invent fixture tokens or reuse data from another event.`

// TestOpenRouterLiveCoreE2E exercises the public API and real model transport.
// Only test doubles implement capabilities. At most 16 paid requests are allowed.
func TestOpenRouterLiveCoreE2E(t *testing.T) {
	if os.Getenv("WISP_OPENROUTER_LIVE") != "1" {
		t.Skip("opt-in live acceptance suite")
	}
	model, err := openrouter.New(openrouter.Config{APIKey: os.Getenv("OPENROUTER_API_KEY"), Model: liveModel, MaxTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	budget := &acceptanceModel{model: model}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "agent.md"), []byte(acceptanceInstructions), 0600); err != nil {
		t.Fatal(err)
	}
	left, right := rand.Text(), rand.Text()
	started := make(chan string, 2)
	release := make(chan struct{})
	waiting := make(chan struct{}, 1)
	var replies atomic.Int32
	tools := []wisp.Tool{
		acceptanceTool{name: "read_fixture", parameters: `{"type":"object","properties":{"key":{"type":"string","enum":["left","right"]}},"required":["key"],"additionalProperties":false}`, execute: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			var input struct {
				Key string `json:"key"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, err
			}
			value := map[string]string{"left": left, "right": right}[input.Key]
			if value == "" {
				return nil, errors.New("unknown fixture key")
			}
			select {
			case started <- input.Key:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case <-release:
				return json.Marshal(map[string]string{"token": value})
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}},
		acceptanceTool{name: "send_reply", parameters: `{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`, execute: func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
			var input struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, err
			}
			if input.Message != "REPLY_OK" {
				return nil, fmt.Errorf("unexpected synthetic reply %q", input.Message)
			}
			replies.Add(1)
			return json.RawMessage(`{"delivered":true}`), nil
		}},
		acceptanceTool{name: "fail_fixture", parameters: `{"type":"object","properties":{},"additionalProperties":false}`, execute: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("synthetic tool failure")
		}},
		acceptanceTool{name: "wait_fixture", parameters: `{"type":"object","properties":{},"additionalProperties":false}`, execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			select {
			case waiting <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	newSession := func(t *testing.T) (*acceptanceSession, *acceptanceSource, *acceptanceSource) {
		t.Helper()
		user, timer := newAcceptanceSource("fixture.user"), newAcceptanceSource("fixture.timer")
		r, err := wisp.New(wisp.Agent{Name: "Core acceptance", Model: "qwen", Instructions: []string{"agent.md"}, Events: []wisp.EventSource{user, timer}, Tools: tools}, wisp.Config{
			Models: map[string]wisp.Model{"qwen": budget}, Instructions: os.DirFS(directory), DatabasePath: filepath.Join(directory, "history.db"), MaxConcurrentRuns: 4, MaxModelTurns: 4, RunTimeout: 60 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		runCtx, stop := context.WithCancel(ctx)
		s := &acceptanceSession{runtime: r, server: httptest.NewServer(r.Handler()), cancel: stop, done: make(chan error, 1)}
		go func() { s.done <- r.Run(runCtx) }()
		t.Cleanup(func() { s.close(t) })
		return s, user, timer
	}
	s, user, timer := newSession(t)
	emitUser, emitTimer := acceptanceReceive(t, ctx, user.ready), acceptanceReceive(t, ctx, timer.ready)
	client := &http.Client{Timeout: 10 * time.Second}
	var description wisp.Description
	getAcceptanceJSON(t, client, s.server.URL+"/api/agent", &description)
	if len(description.Events) != 2 || len(description.Tools) != 4 || description.Model != "qwen" {
		t.Fatalf("incorrect declaration: %+v", description)
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	req, _ := http.NewRequestWithContext(streamCtx, "GET", s.server.URL+"/api/stream", nil)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	revisions := make(chan int64, 16)
	go func() {
		defer close(revisions)
		scanner := bufio.NewScanner(stream.Body)
		for scanner.Scan() {
			if !strings.HasPrefix(scanner.Text(), "data: ") {
				continue
			}
			var data struct {
				Sequence int64 `json:"sequence"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &data) != nil {
				return
			}
			select {
			case revisions <- data.Sequence:
			case <-streamCtx.Done():
				return
			}
		}
	}()
	if seq := acceptanceReceive(t, ctx, revisions); seq != 0 {
		t.Fatalf("new database has revision %d", seq)
	}

	lookupID := emitAcceptance(t, ctx, emitUser, "lookup")
	if !t.Run("concurrent_tools_runs_and_inspection", func(t *testing.T) {
		barrierCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		first, second := acceptanceReceive(t, barrierCtx, started), acceptanceReceive(t, barrierCtx, started)
		if first == second {
			t.Fatal("model requested duplicate fixture keys")
		}
		noopID := emitAcceptance(t, ctx, emitTimer, "noop")
		noop := awaitAcceptance(t, ctx, s.runtime, noopID)
		if noop.Run.Status != "completed" || strings.TrimSpace(noop.Run.Output) != "NO_ACTION" {
			t.Fatalf("no-op: %+v", noop.Run)
		}
		var active []wisp.RunRecord
		getAcceptanceJSON(t, client, s.server.URL+"/api/active-runs", &active)
		if len(active) != 1 || active[0].ID != lookupID {
			t.Fatalf("slow run not active while no-op completed: %+v", active)
		}
		var lookup wisp.History
		getAcceptanceJSON(t, client, s.server.URL+"/api/runs/"+lookupID, &lookup)
		var modelCalls, toolStarts, toolResults int
		for _, a := range lookup.Activity {
			switch a.Kind {
			case "model.started":
				modelCalls++
			case "tool.started":
				toolStarts++
			case "tool.finished":
				toolResults++
			}
		}
		if modelCalls != 1 || toolStarts != 2 || toolResults != 0 {
			t.Fatalf("turn barrier violated: model=%d starts=%d results=%d", modelCalls, toolStarts, toolResults)
		}
		for acceptanceReceive(t, ctx, revisions) == 0 {
		}
		assertFreshAcceptance(t, noop, left, right)
		t.Log("two real model tool calls overlap; independent no-op run completes while they are blocked; HTTP and SSE expose activity")
	}) {
		return
	}
	close(release)
	lookup := awaitAcceptance(t, ctx, s.runtime, lookupID)
	if lookup.Run.Status != "completed" || strings.TrimSpace(lookup.Run.Output) != left+" "+right {
		t.Fatalf("tool results not consumed: %+v", lookup.Run)
	}
	if replies.Load() != 0 {
		t.Fatal("saved model output was sent externally")
	}
	if !t.Run("explicit_reply_and_failure_isolation", func(t *testing.T) {
		failed := awaitAcceptance(t, ctx, s.runtime, emitAcceptance(t, ctx, emitUser, "failure"))
		if failed.Run.Status != "failed" || !strings.Contains(failed.Run.Error, "synthetic tool failure") {
			t.Fatalf("tool failure hidden: %+v", failed.Run)
		}
		reply := awaitAcceptance(t, ctx, s.runtime, emitAcceptance(t, ctx, emitUser, "reply"))
		if reply.Run.Status != "completed" || strings.TrimSpace(reply.Run.Output) != "PRIVATE_FINAL" || replies.Load() != 1 {
			t.Fatalf("explicit reply failed: %+v; replies=%d", reply.Run, replies.Load())
		}
		assertFreshAcceptance(t, reply, left, right)
		t.Log("tool failure remains isolated; later run replies only through its declared tool and saves final text separately")
	}) {
		return
	}
	if !t.Run("cancellation_and_disk_reopen", func(t *testing.T) {
		id := emitAcceptance(t, ctx, emitUser, "cancel")
		acceptanceReceive(t, ctx, waiting)
		s.cancel()
		h := awaitAcceptance(t, ctx, s.runtime, id)
		if h.Run.Status != "failed" || !strings.Contains(h.Run.Error, "canceled") {
			t.Fatalf("shutdown did not record cancellation: %+v", h.Run)
		}
		stopStream()
		stream.Body.Close()
		s.close(t)
		reopened, newUser, _ := newSession(t)
		stored, err := reopened.runtime.History(ctx, lookupID)
		if err != nil || stored.Run.Output != lookup.Run.Output || len(stored.Activity) != len(lookup.Activity) {
			t.Fatalf("disk history changed: %+v %v", stored, err)
		}
		noop := awaitAcceptance(t, ctx, reopened.runtime, emitAcceptance(t, ctx, acceptanceReceive(t, ctx, newUser.ready), "noop"))
		if noop.Run.Status != "completed" || strings.TrimSpace(noop.Run.Output) != "NO_ACTION" {
			t.Fatal("new run after restart failed")
		}
		assertFreshAcceptance(t, noop, left, right)
		var recent []wisp.RunRecord
		getAcceptanceJSON(t, client, reopened.server.URL+"/api/runs", &recent)
		if len(recent) != 6 {
			t.Fatalf("expected one record for each of six events, got %d", len(recent))
		}
		t.Log("cancellation persisted; SQLite reopened intact; new live run starts fresh; six events map to six runs")
	}) {
		return
	}
	t.Logf("core acceptance completed using %s: %d actual model requests", liveModel, budget.calls.Load())
}

type acceptanceModel struct {
	model wisp.Model
	calls atomic.Int32
}

func (m *acceptanceModel) Generate(ctx context.Context, r wisp.Request) (wisp.Response, error) {
	if m.calls.Add(1) > 16 {
		return wisp.Response{}, errors.New("acceptance request budget exceeded")
	}
	return m.model.Generate(ctx, r)
}

type acceptanceTool struct {
	name, parameters string
	execute          func(context.Context, json.RawMessage) (json.RawMessage, error)
}

func (t acceptanceTool) Definition() wisp.ToolDefinition {
	return wisp.ToolDefinition{Name: t.name, Description: "Synthetic acceptance fixture: " + t.name, Parameters: json.RawMessage(t.parameters)}
}
func (t acceptanceTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return t.execute(ctx, args)
}

type acceptanceSource struct {
	name  string
	ready chan wisp.Emit
}

func newAcceptanceSource(name string) *acceptanceSource {
	return &acceptanceSource{name: name, ready: make(chan wisp.Emit, 1)}
}
func (s *acceptanceSource) Definition() wisp.EventDefinition {
	return wisp.EventDefinition{Name: s.name, Description: "Synthetic acceptance facts"}
}
func (s *acceptanceSource) Run(ctx context.Context, emit wisp.Emit) error {
	s.ready <- emit
	<-ctx.Done()
	return ctx.Err()
}

type acceptanceSession struct {
	runtime *wisp.Runtime
	server  *httptest.Server
	cancel  context.CancelFunc
	done    chan error
	once    sync.Once
}

func (s *acceptanceSession) close(t *testing.T) {
	t.Helper()
	s.once.Do(func() {
		s.cancel()
		s.server.CloseClientConnections()
		s.server.Close()
		select {
		case err := <-s.done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("runtime did not stop")
			return
		}
		if err := s.runtime.Close(); err != nil {
			t.Error(err)
		}
	})
}
func acceptanceReceive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value, ok := <-ch:
		if !ok {
			t.Fatal("test stream closed unexpectedly")
		}
		return value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}
func emitAcceptance(t *testing.T, ctx context.Context, emit wisp.Emit, kind string) string {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"kind": kind})
	id, err := emit(ctx, wisp.Event{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func awaitAcceptance(t *testing.T, ctx context.Context, r *wisp.Runtime, id string) wisp.History {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		h, err := r.History(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if h.Run.Status != "running" {
			return h
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}
func getAcceptanceJSON(t *testing.T, client *http.Client, url string, out any) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("inspection returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}
func assertFreshAcceptance(t *testing.T, h wisp.History, secrets ...string) {
	t.Helper()
	for _, a := range h.Activity {
		if a.Kind != "model.started" {
			continue
		}
		var data struct {
			Request wisp.Request `json:"request"`
		}
		if err := json.Unmarshal(a.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Request.Messages) != 3 {
			t.Fatalf("initial context has %d messages instead of system/instructions/event", len(data.Request.Messages))
		}
		for _, m := range data.Request.Messages {
			for _, secret := range secrets {
				if strings.Contains(m.Content, secret) {
					t.Fatal("prior tool result leaked into fresh context")
				}
			}
		}
		return
	}
	t.Fatal("model request missing from persisted history")
}
