# Wisp Agent

**Wisp is a small Go framework and runtime for building your own persistent
agents—not a preconfigured coding agent.**

You declare what can wake your agent (**event sources**), what it can do
(**tools**), which model it uses, and which Markdown instructions govern it.
Wisp handles execution, concurrency, SQLite history, and inspection.

The philosophy is simple: **small core, explicit capabilities, ordinary Go.**
Importing a package never grants a capability. Each event starts a fresh run;
previous history is not automatic memory. Tools are the only intentional way to
affect the world—including replying to a person. Final model text is saved, not
automatically sent.

## Quickstart

You need **Go 1.24 or later**.

### Try the execution loop without an API key

```sh
git clone https://github.com/jacobm-gavin/wisp-agent.git
cd wisp-agent
go test -v -count=1 -run '^ExampleRuntime$' .
```

This runs a complete event → model → tool → completion cycle using deterministic
test doubles. No network calls or external actions occur. The example verifies
that the run completes and saves its output.

### Open the inspection UI

Export your OpenRouter key in your shell, then run:

```sh
go run ./examples/minimal
```

Open <http://127.0.0.1:8080>. Stop with Ctrl+C.

The host creates `wisp.db` in the current directory. Its
[agent declaration](examples/minimal/agent.go) intentionally has **no event
sources or tools**, so it stays dormant and makes no model requests. This is an
inspection host, not a chat interface. Add explicit capabilities to make an agent
act.

The host accepts `-db PATH`, `-listen 127.0.0.1:PORT`, and `-model MODEL_ID`.
Its default model is the paid `qwen/qwen3.8-27b`.

### Use OpenAI or a compatible Chat Completions server

`model/openaicompat` implements Wisp's ordinary `Model` interface using the
non-streaming Chat Completions protocol. Configure it outside `agent.go`, then
give the resulting instance a label used by the agent declaration:

```go
import "github.com/jacobm-gavin/wisp-agent/model/openaicompat"

model, err := openaicompat.New(openaicompat.Config{
    APIKey: os.Getenv("OPENAI_API_KEY"), // optional for local servers
    Model:  "gpt-4.1-mini",
}) // BaseURL defaults to https://api.openai.com/v1

runtime, err := wisp.New(Agent, wisp.Config{
    Models: map[string]wisp.Model{"openai": model},
    // ...instructions and database path...
})
```

For a local vLLM-compatible server, set its API base and model ID explicitly:

```go
model, err := openaicompat.New(openaicompat.Config{
    BaseURL: "http://127.0.0.1:8000/v1",
    Model:   "your-served-model",
})
```

Tool calling on vLLM depends on the server and model configuration. Wisp sends
standard function-tool definitions and surfaces endpoint or model errors; it
does not enable provider-specific tool behavior automatically.

### Start your own application

Want to message a model now? Run the [temporary chat demo](examples/chat/README.md):

```sh
# Export OPENROUTER_API_KEY first. Sends use the paid model.
go run ./examples/chat -listen 127.0.0.1:18081
```

Open <http://127.0.0.1:18081>. This declares a message event source and reply tool,
but no file or Bash access. Each send starts fresh; visible chat is temporary,
not conversation memory. Execution history stays in SQLite.

In a separate directory:

```sh
mkdir my-agent
cd my-agent
go mod init example.com/my-agent
go get github.com/jacobm-gavin/wisp-agent@latest
```

The next two sections provide complete capability files. The
[application wiring](#assemble-your-agent) below combines them into a runnable
program. The API is pre-stable; pin a tested version or commit for deployments.

## Make a tool

A tool implements two methods:

- `Definition()`: its name, description, and JSON argument schema.
- `Execute(ctx, args)`: validate arguments, perform one capability, and return
  a JSON result or error.

For example, save this as `clock.go` in your application:

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    wisp "github.com/jacobm-gavin/wisp-agent"
)

type CurrentTime struct{}

func (CurrentTime) Definition() wisp.ToolDefinition {
    return wisp.ToolDefinition{
        Name:        "current_time",
        Description: "Read the current time in UTC.",
        Parameters: json.RawMessage(
            `{"type":"object","properties":{},"additionalProperties":false}`,
        ),
    }
}

