# Experimental web attach

Phase A scaffold: a browser can **read** a running or finished session by
tailing its JSONL event log. The TUI stays the primary UI. Composer, permissions,
and multi-session management are out of scope here.

## Start

```sh
make serve
# equivalent:
./strike serve --addr 127.0.0.1:8787
```

If `--token` is omitted, strike mints one and prints it. Prefer setting a token
yourself when scripting.

```sh
./strike serve --addr 127.0.0.1:8787 --token "$STRIKE_SERVE_TOKEN"
```

Optional: `--session-dir` overrides `~/.strike/sessions`.

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/health` | no | `{ "ok": true, "version", "commit" }` |
| `GET` | `/` or `/attach` | no | Minimal HTML attach page |
| `GET` | `/v1/sessions/{id}/events` | **yes** | SSE stream of session envelopes |

Auth for `/v1/*`:

- `Authorization: Bearer <token>`, or
- `?token=<token>` (used by `EventSource` on the attach page)

Example:

```sh
curl -s http://127.0.0.1:8787/health
curl -N -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:8787/v1/sessions/<session-id>/events"
```

Open the attach page:

```
http://127.0.0.1:8787/attach?session=<id>&token=<token>
```

Session ids are the filenames under the sessions dir without `.jsonl`
(see TUI `/session` or `~/.strike/sessions/`).

## Security

- Default bind is **loopback** (`127.0.0.1:8787`).
- CORS `Access-Control-Allow-Origin` is only set for `localhost` / `127.0.0.1` /
  `[::1]` origins.
- Binding to `0.0.0.0` or a LAN address prints a warning: anyone who can reach
  the port and knows the token can read transcripts. There is **no TLS** and no
  production auth in this scaffold.
- Treat the token like a password; do not commit it.

## Layout

| Path | Role |
|---|---|
| `cmd/strike/serve.go` | `strike serve` CLI |
| `internal/server` | HTTP handlers, SSE tail of JSONL |
| `internal/server/static` | embedded attach page |

## Not in Phase A

- Sending prompts / ops from the browser
- Permission or question replies
- Multi-session switcher
- TLS / production auth
