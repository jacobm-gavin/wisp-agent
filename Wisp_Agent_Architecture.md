# Wisp Agent

## Architecture and Prototype Implementation Specification

**Status:** Architecture handoff for first prototype\
**Version:** 0.1 design baseline\
**Date:** September 28, 2026\
**Primary implementation language:** Go\
**Working project name:** Wisp Agent (`wisp-agent`), shorthand **Wisp**

------------------------------------------------------------------------

## 1. Purpose of this document

This document is the architecture handoff for **Wisp Agent**, a minimal
persistent agent harness written in Go. It captures the decisions,
constraints, terminology, invariants, motivations, tradeoffs, and
implementation direction established before beginning the first
prototype.

The goal is not merely to describe what files to create. The goal is to
preserve the reasoning that gives the project its identity, so an
implementation agent can build the first version without accidentally
turning Wisp into a conventional heavyweight agent framework.

This document should be treated as the source of truth for the initial
implementation unless a later explicit decision supersedes it.

The central requirement is restraint. The prototype should prove that a
useful persistent agent can be built from a very small set of
primitives. If an implementation choice requires inventing a new
subsystem, abstraction, or policy that is not required by the use case,
the default should be **do less**.

> **Wisp is a minimal, declarative harness for persistent agents.**

The intended product is not "an everything framework." It is a small
runtime that lets a developer declare what can wake an agent, what the
agent can do, what instructions govern it, and what model powers it,
while Wisp handles execution, persistence, observability, and the
repetitive mechanics of running the agent.

------------------------------------------------------------------------

## 2. Executive summary

Wisp exists because modern agent harnesses tend to accumulate
abstractions until it becomes difficult to answer two basic questions:

1.  **What can cause this agent to act?**
2.  **What can this agent actually do?**

Wisp intentionally reduces the external capability surface of an agent
to two first-class primitives:

-   **Events** --- things that happen in the world and can wake the
    agent.
-   **Tools** --- actions the agent can intentionally invoke against the
    world.

This produces the project's defining architectural statement:

> **An agent's external capability surface consists only of Events it
> can receive and Tools it can invoke. Everything else belongs to the
> harness runtime.**

Conceptually:

``` text
Agent capability surface = Events + Tools

A = (E, T)
```

A Wisp agent is declared explicitly in source code. The declaration
should make it possible for a technically literate reader---and ideally
a non-programmer---to understand the agent's capability surface in
roughly thirty seconds.

A representative declaration may eventually look like:

``` go
var Agent = harness.Agent{
    Name:  "Developer",
    Model: "qwen-coder",

    Instructions: harness.Instructions{
        "instructions/base.md",
        "instructions/developer.md",
    },

    Events: harness.Events{
        UserMessageReceived(),
        JiraIssueAssigned(),
        GitHubPullRequestReviewed(),
        EveryDayAt("08:00"),
    },

    Tools: harness.Tools{
        ReadFiles("./workspace"),
        WriteFiles("./workspace"),
        RunCommands("./workspace"),
        ReadJiraIssue(),
        CommentOnJiraIssue(),
        CreateGitHubPullRequest(),
        RespondToWebUser(),
    },
}
```

This is illustrative syntax, not a frozen API. The semantic requirements
are what matter:

-   The model is named in the declaration.
-   One or more Markdown instruction files are named in the declaration.
-   Every distinct Event is visible in the declaration.
-   Every distinct Tool is visible in the declaration.
-   Capability-relevant parameters may appear in the declaration.
-   Implementation mechanics should not.

The core runtime semantics are:

1.  An EventSource emits an Event.
2.  **Every emitted Event creates exactly one Run.**
3.  Every Run starts with **fresh context** constructed from the
    system/runtime context, declared Markdown instructions, the
    triggering Event's facts, and Tool definitions.
4.  Prior Run history is **not implicitly placed into context**.
5.  Runs are contextually independent and may execute concurrently.
6.  Wisp provides **no generic isolation guarantee** between concurrent
    Runs. They may share the same persistent external workspace or
    resources.
7.  A model turn may request zero, one, or multiple Tool calls.
8.  Multiple Tool calls emitted in the same model turn execute
    concurrently.
9.  Wisp waits for all Tool results from that turn before invoking the
    model again.
10. If the model returns with **no Tool calls**, the Run is complete.
11. The final model text is persisted as Run output but is **not
    automatically sent anywhere**.
12. If the agent needs to affect the external world---including replying
    to a human---it must use a Tool.
13. Events contain facts and context about what happened; they should
    not prescribe what the agent ought to do.
14. Behavioral instructions belong in the agent's Markdown instruction
    files.
15. Execution history is persisted for observability and reconstruction,
    but persistence does not imply automatic memory.

For v0.1, Wisp should not add multi-agent orchestration, workflow
graphs, planners, vector databases, sophisticated memory systems, plugin
runtimes, resource locking, transactional Run isolation, dynamic module
loading, hot reloading, or other generalized machinery unless the
prototype proves one is necessary.

------------------------------------------------------------------------

## 3. Why Wisp exists

### 3.1 The problem with general-purpose agent harnesses

Persistent agent platforms often grow by accumulating individually
reasonable features:

-   plugins
-   connectors
-   skills
-   workflows
-   subagents
-   planners
-   memory systems
-   middleware
-   schedulers
-   marketplaces
-   role systems
-   configurable policies
-   tool routers
-   vector stores
-   state machines
-   tracing stacks
-   GUI configuration
-   dynamic plugin discovery

The aggregate result can be powerful, but the mental model becomes
expensive. A developer can no longer determine the agent's behavior by
reading a small amount of code. Instead, behavior emerges from a
collection of hidden runtime registration, mutable configuration,
framework defaults, UI state, installed plugins, and implicit lifecycle
rules.

Wisp's premise is that **understandability is a product feature**, not
merely an implementation concern.

A Wisp agent should be legible. Its declared capability surface should
be easy to inspect. Its execution history should be easy to reconstruct.
The runtime should avoid making decisions on the developer's behalf when
those decisions would introduce hidden behavior.

### 3.2 Persistent agents are not the same thing as continuously running models

"Persistent" does not mean the language model is continuously thinking.

A persistent agent has a lifecycle independent of an interactive
terminal or chat session. It may spend almost all of its time dormant.
It becomes active because something happens in the environment.

``` text
               dormant
                  │
               Event
                  │
                  ▼
                 Run
                  │
          reason / use Tools
                  │
                  ▼
              complete
                  │
                  ▼
               dormant
```

The important property is **environmental re-entry**. A user does not
have to manually invoke a TUI every time the agent should act.

### 3.3 A useful distinction: interactive/coding agents versus persistent agents

A typical coding agent can be modeled approximately as:

``` text
Events
└── user.message

Tools
├── shell
├── read_file
├── write_file
├── search
├── git
└── ...
```

Its lifecycle is usually:

``` text
User ──message──► Agent ──tools──► Environment
                      │
                      ▼
                   response
                    STOP
```

A persistent agent expands the event surface:

``` text
Events
├── user.message
├── timer
├── filesystem.changed
├── jira.issue_assigned
├── github.mention
├── pull_request.reviewed
├── email.received
└── webhook
```

The tools may be almost identical. The key distinction is that a
persistent agent is **event-driven**, not merely interactively invoked.

A useful shorthand is:

> **Interactive agents are primarily invoked; persistent agents are also
> event-driven.**

This is an architectural mental model, not a claim that every product
fits perfectly into one category.

------------------------------------------------------------------------

## 4. Design influences

Wisp intentionally draws from two different software design traditions.

### 4.1 Neovim: a small core with powerful primitives

The lesson from Neovim is not "build a plugin system." The lesson is:

> **Keep the core intentionally small, and expose a small set of
> powerful primitives from which a large ecosystem can be built.**

A strong test for Wisp's core is:

> **Can this new capability be added without changing the harness?**

Examples:

-   Slack support should be Tools and Events.
-   Jira support should be Tools and Events.
-   A camera detecting movement should be an Event.
-   Home Assistant control should be Tools.
-   Kubernetes interaction should be Tools and Events.
-   A proprietary internal system should be Tools and Events.
-   Another process waking the agent should be an Event.

