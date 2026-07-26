# Experimental web cockpit

`strike serve` hosts a browser cockpit that can drive a **live** engine session
(composer, permissions, questions, status) and still **read-only attach** to any
session JSONL log. The TUI remains the primary UI.

## Start

```sh
make serve
# equivalent (live engine defaults to echo for offline use):
./strike serve --addr 127.0.0.1:8787
```

If `--token` is omitted, strike mints one and prints it. Prefer setting a token
yourself when scripting.

```sh
./strike serve --addr 127.0.0.1:8787 --token "$STRIKE_SERVE_TOKEN" --provider echo
```

| Flag | Meaning |
|---|---|
| `--addr` | Bind address (default `127.0.0.1:8787`) |
| `--token` | Bearer token for `/v1/*` (auto-minted if omitted) |
| `--provider` | Live engine provider (default `echo`) |
| `--model` | Optional model id |
| `--session-dir` | Sessions directory for RO listing/tails |
| `--attach-only` | No live engine — JSONL SSE attach only |
| `--dangerously-skip-permissions` | Auto-allow permission asks in the live engine |

Open the cockpit:

```
http://127.0.0.1:8787/attach?token=<token>
```

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/health` | no | `{ "ok": true, "version", "commit" }` |
| `GET` | `/` or `/attach` | no | Cockpit HTML (composer + transcript) |
| `GET` | `/v1/ws` | **yes** | WebSocket: ops in, event envelopes out |
| `POST` | `/v1/ops` | **yes** | Submit one op envelope (JSON) |
| `GET` | `/v1/live/events` | **yes** | SSE of live engine events (+ JSONL backlog) |
| `GET` | `/v1/status` | **yes** | Live status (model, agent, mode, cwd, busy, …) |
| `GET` | `/v1/agents` | **yes** | Selectable agent names |
| `GET` | `/v1/sessions` | **yes** | Session list + `liveId` |
| `GET` | `/v1/sessions/{id}/events` | **yes** | SSE tail of a session JSONL log |

Auth for `/v1/*`:

- `Authorization: Bearer <token>`, or
- `?token=<token>` (EventSource / WebSocket query)

### Op envelopes (client → engine)

JSON objects with a `type` and optional `data`:

| type | data |
|---|---|
| `user.input` | `{ "text": "..." }` |
| `interrupt` | _(empty)_ |
| `permission.reply` | `{ "requestId", "decision": "once\|always\|project\|reject", "message?" }` |
| `question.reply` | `{ "requestId", "answers": ["..."] }` |
| `select.agent` | `{ "name": "build" }` |
| `select.model` | `{ "provider", "model?" }` |
| `set.permission_mode` | `{ "mode": "default\|plan\|accept-edits\|yolo" }` |
| `set.autonomy` | `{ "mode": "supervised\|agent\|checks" }` |
| `set.effort` | `{ "level": "..." }` |

Events use the same envelopes as session JSONL (`type` + `time` + `data`).

Example — full echo turn via HTTP:

```sh
TOKEN=...
# stream live events
curl -N -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8787/v1/live/events &
# send a prompt
curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"type":"user.input","data":{"text":"hello"}}' \
  http://127.0.0.1:8787/v1/ops
```

Permission asks appear as `permission.asked` events; resolve with
`permission.reply` (UI modal or `POST /v1/ops`).

## Security

- Default bind is **loopback** (`127.0.0.1:8787`).
- CORS `Access-Control-Allow-Origin` is only set for `localhost` / `127.0.0.1` /
  `[::1]` origins.
- Binding to `0.0.0.0` or a LAN address prints a warning: anyone who can reach
  the port and knows the token can **read transcripts and submit ops**. There is
  **no TLS**. Dedicated LAN expose (`--expose`) is a separate feature.
- Treat the token like a password; do not commit it.

## Layout

| Path | Role |
|---|---|
| `cmd/strike/serve.go` | `strike serve` CLI + live engine wiring |
| `internal/server` | HTTP/SSE/WS handlers, live hub |
| `internal/server/static` | embedded cockpit page |
| `internal/protocol` | Event + Op JSON envelopes |

## Manual checklist

1. `./strike serve --provider echo --token test` → open attach URL with token.
2. Send a message → streamed `text.delta` → `turn.completed`.
3. Send `run echo hi` → permission modal → allow once → tool result.
4. Switch permission mode / agent from toolbar.
5. RO attach: pick another session id → SSE transcript only.
