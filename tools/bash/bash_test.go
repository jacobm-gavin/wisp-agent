package bash

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

func newTool(t *testing.T) (wisp.Tool, string) {
	t.Helper()
	if !supported {
		t.Skip("unsupported host")
	}
	dir := t.TempDir()
	tool, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tool, dir
}
func execute(t *testing.T, tool wisp.Tool, ctx context.Context, script string) (Result, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"command": script})
	out, err := tool.Execute(ctx, raw)
	var result Result
	if len(out) > 0 {
		if e := json.Unmarshal(out, &result); e != nil {
			t.Fatal(e)
		}
	}
	return result, err
}

func TestCommandsAndWorkingDirectory(t *testing.T) {
	tool, dir := newTool(t)
	if !strings.Contains(tool.Definition().Description, filepath.Base(dir)) {
		t.Fatal("scope missing from declaration metadata")
	}
	result, err := execute(t, tool, context.Background(), "printf 'hello\\n' > 'file with spaces'; tr 'a-z' 'A-Z' < 'file with spaces'; printf 'warning' >&2; pwd -P")
	canonical, _ := filepath.EvalSymlinks(dir)
	if err != nil || result.Stdout != "HELLO\n"+canonical+"\n" || result.Stderr != "warning" || result.ExitCode != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "file with spaces"))
	if err != nil || string(b) != "hello\n" {
		t.Fatalf("file: %q %v", b, err)
	}
	result, err = execute(t, tool, context.Background(), "value=first\nfor item in a b; do printf '%s' \"$item\"; done\ncd /\nprintf '%s' \"$value\"")
	if err != nil || result.Stdout != "abfirst" {
		t.Fatalf("multiline: %+v %v", result, err)
	}
	result, err = execute(t, tool, context.Background(), "printf '%s' \"${value-unset}\"; pwd -P")
	if err != nil || result.Stdout != "unset"+canonical+"\n" {
		t.Fatalf("shell state leaked: %+v %v", result, err)
	}
}
func TestExitFailureAndStdin(t *testing.T) {
	tool, _ := newTool(t)
	result, err := execute(t, tool, context.Background(), "printf before; printf failure >&2; exit 7")
	if err == nil || result.ExitCode != 7 || result.Stdout != "before" || result.Stderr != "failure" {
		t.Fatalf("%+v %v", result, err)
	}
	result, err = execute(t, tool, context.Background(), "cat; printf done")
	if err != nil || result.Stdout != "done" {
		t.Fatalf("stdin not EOF: %+v %v", result, err)
	}
	result, err = execute(t, tool, context.Background(), "if then")
	if err == nil || result.ExitCode == 0 || result.Stderr == "" {
		t.Fatal("syntax error not reported")
	}
}
func TestValidation(t *testing.T) {
	tool, _ := newTool(t)
	for _, raw := range []string{`null`, `[]`, `{}`, `{"command":null}`, `{"command":1}`, `{"command":" "}`, `{"command":"a\u0000b"}`, `{"Command":"true"}`, `{"command":"true","cwd":"/"}`, `{"command":"true"} {}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := New(""); err == nil {
		t.Fatal("empty directory accepted")
	}
	if _, err := New(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing directory accepted")
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path); err == nil {
		t.Fatal("file accepted as directory")
	}
}
func TestOutputBounds(t *testing.T) {
	tool, _ := newTool(t)
	result, err := execute(t, tool, context.Background(), "head -c 70000 /dev/zero | tr '\\000' x; head -c 70000 /dev/zero | tr '\\000' y >&2")
	if err != nil || len(result.Stdout) != MaxOutputBytes || len(result.Stderr) != MaxOutputBytes || !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("output bounds: stdout=%d stderr=%d %v", len(result.Stdout), len(result.Stderr), err)
	}
}
func TestCancellation(t *testing.T) {
	tool, dir := newTool(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := execute(t, tool, ctx, "touch should-not-exist"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("canceled command ran")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := execute(t, tool, ctx, "sleep 20 & wait")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("process group cancellation failed: %v", err)
	}
}
func TestConcurrentCommands(t *testing.T) {
	tool, _ := newTool(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := execute(t, tool, context.Background(), "printf independent")
			if err != nil || result.Stdout != "independent" {
				t.Errorf("%+v %v", result, err)
			}
		}()
	}
	wg.Wait()
}
func TestNoImplicitStartupScript(t *testing.T) {
	tool, _ := newTool(t)
	path := filepath.Join(t.TempDir(), "startup")
	if err := os.WriteFile(path, []byte("printf hidden"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASH_ENV", path)
	result, err := execute(t, tool, context.Background(), "printf explicit")
	if err != nil || result.Stdout != "explicit" {
		t.Fatalf("%+v %v", result, err)
	}
}