If ordinary integrations repeatedly require modifications to the Wisp
runtime, the primitives are probably wrong.

The long-term success criterion is that the ecosystem could contain
thousands of Tool and EventSource implementations while the core Tool
and EventSource concepts remain recognizably close to v0.1.

### 4.2 NixOS: declarative source of truth

The lesson from NixOS is that a desired system should be understandable
from its declaration.

The Wisp analogue is:

> **Declare the agent you want; the runtime realizes it.**

The agent declaration should be the source of truth for capability. A
hidden UI toggle or a package merely present in the dependency tree must
not silently grant a capability.

An important invariant follows:

> **Available in the dependency tree does not mean available to the
> agent.**

Only explicitly declared Events and Tools belong to the agent's
capability surface.

### 4.3 The combined identity

The intended synthesis is:

``` text
Neovim underneath, NixOS on top.
```

Or more precisely:

``` text
Declarative definition
        +
Programmable primitives
```

The runtime is extensible through ordinary code. The agent itself is
declaratively understandable.

------------------------------------------------------------------------

## 5. Core architectural model

### 5.1 The world-agent loop

At the highest level:

``` text
                    WORLD
                      │
                something happens
                      │
                      ▼
                    EVENT
                      │
                      ▼
                 ┌─────────┐
                 │  AGENT  │
                 └────┬────┘
                      │
                   decides
                      │
                      ▼
                    TOOL
                      │
                      ▼
                    WORLD
```

Another useful view:

``` text
                       ┌──────────────┐
                       │    Model     │
                       └──────┬───────┘
                              │
               ┌──────────────┴──────────────┐
               │                             │
            receives                      invokes
               │                             │
               ▼                             ▼
            EVENTS                         TOOLS
               ▲                             │
               │                             ▼
        external world ◄──────────────── external world
```

Events determine **when and why the agent receives agency**.

Tools determine **what the agent can intentionally do with that
agency**.

### 5.2 The capability equation

Conceptually:

``` text
A = (E, T)
```

where:

-   `E` is the set of Event types/sources exposed to the agent.
-   `T` is the set of Tools exposed to the agent.

The equation deliberately does not include every subsystem in the
process. Models, SQLite, queues, HTTP servers, prompt assembly, and
observability are important, but they are runtime infrastructure rather
than external capabilities.

### 5.3 Runtime infrastructure versus capability surface

``` text
Agent capability surface
├── Events
└── Tools

Runtime infrastructure
├── Model adapter
├── Context construction
├── Instruction loading
├── Persistent execution history
├── Event ingestion/dispatch
├── Run execution engine
├── Scheduling machinery
├── Logging/telemetry
└── Web UI
```

A useful rule is:

> **A subsystem belongs to runtime infrastructure until the agent is
> deliberately given an operation that exposes it.**

Examples:

-   SQLite is infrastructure. `QueryDatabase()` becomes a Tool only if
    explicitly exposed.
-   A scheduler is infrastructure. `ScheduleReminder()` becomes a Tool
    only if the model is explicitly allowed to schedule reminders.
-   Persistent memory storage would be infrastructure. `Remember()` and
    `Forget()` would be Tools only if deliberate model-controlled memory
    manipulation is desired.

Do not confuse "the runtime uses this" with "the agent can do this."

------------------------------------------------------------------------

## 6. The declarative agent definition

### 6.1 What the declaration is for

Each agent project should contain a small, obvious declaration that acts
as a comprehensive overview of the agent.

It should answer:

-   What is this agent called?
-   Which model configuration powers it?
-   Which instruction files govern it?
-   What can wake it?
-   What can it do?

A reader should not have to inspect package initialization, hidden
registries, environment-specific plugin discovery, or mutable runtime UI
state to answer those questions.

### 6.2 Illustrative declaration

``` go
var Agent = harness.Agent{
    Name:  "Developer",
    Model: "qwen-coder",

    Instructions: harness.Instructions{
        "instructions/base.md",
        "instructions/developer.md",
    },

    Events: harness.Events{
        UserMessageReceived(),
        JiraIssueAssigned(),
        GitHubMentioned(),
        GitHubPullRequestReviewed(),
        EveryDayAt("08:00"),
    },

    Tools: harness.Tools{
        ReadFiles("./workspace"),
        WriteFiles("./workspace"),
        RunCommands("./workspace"),
        ReadJiraIssue(),
        CommentOnJiraIssue(),
        CreateGitHubPullRequest(),
        RespondToWebUser(),
    },
}
```

Again, exact names/types are not frozen. Preserve the semantics and
readability.

### 6.3 The declaration should contain semantic configuration

Parameters belong in the declaration when they materially explain the
capability.

Good:

``` go
EveryDayAt("08:00")
ReadFiles("./workspace")
WriteFiles("./output")
```

These parameters help a reader understand the agent.

Avoid implementation noise:

``` go
ReadFiles(
    "./workspace",
    Timeout(5*time.Second),
    BufferSize(8192),
    RetryCount(3),
)
```

Those details should live in implementation/configuration layers unless
they become genuinely important to the capability definition.

A useful principle is:

> **`agent.go` should contain exactly the information necessary to
> symbolically understand the agent's behavior and capability surface,
> while hiding implementation mechanics.**

### 6.4 The model belongs in the declaration

Although the Model is not one of the two external capability primitives,
it is core to understanding the agent and should be named in the agent
declaration.

Preferred style:

``` go
Model: "qwen-coder",
```

The declaration should not contain provider implementation details such
as base URL, temperature, token limits, authentication, or HTTP
configuration.

Those details may live in a separately named model configuration:

``` go
ModelConfig{
    Provider:    "openai-compatible",
    Model:       "Qwen/Qwen3-Coder",
    BaseURL:     "...",
    Temperature: 0.2,
    MaxTokens:   16000,
}
```

Rule:

> **The agent declaration names the model configuration; it does not
> configure model implementation details.**

### 6.5 Instructions belong in Markdown files

Persistent behavioral instructions should resolve to **one or more
Markdown files stored in the project**.

Example:

``` text
instructions/
├── base.md
└── developer.md
```

Declaration:

``` go
Instructions: harness.Instructions{
    "instructions/base.md",
    "instructions/developer.md",
},
```

Wisp should load them deterministically in the declared order and
incorporate them into each Run's fresh startup context.

For v0.1, do not invent a prompt templating language, inheritance
hierarchy, conditional include system, macro engine, or prompt DSL.
Markdown is sufficient.

Key principle:

> **Agent instructions are durable project source, not hidden runtime
> configuration.**

### 6.6 One declared Tool equals one distinct capability

The declaration should not hide large capability bundles behind broad
names.

Prefer:

``` go
Tools: harness.Tools{
    ReadFiles("./workspace"),
    WriteFiles("./workspace"),
    RunCommands("./workspace"),
    ReadGitHubIssue(),
    CommentOnGitHubIssue(),
    CreateGitHubPullRequest(),
}
```

Avoid:

``` go
Tools: harness.Tools{
    Filesystem("./workspace"),
    GitHub(),
}
```

if those entries secretly expose many unrelated operations.

Shared clients, authentication, request types, and helper libraries are
fine internally. The capability surface must still be granted
explicitly.

Invariant:

> **One declared Tool = one distinct agent capability.**

### 6.7 Events follow the same granularity rule

Prefer:

``` go
Events: harness.Events{
    GitHubIssueAssigned(),
    GitHubMentioned(),
    GitHubPullRequestReviewed(),
}
```

Avoid a broad `GitHubEvents()` source if it hides materially different
ways the world can give the agent agency.

Invariant:

> **One declared Event = one distinct way the world can wake the
> agent.**

### 6.8 The declaration is a human-facing artifact

A long-term `wisp inspect` command or web UI should be able to derive
something like this directly from the declaration/runtime metadata:

``` text
DEVELOPER AGENT

MODEL
  qwen-coder

INSTRUCTIONS
  instructions/base.md
  instructions/developer.md

WAKES UP WHEN
  ✓ You send it a message
  ✓ A Jira issue is assigned to it
  ✓ A GitHub pull request is reviewed
  ✓ Every day at 8:00 AM

CAN
  ✓ Read files in ./workspace
  ✓ Write files in ./workspace
  ✓ Run commands in ./workspace
  ✓ Read Jira issues
  ✓ Comment on Jira issues
  ✓ Create GitHub pull requests
  ✓ Respond to a web user
```

