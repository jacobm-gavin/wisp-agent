// Package files provides explicitly declared, workspace-scoped UTF-8 file tools.
// It is an integration, not part of the runtime or a general filesystem sandbox.
package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

// MaxBytes bounds both file contents and write inputs to one MiB.
const MaxBytes = 1 << 20

// Workspace owns a directory handle. Reuse one Workspace for cooperating tools;
// Close it only after the runtime stops. Its lock does not coordinate other
// processes or separately opened Workspaces.
type Workspace struct {
	root *os.Root
	gate chan struct{}
}

func Open(directory string) (*Workspace, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &Workspace{root: root, gate: make(chan struct{}, 1)}, nil
}

func (w *Workspace) Close() error { return w.root.Close() }

// ReadFiles grants reading only; opening a Workspace grants no model capability.
func (w *Workspace) ReadFiles() wisp.Tool { return fileTool{w: w} }

// WriteFiles grants creating and replacing file contents, not reading contents.
func (w *Workspace) WriteFiles() wisp.Tool { return fileTool{w: w, write: true} }

type fileTool struct {
	w     *Workspace
	write bool
}

func (t fileTool) Definition() wisp.ToolDefinition {
	if t.write {
		return wisp.ToolDefinition{Name: "write_file", Description: fmt.Sprintf("Create or replace a UTF-8 text file inside %q. Supply complete content, not a diff. expected_sha256 must be empty for a new file; to replace, use the full-file sha256 returned by read_file. Parents must exist. Maximum 1 MiB. No automatic retries; writes are not crash-atomic.", t.w.root.Name()), Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace-relative file path; no .. components"},"content":{"type":"string","description":"Complete new UTF-8 contents"},"expected_sha256":{"type":"string","description":"Empty for creation; previous full-file hash for replacement"}},"required":["path","content","expected_sha256"],"additionalProperties":false}`)}
	}
	return wisp.ToolDefinition{Name: "read_file", Description: fmt.Sprintf("Read a UTF-8 text file inside %q, at most 1 MiB. Returns exact content for an optional inclusive 1-based line range, full-file sha256, and total_lines. Defaults to all lines. No directory listing or binary files.", t.w.root.Name()), Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace-relative file path; no .. components"},"start_line":{"type":"integer","minimum":1},"end_line":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`)}
}

type arguments struct {
	Path     string  `json:"path"`
	Content  *string `json:"content,omitempty"`
	Expected *string `json:"expected_sha256,omitempty"`
	Start    *int    `json:"start_line,omitempty"`
	End      *int    `json:"end_line,omitempty"`
}

func (t fileTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) > 6*MaxBytes+4096 || !utf8.Valid(raw) {
		return nil, errors.New("invalid or oversized arguments")
	}
	var a arguments
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("expected one JSON object")
	}
	if !fs.ValidPath(a.Path) || a.Path == "." || strings.ContainsAny(a.Path, "\\:\x00") {
		return nil, errors.New("path must be a relative file path without traversal")
	}
	// Validate presence and tool-specific fields, including explicit nulls.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for name, value := range fields {
		allowed := name == "path" || (t.write && (name == "content" || name == "expected_sha256")) || (!t.write && (name == "start_line" || name == "end_line"))
		if !allowed {
			return nil, fmt.Errorf("unexpected field %s", name)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("%s cannot be null", name)
		}
	}
	if t.write && (a.Content == nil || a.Expected == nil) {
		return nil, errors.New("content and expected_sha256 are required")
	}
	if a.Start != nil && *a.Start < 1 || a.End != nil && *a.End < 1 {
		return nil, errors.New("line numbers must be positive")
	}
	select {
	case t.w.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-t.w.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.write {
		return t.replace(ctx, a)
	}
	data, err := t.w.read(a.Path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start, end := 1, len(lines)
	if a.Start != nil {
		start = *a.Start
	}
	if a.End != nil {
		end = *a.End
	}
	if len(lines) == 0 && a.Start == nil && a.End == nil {
		start = 0
	} else if start > len(lines) || end > len(lines) || end < start {
		return nil, errors.New("line range is outside the file or reversed")
	}
	content := ""
	if start > 0 {
		content = strings.Join(lines[start-1:end], "")
	}
	return json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		SHA     string `json:"sha256"`
		Total   int    `json:"total_lines"`
		Start   int    `json:"start_line"`
		End     int    `json:"end_line"`
	}{a.Path, content, digest(data), len(lines), start, end})
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func textFile(b []byte) bool { return utf8.Valid(b) && !bytes.ContainsRune(b, 0) }

func (w *Workspace) read(path string) ([]byte, error) {
	info, err := w.root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("only regular files are supported")
	}
	f, err := w.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readFile(f)
}
func readFile(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("only regular files are supported")
	}
	if info.Size() > MaxBytes {
		return nil, errors.New("file exceeds 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, errors.New("file exceeds 1 MiB")
	}
	if !textFile(data) {
		return nil, errors.New("file must be UTF-8 text without NUL bytes")
	}
	return data, nil
}

func (t fileTool) replace(ctx context.Context, a arguments) (json.RawMessage, error) {
	data := []byte(*a.Content)
	if len(data) > MaxBytes || !textFile(data) {
		return nil, errors.New("content must be UTF-8 text without NUL bytes, at most 1 MiB")
	}
	var f *os.File
	var err error
	if *a.Expected == "" {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		f, err = t.w.root.OpenFile(a.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	} else {
		if len(*a.Expected) != 64 {
			return nil, errors.New("expected_sha256 must be a full SHA-256 hex digest")
		}
		info, statErr := t.w.root.Stat(a.Path)
		if statErr != nil {
			return nil, statErr
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("only regular files are supported")
		}
		f, err = t.w.root.OpenFile(a.Path, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if *a.Expected != "" {
		old, err := readFile(f)
		if err != nil {
			return nil, err
		}
		if digest(old) != *a.Expected {
			return nil, errors.New("file changed: read it again before replacing")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	// Once mutation starts, finish the bounded write rather than cancel midway.
	// OS errors/crashes can leave partial contents; no atomicity is promised.
	n, err := f.WriteAt(data, 0)
	if err != nil {
		return nil, fmt.Errorf("write may be partial: %w", err)
	}
	if n != len(data) {
		return nil, io.ErrShortWrite
	}
	if err = f.Truncate(int64(len(data))); err != nil {
		return nil, fmt.Errorf("truncate failed after write: %w", err)
	}
	if err = f.Sync(); err != nil {
		return nil, fmt.Errorf("sync failed after write: %w", err)
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Path  string `json:"path"`
		Bytes int    `json:"bytes_written"`
		SHA   string `json:"sha256"`
	}{a.Path, len(data), digest(data)})
}