func (CurrentTime) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    var args map[string]json.RawMessage
    if err := json.Unmarshal(raw, &args); err != nil {
        return nil, err
    }
    if args == nil || len(args) != 0 {
        return nil, fmt.Errorf("current_time expects an empty JSON object")
    }
    return json.Marshal(map[string]string{
        "utc": time.Now().UTC().Format(time.RFC3339Nano),
    })
}
```

It is callable only when you explicitly put `CurrentTime{}` in `Agent.Tools`.

Tool authors must validate arguments; the runtime does not enforce every JSON
Schema constraint. Honor cancellation and make implementations safe for
concurrent calls. Keep resource-specific locking inside the tool. An execution
error fails its run; Wisp does not automatically retry effects.

### Use the included file tools

Import `github.com/jacobm-gavin/wisp-agent/tools/files`, open a workspace,
and declare read and write separately:

```go
workspace, err := files.Open("./workspace") // directory must already exist
if err != nil {
    return err
}
defer workspace.Close() // after the runtime has stopped

// In your Agent declaration:
Tools: []wisp.Tool{workspace.ReadFiles(), workspace.WriteFiles()},
```

This is a wiring fragment, not a standalone file. Declare only `ReadFiles()`
for read-only access. Reads support line ranges; writes require the previous
content hash before replacing a file. Both support UTF-8 text up to 1 MiB.

See [file-tool documentation](tools/files/README.md) for arguments and examples.
Workspace scoping is not an OS sandbox, and writes are not crash-atomic.

## Make an event source

An event source implements:

- `Definition()`: one distinct way the agent can wake up.
- `Run(ctx, emit)`: detect occurrences and emit their facts until canceled.

Save this as `ticks.go`:

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    wisp "github.com/jacobm-gavin/wisp-agent"
)

type Ticks struct {
    Every time.Duration
}

func (s Ticks) Definition() wisp.EventDefinition {
    return wisp.EventDefinition{
        Name:        "timer.tick",
        Description: fmt.Sprintf("A timer fires every %s.", s.Every),
    }
}

func (s Ticks) Run(ctx context.Context, emit wisp.Emit) error {
    if s.Every <= 0 {
        return fmt.Errorf("timer interval must be positive")
    }
    ticker := time.NewTicker(s.Every)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case at := <-ticker.C:
            data, err := json.Marshal(map[string]time.Time{"fired_at": at.UTC()})
            if err != nil {
                return err
            }
            if _, err := emit(ctx, wisp.Event{Data: data, Timestamp: at}); err != nil {
                return err
            }
        }
    }
}
```

Declare it with `Events: []wisp.EventSource{Ticks{Every: time.Minute}}`.

An event says **what happened**, not what the agent should do. Put behavioral
policy in Markdown instructions. The model can decide no action is needed.

A successful `emit` returns the ID of one durably accepted run. It may wait for
capacity; always handle its error. Repeated emissions create separate runs,
not deduplicated deliveries. This simple timer is illustrative: Go tickers can
drop ticks when consumers are slow; it is not a durable scheduler.

Stop background work before `Run` returns. Returning nil ends that source, but
already accepted runs continue. Returning an unexpected error stops the runtime.
Do not retain the `emit` callback after the source returns. Events receive no
automatic reply—communication belongs in a separate tool.

## Contributing and development

Read the [development principles](CONTRIBUTING.md) first. The
[architecture specification](Wisp_Agent_Architecture.md) is the design authority.

Prefer small end-to-end changes over speculative abstractions. New capabilities
should ordinarily be tools or event sources, without modifying the runtime.
Keep declarations readable and test the observable execution invariants.

From the repository root:

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./...
go test -run '^$' -fuzz FuzzResponseProtocol -fuzztime=10s ./model/openrouter
```

Format changed Go files with `gofmt`. Keep tests next to their packages.
Normal tests need no API key: they use deterministic models and temporary
resources. CI runs on Linux and macOS. Paid live checks are
[opt-in](#live-model-tests).

Useful starting points:

- [Public types](agent.go): `Agent`, `EventSource`, `Tool`, and `Model`.
- [Core notes](docs/core.md): implementation decisions and invariant test map.
- [Runnable API example](example_test.go): a complete offline execution loop.
- [File tools](tools/files): real integrations without core changes.

## Assemble your agent

With `clock.go` and `ticks.go` from above, create an `instructions` directory
and save this as `instructions/base.md`:

```markdown
You are a small demonstration agent.
When a timer fires, use current_time once, then summarize the observed time.
Your final text is saved in execution history; it is not a message to a user.
```

Save this as `main.go`:

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    wisp "github.com/jacobm-gavin/wisp-agent"
    "github.com/jacobm-gavin/wisp-agent/model/openrouter"
)

var Agent = wisp.Agent{
    Name:         "Clock demo",
    Model:        "qwen",
    Instructions: []string{"instructions/base.md"},
    Events:       []wisp.EventSource{Ticks{Every: time.Minute}},
    Tools:        []wisp.Tool{CurrentTime{}},
}

func main() {
    if err := run(); err != nil {
        log.Fatal(err)
    }
}

func run() error {
    model, err := openrouter.New(openrouter.Config{
        APIKey: os.Getenv("OPENROUTER_API_KEY"),
        Model:  "qwen/qwen3.8-27b",
    })
    if err != nil {
        return err
    }
    runtime, err := wisp.New(Agent, wisp.Config{
        Models:       map[string]wisp.Model{"qwen": model},
        Instructions: os.DirFS("."),
        DatabasePath: "wisp.db",
        RunTimeout:   time.Minute,
    })
    if err != nil {
        return err
    }
    defer runtime.Close()

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()
    return runtime.Run(ctx)
}
```

