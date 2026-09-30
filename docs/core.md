# Core implementation notes

The [architecture specification](../Wisp_Agent_Architecture.md) is authoritative.
The core implements its runtime with a separate OpenRouter model adapter. Real
tools and event integrations remain future work. The browser is an inspection surface;
there is no implicit chat capability.

## Boundaries

The public package is `wisp`. The runtime keeps provider-neutral transcripts and
owns SQLite. Tools and event sources use ordinary Go interfaces and never receive
the history store. `Agent` contains semantic declarations; `Config` supplies the
model registry, instruction filesystem, and database path. Only the named model is
used. No registry auto-discovers capabilities.

The core package is intentionally flat. `runtime.go` owns lifecycle and event intake,
`run.go` owns the model/tool loop, `store.go` owns history, and `web.go` exposes
inspection. Interfaces for storage, middleware, scheduling, and plugins have not
been introduced.

`model/openrouter` translates the public model contract to OpenRouter's
[chat-completions protocol](https://openrouter.ai/docs/guides/features/tool-calling).
Provider credentials, the remote model ID, HTTP settings, and token limits stay
in adapter configuration. The adapter disables reasoning and supports text and
function tools. It does not add retries or inject any previous run context.

## Decisions

- Event source metadata names exactly one wakeup type. The runtime assigns that
  name to emitted events, so a source cannot silently impersonate another type.
- Acceptance stores an event, its run, and its initial activity in one transaction.
  An emitter receives the run ID only after commit. This is an acceptance contract,
  not an exactly-once transport guarantee. Sources must handle returned errors.
- SQLite uses one operational connection and WAL. Foreign keys and busy timeout
  are configured on every connection. In-memory tests retain a separate connection
  so cancellation cannot destroy the database when an operational connection closes.
- Model requests include the exact startup context and tool definitions in the
  journal. Responses and tool results are recorded separately, with timestamps,
  turn numbers, and tool call IDs. This supports reconstruction without loading
  history into new model requests.
- A tool batch is structurally validated before any call executes. Calls are
  journaled before dispatch. Results are journaled as they settle and supplied to
  the next model turn in the model's original call order. Tools validate their own
  arguments against their declared JSON schemas; typed schema generation is deferred.
- A failing tool does not abandon sibling calls. The runtime waits for the entire
  batch, records its results, then fails the run. It does not retry external effects.
- Model/tool panics become run failures. Source failures stop the runtime and are
  returned to the caller. Failure to persist a terminal state also stops the runtime;
  interrupted work remains detectable on reopening.
- Shutdown cancels active work and joins it before returning. Result/terminal
  writes have a separate five-second persistence context so cancellation can be
  recorded. Cooperative implementations are required; arbitrary Go code cannot be
  forcibly stopped. No hidden resume or recovery loop is provided.
- SSE sends a persisted activity revision. Clients refetch snapshots, including on
  reconnect. The database is the source of truth; no parallel event-history buffer
  or notification subsystem is required.

## Acceptance coverage

| Requirement | Evidence |
| --- | --- |
| Fresh ordered context, two event types, concurrent runs, no implicit response | `TestFreshConcurrentRunsAndExplicitCommunication` |
| Parallel calls, wait-for-all barrier, correct call IDs | `TestParallelToolsWaitForAllAndPreserveCallIDs` |
| Explicit communication over multiple turns | `TestResponseToolIsExplicitAndMultiTurn` |
| Model/tool failure and panic isolation, undeclared capabilities rejected | `TestRunFailuresAreRecordedAndDoNotStopOtherRuns` |
| Failed batch settles all siblings | `TestFailedToolStillWaitsForSibling` |
| Cancellation stops intake and persists failures | `TestShutdownCancelsAndSettlesRuns` |
| Durable history, interrupted recovery, no memory across restart | `TestPersistenceAndInterruptedRecovery` |
| Atomic acceptance, one run per stored event | `TestAcceptanceIsAtomic` |
| Persistence gates effects and reports terminal write failures | `TestPersistenceFailurePreventsToolEffects`, `TestTerminalPersistenceFailureIsReturned` |
| Declaration validation and immutable metadata/context snapshots | `TestAcceptanceAndDeclarationValidation`, `TestDeclarationAndRequestSnapshots` |
| Read-only UI/API, SSE revision and reconnect | `TestInspectionHTTPAndSSE` |

The full prototype's real file/command/reply tools and user/timer integrations
remain outside this milestone. `TestOpenRouterLive` is an opt-in network check for
`qwen/qwen3.8-27b`; it verifies text completion and an entire persisted run
using two synthetic tool results. Offline adapter tests verify wire encoding,
multiple calls, cancellation, errors, redaction, redirects, and truncation.
Resource isolation, multi-agent orchestration, planners,
plugins, automatic memory, and workflow engines remain non-goals.
