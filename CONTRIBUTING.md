# Development principles

[Wisp_Agent_Architecture.md](Wisp_Agent_Architecture.md) is the design authority,
unless an explicit later decision supersedes it. Read sections 24–28 before
changing the architecture. Wisp is a minimal harness, not an everything framework.

## Before adding a feature

Apply the five design tests in section 25:

1. Is it an action (Tool), a wakeup (EventSource), or runtime configuration?
2. Can it be implemented outside the core using those existing primitives?
3. Can a reader still understand the agent declaration in thirty seconds?
4. What current requirement requires a new abstraction? Speculation is not enough.
5. Are capabilities still determined by explicit source and configuration, rather
   than hidden registration, installation, or mutable UI state?

Declare each distinct capability and wakeup separately. Keep provider credentials,
transport mechanics, and execution limits outside the agent declaration. Events
carry facts; persistent behavioral policy belongs in declared Markdown files.
These are authoring/review obligations: arbitrary Go integrations are trusted code,
and Wisp cannot prove their descriptions are honest or sandbox their effects.

## Implementation method

- Build small end-to-end slices using ordinary Go and narrow interfaces. Add a
  dependency or abstraction only when a concrete need earns it.
- Keep model protocol details in adapters and resource-specific synchronization
  inside Tools. Do not add a generic isolation or scheduling framework.
- Test observable invariants, not just helper functions. Use synchronization
  barriers to prove overlap, wait-for-all behavior, and stable call/result IDs.
- Preserve fresh per-run context, one accepted event per run, visible failures,
  and saved final output without implicit delivery. Never add automatic history
  retrieval, retries of external effects, or crash replay as hidden conveniences.
- Derive capability inspection from the declaration and behavior from persisted
  history. Do not maintain a separate capability registry for the UI.
- Simplify after the slice works. No planners, workflow graphs, cross-run tasks,
  plugin loaders, prompt DSLs, or multi-agent orchestration without an explicit
  architectural decision supported by a concrete use case.

Keep tests beside their Go packages. Run `go test -race ./...`, `go vet ./...`,
and `CGO_ENABLED=0 go build ./...`; format changed Go files with `gofmt`.
The opt-in paid live suite and its bounds are documented in the
[acceptance record](docs/core-v0.1-acceptance.md). Never commit credentials.

## Scope and review

The current milestone is the framework core, not the full developer-agent
prototype in sections 20 and 27. Read/write file Tools now live outside the core
in `tools/files`. Command/reply Tools, user/timer sources, and a message-submission
interface remain deferred. Synthetic fixtures prove runtime behavior, not those
remaining integrations.
Typed Tool/schema helpers are an ergonomic direction, not a frozen requirement;
let real implementations establish the need before expanding the API.

For each change, identify the affected architecture section and update its
invariant tests and the [implementation notes](docs/core.md) when appropriate.
Record departures explicitly; do not silently rewrite the architecture to match
the implementation or call the full prototype complete based only on core tests.
