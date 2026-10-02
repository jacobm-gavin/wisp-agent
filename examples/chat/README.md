# Temporary chat demo

```sh
# Export OPENROUTER_API_KEY first.
go run ./examples/chat -listen 127.0.0.1:18081 -db /path/to/chat.db
```

Open http://127.0.0.1:18081 using that exact host and port. Uses paid
`qwen/qwen3.8-27b`; model calls occur only when messages are sent.

The declaration grants `chat.Messages()` and `chat.Respond()` explicitly.
No Bash or file tools are granted. The integration lives in
`integrations/webchat`; the runtime's execution semantics are unchanged.

To explicitly grant workspace read/write and unrestricted Bash, add
`-workspace /absolute/path/to/workspace` (the directory must exist). The optional
declaration is in `workspaceAgent` in `agent.go`. Bash is not sandboxed; use a
trusted workspace. Without this flag, the demo remains chat-only.

Each submission emits one `web.message` event containing text and a message ID.
Only `respond_to_user` delivers a reply to that ID. Final model text remains
private run output. Each message starts fresh; earlier visible messages are not
injected into model context. The UI shows run status/failure and links to inspection.

Chat messages are in memory, shared by local browser tabs, capped at 100 per host
lifetime, and cleared when the host restarts. Execution history, including message
and reply contents, remains in SQLite. Restart the host for a new temporary chat.
This is not a durable conversation or authenticated multi-user service.

Only loopback binding is allowed. The host checks the exact Host header; message
submission requires same-origin JSON requests. Local programs can still access
the service. Keep it local and do not forward it publicly. SIGINT/SIGTERM stop
the server and runtime cleanly.
