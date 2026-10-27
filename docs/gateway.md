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
| `strike daemon logout` | Replace that file with a new token and print it. Short-lived grants drop on the next `/v1` request. |
| `strike daemon grant [--ttl 15m] [--base URL]` | Ask the running daemon for a short-lived bearer. The secret is printed on stdout only. |
| `strike daemon revoke [--base URL]` | Drop every short-lived grant. Does not rotate the host token file. |

`logout` does not kill the process. A serve started with `--token-file`
loads the new token on the next `/v1` request; the previous bearer fails,
and every grant is dropped when that read observes the new file. A serve
started with `--token` keeps the value it was given until restart, so
`logout` does not affect it. The token is a password. Do not commit it or
pass it on a shared command line.

`grant` and `revoke` read the host token from `~/.strike/daemon.token` and
send it as a bearer to the loopback daemon. They do not print that host
token. Errors on those commands are passed through `pkg/redact` and the
exact secret is replaced before anything is written.

Provider credentials (`strike auth logout <provider>`) are a different
store. Logging out of a model provider does not rotate the gateway token.

## Short-lived grants

A grant is a second bearer, minted with the same 32-hex `MintToken` as the
host token, so the two are the same length. The daemon stores only the
SHA-256. The raw value is returned once (`POST /v1/gateway/grants`,
`Cache-Control: no-store`) and is never written to disk.

| Rule | Behavior |
|---|---|
| TTL | Default 15 minutes. Minimum 1 minute. Maximum 24 hours. |
| Cap | 32 live grants. The next mint returns HTTP 429. |
| Accept | Every presented value, including an empty header, is hashed. Each hash is compared with `subtle.ConstantTimeCompare` against a fixed 32-slot array, so a miss is not faster when fewer grants are live. Unused slots are zero. A match on those zeros would require a SHA-256 preimage of 32 zero bytes. |
| Power | A grant calls `/v1/*` the same way the host token does. Mint, revoke-all, and rotate return 403 `host token required`. |
| Host change | Replacing the token file, or `strike daemon logout` against a `--token-file` process, drops every grant on the next `currentToken` read. The first observation at startup records the hash and does not revoke. |
| Browser logout | `POST /v1/gateway/logout` clears the `strike_serve_token` cookie and revokes the presented grant if it is a grant. It does not rotate the host token. |
| Cookie handoff | `/?token=` and `/attach?token=` still set the cookie and redirect to a URL without the secret. If the query value is a live grant, the cookie stores that grant. It is not upgraded to the host token. `?token=` on `/v1/*` is still rejected. |
| Rotate | `POST /v1/gateway/rotate` requires `--token-file` (409 otherwise) and the host token. It writes a new 0600 file, returns the new token once, and revokes grants. |
| Open loopback | Without `--auth`, `GET /v1/gateway` reports `credential: open`. Mint, revoke, and rotate return 403. |

## Reaching a gateway that is not on this machine

The process stays on loopback, or on a tailnet address you opted into
with `--allow-private-bind` and `--auth`. Nothing in this tree opens a
public port.

`strike connect user@host` is the path that keeps the secret off the URL:

1. SSH local-forward to the remote loopback only (`--remote` must be
   loopback; the default is `127.0.0.1:8787`). The SSH options are
   `BatchMode=yes`, `ForwardAgent=no`, and `ExitOnForwardFailure=yes`.
   There is no `StrictHostKeyChecking=no`.
2. On the remote, the forwarded command is shell-quoted
   `strike daemon grant`. The host token is read on that machine. It is
   not placed on the SSH command line from the laptop.
3. A second loopback listener on the laptop reverse-proxies to that
   forward. It strips `?token=` from the request URI. It adds
   `Authorization: Bearer` only when the browser did not send one. Its
   error log is discarded. The URL it prints has no token.
4. Ctrl+C closes the forward and the proxy. The grant lived in that
   process's memory.

```sh
strike connect user@host
strike connect --ttl 15m --local 127.0.0.1:8787 --save lab user@host
strike connect lab
strike connect --list
strike connect --forget lab
```

Saved CLI profiles are `~/.strike/gateway-profiles.json`, mode 0600:
name, ssh destination, remote port, local port, ttl. Load refuses the
file if it contains a `"token"` key or `token=`. A public or unspecified
local listen address is rejected.

A plain forward still works, and still needs a credential the browser
can present:

```sh
ssh -L 8787:127.0.0.1:8787 user@host
```

Prefer `strike connect` so the grant is injected on loopback. Opening
`/?token=` remains the one-shot cookie handoff when you already hold a
bearer. Do not put that URL in a chat log or a screenshot.

