// Package bash provides an unrestricted Bash command tool with a declared
// working directory. The directory is not a sandbox or permission boundary.
package bash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

// MaxOutputBytes is the retained byte limit for each of stdout and stderr.
// Excess output is drained and discarded, not allowed to block the command.
const MaxOutputBytes = 64 * 1024

type commandTool struct{ directory, executable string }

// New declares where every invocation starts. The directory must already exist.
// Commands may cd elsewhere, use absolute paths, access the network, and inherit
// the host's privileges and environment. No approval or command filtering occurs.
// Supported hosts are Linux and macOS with Bash installed.
func New(directory string) (wisp.Tool, error) {
	if !supported {
		return nil, errors.New("bash tool supports Linux and macOS only")
	}
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("working directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("working directory must be a directory")
	}
	executable, err := exec.LookPath("bash")
	if err != nil {
		return nil, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	return &commandTool{directory: absolute, executable: executable}, nil
}

func (t *commandTool) Definition() wisp.ToolDefinition {
	return wisp.ToolDefinition{Name: "bash", Description: fmt.Sprintf("Run an arbitrary Bash command starting in %q. Commands are unrestricted; this directory is NOT a sandbox. Each call starts a fresh noninteractive shell; cd and variables do not persist. stdin is closed. stdout/stderr are separately capped at 65536 bytes with truncation flags. Always inspect exit_code: a nonzero exit is returned with output so you can diagnose, correct, and retry using another call. No automatic retries. Use foreground commands, not background services.", t.directory), Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Bash script to execute, including pipelines, redirects, or multiline commands"}},"required":["command"],"additionalProperties":false}`)}
}

// Result is retained in execution history, including on command failure.
// Invalid UTF-8 output is replaced during JSON encoding.
type Result struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

func (t *commandTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if len(args) != 1 || args["command"] == nil {
		return nil, errors.New("expected only the command argument")
	}
	var script string
	if err := json.Unmarshal(args["command"], &script); err != nil {
		return nil, err
	}
	if strings.TrimSpace(script) == "" || strings.ContainsRune(script, 0) {
		return nil, errors.New("command must be nonempty and contain no NUL bytes")
	}
	cmd := exec.CommandContext(ctx, t.executable, "--noprofile", "--norc", "-c", script)
	cmd.Dir = t.directory
	// Keep normal environment access but do not execute implicit startup scripts.
	cmd.Env = cmd.Environ()
	env := cmd.Env[:0]
	for _, entry := range cmd.Env {
		if !strings.HasPrefix(entry, "BASH_ENV=") && !strings.HasPrefix(entry, "ENV=") {
			env = append(env, entry)
		}
	}
	cmd.Env = env
	var stdout, stderr cappedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	configureProcess(cmd)
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		_ = killGroup(cmd)
	}
	result, _ := json.Marshal(Result{Stdout: string(stdout.data), Stderr: string(stderr.data), ExitCode: -1, StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated})
	if cmd.ProcessState != nil {
		result, _ = json.Marshal(Result{Stdout: string(stdout.data), Stderr: string(stderr.data), ExitCode: cmd.ProcessState.ExitCode(), StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated})
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	// A command reporting failure is a completed observation, not a broken tool.
	// Let the model inspect the output and choose a recovery in its next turn.
	// Signals and transport/start failures remain execution errors.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.Exited() {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("bash command failed: %w", err)
	}
	return result, nil
}

type cappedOutput struct {
	data      []byte
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := MaxOutputBytes - len(w.data)
	if len(p) > remaining {
		w.truncated = true
		p = p[:remaining]
	}
	w.data = append(w.data, p...)
	return n, nil
}
