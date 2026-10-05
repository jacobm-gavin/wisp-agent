# Open issue #1: OpenAI-compatible model provider

## Background

Issue #1 requests an additional model adapter that uses an OpenAI-compatible
chat-completions API, while preserving Wisp's existing provider-neutral runtime
contract.

This aligns with the architecture guidance:

- The runtime should depend on a small provider-neutral `Model` interface.
- Provider-specific wire details should live in adapters, not `agent.go`.
- Model configuration should hold endpoint/auth/token and other transport
  options outside the declaration.

Today the repository includes `model/openrouter` as the only built-in adapter.
That package already demonstrates the expected adapter boundaries and failure
behavior for `Generate(ctx, req)`.

## Implementation notes

### Scope and package shape

- Add a new package at `model/openaicompat` (or equivalent clear naming).
- Expose a `Config` and `New(Config) (*Model, error)` constructor.
- Keep the public runtime API unchanged (`wisp.Model` is already sufficient).

### Config expectations

Suggested fields:

- `APIKey string`
- `BaseURL string` (base URL or full chat-completions endpoint)
- `Model string`
- `MaxTokens int` (optional default)
- `Client *http.Client` (optional override)
- Optional provider flags that are known to be widely supported

Validation should reject empty API key/model and malformed base URL values.

### Request/response behavior

- Convert `wisp.Request` messages + tool definitions to OpenAI-compatible JSON.
- Preserve tool-call IDs and function arguments exactly when round-tripping.
- Enforce one-choice response semantics (matching current adapter behavior).
- Return structured errors for non-2xx responses with bounded/redacted details.
- Keep cancellation behavior by always using request contexts.

### Compatibility and invariants

- Do not leak provider settings into `wisp.Agent`.
- Keep provider quirks isolated in the adapter package.
- Maintain transcript safety checks for tool arguments (`json.Valid` / object).
- Keep redirect handling strict to avoid credential forwarding surprises.

### Test plan

Mirror the `model/openrouter` approach:

- Constructor validation tests
- Encode/decode protocol tests (including malformed function calls)
- Error redaction and truncation tests
- Cancellation and HTTP failure-path tests
- Fuzzing for response parsing robustness
- Optional opt-in live test gated by environment variables

### Integration touchpoints

- `examples/minimal` and `examples/chat` can be updated to show adapter
  substitution via runtime model mapping, without changing runtime internals.
- README quickstart can mention that OpenRouter is one adapter and additional
  OpenAI-compatible adapters follow the same `wisp.Model` registration pattern.