The declaration should contain enough structured metadata to make this
possible without maintaining a second manual description.

------------------------------------------------------------------------

## 7. Events

### 7.1 Definition

An Event is a fact that something happened in the external environment
which should give the agent an opportunity to reason and act.

Examples:

-   a user sent a message
-   a Jira issue was assigned
-   a pull request was reviewed
-   a timer fired
-   a file changed
-   CI completed
-   an email arrived
-   a webhook was received

### 7.2 Events contain facts, not behavioral instructions

This is a critical separation of concerns.

A Jira Event may provide:

``` text
Event: jira.issue_assigned

Issue: PROJ-142
Summary: Fix authentication timeout
Description:
Requests occasionally time out after 30 seconds...

Reporter: Alice
Priority: High
```

It should not contain instructions such as:

``` text
Investigate the repository.
Implement the fix.
Run tests.
Commit your changes.
Create a pull request.
```

Those instructions belong in the project's Markdown instruction files.

This enables the same Event implementation to be reused by very
different agents:

``` text
JiraIssueAssigned()
        │
        ├── Developer instructions → potentially implement
        ├── Triage instructions    → potentially classify
        └── Manager instructions   → potentially summarize/delegate
```

Invariant:

> **An Event describes external state or a change in external state. It
> should not prescribe the agent's response.**

A useful phrasing is:

> **EventSources decide that something happened. The model decides
> whether it matters.**

### 7.3 Every emitted Event creates a Run

Once an EventSource has emitted an Event, Wisp does not perform semantic
filtering.

Invariant:

> **Every emitted Event creates exactly one Run.**

An EventSource naturally performs the mechanical filtering necessary to
define its Event. For example, `JiraIssueAssigned()` does not need to
emit an Event for every HTTP response from Jira. But once it emits an
actual `jira.issue_assigned` Event, Wisp starts a Run.

Do not introduce a Wisp-level "ignore event," "handled," or "should
run?" policy mechanism in v0.1.

The model may determine that the Event requires no action. That is a
normal successful Run.

``` text
Event
  ↓
Run
  ↓
Model
  ↓
No Tool calls
  ↓
Complete
```

### 7.4 Events are input-only

Events are the mechanism by which information enters the agent runtime
from the external world.

They do not automatically receive a response when a Run completes.

This is especially important for user messages. A user-message Event
should not have special bidirectional behavior simply because it
originated from a chat UI.

Invariant:

> **Events are input-only. Intentional outward effects happen through
> Tools.**

This avoids coupling a Run to its triggering transport and keeps the
Event primitive uniform across web chat, Teams, Jira, timers,
filesystems, and future integrations.

### 7.5 Illustrative EventSource interface

The exact API remains open, but an implementation might converge on
something approximately like:

``` go
type Event struct {
    Source    string
    Type      string
    Data      any
    Timestamp time.Time
}

type EventSource interface {
    Name() string
    Start(ctx context.Context, emit func(Event)) error
}
```

This is illustrative, not normative. The desired properties are:

-   EventSource implementations are ordinary Go code.
-   They can run independently and emit Events.
-   They do not contain agent decision logic.
-   Their Event payloads provide the minimal facts necessary for
    reasoning.
-   They can expose enough metadata for observability and human-readable
    inspection.

Do not introduce event-handler callbacks that directly encode what the
agent should do:

``` go
// Avoid this design direction.
OnJiraIssue(func(issue Issue) {
    // hardcoded behavior
})
```

That moves agency from the model into deterministic application code and
undermines the purpose of the harness.

------------------------------------------------------------------------

## 8. Tools

### 8.1 Definition

A Tool is a distinct capability the model can intentionally invoke to
inspect or affect the external world.

Examples:

-   read a file
-   write a file
-   run a command
-   query a database
-   read a Jira issue
-   comment on Jira
-   create a pull request
-   send a Teams message
-   respond to a web user
-   send an email

### 8.2 Tools are the only intentional outward action mechanism

This is a major invariant.

> **If the agent intentionally affects the external world, it does so
> through a Tool.**

That includes communication with a human.

For example, web chat might be modeled as:

``` go
Events{
    WebMessageReceived(),
}

Tools{
    RespondToWebUser(),
}
```

Microsoft Teams might later be:

``` go
Events{
    TeamsMessageReceived(),
}

Tools{
    RespondToTeamsUser(),
}
```

This is preferable to making a user-message Event magically route the
Run's final text back to its origin.

The model may even receive information through one channel and respond
through another if the declared capabilities and instructions permit it.
Wisp does not need transport-specific response semantics.

### 8.3 Final model text is not itself an external action

If the model ends a Run with:

``` text
The ticket appears to already be resolved.
```

that text becomes the Run's final output for persistence and
observability.

It is not automatically posted to Jira, sent to the user, or delivered
anywhere else.

If external communication is required, the model must call the
appropriate Tool before ending the Run.

This preserves a strong boundary:

> **The model can reason and generate freely inside a Run. Only Tools
> intentionally affect the world.**

### 8.4 Tool granularity

Tools should be small enough that the agent declaration tells the truth
about what the agent can do.

An integration package may internally share one Jira client, but should
expose individual capabilities such as:

``` go
jira.ReadIssue(client)
jira.CommentOnIssue(client)
jira.AssignIssue(client)
```

rather than a broad `jira.All(client)` that invisibly grants unrelated
capabilities.

### 8.5 Tool authoring direction

A low-level runtime contract may eventually resemble:

``` go
type Tool interface {
    Definition() ToolDefinition
    Execute(context.Context, json.RawMessage) (ToolResult, error)
}
```

But extension authors ideally should not be forced to manually decode
JSON or hand-author schemas for simple Tools.

An ergonomic constructor may eventually look approximately like:

``` go
type WeatherArgs struct {
    City string `json:"city" description:"City to get weather for"`
}

func Weather() harness.Tool {
    return harness.NewTool(
        "weather",
        "Get the current weather for a city",
        func(ctx context.Context, args WeatherArgs) (WeatherResult, error) {
            return getWeather(ctx, args.City)
        },
    )
}
```

Wisp could derive the model-facing JSON schema from the typed Go
arguments.

This API is not yet a settled invariant. It is a strong ergonomic
direction to explore during the prototype.

### 8.6 Tool implementation owns resource-specific semantics

Wisp should not generically understand what a filesystem path, Git
workspace, database row, Jira issue, or deployment environment means.

If a Tool requires internal locking or resource-specific protection,
that Tool may implement it.

Examples:

-   A workspace Tool can use a mutex if it needs exclusive access.
-   A coding Tool can use Git worktrees if it wants isolated workspaces.
-   A database Tool can use transactions.
-   An API Tool can enforce its own rate limits.

These are Tool concerns unless repeated real-world use demonstrates a
compelling reason to elevate them into the Wisp core.

------------------------------------------------------------------------

## 9. Runs

### 9.1 Definition

A Run is the bounded execution created by one Event.

Invariant:

> **One Event → one Run.**

A Run is the primary unit of execution, observability, failure
isolation, and later potentially retry/replay.

### 9.2 Runs are bounded

For v0.1, Wisp does not have long-lived suspended workflows or resumable
execution continuations.

A Run begins, reasons, invokes Tools as needed, and terminates as either
completed or failed.

``` text
Event
└── Run
    ├── ModelCall
    ├── ToolCall(s)
    ├── ToolResult(s)
    ├── ModelCall
    └── Completion / Failure
```

If something relevant happens later, that later occurrence should
normally enter Wisp as another Event and therefore another Run.

Example:

``` text
JiraIssueAssigned
        ↓
      Run #1
        ↓
   Start build / create PR
        ↓
      finish

...later...

CICompleted
        ↓
      Run #2
        ↓
   inspect result
```

Do not introduce Threads, Tasks, workflow continuations, suspended Runs,
or generic event correlation in v0.1.

### 9.3 Every Run starts fresh

A new Run does not automatically continue the conversational context of
previous Runs.

