# Web workspace

`strike serve` hosts a React workspace that can drive a **live** engine session
and read-only attach to durable session JSONL logs.

## Start

```sh
make serve
# equivalent (live engine defaults to echo for offline use):
./strike serve --addr 127.0.0.1:8787
```

Loopback serving is unauthenticated by default. Enable authentication explicitly;
strike mints a token when `--auth` is used without `--token`.

```sh
./strike serve --auth --token "$STRIKE_SERVE_TOKEN" --provider echo
```

| Flag | Meaning |
|---|---|
| `--addr` | Bind address (default `127.0.0.1:8787`) |
| `--auth` | Require bearer authentication; auto-mint a token when omitted |
| `--expose` | Bind `0.0.0.0` (LAN). Requires `--auth`; loud WARNING; prints LAN URLs |
| `--allow-cidr` | With `--expose`, only accept client IPs in these CIDRs (repeatable) |
| `--token` | Bearer token for `/v1/*`; requires `--auth` |
| `--provider` | Live engine provider (default `echo`) |
| `--model` | Optional model id |
| `--session-dir` | Sessions directory for `--attach-only`; rejected in live mode |
| `--attach-only` | No live engine — JSONL SSE attach only |
| `--auto`, `--dangerously-skip-permissions` | Auto-allow permission asks in the live engine (equivalent) |

Open the cockpit:

```
http://127.0.0.1:8787/attach
```

## LAN expose (`--expose`)

```sh
./strike serve --auth --expose --token "$STRIKE_SERVE_TOKEN" --provider echo
# optional client allowlist:
./strike serve --auth --expose --allow-cidr 192.168.0.0/16 --token "$STRIKE_SERVE_TOKEN"
```

Behavior:

- Default without `--expose` stays **localhost-only**. A non-loopback `--addr`
  (e.g. `0.0.0.0:8787`) is **rejected** unless `--expose` is set.
- `--expose` rewrites a loopback `--addr` host to `0.0.0.0` (same port). An
  explicit non-loopback host is kept (bind one interface).
- Auth is required for network exposure. The full cockpit URL
  including `?token=` is printed **once** on stdout together with detected LAN
  IPs.
- A loud WARNING is printed on stderr (no TLS; token is bearer secret).
- Optional `--allow-cidr` denies clients outside the list (including `/health`).

### Threat model

| Risk | Notes |
|---|---|
| Session transcripts on LAN | Anyone with the token can SSE/WS read JSONL and live events |
| Live control plane | Token holders can submit ops (prompts, permission replies, tools) |
| No TLS | Tokens and payloads are cleartext on the wire — untrusted Wi‑Fi is unsafe |
| Token in URL | Query `?token=` may land in browser history / proxies; prefer Bearer when scripting |
| CSRF / CORS | Localhost origins always allowed; with `--expose`, private-network browser origins are also allowed for Vite-style dev. Public internet origins are never reflected |
| Shared networks | Prefer `--allow-cidr` to your LAN, or do not use `--expose` |

**Safer alternative:** keep loopback bind and forward with SSH:

```sh
# on the machine running strike:
./strike serve --auth --token "$STRIKE_SERVE_TOKEN"
# on the laptop/phone-side jump host:
ssh -L 8787:127.0.0.1:8787 user@strike-host
# open http://127.0.0.1:8787/attach?token=...
```

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/health` | no* | `{ "ok": true, "version", "commit" }` |
| `GET` | `/` or `/attach` | no* | Cockpit HTML (composer + transcript) |
| `GET` | `/v1/bootstrap` | mode | Version, status, capabilities, agents, skills |
| `GET` | `/v1/ws` | mode | WebSocket: ops in, event envelopes out |
| `POST` | `/v1/ops` | **yes** | Submit one op envelope (JSON) |
| `GET` | `/v1/live/events` | **yes** | SSE of live engine events (+ JSONL backlog) |
| `GET` | `/v1/status` | **yes** | Live status (model, agent, mode, cwd, busy, …) |
| `GET` | `/v1/agents` | **yes** | Selectable agent names |
| `GET` | `/v1/sessions` | **yes** | Session list + `liveId` |
| `GET` | `/v1/sessions/{id}/events` | **yes** | SSE tail of a session JSONL log |

\*Still subject to `--allow-cidr` when set.

With `--auth`, authenticate `/v1/*` using:

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

## Vite dev / production web toolchain

Production cockpit HTML is **embedded** in the Go binary
(`internal/server/static/index.html`). Optional `web/` is a Vite workspace for
local UI iteration and CI asset checks.

```sh
# terminal 1 — API (loopback)
./strike serve --provider echo

# terminal 2 — Vite dev server (proxies /health and /v1 → strike)
cd web && npm install && npm run dev
# open http://127.0.0.1:5173
```

| Env | Meaning |
|---|---|
| `STRIKE_API_ORIGIN` | Proxy target (default `http://127.0.0.1:8787`) |
| `VITE_HOST` | Vite bind host (default `127.0.0.1`; use `0.0.0.0` only with care) |
| `VITE_PORT` | Dev port (default `5173`) |

```sh
make web-build   # npm ci && npm run build → internal/server/static
```

CI runs `make web-build` when `web/package.json` is present. The Go binary does
not require Node at runtime: generated Vite assets under `internal/server/static`
are embedded at compile time. Edit sources under `web/src`, then build.

Lifecycle: run Vite as a **sibling** process of `strike serve` (two terminals or
a process supervisor). Strike does not spawn Vite as a child.

## Security (summary)

- Default bind is **loopback** (`127.0.0.1:8787`).
- Non-loopback binds require **`--auth`** and **`--expose`**.
- CORS allows localhost always; with `--expose`, also private-network origins.
- Treat the token like a password; do not commit it.
- See **Threat model** under LAN expose above.

## Layout

| Path | Role |
|---|---|
| `cmd/strike/serve.go` | `strike serve` CLI + live engine wiring + `--expose` |
| `internal/server` | HTTP/SSE/WS handlers, live hub, bind/CIDR helpers |
| `internal/server/static` | embedded cockpit page |
| `web/` | optional Vite dev proxy + `npm run build` |
| `internal/protocol` | Event + Op JSON envelopes |

## Manual checklist

1. `./strike serve --provider echo` → open the token-free loopback URL.
2. Send a message → streamed `text.delta` → `turn.completed`.
3. Send `run echo hi` → permission modal → allow once → tool result.
4. Switch permission mode / agent from toolbar.
5. RO attach: pick another session id → SSE transcript only.
6. `./strike serve --auth --expose --token test` → WARNING on stderr; phone on LAN loads
   printed cockpit URL; `/health` and live stream work with token.
7. `./strike serve --addr 0.0.0.0:8787` without `--expose` → error.
