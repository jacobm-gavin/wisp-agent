# Core v0.1 acceptance

**Result: passed.** Verified on 2026-09-30 UTC using OpenRouter model
`qwen/qwen3.8-27b`, with Go's race detector enabled. Runtime implementation baseline:
`f577deb`; the acceptance suite adds tests and documentation without changing the
runtime.

This milestone is the framework core. It does not claim completion of the
architecture's full developer-agent prototype, which still needs real tools,
event integrations, and a message-submission/reply interface. It is not a claim
of production readiness or provider-independent reliability.

## Live evidence

`TestOpenRouterLiveCoreE2E` used eight real model requests across six event-created
runs. Two synthetic EventSources and four synthetic Tools were explicitly declared.
Instructions were loaded from a Markdown file, history used a real SQLite file,
and inspection ran through a local HTTP server. No real external tool actions
were granted.

| Check | Observed result |
| --- | --- |
| Parallel tool execution | Qwen requested two fixture calls in the same turn; both entered execution before either was released. |
| Turn barrier | While those calls were blocked, the journal contained one model request, two tool starts, and no tool results. |
| Concurrent independent runs | A second source emitted a no-op event; its live model run completed while the first run's tools remained blocked. |
| Result association | The first run returned both randomly generated fixture tokens in the correct order after consuming tool results. |
| Fresh context | Persisted initial requests contained only runtime instructions, declared Markdown, and the current event; prior fixture tokens were absent. |
| Explicit communication | Lookup output caused no reply. A later run invoked the synthetic reply tool once and saved a distinct private final output. |
| Failure isolation | A model-requested tool failed visibly; a subsequent live run completed successfully. |
| Cooperative cancellation | Cancellation interrupted a model-requested waiting tool and persisted the failed run before shutdown. |
| HTTP/SSE inspection | HTTP returned declared capabilities, active runs, full activity, and recent history; SSE advanced its persisted revision. |
| Durable history | Closing and reopening SQLite preserved completed output and activity. A new live run after reopening started fresh. |
| One accepted event per run | The six submitted events produced exactly six stored runs. |

The original `TestOpenRouterLive` also passed: one plain completion and a two-turn,
two-tool round trip (three additional model requests).

## Deterministic checks

The offline suite covers cases that should not depend on model choices: malformed
responses, undeclared tools, error/panic boundaries, bounded intake, turn limits,
deadlines, cancellation under contention, atomic writes, database ownership across
processes, forced-crash recovery, rejected database versions, and server shutdown
with active SSE connections. The test-to-requirement map is in [core notes](core.md).

Verification commands:

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./...
WISP_OPENROUTER_LIVE=1 go test -race -v -count=1 -run TestOpenRouterLive -timeout 6m ./model/openrouter
```

Export `OPENROUTER_API_KEY` to run the last command. Live tests are opt-in and
bounded; they make no automatic retries. Temporary databases contain synthetic
test data and are removed by Go's test cleanup. API credentials are not written
to prompts, test artifacts, or execution history.

Core v0.1 is ready to be exercised by real integrations through its public Go
interfaces. Remaining work should be driven by those integrations; automatic
memory, workflow engines, resource isolation, and multi-agent orchestration remain
outside this milestone.