Invariant:

> **Every Run is contextually independent. Wisp constructs its initial
> context from the Agent and the triggering Event; prior Run history is
> not implicitly included.**

Fresh startup context should contain the information necessary for the
Run, conceptually:

``` text
Fresh Run context
├── runtime/system context
├── declared Markdown instructions
├── triggering Event facts/payload
└── Tool definitions
```

The exact message ordering and provider-specific encoding may vary by
model adapter.

Within the Run, context accumulates normally:

``` text
SYSTEM / INSTRUCTIONS / EVENT
            ↓
          MODEL
            ↓
        TOOL CALLS
            ↓
        TOOL RESULTS
            ↓
          MODEL
            ↓
           ...
            ↓
        FINAL OUTPUT
```

### 9.4 Execution history is not memory

Wisp should persist what happened, but that persisted history is not
automatically fed into future Runs.

This distinction is fundamental:

> **Execution history is a record, not context.**

This postpones the difficult problem of agent memory until there is a
concrete use case.

If future Wisp gains persistent memory, retrieval from prior Runs,
summaries, or durable facts, those should be deliberately incorporated
into startup context rather than arising accidentally because a database
exists.

### 9.5 Run completion semantics

The Run loop is intentionally simple.

Conceptually:

``` go
for {
    response := model.Generate(context)

    if len(response.ToolCalls) == 0 {
        return Complete(response)
    }

    results := executeToolCalls(response.ToolCalls)
    context.Append(response, results)
}
```

Invariant:

> **A Run ends when the model produces a response containing no Tool
> calls.**

Semantically, a Tool call means the model believes it still needs to
inspect or affect the world before finishing. A response with no Tool
calls means it has no further external action to request.

This handles both action-heavy and no-op Runs with the same rule.

### 9.6 No special completion Tool

Do not add a `finish_run`, `yield`, `acknowledge`, or similar Tool for
v0.1.

The absence of Tool calls is sufficient to express completion.

### 9.7 Failure is a Run boundary

A failed model call, Tool execution, or unrecoverable runtime error
should fail the current Run rather than create hidden long-lived
recovery state.

The exact retry policy is intentionally deferred. The important
architectural point is that Runs are the failure boundary and failure
should be observable.

------------------------------------------------------------------------

## 10. Concurrency model

### 10.1 Runs may execute concurrently

Wisp should not force one global Run at a time.

A persistent agent must remain responsive when one Run is slow. A long
build or API request should not prevent an unrelated user Event from
beginning a new Run.

Therefore:

> **Runs may execute concurrently.**

Illustration:

``` text
Event A ──► Run A ─────────────────────────►
Event B ───────► Run B ───────────►
Event C ─────────────► Run C ─────────────────►
```

### 10.2 Wisp does not guarantee isolation between concurrent Runs

This is equally important.

> **Concurrency is provided; generic isolation is not.**

Concurrent Runs may access the same persistent workspace or external
resources if their Tools expose those resources.

Example:

``` text
Run A                         Run B
  │                             │
WriteFiles("./workspace")    WriteFiles("./workspace")
  │                             │
  └──────── same world ─────────┘
```

Wisp does not attempt to understand whether these operations conflict.

This matches the project philosophy: the core should not invent a
generic resource graph, workspace transaction system, file lock
hierarchy, API conflict detector, or scheduler simply because some Tools
might conflict.

### 10.3 Why not serialize all Runs?

Global serialization is attractive because it is easy to reason about
and prevents many self-conflicts. But it creates a bad persistent-agent
experience.

A slow Run could block:

-   a human saying "hello"
-   a production alert
-   a Teams message
-   an unrelated timer
-   a separate external event

Responsiveness is a core reason Wisp exists as a persistent harness.

### 10.4 Why not add isolation now?

True generic isolation would require Wisp to understand
application-specific resource semantics:

-   workspace ownership
-   filesystem paths
-   Git branches/worktrees
-   database records
-   external issue IDs
-   deployment environments
-   mutable API resources

That immediately leads toward locks, scopes, transactions, conflict
detection, priorities, and resource declarations.

Those abstractions may someday be useful, but the prototype should not
invent them preemptively.

### 10.5 Context independence makes concurrency tractable

Concurrent Runs do not share in-progress model context.

Run B does not automatically receive Run A's half-finished Tool
transcript merely because Run A exists.

Each Run starts fresh from its own triggering Event and agent
definition.

This yields a clean principle:

> **Runs share the world, but not their in-progress context.**

### 10.6 Concurrency limits are deferred

The runtime will eventually need protection against unbounded event
storms or resource exhaustion. A maximum concurrent Run count or
backpressure policy is reasonable future infrastructure.

However, do not let v0.1 grow a scheduler framework merely to anticipate
this.

A minimal safe implementation may choose a conservative
hardcoded/default limit if required for operational safety, but it
should not become a large user-facing scheduling abstraction yet.

------------------------------------------------------------------------

## 11. Parallel Tool calls within a model turn

### 11.1 Multiple Tool calls in one turn execute concurrently

Modern tool-calling models may emit multiple Tool calls in the same
response when they consider those operations independent.

Wisp should execute those calls concurrently.

``` text
Model turn
  │
  ├── Tool A ───────────────►
  ├── Tool B ─────►
  └── Tool C ──────────►
             │
             ▼
       wait for all
             │
             ▼
       next model turn
```

Invariant:

> **Tool calls emitted in the same model turn execute concurrently. Wisp
> waits for all results before beginning the next model turn.**

### 11.2 The turn boundary expresses dependency

If Tool B depends on Tool A's result, the model should request Tool A,
receive its result, and then request Tool B in a later model turn.

``` text
Model turn 1
   ↓
Tool A
   ↓
Result A
   ↓
Model turn 2
   ↓
Tool B
```

Wisp should not infer a dependency graph among Tool calls emitted
together.

### 11.3 No isolation guarantee here either

If a model emits two logically conflicting Tool calls in the same turn,
Wisp does not generically protect the model from that mistake.

For example:

``` text
WriteFile("config.json", versionA)
WriteFile("config.json", versionB)
```

may conflict.

If a specific Tool implementation cannot safely be invoked concurrently,
that Tool may synchronize internally.

The runtime's responsibility is mechanical concurrency and correct
result association, not semantic conflict inference.

### 11.4 Result association must be deterministic

Even though Tool executions finish in nondeterministic wall-clock order,
every result must remain associated with its originating model Tool-call
identifier.

The next model request should be constructed according to the model
provider's tool-result protocol, not merely in "whichever goroutine
finished first" order.

This is an implementation correctness requirement.

### 11.5 Go is a natural fit

Goroutines and `context.Context` make this execution model
straightforward.

A conceptual implementation:

``` go
results := make([]ToolResult, len(calls))

var wg sync.WaitGroup
for i, call := range calls {
    wg.Add(1)
    go func(i int, call ToolCall) {
        defer wg.Done()
        results[i] = executeTool(ctx, call)
    }(i, call)
}
wg.Wait()
```

Production code may use `errgroup`, structured result channels, or
another idiomatic approach. The architecture does not require a specific
primitive.

------------------------------------------------------------------------

## 12. Context construction

### 12.1 Fresh context per Run

Context construction is runtime infrastructure.

Each Run should build a new model context from explicit sources rather
than inheriting an implicit global conversation.

Conceptually:

``` text
Run startup context
    │
    ├── system/runtime message(s)
    ├── instruction Markdown file 1
    ├── instruction Markdown file 2
    ├── ...
    ├── Event facts/payload
    └── Tool definitions
```

### 12.2 Instructions and Events have different responsibilities

Instruction files answer:

> **How should this agent behave?**

The Event answers:

> **What just happened?**

Tools answer:

> **What is this agent allowed to do?**

The model answers:

> **Given those things, what should happen now?**

This separation is central and should remain visible in the code.

### 12.3 Prior Runs are not injected by default

The context builder should not automatically load "recent history"
merely because it is easy to query from SQLite.

That would quietly create a memory design that has not been
intentionally specified.

### 12.4 Provider-specific adaptation belongs below the Run semantics

Different model APIs represent system messages, Tool schemas, Tool
results, IDs, and parallel Tool calls differently.