### Named tunnels this process does not start

Point them at the loopback port. Strike does not mint a public URL.

| Tunnel | What it is |
|---|---|
| Tailscale Serve | Recommended when the browser is on your tailnet. It proxies to loopback. The daemon can stay on `127.0.0.1`. |
| Tailscale bind | `strike daemon install --addr <tailnet-ip>:8787 --allow-private-bind` listens on the tailnet interface itself. Still requires `--auth`. |
| SSH reverse | `ssh -R 127.0.0.1:8787:127.0.0.1:8787 user@host` publishes the daemon onto that host's loopback. You still need a bearer there. |
| Public tunnel (Cloudflare Tunnel and similar) | This is a public edge. A public URL plus the token is the account. Do not use one unless you mean to. |

## Web UI switcher

The header mounts a Gateways control (Alt+W) registered with the web shell; Alt+G belongs to Goals, which landed first. The Daemon
view mounts the same component inline, on the same signals. The navbar
and command palette do not own this control. The existing connection
chip stays in the header so another stream can keep owning heartbeat
copy; the switcher adds Unauthorized and Reconnecting to that chip's
labels.

Profiles in the page are `localStorage` key `strike.gateways`: name,
base URL, optional `user@host` hint. They do not contain a token. The
token for a profile is `sessionStorage` key `strike.gateway.tokens`
(per profile id, plus `this` for the page you loaded). A legacy
`localStorage` key `strike.token` is copied into the `this` session
entry and then deleted. Legacy `strike.remotes` bookmarks are copied
into `strike.gateways` without a secret.

The token input is `type=password`. Rotate's status line does not print
the new value. The client refuses a base URL that contains `token=`, a
userinfo, a query, or a fragment, and it refuses a literal public IP.
A non-IP hostname is saved with a warning (Tailscale MagicDNS is not a
literal private address). The switcher does not open an SSH session.
`strike connect <hint>` is shown as text next to a saved profile.

Connection states the panel can show: connected, unauthorized,
unreachable, reconnecting, checking. Reconnect probes `/health` and
then `GET /v1/gateway`.

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

What an attacker gets:

- A process that can connect to the bind address and that knows the host
  token or a live grant can do what the authenticated API allows: send
  ops, read sessions, read files in the serve working directory. Treat
  either secret like a password for that user account. A grant cannot
  mint another grant, revoke the others, or rotate the host file.
- Binding publicly is rejected so a stolen token is not reachable from
  the internet through this process. A private or tailnet bind is still
  a network edge; use it only on a network you trust, with `--auth`.
- The static shell and `/health` are reachable by anyone who can open the
  port. They confirm the process is up. They do not accept ops. They do
  not return the token.
- CORS reflects loopback and private/CGNAT browser origins only. Public
  origins are not reflected. State-changing requests with any other
  `Origin` are rejected.
- Query tokens on `/v1` are rejected so the secret is not copied into
  access logs. `strike connect`'s local proxy also strips `?token=` and
  does not log the grant. `strike daemon grant` prints the grant on
  stdout on purpose; do not redirect that stream into a shared log.
- `strike daemon logout` rotates the file and drops grants on the next
  `/v1` read. A cookie already stored in a browser fails on its next
  request. Close those tabs after logout. Browser "Log out" only clears
  this browser's cookie and the presented grant.
- The `strike connect` proxy holds the grant in memory until you press
  Ctrl+C. Anyone who can send HTTP to that loopback port while it is
  running is authenticated as the grant. It binds loopback only.
- SSH from `strike connect` does not forward an agent and does not
  disable host-key checking. A missing key fails (`BatchMode=yes`)
  instead of prompting. The remote command cannot target a non-loopback
  daemon address.
- Browser tokens live in `sessionStorage` for the daemon origin. Script
  on that origin can read them. The shell is the daemon's own page, so
  an XSS bug in the cockpit is a token disclosure. They are not in
  `localStorage`, not in the address bar, and not in the profile file.
- Host tokens and grants are 32 hex characters. `tokenEqual` returns
  false immediately when either side is empty. Otherwise it calls
  `subtle.ConstantTimeCompare`, which depends on length and not on the
  bytes. The grant accept path hashes every presented value and compares
  those 32-byte digests against a fixed 32-slot array, so a wrong grant
  is not faster than a right one and a miss is not faster when fewer
  grants are live.
- A public tunnel in front of loopback removes the "no public listener"
  property. The token is then the only boundary. Strike does not start
  that tunnel.
