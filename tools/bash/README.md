# Bash tool

An ordinary `wisp.Tool` for arbitrary Bash commands. No command allowlist,
approval flow, or safety gating is applied. Linux and macOS require Bash on PATH.

```go
import "github.com/jacobm-gavin/wisp-agent/tools/bash"

shell, err := bash.New("./workspace") // existing directory, resolved at construction
if err != nil {
    return err
}

// In your Agent declaration:
Tools: []wisp.Tool{shell},
```

The path is the working directory for each invocation and appears in inspection
metadata. It is **not a security boundary**: commands can use `cd`, absolute paths,
the network, and the host user's permissions. They inherit environment variables,
including secrets. Run only in a trusted environment; isolation and safety gating
are future work. Importing this package does not grant a capability automatically.

The model calls `bash` with `{"command":"pwd; git status --short"}`. Scripts,
pipelines, redirects, and multiline commands are passed unchanged to Bash `-c`.
Each call starts a fresh noninteractive shell in the declared directory; shell
variables and directory changes do not persist. Stdin is EOF. Profile/rc loading
and the `BASH_ENV`/`ENV` startup hooks are disabled; otherwise the host environment
is inherited. Default Bash exit semantics apply (no implicit `set -e` or pipefail).

The JSON result contains `stdout`, `stderr`, `exit_code`, `stdout_truncated`, and
`stderr_truncated`. Each output stream retains at most 64 KiB; excess is drained
and discarded. Invalid UTF-8 is replaced during JSON encoding. Nonzero exit is a
tool error and fails the run under Wisp's normal semantics; captured output is
still recorded in history. Start failures use exit code -1. There are no retries.

Calls can run concurrently and share filesystem state without locking. Configure
`wisp.Config.RunTimeout` or another caller deadline to bound command duration.
Cancellation kills the shell's process group, including ordinary child processes.
Output-pipe waits are bounded. This is not a background-service manager or process
sandbox: detached children that escape the group are not guaranteed to be stopped.
No changes to Wisp's runtime or automatic registration are needed.

Verify with `go test -race -count=1 ./tools/bash`.
