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
| `strike daemon logout` | Replace that file with a new token and print it. Short-lived grants drop, and paired browser sessions are revoked, on the next `/v1` request. |
| `strike daemon grant [--ttl 15m] [--base URL]` | Ask the running daemon for a short-lived bearer. The secret is printed on stdout only. |
| `strike daemon revoke [--base URL]` | Drop every short-lived grant. Does not rotate the host token file. |

`logout` does not kill the process. A serve started with `--token-file`
loads the new token on the next `/v1` request; the previous bearer fails,
every grant is dropped, outstanding pairing codes are dropped, and paired
browser sessions are revoked. A serve started with `--token` keeps the
value it was given until restart, including grants and paired sessions, so
`logout` does not affect it. The token is a password. Do not commit it or
pass it on a shared command line.

`grant` and `revoke` read the host token from `~/.strike/daemon.token` and
send it as a bearer to the loopback daemon. They do not print that host
token. Errors on those commands are passed through `pkg/redact` and the
exact secret is replaced before anything is written.

## Pairing

A phone or another browser must not receive the daemon token in a URL.
`strike serve` (when `--auth` is on) and `strike gateway url` mint a
short-lived code and print a URL whose fragment is `#pair=<code>`. The
fragment is not sent on the HTTP request, so it does not land in access
logs, proxy logs, or `Referer`. The web UI draws the same QR from
`POST /v1/pair` and offers Copy. `strike gateway url --qr` and
`strike gateway qr` draw a UTF-8 block QR in the terminal.

`strike connect` is not this command. SSH forwards and a gateway switcher
belong to #1265, which can alias `strike connect --qr` at this URL.

| Command | Effect |
|---|---|
| `strike gateway url [--qr]` | Mint one code and print the URL. `--qr` adds a block QR. |
| `strike gateway devices` | List paired browsers. |
| `strike gateway revoke <id>` | Revoke one paired browser. |

The token is read from `--token-file` or `~/.strike/daemon.token`. These
commands never mint a token file and never print the daemon token. If a
response body contains that token, the command refuses to print it.

### HTTP API

Native clients (a future React Native app included) use the same routes.
All JSON. `Cache-Control: no-store`. Send the daemon token as
`Authorization: Bearer` or the HttpOnly cookie. Do not put `token`, `code`,
or `pair` in the query string; those requests fail with `query_secret` and
do not consume a code.

| Request | Who | Result |
|---|---|---|
| `GET /v1/gateway` | daemon token | `{bind, auth, loopback, phoneReachable, lan, urls[], optIn, lanWarning, advertiseWarning, unbound}`. No code and no token. |
| `POST /v1/pair` | daemon token | `201` `{code, expiresAt, ttlSeconds, url, gateway}`. A paired session gets `403` `daemon_token_required`. Without `--auth`, `400` `auth_disabled` plus `gateway` and no code. |
| `POST /v1/pair/redeem` | public, rate limited | Body `{code, label, delivery}`. `delivery` is `cookie` (default) or `token`. |
| `GET /v1/pair/devices` | daemon token | `{devices: [{id, label, createdAt, lastSeen, revoked}]}`. |
| `DELETE /v1/pair/devices/{id}` | daemon token | Revoke one browser. |
| `POST /v1/pair/revoke-all` | daemon token | Revoke every session and drop outstanding codes. |
| `POST /v1/pair/revoke-self` | that session | Revoke the calling session and clear the cookie. The daemon token gets `400` `use_device_revoke`. |

`cookie` delivery sets `strike_serve_token` (`HttpOnly`, `SameSite=Strict`,
`Path=/`, `Max-Age` 30 days) and the JSON body has no `sessionToken`.
`token` delivery returns `sessionToken` (`s1.` plus 32 random bytes) and
sets no cookie. Use `token` from a native client or a cross-origin scan.
Browsers on the gateway origin use `cookie`.

Codes look like `p1.` plus 16 random bytes, URL-safe, stored only as
SHA-256. Default life is 5 minutes. The first successful redeem burns the
code (`410` `code_used` after that, `410` `code_expired` after the TTL).
At most 32 live codes (`429` `too_many_codes`). Redeem attempts are limited
to 8 per minute per client IP and 64 per minute globally (`429`
`rate_limited`, `Retry-After: 60`). Comparisons walk every stored hash in
constant time. Error bodies do not echo the code or the daemon token.

`strike serve --advertise-url <url>` puts that tunnel origin in the QR
while the process stays bound to its listen address. The URL must be
http(s), with a host, and without userinfo, query, or fragment. The default
bind stays loopback. The API does not bind `0.0.0.0`. When `phoneReachable`
is false, `optIn` is the exact step to take (private bind with
`--allow-private-bind`, an SSH local forward, or `--advertise-url` for a
tunnel you run). A private bind sets `lanWarning`.

The web client may rewrite a loopback QR to `location.origin` when that
origin is not itself loopback, so an operator who already arrived through
a tunnel still hands the phone a reachable URL. The server does not trust
the request `Host` header for this.

