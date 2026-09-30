# Wisp Agent

A small Go runtime for persistent agents, defined by the **events they receive**
and the **tools they can invoke**.

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

This repository is a **library**, not a standalone agent application. There is
currently no CLI or `main.go`, so `go run .` will not start an agent. The agent
declarations in [example_test.go](example_test.go) and the
[live integration test](model/openrouter/live_test.go) are test fixtures.

## Run the example

Install Go 1.24 or later, then:

```sh
git clone https://github.com/jacobm-gavin/wisp-agent.git
cd wisp-agent
go test -v -count=1 -run '^ExampleRuntime$' .
```

This runs the complete event → model → tool → model loop with synthetic
capabilities and an in-memory database, then exits. It requires no credentials,
makes no network inference requests, and does not start a web server.

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
`GET /api/agent`, `/api/runs`, `/api/runs/{id}`, and `/api/stream`. The handler is
read-only; a user-message endpoint or reply transport must be supplied as an
explicit event source or tool. `ListRuns` and `History` also expose inspection in Go.

## Running and deployment

Once your application has `main.go`, its agent declaration, and the declared
instruction files, run these commands **from that application directory**:

```sh
go run .
# Or build a standalone executable for the current platform:
CGO_ENABLED=0 go build -o my-agent .
./my-agent
```

Application startup should create a context canceled by `SIGINT`/`SIGTERM`, call
`wisp.New`, and pass that context to `runtime.Run`. For inspection, serve
`runtime.Handler()` with `net/http` on `127.0.0.1:8080`, then open
<http://127.0.0.1:8080>. The runtime does not start an HTTP listener itself.

Deploy the resulting executable and instruction files together, keeping the
working directory consistent with `os.DirFS(".")`. Configure `DatabasePath` to
use a writable, persistent location; one runtime must exclusively own that file.
The UI assets are embedded in the binary. A Go installation and external database
server are not needed on the target machine.

Supply provider credentials through the application's environment. On shutdown,
cancel the runtime context, stop the HTTP server, wait for `runtime.Run` to return,
then call `runtime.Close`. A service manager can run the executable with that
working directory, environment, and persistent storage. The inspection UI has no
authentication and should remain bound to localhost.

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
- One runtime owns each SQLite file. Restart marks unfinished runs failed and
  never replays them. Instructions and capabilities are fixed until restart.

The core provides concurrency, with no generic workspace isolation, retries,
automatic memory, or workflow machinery. Run deadlines and admission limits are
not yet configurable. Inspection exposes raw event/model/tool data.

## Development

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./...
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
