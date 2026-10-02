# HTTP-backed event sources

Status: agreed design; implementation handoff.

## Decision

Webhook reception is a transport implementation of an ordinary `wisp.EventSource`,
not a new Wisp primitive. An HTTP-backed source also implements Go's standard
`http.Handler`. The application explicitly declares the source and mounts it on
an application-owned HTTP router. Multiple sources can share one server/listener.

Explicit wiring is a design requirement, not boilerplate to eliminate through
discovery. This follows the architecture's explicit capability declarations,
facts-versus-policy separation, and ordinary-Go extension model (sections 6–7,
16, and 25 of `Wisp_Agent_Architecture.md`).

## Intended usage

Illustrative application code, not a new core API:

```go
assigned := github.IssueAssigned(client)

agent := wisp.Agent{
    Name:         "Developer",
    Model:        "qwen",
    Instructions: []string{"instructions/developer.md"},
    Events:       []wisp.EventSource{assigned},
    Tools:        []wisp.Tool{/* individually declared capabilities */},
}

mux := http.NewServeMux()
mux.Handle("/hooks/github/assigned", assigned)
// Mount other sources and inspection routes explicitly on this same mux.
// The application owns the HTTP server, listener, and coordinated shutdown.
```

The same source instance appears in the declaration and route binding. Neither
importing a package nor mounting a handler starts an event source or grants it
agency. Non-HTTP sources remain unchanged.

## Responsibilities

- **Agent declaration:** explicitly lists each distinct way the agent can wake.
- **Application host:** owns paths, routing, listener configuration, exposure, and
  coordinated HTTP/runtime startup and shutdown. Sharing one listener is supported,
  not mandatory; public ingress and private inspection may need separate listeners.
- **Event source:** implements `Definition`, `Run`, and `ServeHTTP`. It verifies
  provider authentication/signatures, bounds and decodes requests, selects the
  declared occurrence, and transforms the payload into event facts. Payload
  handling stays ordinary Go code written by the source author.
- **Markdown instructions:** define what the agent should do about those facts.
- **Wisp runtime:** retains existing acceptance and run semantics. Every accepted
  emitted event creates one run; there is no post-emission semantic filtering.

## Lifecycle and delivery requirements

`Run(ctx, emit)` establishes the source's active lifetime. Its handler must reject
requests before readiness and after intake stops rather than acknowledge events
that were not accepted. Coordinate handler access to lifecycle state safely under
concurrent requests; do not keep using an emission callback after `Run` returns.

For an event-bearing delivery, acknowledge success only after `Emit` succeeds,
not after model execution. Bound admission waits and request sizes. Report failed
acceptance with a provider-appropriate retryable response. Requests that do not
represent this source's declared occurrence may be acknowledged without emission;
provider handshake behavior belongs in the source implementation.

HTTP acknowledgment is transport bookkeeping, not an agent reply. Intentional
external responses still require an explicitly declared Tool. Do not inject
behavioral policy into event payloads or send final model output to the webhook.

Provider redelivery is not exactly-once delivery. No generic deduplication,
durable queue, replay, or retry subsystem is part of this work. Add provider-specific
handling only when a concrete integration requires it. Do not accidentally expose
the unauthenticated inspection UI alongside public webhook ingress.

## Implementation scope and acceptance

Implement and demonstrate this pattern using the existing interfaces. No new
`Webhook` primitive, `HostConfig.Webhooks`, route registry, automatic HTTP-source
discovery, capability auto-registration, or expansion of `EventSource` is wanted.
Do not create a server inside each source. Keep any necessary adaptation outside
the run engine; inspect the current web-chat integration for reusable lessons,
without treating its chat-specific behavior as a generic webhook contract.

Tests should demonstrate:

- Two individually declared HTTP-backed sources share one explicitly wired server
  and emit distinguishable facts through their respective routes.
- A valid accepted emission creates exactly one run, while rejected requests and
  irrelevant provider traffic create none.
- Mounting a source without running it cannot accept an event; declaring a source
  without mounting it does not expose an HTTP endpoint.
- Concurrent requests, admission cancellation, and shutdown respect source
  lifetime and leave no background work or silently acknowledged lost events.
- Model completion is independent of webhook acknowledgment, and final model text
  is never automatically delivered to the sender.

Use deterministic models and local HTTP tests. No live provider credentials or
public listener are needed to prove the hosting pattern.
