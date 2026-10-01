# File tools

Two ordinary `wisp.Tool` implementations, outside the runtime. Opening a
workspace does not register either capability. Declare each one explicitly:

```go
workspace, err := files.Open("./workspace")
if err != nil {
    return err
}
defer workspace.Close() // after the runtime has stopped

agent := wisp.Agent{
    Name:         "Developer",
    Model:        "qwen",
    Instructions: []string{"instructions/base.md"},
    Events:       []wisp.EventSource{messageSource}, // application-provided
    Tools:        []wisp.Tool{workspace.ReadFiles(), workspace.WriteFiles()},
}
```

Import `github.com/jacobm-gavin/wisp-agent/tools/files`. To grant read-only
access, declare only `workspace.ReadFiles()`. The scope is visible in tool
descriptions and inspection metadata. The minimal example remains dormant.

## Model contract

`read_file` arguments: `path`, optional `start_line` and `end_line` (inclusive,
1-based). Returns exact `content`, full-file `sha256`, `total_lines`, and the
returned range. Defaults to the entire file; invalid ranges fail rather than
silently truncating. Empty files return empty content and range 0–0. CRLF and
trailing newlines are preserved. A terminal newline does not add a phantom line.

`write_file` arguments: `path`, complete `content`, and `expected_sha256`.

- Creation requires an empty hash and fails if the path exists.
- Replacement requires the current full-file SHA-256 from `read_file`. A stale
  hash fails before mutation. Range reads also return the full hash; instructions
  should tell models to read all contents before replacing a file.
- Returns `path`, `bytes_written`, and the new `sha256`.
- Parent directories must exist. New files use mode 0600 (subject to umask);
  replacement retains permissions. Empty content is valid.

Both tools accept regular UTF-8 text files without NUL bytes, at most 1 MiB.
There is no automatic truncation, directory creation, listing, deletion, shell
execution, patch language, or fallback overwrite. Tool errors fail the run under
Wisp's existing semantics; there is no hidden retry.

## Boundaries and limitations

Paths must be workspace-relative without `..`, absolute paths, backslashes, or
colons. Go 1.24's `os.Root` anchors access to the opened directory and rejects
symlink traversal outside it. Relative symlinks within the workspace are allowed.
No string-prefix containment check is used.

Use a dedicated, trusted workspace. This is **not an OS sandbox**: hard links,
mounted filesystems, devices, and hostile concurrent filesystem mutation require
external isolation. Pre-open and post-open regular-file checks reject ordinary
special-file use, but not an adversary swapping in a device or FIFO between those
checks. Blocking filesystem syscalls cannot be forcibly canceled by a Go context.

Tools sharing one `Workspace` serialize filesystem operations with a cancelable
gate. This keeps their hash-check/write sequence consistent, including symlink
aliases, without changing the core's concurrent run/tool semantics. Separate
Workspaces and external processes are not coordinated; the hash check is not a
cross-process compare-and-swap guarantee.

Writes update files in place, then truncate and sync. They are **not crash-atomic**:
I/O errors or crashes may leave partial content. Cancellation is honored before
mutation and while waiting for the gate; once mutation begins, the bounded write
finishes rather than stopping midway. Keep version control/backups. Close the
workspace only after all calls finish.

## Inspiration and verification

Line-range viewing is inspired by
[Anthropic's editor example](https://github.com/anthropics/claude-quickstarts/blob/main/computer-use-demo/computer_use_demo/tools/edit.py);
complete-file replacement by [Aider's whole edit format](https://aider.chat/docs/more/edit-formats.html).
Unlike a bundled editor dispatcher, Wisp declares read and write separately. A
content hash guards common stale-overwrite mistakes. Containment follows Go's
[traversal-resistant API guidance](https://go.dev/blog/osroot).

```sh
go test -race -count=1 ./tools/files
# Optional paid Qwen test; exported OPENROUTER_API_KEY required.
WISP_OPENROUTER_LIVE=1 go test -race -v -count=1 -run TestOpenRouterLiveFileTools -timeout 2m ./tools/files
```

The live test uses only temporary files and SQLite history. It allows at most
eight requests to `qwen/qwen3.8-27b`, with 512 output tokens per request. On
2026-10-01 it passed in four model turns: read → hash-checked write → read-back →
completion, with verified disk contents and persisted tool activity.
Offline tests cover the same runtime path plus path boundaries, malformed inputs,
stale writes, concurrency, line ranges, Unicode, size limits, and cancellation.
