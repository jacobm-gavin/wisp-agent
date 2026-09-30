package wisp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCrashRecoveryReleasesProcessOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashProcessHelper$")
	cmd.Env = append(os.Environ(), "WISP_CRASH_HELPER_DB="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "ready" }()
	if !receive(t, ready) {
		t.Fatal("helper failed to establish a live run")
	}
	m := modelFunc(func(context.Context, Request) (Response, error) { return Response{}, nil })
	if other, err := New(declaration(), config(m, path)); err == nil {
		other.Close()
		t.Fatal("another process acquired the live database")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected forced termination")
	}
	r := newRuntime(t, declaration(), m, path)
	defer r.Close()
	h, err := r.History(context.Background(), "interrupted")
	if err != nil || h.Run.Status != "failed" || len(h.Activity) != 2 {
		t.Fatalf("crash recovery lost history: %+v %v", h, err)
	}
}

func TestCrashProcessHelper(t *testing.T) {
	path := os.Getenv("WISP_CRASH_HELPER_DB")
	if path == "" {
		t.Skip("subprocess fixture")
	}
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.accept(context.Background(), "interrupted", "event", "fixture", Event{Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	// The parent kills this process, simulating a crash with the DB still open.
	select {}
}
