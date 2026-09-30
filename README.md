# Wisp Agent

**Wisp is a Go agent framework and runtime library. Developers import Wisp to
build their own persistent agent applications.**

Wisp supplies the execution machinery: model calls, fresh run contexts, tool
execution, concurrency, SQLite history, and inspection. You define the agent:

- Construct an `Agent` declaring its model, Markdown instructions, event sources,
  and tools.
- Implement or import `EventSource`s that detect something and emit `Event`
  values containing facts.
- Implement or import `Tool`s exposing individual capabilities the model can
  invoke.

An `Agent` is a declaration, an `Event` is data, and `EventSource` and `Tool` are
Go interfaces. Integrations are ordinary Go code. Every capability must be
explicitly declared; importing a package does not grant it to the agent.

Each event starts a fresh run. The model requests tools, receives their results,
and continues until it returns no tool calls. Runs can overlap; tool calls from
one model turn execute concurrently. SQLite records what happened without turning
that history into automatic memory.

## Status

Early core implementation; the API may change. Includes declaration validation,
instruction loading, run execution, cancellation, SQLite history, and an embedded
read-only activity UI with SSE updates. Models, tools, and event sources are Go
interfaces. An OpenRouter model adapter is included; real tool and event
integrations are not bundled.

This repository develops the framework. The root package is a **library**, and
your agent is an application built with it. A runnable example host lives in
[examples/minimal](examples/minimal), with its declaration in
[agent.go](examples/minimal/agent.go). Its Events and Tools lists are deliberately
empty: it starts the runtime and inspection UI but has no way to wake or act.

## Run the minimal agent

Install Go 1.24 or later, then:

```sh
git clone https://github.com/jacobm-gavin/wisp-agent.git
cd wisp-agent
# OPENROUTER_API_KEY must be exported in your shell.
go run ./examples/minimal
```

Open <http://127.0.0.1:8080>. The host creates `wisp.db` in the working directory
and remains dormant; startup makes no model requests. Stop it with Ctrl+C.
Use `-db /path/to/history.db`, `-listen 127.0.0.1:9090`, or `-model MODEL_ID`
to configure the host. Only loopback inspection addresses are accepted.

To exercise the full loop without credentials or network requests, run the
synthetic test example instead:

```sh
go test -v -count=1 -run '^ExampleRuntime$' .
```

## Usage

To build your own agent, create a separate Go application and add Wisp:

```sh
mkdir my-agent
cd my-agent
go mod init example.com/my-agent
go get github.com/jacobm-gavin/wisp-agent@latest
```

Import `github.com/jacobm-gavin/wisp-agent` as `wisp`. Define your application
agent in `agent.go` and its startup code in `main.go`. Supply your event sources
and tools, plus a model implementation or the OpenRouter adapter below:

```go
agent := wisp.Agent{
    Name:         "Assistant",
    Model:        "local",
    Instructions: []string{"instructions/base.md"},
    Events:       []wisp.EventSource{messageSource},
    Tools:        []wisp.Tool{readFile, respond},
}

runtime, err := wisp.New(agent, wisp.Config{
    Models:       map[string]wisp.Model{"local": model},
    Instructions: os.DirFS("."),
    DatabasePath: "wisp.db",
})
if err != nil {
    return err
}
defer runtime.Close()
return runtime.Run(ctx)
```

The snippet assumes application-provided capabilities and a cancellation context.
Only declared sources are started and only declared tools are callable. Model
configuration stays outside the agent declaration. Markdown instructions are
loaded in order when the runtime is constructed.

Mount `runtime.Handler()` on a local HTTP server for the activity UI. It exposes
`GET /api/agent`, `/api/runs`, `/api/active-runs`, `/api/runs/{id}`, and `/api/stream`. The handler is
read-only; a user-message endpoint or reply transport must be supplied as an
explicit event source or tool. `ListRuns` and `History` also expose inspection in Go.

## Running and deployment

Build the included host from the repository root:

```sh
CGO_ENABLED=0 go build -o wisp-minimal ./examples/minimal
./wisp-minimal -db /absolute/path/to/history.db
```

Deploy that executable to the same OS/architecture, export `OPENROUTER_API_KEY`,
and choose an existing writable directory for persistent history. Instructions
and UI assets are embedded; the target needs neither Go nor a database server.
A service manager can run the same command and environment. SIGINT/SIGTERM stop
intake, cancel work, close the HTTP server, and release the database.

SQLite history belongs to one runtime. A companion `.lock` file enforces this
across processes and is released automatically on exit or crash. Leave that file
in place; use local storage and do not give the same database multiple hard-link
names. Inspection has no authentication and stays on localhost.

For your own application, use the minimal host's startup/shutdown code as a
reference. If you use `os.DirFS(".")` instead of embedded instructions, deploy
the Markdown files too and set the working directory accordingly.

## Runtime contract

- Successful emission durably creates exactly one event/run pair. Repeated
  emissions are separate events; external delivery is not deduplicated.
- Every run starts with runtime instructions, declared Markdown, its event facts,
  and declared tool definitions. Previous runs are never injected.
- Tool results retain their call IDs and the call order within each model
  response. All calls settle before the next turn; a model/tool error fails its run.
- Final text is saved for inspection. External communication requires a tool.
- Cancellation stops intake and joins sources and runs. Implementations must honor
  `context.Context`; tools own argument validation and resource-specific locking.
- By default, up to 16 runs execute concurrently and each run can make 64 model
  calls. `Emit` waits for capacity before acceptance; canceled waits create nothing.
  Configure `MaxConcurrentRuns`, `MaxModelTurns`, and optional `RunTimeout` in
  `wisp.Config`. The included host sets a five-minute run timeout.
- One runtime owns each SQLite file. Restart marks unfinished runs failed and
  never replays them. Instructions and capabilities are fixed until restart.

The core provides concurrency, with no generic workspace isolation, retries,
automatic memory, or workflow machinery. Inspection exposes raw event/model/tool data.

## Development

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./...
go test -run '^$' -fuzz FuzzResponseProtocol -fuzztime=10s ./model/openrouter
```

Tests use deterministic models and synthetic capabilities; no credentials or
inference server are needed. A [runnable example](example_test.go) exercises the
public API with test doubles (`go test -run ExampleRuntime`). See the [architecture](Wisp_Agent_Architecture.md)
for design rationale and [core notes](docs/core.md) for implementation decisions
and acceptance coverage.

## OpenRouter

Use `github.com/jacobm-gavin/wisp-agent/model/openrouter` to configure a model:

```go
model, err := openrouter.New(openrouter.Config{
    APIKey: os.Getenv("OPENROUTER_API_KEY"),
    Model:  "qwen/qwen3.8-27b",
})
```

Check `err`, then register `model` in `wisp.Config.Models` under the name used by
your agent. The adapter supports text and function-tool turns, disables reasoning,
and makes no automatic retries. Provider errors and token truncation fail the run.

An opt-in live test checks completion and a persisted Wisp run with synthetic
tool calls. It sends only test prompts and random fixture values to OpenRouter:

```sh
WISP_OPENROUTER_LIVE=1 go test -v -count=1 -run TestOpenRouterLive -timeout 4m ./model/openrouter
```

Export `OPENROUTER_API_KEY` in the calling shell. Normal tests skip this network
check. The live test uses the paid model, makes at most five requests with 1,024
output tokens per request, and depends on provider availability and rate limits.