Those differences belong in model adapters.

The Run engine should work with a small provider-neutral internal
representation wherever practical.

------------------------------------------------------------------------

## 13. Model adapters

### 13.1 Models are adapters, not architectural primitives

A model powers reasoning but should not dominate the runtime design.

A minimal conceptual interface is sufficient:

``` go
type Model interface {
    Generate(
        ctx context.Context,
        messages []Message,
        tools []ToolSchema,
    ) (Response, error)
}
```

The exact Go types are open to implementation discovery.

### 13.2 First adapter direction

An OpenAI-compatible HTTP API is a sensible first target because it
supports many hosted and local inference servers, including vLLM-style
deployments.

Other adapters can later support Anthropic, OpenAI-specific APIs, or
other model protocols without changing Event/Tool/Run semantics.

### 13.3 Keep provider details out of `agent.go`

Authentication, endpoints, token limits, temperature, HTTP clients,
retry behavior, and provider-specific flags belong in model
configuration/runtime implementation.

The agent declaration should remain readable.

------------------------------------------------------------------------

## 14. Persistence

### 14.1 SQLite is the initial durable store

SQLite is a strong fit for v0.1:

-   single-process friendly
-   embedded
-   easy to inspect
-   durable
-   no external service
-   consistent with a single-binary/local-first runtime

### 14.2 Persist enough to reconstruct execution

The guiding requirement is:

> **Persist everything necessary to understand what happened.**

Likely entities include:

``` text
events
runs
model_calls
tool_calls
tool_results
```

Potentially also a normalized message/transcript representation if
useful.

The exact schema should be designed for the prototype rather than
treated as frozen by this document.

### 14.3 Suggested relationships

Conceptually:

``` text
Event
  │ 1:1
  ▼
Run
  │
  ├── ModelCall #1
  │      │
  │      └── ToolCall(s)
  │              │
  │              └── ToolResult
  │
  ├── ModelCall #2
  │      └── ...
  │
  └── final output / failure
```

Useful identifiers should make this chain easy to query and render.

### 14.4 Persistence is not memory

Reiterating the boundary:

``` text
Execution History
      │
      └── v0.1 requirement

Agent Memory
      │
      └── deferred problem
```

Do not automatically turn persisted transcripts into future context.

### 14.5 SQLite should be owned by the harness

Tool and Event authors should not need to know how Wisp persists Runs in
order to implement normal capabilities.

The runtime owns execution-history persistence.

------------------------------------------------------------------------

## 15. Observability and web UI

### 15.1 Observability is part of the product

Persistent agents are difficult to trust if their activity is opaque.

The web UI should answer a simple question:

> **What is my agent doing?**

The initial UI is not intended to become a full agent-management
platform.

### 15.2 What the UI should expose

At minimum, useful views include:

-   agent identity
-   model name/reference
-   declared instruction files
-   declared Events
-   declared Tools
-   current active Runs
-   recent completed/failed Runs
-   triggering Event for each Run
-   model calls
-   Tool calls and arguments
-   Tool results/errors
-   final model output
-   timestamps and durations

Example activity view:

``` text
Agent: Developer                               ● RUNNING

Current Run
Triggered by: jira.issue_assigned
Started: 14:31:04
Model: qwen-coder

Activity

14:31:07  MODEL
            Need to inspect the repository...

14:31:08  TOOL  read_file
            {"path":"internal/server.go"}

14:31:08  RESULT
            8.2 KB · 241 lines

14:31:11  TOOL  run_command
            go test ./...

14:31:14  RESULT
            PASS
```

With concurrent Runs, the UI should group/interleave by Run ID rather
than pretending there is one linear global reasoning stream.

### 15.3 Static capability plus actual behavior

The UI can present two complementary truths:

1.  **What the agent could do** --- derived from declared Events and
    Tools.
2.  **What the agent actually did** --- derived from persisted Run
    history.

This reinforces the project's inspectability goal.

### 15.4 Keep the frontend simple

For the first prototype, an embedded HTML/CSS/JavaScript UI is
preferable to adding a large frontend framework unless a concrete need
appears.

A Go binary can embed static assets.

### 15.5 SSE is a reasonable first streaming mechanism

Server-Sent Events are likely sufficient for streaming runtime activity
from Wisp to a browser because the primary need is server-to-client
activity updates.

WebSockets are unnecessary unless the implementation discovers a real
bidirectional streaming requirement.

User input can use a normal HTTP endpoint that causes the relevant
user-message EventSource to emit an Event.

### 15.6 Illustrative API surface

Potential read APIs:

``` text
GET /api/agent
GET /api/runs
GET /api/runs/{id}
GET /api/events
GET /api/stream
```

A user-message ingestion endpoint may also be required.

Exact routes are not architectural commitments.

------------------------------------------------------------------------

## 16. Extension and reuse philosophy

### 16.1 Wisp should support custom Tools and Events in ordinary Go

The initial ecosystem is source code.

A developer should be able to implement a new Tool or EventSource
directly in Go, import ordinary libraries as needed, and explicitly
declare the resulting capability.

There should be no requirement to write a plugin manifest, package a
special archive, run a marketplace installer, or implement a broad
plugin lifecycle interface.

### 16.2 Do not introduce a first-class `Plugin` abstraction

Avoid an interface like:

``` go
type Plugin interface {
    Name() string
    Init(*Runtime) error
    Start(context.Context) error
    Stop(context.Context) error
    Tools() []Tool
    Events() []EventSource
    Routes() []HTTPRoute
    Middleware() []Middleware
    ConfigSchema() any
}
```

This becomes a framework inside the framework and immediately creates
lifecycle, dependency, migration, configuration, UI-hook, middleware,
and versioning complexity.

An integration is simply a collection of ordinary Tools and
EventSources.

### 16.3 Reuse should use normal Go mechanisms

A substantial integration can be a normal Go module:

``` bash
go get github.com/example/wisp-github
```

Then the agent explicitly chooses capabilities:

``` go
Tools: harness.Tools{
    github.ReadIssue(gh),
    github.CommentOnIssue(gh),
    github.CreatePullRequest(gh),
},

Events: harness.Events{
    github.IssueAssigned(gh),
    github.PullRequestReviewed(gh),
},
```

Small capabilities may simply be copied into a project.

### 16.4 Discovery must be separate from execution

A future community registry or "marketplace" is acceptable as a
**discovery/catalog layer**.

It should not become a hidden runtime that grants capabilities merely
because they are installed.

Invariant:

> **Nothing becomes part of an agent's capability surface without
> appearing explicitly in its agent definition.**

Avoid side-effect registration patterns such as:

``` go
import _ "github.com/foo/github-tools"
```

or:

``` go
harness.AutoDiscoverTools()
harness.LoadPlugins("./plugins")
```

### 16.5 Dependencies may implement mechanisms; declaration grants capabilities

Using libraries for HTTP, JSON, OAuth, SQLite, Git, etc. is entirely
appropriate.

The concern is not dependencies themselves. The concern is implicit
capability.

A useful principle:

> **Dependencies may implement mechanisms; the agent's capabilities
> should remain explicit.**

### 16.6 The reusable unit should remain Tool or Event

Do not build opaque "Software Engineer Agent," "Research Agent," or
"DevOps Agent" packages as the fundamental extension unit.

People can publish examples and starter projects, but Wisp's reusable
primitive should remain a capability or EventSource that can be composed
explicitly.

------------------------------------------------------------------------

## 17. Go as the implementation language

The language decision is settled: **Wisp should be implemented in Go.**

### 17.1 Why Go fits the runtime

Wisp is mostly a persistent systems program rather than a
machine-learning research library.

The runtime needs:

-   concurrent EventSources
-   concurrent Runs
-   concurrent Tool calls
-   HTTP clients and servers
-   subprocess management
-   filesystem access
-   cancellation
-   SQLite
-   embedded static assets
-   long-running process reliability
-   low idle overhead
-   simple deployment

Go is particularly strong in these areas.

### 17.2 Most capabilities are protocol integrations

Many expected Tools and Events do not require Python ML libraries:

