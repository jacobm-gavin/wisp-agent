package files

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

func workspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, dir
}
func call(t *testing.T, tool wisp.Tool, args any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestCreateReadReplace(t *testing.T) {
	w, dir := workspace(t)
	read, write := w.ReadFiles(), w.WriteFiles()
	if read.Definition().Name != "read_file" || write.Definition().Name != "write_file" {
		t.Fatal("capabilities must be separate")
	}
	original := "hello\r\n世界\nlast"
	made := call(t, write, map[string]any{"path": "a.txt", "content": original, "expected_sha256": ""})
	got := call(t, read, map[string]any{"path": "a.txt"})
	if got["content"] != original || got["sha256"] != made["sha256"] || got["total_lines"] != float64(3) {
		t.Fatal(got)
	}
	part := call(t, read, map[string]any{"path": "a.txt", "start_line": 2, "end_line": 2})
	if part["content"] != "世界\n" || part["sha256"] != got["sha256"] {
		t.Fatal(part)
	}
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0700); err != nil {
		t.Fatal(err)
	}
	call(t, write, map[string]any{"path": "a.txt", "content": "x", "expected_sha256": got["sha256"]})
	b, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(b) != "x" {
		t.Fatalf("%q %v", b, err)
	}
	info, err := os.Stat(filepath.Join(dir, "a.txt"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("permissions changed")
	}
	call(t, write, map[string]any{"path": "a.txt", "content": "", "expected_sha256": digest([]byte("x"))})
	if got := call(t, read, map[string]any{"path": "a.txt"}); got["content"] != "" || got["total_lines"] != float64(0) {
		t.Fatal("empty replacement failed", got)
	}
}
func TestEmptyAndLineRanges(t *testing.T) {
	w, _ := workspace(t)
	for _, content := range []string{"", "\n", "a\n", "a\nb", "a\n\n"} {
		t.Run(strings.ReplaceAll(content, "\n", "N"), func(t *testing.T) {
			name := "file" + digest([]byte(content))
			call(t, w.WriteFiles(), map[string]any{"path": name, "content": content, "expected_sha256": ""})
			got := call(t, w.ReadFiles(), map[string]any{"path": name})
			if got["content"] != content {
				t.Fatal(got)
			}
		})
	}
}
func TestRejectedInputsDoNotMutate(t *testing.T) {
	w, dir := workspace(t)
	call(t, w.WriteFiles(), map[string]any{"path": "safe", "content": "keep", "expected_sha256": ""})
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"path":"safe"}`, `{"path":"safe","content":null,"expected_sha256":""}`,
		`{"path":"safe","content":"bad","expected_sha256":""}`,
		`{"path":"safe","content":"bad","expected_sha256":"stale"}`,
		`{"path":"safe","content":"bad","expected_sha256":"` + strings.Repeat("0", 64) + `"}`,
		`{"path":"safe","content":"bad","expected_sha256":"","extra":true}`,
		`{"Path":"safe","content":"bad","expected_sha256":""}`,
		`{"path":"missing/child","content":"bad","expected_sha256":""}`,
		`{"path":"safe","content":"bad","expected_sha256":"","start_line":1}`,
		`{"path":"new","content":"\u0000","expected_sha256":""}`,
		`{"path":"safe","content":"bad","expected_sha256":""} {}`,
	} {
		if _, err := w.WriteFiles().Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"path":"safe","start_line":0}`, `{"path":"safe","start_line":2}`, `{"path":"safe","end_line":2}`, `{"path":"safe","start_line":null}`, `{"path":"safe","content":"x"}`, `{"path":"missing"}`, `{"path":"safe","start_line":1.5}`} {
		if _, err := w.ReadFiles().Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "safe"))
	if string(b) != "keep" {
		t.Fatal("rejected write mutated file")
	}
}

func TestCancellationWhileWaitingAndClosedWorkspace(t *testing.T) {
	w, _ := workspace(t)
	w.gate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := w.ReadFiles().Execute(ctx, json.RawMessage(`{"path":"file"}`))
	<-w.gate
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gate wait ignored cancellation: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteFiles().Execute(context.Background(), json.RawMessage(`{"path":"file","content":"x","expected_sha256":""}`)); err == nil {
		t.Fatal("closed workspace accepted a write")
	}
}
func TestWorkspaceBoundaries(t *testing.T) {
	w, dir := workspace(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "secret")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", target, "escape/secret", "escape/new", "a/../../secret", ".", "a\\b", "/etc/passwd"} {
		for _, tool := range []wisp.Tool{w.ReadFiles(), w.WriteFiles()} {
			args := map[string]any{"path": path}
			if tool.Definition().Name == "write_file" {
				args["content"] = "bad"
				args["expected_sha256"] = ""
			}
			raw, _ := json.Marshal(args)
			if _, err := tool.Execute(context.Background(), raw); err == nil {
				t.Errorf("accepted %s: %s", tool.Definition().Name, path)
			}
		}
	}
	// Replacement through an escaping link must also fail, even with the right hash.
	raw, _ := json.Marshal(map[string]any{"path": "escape/secret", "content": "bad", "expected_sha256": digest([]byte("outside"))})
	if _, err := w.WriteFiles().Execute(context.Background(), raw); err == nil {
		t.Fatal("escaped replacement")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "outside" {
		t.Fatal("outside file modified")
	}
	call(t, w.WriteFiles(), map[string]any{"path": "inside", "content": "ok", "expected_sha256": ""})
	if err := os.Symlink("inside", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if got := call(t, w.ReadFiles(), map[string]any{"path": "alias"}); got["content"] != "ok" {
		t.Fatal(got)
	}
}
func TestLimitsAndFileTypes(t *testing.T) {
	w, dir := workspace(t)
	for name, data := range map[string][]byte{"large": []byte(strings.Repeat("a", MaxBytes+1)), "binary": {0, 1, 2}, "invalid": {0xff}} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"path": name})
		if _, err := w.ReadFiles().Execute(context.Background(), raw); err == nil {
			t.Fatal("accepted", name)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadFiles().Execute(context.Background(), json.RawMessage(`{"path":"directory"}`)); err == nil {
		t.Fatal("read directory")
	}
	raw, _ := json.Marshal(map[string]any{"path": "new", "content": strings.Repeat("a", MaxBytes+1), "expected_sha256": ""})
	if _, err := w.WriteFiles().Execute(context.Background(), raw); err == nil {
		t.Fatal("oversized write")
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatal("created oversized file")
	}
	call(t, w.WriteFiles(), map[string]any{"path": "limit", "content": strings.Repeat("a", MaxBytes), "expected_sha256": ""})
	got := call(t, w.ReadFiles(), map[string]any{"path": "limit"})
	if len(got["content"].(string)) != MaxBytes {
		t.Fatal("boundary truncated")
	}
}
func TestCancellationAndConcurrentStaleWrites(t *testing.T) {
	w, dir := workspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.WriteFiles().Execute(ctx, json.RawMessage(`{"path":"canceled","content":"x","expected_sha256":""}`)); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := os.Stat(filepath.Join(dir, "canceled")); !os.IsNotExist(err) {
		t.Fatal("canceled write created file")
	}
	original := call(t, w.WriteFiles(), map[string]any{"path": "shared", "content": "old", "expected_sha256": ""})
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(map[string]any{"path": "shared", "content": "new", "expected_sha256": original["sha256"]})
			if _, err := w.WriteFiles().Execute(context.Background(), raw); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("got %d successful stale writes", successes.Load())
	}
}
