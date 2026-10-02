// The chat example grants only web-message intake and explicit UI replies.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
	"github.com/jacobm-gavin/wisp-agent/model/openrouter"
)

//go:embed instructions/*.md
var instructions embed.FS

func main() {
	database := flag.String("db", "wisp-chat.db", "persistent SQLite history file")
	address := flag.String("listen", "127.0.0.1:18081", "loopback inspection address")
	modelID := flag.String("model", "qwen/qwen3.8-27b", "OpenRouter model ID")
	workspace := flag.String("workspace", "", "optional existing directory: grants read/write and unrestricted Bash")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWithWorkspace(ctx, *database, *address, *modelID, *workspace); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, database, address, modelID string) (err error) {
	return runWithWorkspace(ctx, database, address, modelID, "")
}

func runWithWorkspace(ctx context.Context, database, address, modelID, workspace string) (err error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return errors.New("inspection address must use a loopback IP (127.0.0.1 or ::1)")
	}
	model, err := openrouter.New(openrouter.Config{APIKey: os.Getenv("OPENROUTER_API_KEY"), Model: modelID})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	agent := Agent
	if workspace != "" {
		var closeWorkspace func() error
		agent, closeWorkspace, err = workspaceAgent(workspace)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, closeWorkspace()) }()
	}
	runtime, err := wisp.New(agent, wisp.Config{
		Models: map[string]wisp.Model{"qwen": model}, Instructions: instructions, DatabasePath: database,
		MaxConcurrentRuns: 16, MaxModelTurns: 64, RunTimeout: 5 * time.Minute,
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close()) }()
	log.Printf("Wisp temporary chat: http://%s", listener.Addr())
	return serve(ctx, runtime, listener)
}

// serve owns both lifetimes: a failure in either stops the other, and all HTTP
// requests (including SSE) finish before the caller closes runtime storage.
func serve(parent context.Context, runtime *wisp.Runtime, listener net.Listener) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	mux := http.NewServeMux()
	mux.Handle("/api/chat", chat.Handler())
	mux.Handle("/api/chat/", chat.Handler())
	mux.Handle("/", runtime.Handler())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != listener.Addr().String() {
			http.Error(w, "use the declared loopback address", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	httpDone := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		httpDone <- err
		cancel()
	}()
	runtimeErr := runtime.Run(ctx)
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	if err := <-httpDone; err != nil {
		runtimeErr = errors.Join(runtimeErr, fmt.Errorf("inspection server: %w", err))
	}
	return errors.Join(runtimeErr, shutdownErr)
}