-   GitHub / Jira / Slack / Teams → HTTP APIs or webhooks
-   email → SMTP/IMAP/APIs
-   database → SQL
-   shell → subprocesses
-   files → filesystem APIs
-   MCP → protocol transport
-   model serving → HTTP

A small explicit Go implementation of the exact API endpoints needed can
be more aligned with Wisp's philosophy than importing a massive
agent-oriented SDK.

### 17.3 Do not design a Python extension system in advance

If a concrete future capability genuinely requires Python-only
functionality, Wisp can later support an external Tool protocol such as
a subprocess speaking JSON over stdin/stdout or another narrow boundary.

Do not add that architecture to v0.1 without a real use case.

------------------------------------------------------------------------

## 18. Suggested repository boundaries

There are two related concepts: the **Wisp runtime** and a **project
that declares a particular agent**.

### 18.1 Wisp runtime repository

One possible shape:

``` text
wisp-agent/
├── cmd/
│   └── wisp/
│       └── main.go
│
├── internal/
│   ├── runtime/
│   ├── run/
│   ├── model/
│   ├── store/
│   └── web/
│
├── event.go
├── tool.go
├── agent.go
├── model.go
│
├── web/
│   ├── index.html
│   ├── app.js
│   └── style.css
│
├── go.mod
└── go.sum
```

This is only a starting direction. Prefer package boundaries that become
justified by actual code rather than creating folders merely to mirror a
conceptual diagram.

### 18.2 A user-defined agent project

Eventually, a project using Wisp might be as small as:

``` text
my-agent/
├── main.go
├── agent.go
│
├── instructions/
│   ├── base.md
│   └── developer.md
│
├── events/
│   ├── jira.go
│   └── timer.go
│
├── tools/
│   ├── filesystem.go
│   ├── shell.go
│   └── jira.go
│
├── go.mod
└── go.sum
```

`main.go` should ideally be boring:

``` go
func main() {
    wisp.Run(Agent)
}
```

### 18.3 Boundary principle

> **The Wisp repository implements the runtime. An agent project
> describes one particular agent.**

For the earliest prototype, it is completely acceptable to keep a sample
agent inside the Wisp repository while the API evolves. Do not
prematurely split repositories if doing so slows iteration.

------------------------------------------------------------------------

## 19. The minimal runtime loop

The orchestration code should remain intentionally boring.

Conceptually:

``` go
func (r *Runtime) HandleEvent(ctx context.Context, event Event) {
    run := r.store.CreateRun(event)

    go r.executeRun(ctx, run, event)
}
```

And the Run loop:

``` go
func (r *Runtime) executeRun(
    ctx context.Context,
    run Run,
    event Event,
) error {
    messages := r.context.Build(r.agent, event)

    for {
        response, err := r.model.Generate(
            ctx,
            messages,
            r.tools.Schemas(),
        )
        if err != nil {
            return r.store.FailRun(run.ID, err)
        }

        r.store.RecordModelCall(run.ID, response)

        if len(response.ToolCalls) == 0 {
            return r.store.CompleteRun(run.ID, response.Text)
        }

        results := r.tools.ExecuteConcurrent(ctx, response.ToolCalls)
        r.store.RecordToolResults(run.ID, results)
        messages = append(messages, adapt(response, results)...)
    }
}
```

This is pseudocode. It demonstrates the desired simplicity, not a
mandated API.

If the core Run loop becomes a large orchestration framework with many
policy layers, pause and re-evaluate whether complexity has leaked into
the wrong place.

------------------------------------------------------------------------

## 20. Prototype scope: what to build

The first prototype should be small but end-to-end. It should prove the
primitives rather than cover every integration.

### 20.1 Required prototype capabilities

The prototype should demonstrate:

1.  A declarative Agent definition.
2.  A named model configuration.
3.  One or more Markdown instruction files.
4.  At least two distinct Event types, ideally:
    -   a human/web message Event
    -   a timer or simple programmatic/webhook Event
5.  At least several distinct Tools, ideally:
    -   read file
    -   write file
    -   run command
    -   respond to web user
6.  Fresh context for each Event-created Run.
7.  Every Event creating one bounded Run.
8.  Concurrent Runs.
9.  Concurrent Tool execution when one model turn emits multiple calls.
10. No generic Run isolation.
11. No implicit prior-Run context.
12. Run completion when the model emits no Tool calls.
13. SQLite persistence of Event/Run/model/Tool activity.
14. A minimal web UI that can:
    -   submit a user message Event
    -   display declared capabilities
    -   show active/recent Runs
    -   show Run activity
15. Live activity streaming, preferably via SSE.
16. Graceful enough shutdown that the process can be stopped without
    corrupting SQLite.

### 20.2 Useful first demonstration

A good prototype scenario is a simple local developer agent:

``` text
Instructions:
  You are a local development assistant.
  Use the available tools when needed.
  Reply to human messages using RespondToWebUser.

Events:
  WebMessageReceived()
  EveryMinute() or a manual webhook event

Tools:
  ReadFiles("./workspace")
  WriteFiles("./workspace")
  RunCommands("./workspace")
  RespondToWebUser()
```

Then demonstrate:

-   User sends "hello" while another Run is executing a slow command.
-   A second Run starts rather than waiting globally.
-   Two independent reads requested in one model turn happen
    concurrently.
-   A Run that decides no action is required completes with no Tools.
-   Run history appears in the UI.

The demonstration should make the concurrency and fresh-context
semantics visible.

------------------------------------------------------------------------

## 21. Explicit v0.1 non-goals

The following are intentionally **not** part of the first architecture
unless implementation evidence forces reconsideration.

### 21.1 No multi-agent orchestration

The ability to declare multiple agents is interesting and likely worth
considering later, but it introduces questions around routing,
inter-agent communication, ownership, shared state, deployment, and UI
that are not needed to prove Wisp's core.

Do not build it now.

### 21.2 No planner abstraction

The model already reasons and chooses Tools. Do not add a separate
planner/executor hierarchy without a concrete use case.

### 21.3 No workflow or DAG engine

A persistent agent is not a generalized workflow engine.

Later external events naturally create later Runs.

### 21.4 No generic plugin runtime

Use ordinary Go packages and explicit declaration.

### 21.5 No marketplace execution system

A future registry may help discover source/packages. It should not
become a hidden runtime capability loader.

### 21.6 No vector database

There is no current requirement for semantic memory/RAG in the harness
core.

### 21.7 No sophisticated persistent memory

Persist execution history, but do not invent automatic memory retrieval,
summaries, embeddings, long-term facts, or context compaction policies
yet.

### 21.8 No cross-Run Threads or Tasks

One Event creates one bounded Run. Later facts create later Events/Runs.

### 21.9 No generic resource-locking or isolation framework

Runs may overlap. Tools/resources own their own concurrency behavior.

### 21.10 No Event priority scheduler

Do not introduce "urgent Events," interruption policies, cancellation
priorities, or sophisticated scheduling until a real workload requires
them.

### 21.11 No prompt DSL

Markdown instruction files are enough.

### 21.12 No Python extension layer

Go first. Add cross-language capability only when a real requirement
appears.

### 21.13 No dynamic module loading or hot reload requirement

Recompile/restart is acceptable for v0.1.

### 21.14 No permissions framework beyond explicit capability declaration

The explicit Tool list is the first capability boundary. Do not
preemptively build a policy engine.

### 21.15 No huge frontend stack

The UI exists to make the runtime legible, not to become a product
suite.

------------------------------------------------------------------------

## 22. Intentionally deferred implementation questions

These questions are real, but they do not need architectural answers
before the prototype.

### 22.1 Retry policy

When should model/API/Tool calls be retried? With what backoff? Which
errors are terminal?

Start conservatively. Make failures visible. Avoid hiding repeated
behavior behind aggressive retries.

### 22.2 Timeouts

Model calls, Tools, HTTP clients, subprocesses, and Runs will eventually
need timeout semantics.

Use `context.Context` throughout so this can be added cleanly, but do
not invent a complex timeout policy layer yet.

### 22.3 Cancellation

It will likely be useful to cancel a Run, especially from the UI. This
can be added after the basic execution model works.

### 22.4 Run concurrency limits and backpressure

