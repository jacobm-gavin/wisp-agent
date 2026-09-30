package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

type unusedModel struct{}

func (unusedModel) Generate(context.Context, wisp.Request) (wisp.Response, error) {
	return wisp.Response{}, errors.New("dormant agent must not invoke its model")
}

func TestServeCancelsOpenStreamsAndReleasesRuntime(t *testing.T) {
	r, err := wisp.New(Agent, wisp.Config{Models: map[string]wisp.Model{"qwen": unusedModel{}}, Instructions: instructions, DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, r, listener) }()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + listener.Addr().String() + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() {
		t.Fatal("SSE failed to connect")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown did not join the open SSE connection")
	}
	if err := r.Close(); err != nil {
		t.Fatal("runtime still active after server shutdown", err)
	}
}

func TestServerFailureStopsRuntime(t *testing.T) {
	r, err := wisp.New(Agent, wisp.Config{Models: map[string]wisp.Model{"qwen": unusedModel{}}, Instructions: instructions, DatabasePath: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := serve(ctx, r, listener); err == nil {
		t.Fatal("listener failure was hidden")
	}
	if err := r.Close(); err != nil {
		t.Fatal("runtime was not stopped", err)
	}
}

func TestStartupRejectsPublicBindAndMissingCredentials(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	if err := run(context.Background(), ":memory:", "0.0.0.0:0", "test"); err == nil {
		t.Fatal("public inspection bind accepted")
	}
	if err := run(context.Background(), ":memory:", "127.0.0.1:0", "test"); err == nil {
		t.Fatal("missing credentials accepted")
	}
}