Opening `/?token=` once is still the legacy cookie handoff for the daemon
token itself. It is not the phone path. Do not print it from `strike serve`.

In-browser scanning uses `BarcodeDetector` and `getUserMedia` where the
browser has them (`Permissions-Policy: camera=(self)`). A scanned link is
rejected when `token`, `code`, or `pair` is in the query string. Same-origin
scans redeem with `delivery: cookie`. Cross-origin scans redeem with
`delivery: token` and no credentials, then store that base URL in
`strike.gateways` and the session in `sessionStorage` (`strike.gateway.tokens`).
The phone camera opening the URL in a browser does not need this scanner.
A small install hint listens for `beforeinstallprompt` after a same-origin
redeem. The service worker is owned by the notification and PWA work, not
by this dialog.

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

`strike connect user@host` is the path that keeps the secret off the URL.
A phone that should open the cockpit uses the pairing URL (`#pair=`, see
Pairing). When the QR should name a tunnel you already run, pass
`--advertise-url`. Strike does not start that tunnel, and the daemon token
is not part of the pairing URL.

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

## Terminal

`/v1/terminals` is a shell as the user who started `strike serve`. The
routes sit behind the same auth middleware as the rest of `/v1`. A browser
WebSocket cannot set `Authorization`, so the stream accepts the HttpOnly
`strike_serve_token` cookie or a single-use ticket.

The client mints that ticket with `POST /v1/terminals/{id}/ticket` (bearer
or cookie) and presents it as the WebSocket subprotocol `ticket.<hex>`
next to `strike.terminal`. The server echoes only `strike.terminal`. The
ticket lasts 30 seconds, is bound to that terminal id, and is consumed on
every upgrade that presents it, including when the cookie or bearer already
matched. A mismatch or a replay is 401. A query-string `token` or `ticket`
on a terminal route is rejected. The ticket is not a bypass for any other
`/v1` path.

Loopback listeners allow the terminal. Any other bind requires
`--terminal-remote`, and that flag requires `--auth`. A tailnet or private
bind has no shell until the operator passes the flag.

The client does not pick an arbitrary working directory. Create resolves
the path inside the active workspace and rejects `..`, a sibling prefix,
and a symlink that leaves the workspace. On Linux the daemon watches the
foreground process group: the shell behind `bwrap`, and a child that is
currently foreground (`bash -c 'cd /tmp'`). A cwd outside the workspace
kills the shell. An unreadable cwd kills it when no OS sandbox was applied.
Background helpers are not walked. A command that exits before the next
sample can still finish; the sandbox is the write boundary when wrap applies.

The policy is compiled at create from the live engine for that workspace,
the same helper bash uses, so plan and phase hard-denies and deny paths
match the current session. `--attach-only` does not load a dial, and the
terminal routes refuse a shell there. A missing engine with a non-off dial
fails closed. When the dial is not `off`, the shell is started through
`sandbox.Wrap`. If that helper is missing and degrade is not allowed,
create fails closed.

Plan mode on the workspace or the resolved live session refuses a new
shell. An omitted session id uses the active root. A non-plan live mode
does not clear a workspace plan lock. Other permission modes allow the
shell: the user is at the keyboard, which is why `!` skips the bash prompt
too. Keystrokes are not written to the audit log. `terminal.create` and
`terminal.kill` are.

The shell belongs to the user. The agent does not write to the PTY. The
panel says when the bound session is busy in the same directory.
`onSessionEnd` defaults to `kill` (the process ends when that session root
closes). `keep` leaves it running. Closing the browser tab does not kill
the PTY; the next visit reattaches and replays the scrollback ring.

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
- The terminal is a shell as the serve user. It is authenticated, loopback
  unless `--terminal-remote` is combined with `--auth`, and confined to the
  workspace at create and by the foreground cwd watch. See Terminal above.
  Keystrokes are not logged. A presented websocket ticket is consumed even
  when the cookie already authorized the upgrade, and it does not authorize
  any other route. `--attach-only` does not start a shell.
- `strike daemon logout` rotates the file. On the next `/v1` read every
  grant is dropped, outstanding pairing codes are dropped, and paired
  browser sessions are revoked. A cookie already stored in a browser fails
  on its next request. Close those tabs after logout. Browser "Log out"
  (`POST /v1/gateway/logout`) clears this browser's cookie and the presented
  grant. `POST /v1/pair/revoke-self` revokes the calling paired session.
  A `--token` process keeps the old value until restart.
- Pairing codes are single-use, expire in minutes, and are stored as
  SHA-256. The URL fragment `#pair=` is not sent to the server. Redeem
  rejects `token`, `code`, or `pair` in the query string without consuming
  the code. The daemon token is never placed in that URL. A paired session
  cookie is not the daemon token and cannot mint codes or list devices.
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
  grants are live. Pairing compares SHA-256 digests the same way.
- A public tunnel in front of loopback removes the "no public listener"
  property. The token is then the only boundary. Strike does not start
  that tunnel. `--advertise-url` only changes the QR text.