Export `OPENROUTER_API_KEY`, then run `go run .` from your application directory.
**This example makes paid model calls once a minute until you stop it.** The first
event occurs after one minute. Final output is stored in `wisp.db`, not printed.

The model name in `Agent` is a configuration reference; credentials, provider
settings, and execution limits live outside the declaration. Instructions load
in declaration order at startup.

This compact example has no HTTP server. For a host with inspection and graceful
HTTP shutdown, use [examples/minimal/main.go](examples/minimal/main.go) as a
reference. Mount `runtime.Handler()` on a loopback server. Its read-only routes
are `/api/agent`, `/api/runs`, `/api/active-runs`, `/api/runs/{id}`, and
`/api/stream`. `ListRuns`, `ActiveRuns`, and `History` also expose records in Go.

## Running and deployment

Build the included inspection host from the repository root:

```sh
CGO_ENABLED=0 go build -o wisp-minimal ./examples/minimal
./wisp-minimal -db /absolute/path/to/history.db
```

Export the API key before starting. Choose an existing writable directory for
history. The executable targets the build machine's OS/architecture; embedded
instructions and UI assets mean the target needs neither Go nor a database
server. The host remains dormant until capabilities are added.

A service manager can run the same command and environment. SIGINT/SIGTERM stop
intake, cancel work, close HTTP connections, and release the database.

Use local storage and one runtime per SQLite file. Leave its companion `.lock`
file in place; OS ownership is released on exit or crash. Do not alias a database
through hard links. Inspection has no authentication and may expose sensitive
event/model/tool data—keep it on localhost.

For your own application, build its package instead. If instructions use
`os.DirFS(".")`, deploy the Markdown files and set the working directory;
alternatively embed them as the minimal host does.

## Runtime guarantees and limits

- Every accepted event creates one run with fresh context: runtime instructions,
  declared Markdown, event facts, and tool definitions. No implicit prior history.
- Runs can overlap. Calls within one model turn execute concurrently, and all
  settle before the next turn. Results retain their original call IDs and order.
- A response without tool calls completes the run. Final text is saved; only
  declared tools communicate externally.
- Model/tool errors fail their run. Cancellation joins sources and runs;
  implementations must cooperate with `context.Context`.
- Defaults are 16 concurrent runs and 64 model calls per run. Configure
  `MaxConcurrentRuns`, `MaxModelTurns`, and optional `RunTimeout` in `wisp.Config`.
  The included inspection host uses a five-minute timeout.
- Restart marks unfinished runs failed; it never replays them. Changes to
  instructions or capabilities require a new runtime.

There is no generic resource isolation, automatic memory, retry machinery,
planner, workflow engine, or plugin loader.

## Live model tests

The OpenRouter adapter supports text and function tools, disables reasoning, and
does not automatically retry. Provider errors and token truncation fail the run.

With an exported `OPENROUTER_API_KEY`:

```sh
WISP_OPENROUTER_LIVE=1 go test -race -v -count=1 -run TestOpenRouterLive -timeout 6m ./model/openrouter
WISP_OPENROUTER_LIVE=1 go test -race -v -count=1 -run TestOpenRouterLiveFileTools -timeout 2m ./tools/files
```

Both use paid `qwen/qwen3.8-27b`. The core suite allows at most 21 requests with
at most 1,024 output tokens each; the file suite allows eight requests with 512
output tokens each. They use synthetic data and temporary resources. Provider
availability and rate limits can affect results. Normal tests skip live checks.

## Project status

**Core v0.1 is implemented and acceptance-tested; the API is pre-stable.**
See the [acceptance record](docs/core-v0.1-acceptance.md).

Included: the runtime, SQLite history, inspection UI/SSE, OpenRouter adapter,
read/write file tools, an unrestricted Bash tool, and an optional temporary chat
event-source/reply-tool integration with a runnable example.

The components still need a combined developer-agent declaration and end-to-end
demonstration with file/command/chat capabilities and a second event source. The
timer above is an authoring example, not a bundled scheduling service. Core
acceptance is not a claim of full prototype completion or production readiness.
