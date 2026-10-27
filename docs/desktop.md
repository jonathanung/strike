# Desktop shell

`desktop/` is a Rust gateway manager and a Tauri window around the Leptos
cockpit that `strike serve` already hosts. It does not replace the web UI
and it is not part of `go.work`.

The window (or a browser, for the headless binary) talks only to a proxy on
`127.0.0.1`. That proxy forwards to the gateway you picked and adds the
bearer. The token is not put in the page URL, the cookie jar, or
`gateways.json`.

## Run it

```sh
# gateway manager, no WebKit required
# Linux links libdbus for the keyring: pkg-config and libdbus-1-dev
cargo run --manifest-path desktop/Cargo.toml -p strike-desktop
# open the printed http://127.0.0.1:<port>/_desktop/

# Tauri window, tray, and OS notifications (WebKitGTK on Linux)
# sudo apt install libwebkit2gtk-4.1-dev libgtk-3-dev \
#   libayatana-appindicator3-dev librsvg2-dev patchelf libssl-dev
cargo run --manifest-path desktop/Cargo.toml -p strike-desktop-app
```

Tests:

```sh
make desktop-test
```

CI runs that crate test. It does not build the Tauri package.

A local daemon is spawned only when you connect the "sidecar" gateway
(`strike serve --auth` on `127.0.0.1:0`). Remote-only use never starts one.
The spawned process mints its own token and prints it on a pipe this shell
reads. The token is not passed on the command line.

## Gateways

| How you add it | What the shell does |
|---|---|
| `http://127.0.0.1:8787` | Connects to a daemon already on this machine |
| `https://…` | Connects with normal Web PKI, or a pinned self-signed certificate |
| `http://100.64.…` or another Tailscale address | Refused until you allow cleartext. HTTPS needs no extra checkbox |
| `ssh://user@host:8787` or `user@host` | Runs `strike connect`, which forwards to that host's loopback and keeps a 15-minute grant in that process |
| `sidecar` | Spawns `strike serve` |
| `strike://connect?url=…&token=…` | Saves the gateway and stores the token |
| `strike://pair?code=…` | Refused. Pairing exchange is #1289 |

`Alt+Shift+W` on the cockpit returns to this list. `Alt+W` stays the
in-page gateway switcher and `Alt+G` stays Goals. `j` / `k` move, Enter
connects, `n` focuses the name field, `t` toggles the theme.

The cockpit's navbar switcher is the one in the web UI. This shell does
not copy it. A corner link is injected only into HTML that passed through
the proxy, so a browser pointed straight at `strike serve` does not grow it.

## Security

- The proxy binds `127.0.0.1` and rejects any other `Host`.
- Requests with a cross-site `Origin` or `Sec-Fetch-Site` are refused,
  including GET. No CORS headers are added. The window loads this origin
  directly, so that does not block the cockpit.
- Upstream `Set-Cookie` is dropped so the daemon token is not stored in the
  browser. JSON and SSE both go through the proxy, which sets
  `Authorization`.
- Certificate trust is trust-on-first-use. The saved pin is the SHA-256 of
  the end-entity certificate. A later mismatch is an error, not a new prompt
  that overwrites the pin by itself.
- An SSH gateway is `strike connect` with an argv list, not a shell.
  The host must look like `user@host`. The grant is not stored here.
  Host-key policy is whatever `strike connect` already uses.
- The shell does not pass `--allow-private-bind` and does not change the
  daemon's bind or CORS.

## What is not built

- **Auto-update.** Do not ship a release pipeline in this change. When one
  exists, the recommended path is the Tauri updater with a minisign key,
  GitHub Releases as the endpoint, a check on launch, and an explicit
  confirm. No silent install, and no phone-home in this shell today.
- **In-app QR camera.** Paste the link. Scanning waits on #1289, and the
  phone's own camera can open a URL without this shell.
- **A second gateway switcher.** The page already has one. This list is
  the shell's saved gateways, including ones the browser cannot reach
  until `strike connect` or a Tailscale address is up.
- **Session notification stream.** The tray notifies when a gateway connects
  or fails. It does not subscribe to the in-app notification centre.
- **Protocol field on `/health`.** `version` there is the product version.
  This shell compares `protocol` or `protocolVersion` to `1.19.0` only
  when that field is present.
- **Windows and macOS bundles.** Not measured here. On Windows the same
  Linux build can run under WSL2; a native WebView2 shell is a `tauri build`
  on Windows, with the keychain crate's Windows credential store. macOS uses
  the Keychain when built on macOS.

## Second machine

Still needs a real second computer:

- Tailscale or another VPN path, not just a second port on localhost
- SSH to a host that is not this VM, including host-key prompts
- macOS Keychain and Windows Credential Manager
- a packaged install registering `strike://`
- installer size for `.dmg` / `.msi` / AppImage
