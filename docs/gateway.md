# Gateway

`strike serve` is the gateway: one process on the host, the engine inside
it, and the web UI talking only to that process. This page is the security
story that is true today. It is not a second product.

## Bind

The default address is `127.0.0.1:8787`. `ResolveDaemonBindAddr` rejects
public addresses and the unspecified address (`0.0.0.0`, `::`). A private
or Tailscale address is accepted only with both `--allow-private-bind` and
`--auth`. There is no flag that opens a public port, and there is no TLS
terminator in the process.

`strike daemon install` writes a user unit that runs
`strike serve --auth --token-file ~/.strike/daemon.token`. Install without
`--addr` stays on loopback.

## Authentication

With `--auth`, every `/v1/*` request needs the bearer. That includes ops,
SSE (`/v1/live/events`), and the WebSocket (`/v1/ws`). The check reads the
token file on each request when the process was started with `--token-file`,
so a rotated file applies without a restart. A missing or empty file denies
`/v1` until a token is written again.

Accepted credentials:

- `Authorization: Bearer <token>` (scheme is case-insensitive)
- HttpOnly `SameSite=Strict` cookie `strike_serve_token`, set by opening
  `/?token=` or `/attach?token=` once. The server redirects to a URL that
  does not contain the secret.

`?token=` on `/v1/*` is rejected. It would land in shell history, proxy
logs, and `Referer`.

`/health` and the static shell (`/`, `/assets/*`) are not token-gated.
They do not return sessions, files, or events. The shell has to load before
the cookie handoff can run.

Without `--auth`, loopback is open. That is the local `strike serve
--provider echo` path. The installed daemon unit always passes `--auth`.
Do not put an unauthenticated serve on a private address: `--allow-private-bind`
requires `--auth`.

## Token commands

| Command | Effect |
|---|---|
| `strike daemon token` | Print `~/.strike/daemon.token`, minting a 0600 file if it is missing. |
| `strike daemon logout` | Replace that file with a new token and print it. |

`logout` does not kill the process. A serve started with `--token-file`
loads the new token on the next `/v1` request; the previous bearer fails.
A serve started with `--token` keeps the value it was given until restart.
The token is a password. Do not commit it or pass it on a shared command line.

Provider credentials (`strike auth logout <provider>`) are a different
store. Logging out of a model provider does not rotate the gateway token.

## Reaching a gateway that is not on this machine

The process stays on loopback (or on a tailnet address you opted into).
Two ways to use it from somewhere else:

1. SSH local forward. On the machine with the browser:
   `ssh -L 8787:127.0.0.1:8787 user@host`, then open
   `http://127.0.0.1:8787/?token=…` once. The token never crosses a public
   HTTP listener.
2. A named HTTPS tunnel you run yourself (for example a Tailscale serve or
   an SSH reverse tunnel to a host you control). Point it at the loopback
   port. Strike does not start that tunnel and does not mint a public URL.

There is no multi-gateway hub in the client. Saved daemons in the Daemon
view are URLs and one token in browser storage. A switcher that discovers
gateways and mints short-lived tokens over SSH is not built; see
[webui-planned.md](webui-planned.md).

## What stays on the host

Sessions (JSONL), the workspace registry, memory, plans, artifacts, the
audit log, and provider credentials stay under `~/.strike` on the gateway
host. The browser receives event text and file listings the API already
returns to an authenticated caller. It does not receive the token file
path's contents except when you print them with `strike daemon token`.

Child processes run as the user who started `strike serve`. OS sandboxing
for bash is a separate dial (`sandbox` in config, default workspace-write)
and is not disabled by `--auth`.

## Threat model (this process)

- A process that can connect to the bind address and that knows the token
  can do what the authenticated API allows: send ops, read sessions, read
  files in the serve working directory. Treat the token like a password
  for that user account.
- Binding publicly is rejected so a stolen token is not reachable from the
  internet through this process. A private bind is still a network edge;
  use it only on a network you trust, with `--auth`.
- The static shell and `/health` are reachable by anyone who can open the
  port. They confirm the process is up. They do not accept ops.
- CORS reflects loopback and private/CGNAT browser origins only. Public
  origins are not reflected. State-changing requests with any other
  `Origin` are rejected.
- Query tokens on `/v1` are rejected so the secret is not copied into logs.
- `strike daemon logout` rotates the file. It does not revoke a cookie
  already stored in a browser until that browser's next request fails and
  the user pastes the new token. Close those tabs after logout.