Unbounded goroutines are not an operational policy. The system will
eventually need sensible limits. Determine the simplest mechanism after
observing the prototype.

### 22.5 Tool-specific concurrency limits

Some APIs or resources may need a mutex, semaphore, or rate limiter.
Keep that local to the Tool until patterns justify a shared helper.

### 22.6 Event buffering and delivery guarantees

The prototype needs a functional event ingestion path, but it does not
need to promise exactly-once delivery, distributed durability, or a
message-broker architecture.

### 22.7 Crash recovery

Persist enough state that incomplete Runs can be identified after
restart. Automatic resume semantics are not defined and should not be
invented casually.

A simple prototype can mark abandoned Runs failed/interrupted on startup
if necessary.

### 22.8 Model-specific quirks

Parallel Tool call flags, schema restrictions, message-role differences,
and result encoding should be handled in adapters.

### 22.9 Schema generation ergonomics

Typed Go arguments → JSON Schema is desirable, but the exact
library/implementation should be chosen pragmatically.

### 22.10 Secrets and configuration

API keys, base URLs, database locations, and other deployment-specific
values need a configuration mechanism. Keep it separate from the
declarative capability overview.

### 22.11 Authentication for the web UI

Localhost-only is acceptable for an initial prototype. Do not
accidentally expose an unauthenticated agent control surface on a public
interface.

------------------------------------------------------------------------

## 23. Potential future directions that should not contaminate v0.1

These are plausible extensions, not requirements.

### 23.1 Multiple declared agents

A future Wisp process may host more than one Agent declaration.

An attractive model would be independent per-agent capability surfaces
and Runs, possibly sharing one runtime/store/UI.

However, do not bake multi-agent assumptions into every v0.1 type unless
they come nearly for free.

### 23.2 Persistent memory

Future memory may be implemented as deliberate startup-context
construction, explicit memory Tools, or both.

The current architecture leaves room for this because Runs already have
a context-construction boundary.

### 23.3 Tool-managed workspace isolation

A coding-oriented Tool package might later use Git worktrees,
containers, or snapshots to isolate concurrent Runs.

That should remain an integration concern unless a generic pattern
becomes undeniable.

### 23.4 Community capability registry

A registry could catalog ordinary Go packages or source snippets and
make it easy to find `JiraIssueAssigned`, `RespondToTeamsUser`, etc.

Discovery remains separate from capability grant.

### 23.5 External/non-Go Tools

If needed, a narrow subprocess/HTTP/MCP-style adapter could expose
external implementations as ordinary Wisp Tools without changing the
core model.

### 23.6 Agent-controlled scheduling

If someday the model should intentionally create future wakeups,
scheduling can be exposed as a Tool, with the future wakeup arriving as
an Event.

That composes with the existing primitives:

``` text
Tool: ScheduleReminder(...)
        ↓
  runtime scheduler
        ↓ later
Event: ReminderDue
```

### 23.7 Human approval

Approval can also compose naturally:

``` text
Tool: RequestApproval(...)
        ↓
external human flow
        ↓
Event: ApprovalReceived
```

No separate workflow engine is required merely to express this.

------------------------------------------------------------------------

## 24. Architectural invariants --- normative checklist

This section is intentionally repetitive. These are the decisions that
should survive implementation refactors.

### 24.1 Product identity

**MUST:** Wisp remains a minimal persistent agent harness, not a general
workflow/agent platform.

**MUST:** Understandability and explicitness are first-class design
goals.

**MUST:** Go is the primary implementation language.

### 24.2 Capability surface

**MUST:** An agent's external capability surface is represented by
Events and Tools.

**MUST:** Every distinct Tool capability is explicitly declared.

**MUST:** Every distinct Event/wakeup mechanism is explicitly declared.

**MUST NOT:** A dependency silently grant capabilities by being
installed/imported.

### 24.3 Agent declaration

**MUST:** The declaration names the model configuration.

**MUST:** The declaration names one or more Markdown instruction files.

**MUST:** The declaration enumerates Events and Tools.

**SHOULD:** Capability-relevant parameters be visible in the
declaration.

**SHOULD NOT:** Implementation parameters pollute the declaration.

### 24.4 Instructions

**MUST:** Persistent behavioral instructions live in project Markdown
files.

**MUST:** Event payloads contain facts/context, not task-policy
instructions.

**MUST NOT:** Wisp require a prompt DSL in v0.1.

### 24.5 Events

**MUST:** Every emitted Event create exactly one Run.

**MUST:** Events be input-only.

**MUST:** EventSources remain focused on detecting/representing what
happened rather than deciding what the agent should do.

**MUST NOT:** Wisp semantically filter an emitted Event before creating
a Run.

### 24.6 Runs

**MUST:** Every Run start from fresh context.

**MUST NOT:** Prior Run history be implicitly added to new Run context.

**MUST:** One Event map to one bounded Run.

**MUST:** A Run complete when the model produces a response with no Tool
calls.

**MUST:** The final response be persisted as Run output.

**MUST NOT:** The final response be automatically delivered to an
external transport.

### 24.7 External effects

**MUST:** Intentional external effects happen through Tools.

**MUST:** Replying to a user/chat be represented as a Tool capability.

### 24.8 Run concurrency

**MUST:** Multiple Runs be allowed to execute concurrently.

**MUST NOT:** Wisp claim generic isolation between Runs.

**MUST NOT:** The core attempt to infer resource conflicts among
arbitrary Tools in v0.1.

**MAY:** Individual Tools implement their own locking, transactions,
rate limits, or isolation.

### 24.9 Tool-call concurrency

**MUST:** Multiple Tool calls emitted in the same model turn execute
concurrently.

**MUST:** Wisp wait for all calls from that turn before the next model
turn.

**MUST:** Tool results remain associated with the correct Tool-call IDs.

**MUST NOT:** Wisp infer hidden dependencies among Tool calls emitted
together.

### 24.10 Persistence

**MUST:** Wisp persist enough execution history to reconstruct what
happened.

**MUST:** Persistence remain runtime infrastructure.

**MUST NOT:** Persistence automatically become agent memory.

### 24.11 Extension model

**MUST:** Custom Tools and Events be implementable as ordinary Go code.

**MUST NOT:** v0.1 require a generic plugin runtime.

**MUST:** Reusable packages still require explicit capability
declaration.

### 24.12 Observability

**MUST:** The prototype make active/recent Runs and their model/Tool
activity inspectable.

**SHOULD:** The UI derive the declared capability overview from runtime
declaration metadata rather than duplicated prose.

------------------------------------------------------------------------

## 25. Design tests for future decisions

When considering a new feature, use these questions before creating a
new abstraction.

### 25.1 Primitive test

Ask:

> **Is this something the agent can do, something that can wake the
> agent, or just runtime/configuration data?**

-   If the agent can do it → likely a Tool.
-   If it can wake the agent → likely an Event.
-   If neither → likely runtime infrastructure or configuration.

Only create a new architectural category if a real use case genuinely
cannot fit cleanly.

### 25.2 Core-change test

Ask:

> **Can this capability be implemented without changing Wisp's core?**

If not, determine whether the core primitive is genuinely insufficient
or whether integration-specific concerns are leaking upward.

### 25.3 Declaration legibility test

Ask a reader to inspect `agent.go` for thirty seconds.

Can they accurately tell:

-   what model powers the agent?
-   which instructions govern it?
-   what can wake it?
-   what it can do?

If not, hidden capability or implementation noise has leaked into the
design.

### 25.4 Complexity test

Ask:

> **What current prototype requirement forces this abstraction to
> exist?**

If the answer is "we might need it someday," defer it.

### 25.5 Source-of-truth test

Ask:

> **Could two installations with the same source and configuration
> silently have different capability surfaces because of mutable runtime
> state?**

If yes, the design is drifting away from declarative explicitness.

------------------------------------------------------------------------

## 26. Suggested implementation sequence for Codex

The implementation agent should favor vertical slices over building
every interface in isolation.

### Phase 1 --- Skeleton and declaration

Create the Go module and the smallest types required to express:

``` text
Agent
Model reference
Instructions
Events
Tools
```

Create a sample Agent declaration early. Use it as a readability test
throughout development.

