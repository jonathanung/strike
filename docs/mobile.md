# Phone cockpit

The gateway is a web page. A phone on the same tailnet opens it in the
browser. There is no React Native client (#1291 stays planned). This page
is the reachability story. The layout lives in `webui/src/mobile.rs` and
`webui/mobile.css`.

## What the phone UI is

Below 720px wide, and in a short landscape window (height under 500px and
width under 1024px), the shell is one pane plus a bottom nav: Sessions,
Chat, Activity, Files, Daemon. The composer stays on Chat. Approval and
model open as sheets. Enter inserts a newline; Send is the button. Targets
that a thumb hits are at least 44px. Pins stay visible without hover.

1024×594 and 1440×900 do not match that query, so the three-column shell
stays. A drawer was rejected: five destinations would cost an extra tap.
Shrinking the three columns into 360px was rejected because the composer
ends up below the fold.

The page height follows `visualViewport` (`--vv-height`, `--vv-top`) so the
composer stays in view when the keyboard opens. `viewport-fit=cover` and
`env(safe-area-inset-*)` pad the header and the nav. `interactive-widget=resizes-content`
is set for browsers that honor it. Pull down on a pane that is already at
the top, or bring the page back from sleep (`visibilitychange` /
`pageshow`), and the client refreshes. If the live stream is not open it
is recreated.

Memory, knowledge, and artifacts are not drawn. The daemon routes exist
for memory and artifacts, and the panels are still
[planned](webui-planned.md). A phone nav item for an empty panel would
pretend otherwise.

The manifest is standalone. `theme_color` and `background_color` are the
dark ground token `#14131c` from `schemas/ui-tokens.json`. The live
`theme-color` meta follows the active theme. Install is Add to Home Screen
/ the browser install UI. Service worker and manifest were already served
at the origin root.

## Bind

The flag is `--addr`. There is no `--listen`.

Default is `127.0.0.1:8787`. Public addresses and `0.0.0.0` / `::` are
rejected. A private or Tailscale address (RFC1918 or `100.64.0.0/10`) is
accepted only with both `--allow-private-bind` and `--auth`. When the
process is not on loopback, the banner prints a warning and points here.
The listener is cleartext HTTP. Prefer HTTPS in front of loopback so the
phone gets a secure context (service worker, clipboard).

### HTTPS on the tailnet (preferred)

Leave the daemon on loopback and let Tailscale terminate TLS:

```sh
strike serve --auth --token-file ~/.strike/daemon.token
tailscale serve --bg 127.0.0.1:8787
```

`tailscale status` prints the MagicDNS name. The phone opens
`https://<host>.<tailnet>.ts.net/?token=…` once. The proxy is on loopback
and sends `X-Forwarded-Proto: https`, so the attach cookie is `Secure`.
After the redirect the URL has no token.

### Cleartext on the tailnet IP

```sh
strike serve --auth --token-file ~/.strike/daemon.token \
  --addr 100.x.y.z:8787 --allow-private-bind
```

The phone opens `http://100.x.y.z:8787/?token=…` once. The cookie is not
`Secure`, because the browser is on HTTP. `X-Forwarded-Proto` is ignored
unless the peer is loopback, so a client on the tailnet cannot flip the
flag.

WireGuard is the same cleartext path. `ip.IsPrivate()` covers RFC1918, so
the daemon accepts a WireGuard address only with both flags:

```sh
strike serve --auth --token-file ~/.strike/daemon.token \
  --addr <wg-ip>:8787 --allow-private-bind
```

The phone opens `http://<wg-ip>:8787/?token=…`. The cookie rules match
`http://100.x`: not `Secure`, and a non-loopback client cannot set
`X-Forwarded-Proto`.

SSH local forward is the other path. It is already in
[gateway.md](gateway.md). It is a poor phone path: the phone would need
its own SSH client.

## Cookie, origin, resume

The attach cookie is HttpOnly and `SameSite=Strict`. It is set by
`/?token=` or `/attach?token=` and the redirect strips the secret.
`?token=` on `/v1/*` is still rejected.

State-changing requests with an `Origin` must be either a loopback /
private / CGNAT origin, or the same host as the request `Host`. That second
case is MagicDNS: the page at `https://host.tailnet.ts.net` posts to
itself. `https://evil.example` posting at that host is forbidden. CORS
reflects the same set and does not send `Access-Control-Allow-Credentials`.
Same-origin phone loads do not need CORS; the check still runs because
browsers send `Origin` on `POST`.

`/v1/live/events` and `/v1/sessions/{id}/events` write SSE `id:` as the
byte offset after each JSONL line. EventSource sends that value back as
`Last-Event-ID` when the same connection retries (radio drop, sleep). A
cursor past the end of a replaced log starts over. WebSocket clients pass
the same offset as `?after=`.

A phone resume (`visibilitychange`, `pageshow` from bfcache, or pull)
looks at `EventSource.readyState`. `CONNECTING` (0) is left alone so the
browser's `Last-Event-ID` retry is not cancelled. `OPEN` (1) or `CLOSED`
(2) opens a new `/v1/live/events?root=…&after=<offset>` from the last
`MessageEvent.lastEventId` and keeps the transcript already on screen.
Switching sessions clears that cursor and the column. A brand-new
EventSource does not send `Last-Event-ID` by itself; `?after=` is how the
recreated stream skips the replay.

## Pairing

QR and a copyable pairing URL are #1289. This stream does not draw a second
code. Until that lands, the phone flow is the MagicDNS or `100.x` URL
above, opened once with `?token=`.

## What a real phone still has to prove

This repo's browser pass uses headless Chrome device metrics (390×844 and
360×740, DPR 3, touch emulation) and a short landscape 844×390. It does
not run on iOS or Android.

Still needs a device:

- The software keyboard actually covering the composer (Safari and Chrome),
  including the accessory bar.
- `env(safe-area-inset-*)` with a notch or home indicator. Emulation
  reports 0.
- Add to Home Screen, standalone status bar, and the SVG icon on iOS
  (older iOS wants a PNG).
- Cookie storage in standalone mode and after the phone sleeps on both
  `https://*.ts.net` and `http://100.x`.
- Tailscale serve from a phone that is only on the tailnet, including a
  radio change and a locked screen.
- Pull-to-refresh against the real overscroll, which headless touch does
  not reproduce.