Do not over-generalize the type system before an end-to-end Run works.

### Phase 2 --- Model adapter and Run engine

Implement one model adapter, preferably OpenAI-compatible HTTP.

Implement:

-   fresh context construction
-   one Event → one Run
-   model → Tool calls → results → model loop
-   no Tool calls → completion
-   Tool-call ID/result association

Initially, an Event may be manually injected from a test or CLI if
needed.

### Phase 3 --- Tool registry and typed Tools

Implement enough Tool metadata/schema/execution machinery for:

-   a simple read-only Tool
-   a simple mutating Tool
-   a user response Tool

Validate multiple Tool calls in one model turn can execute concurrently.

Avoid designing a plugin system.

### Phase 4 --- EventSources and concurrent Runs

Implement at least:

-   a web/user-message EventSource
-   a timer/manual webhook EventSource

Each emitted Event should start a goroutine/task for an independent Run.

Prove that a slow Run does not globally block a new user Event.

Do not add generic resource isolation.

### Phase 5 --- SQLite persistence

Persist:

-   Event received
-   Run started/completed/failed
-   each model call
-   each Tool call
-   each Tool result
-   final model output

Ensure IDs/foreign keys make execution easy to reconstruct.

### Phase 6 --- Observability UI

Embed a minimal web UI.

Display:

-   declared capability surface
-   active Runs
-   recent Runs
-   Run detail timeline

Add SSE for live activity if straightforward.

Add a simple user-message form that emits the appropriate Event. The
agent replies only by invoking `RespondToWebUser`.

### Phase 7 --- Tests and cleanup

Add tests around the architectural invariants rather than only helper
functions.

Useful tests:

-   one Event creates one Run
-   fresh context excludes prior Run transcript
-   no Tool calls completes a Run
-   two Tool calls in one turn can overlap in execution time
-   next model call waits for both Tool results
-   two Runs can overlap in execution time
-   one Run's in-progress context is not injected into another
-   final model output is persisted but not automatically sent to the
    user
-   user response occurs only when response Tool is called

Then simplify. Delete abstractions that turned out unnecessary.

------------------------------------------------------------------------

## 27. Prototype acceptance criteria

The first prototype can be considered architecturally successful when
all of the following are demonstrable.

### Declaration and startup

-   [ ] The agent is defined by a compact Go declaration.
-   [ ] The declaration explicitly lists model, instructions, Events,
    and Tools.
-   [ ] Instructions are loaded from one or more Markdown files.
-   [ ] The process starts as a normal Go program without a plugin
    loader.

### Event semantics

-   [ ] A user-message Event can be emitted.
-   [ ] A second non-user Event type can be emitted.
-   [ ] Every emitted Event creates exactly one Run.
-   [ ] An Event payload contains facts rather than behavioral workflow
    instructions.

### Run semantics

-   [ ] Each Run starts from fresh context.
-   [ ] Prior Run history is not implicitly included.
-   [ ] A Run can invoke Tools across multiple model turns.
-   [ ] A response with zero Tool calls completes the Run.
-   [ ] A model can decide an Event requires no external action and
    complete successfully.
-   [ ] The Run's final model text is stored but not automatically
    delivered externally.

### Tool semantics

-   [ ] Tools are individually declared.
-   [ ] Tool schemas are exposed to the model.
-   [ ] External user communication happens through a response Tool.
-   [ ] Multiple Tool calls from one model turn execute concurrently.
-   [ ] All Tool results are available before the next model call.
-   [ ] Results remain correctly associated with Tool-call IDs.

### Concurrency

-   [ ] Multiple Runs can be active concurrently.
-   [ ] A slow Run does not prevent a separate user Event from beginning
    another Run.
-   [ ] Wisp does not claim or implement generic shared-resource
    isolation.

### Persistence and observability

-   [ ] SQLite records Events and Runs.
-   [ ] Model calls are inspectable.
-   [ ] Tool calls/results are inspectable.
-   [ ] Run completion/failure is inspectable.
-   [ ] A web UI displays the declared capability surface.
-   [ ] A web UI displays active/recent Run activity.

### Restraint

-   [ ] No multi-agent orchestration was added.
-   [ ] No workflow/DAG engine was added.
-   [ ] No planner abstraction was added.
-   [ ] No vector store was added.
-   [ ] No automatic persistent memory was added.
-   [ ] No generic plugin runtime was added.
-   [ ] No resource-locking framework was added.
-   [ ] No hidden auto-discovered capabilities were added.

------------------------------------------------------------------------

## 28. Guidance to the implementation agent

The implementation agent should optimize for a coherent prototype, not
theoretical completeness.

### 28.1 Do not "help" by adding framework features

If the specification says a concern is deferred, do not silently
implement it because it is common in other agent frameworks.

In particular, do not add:

-   a planner
-   tasks/threads
-   workflow state machines
-   generalized memory
-   plugin loading
-   broad integration bundles
-   auto-registration
-   permissions DSLs
-   complex schedulers
-   resource graphs
-   multi-agent routing

unless the current prototype literally cannot function without them.

### 28.2 Prefer ordinary Go

Prefer the standard library and small focused dependencies.

Do not reproduce infrastructure that a mature small library already
solves well, but also do not introduce a large framework dependency
merely because it provides one needed helper.

### 28.3 Keep the interfaces earned

Start with the narrowest useful contracts. Let real Tools, EventSources,
and model adapters force the abstractions to become more precise.

Avoid large speculative interfaces.

### 28.4 Make concurrency visible and testable

Do not merely spawn goroutines and assume the semantics are correct.

Use Run IDs, Tool-call IDs, timestamps, and tests to prove:

-   Runs overlap when expected.
-   Tool calls overlap within a turn.
-   next-turn progression waits for all results.
-   context remains per-Run.

### 28.5 Preserve inspectability

Whenever there is a choice between hidden convenience and explicit
behavior, bias toward explicit behavior.

The user should be able to read the agent declaration and the Run
timeline and understand the system.

### 28.6 Favor a working end-to-end slice

A small end-to-end system that receives an Event, starts a Run, calls a
model, invokes Tools, persists history, and shows the Run in a browser
is more valuable than a perfectly abstracted library with no full
execution path.

------------------------------------------------------------------------

## 29. Canonical mental model

The entire project should be reducible to this picture:

``` text
                         WISP AGENT

              ┌──────────────────────────┐
              │   Declarative Agent      │
              │                          │
              │  Model                   │
              │  Instructions (.md)      │
              │  Events                  │
              │  Tools                   │
              └────────────┬─────────────┘
                           │
                           ▼
                     Wisp Runtime

WORLD                                                     WORLD
  │                                                         ▲
  │ facts / changes                                         │ actions
  │                                                         │
  ▼                                                         │
EVENT ───────────────► fresh independent RUN ────────────► TOOLS
                              │
                              │ model turn
                              ▼
                            MODEL
                              │
                     ┌────────┴────────┐
                     │                 │
                tool call(s)       no tool calls
                     │                 │
                     ▼                 ▼
              execute concurrently   complete
                     │
                     ▼
                all results
                     │
                     └────────────► next model turn

Runs may overlap.
Runs share the external world exposed by their Tools.
Wisp does not provide generic isolation.
Run history is persisted for observability, not implicitly reused as memory.
```

This should remain understandable even as Wisp grows.

------------------------------------------------------------------------

## 30. Final architectural statement

Wisp's value is the constraint.

It should be possible to understand what an agent can perceive and what
it can do by reading a small piece of source code. The runtime should be
powerful enough to keep that agent alive, receive environmental Events,
execute independent Runs, invoke Tools, persist history, and expose
activity---but small enough that it does not become the source of hidden
agency itself.

The architecture can be summarized as:

> **A declarative persistent agent harness where everything the agent
> can perceive and intentionally do is explicit, inspectable code.**

Or, even more compactly:

> **Persistent agents defined by Events and Tools.**

The implementation should protect this simplicity aggressively.

If a future feature can be expressed as a Tool, an Event, or ordinary
runtime configuration, use the existing primitive. Add a new
architectural concept only after a concrete use case proves the existing
model insufficient.

That restraint is not a temporary limitation of the prototype. It is the
central design philosophy of Wisp.
