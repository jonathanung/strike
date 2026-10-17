# Web-first strike: Rust UI + Go daemon

Status: walking skeleton on branch `worktree-rust-webui-daemon`. Not merged.

## Decisions

- The web UI is the primary client. One responsive Leptos (Rust/WASM) app,
  installable as a PWA, serves desktop browser, web, and phone. A Tauri
  desktop shell can wrap the same bundle later; it is not required.
- The Go engine stays separate and mostly unchanged. The UI talks to it only
  through the existing daemon API (`/health`, `/v1/*`, `/v1/ws`, `/v1/ops`,
  protocol Ops/Events). No engine rewrite.
- Strike runs as a per-user daemon: `strike daemon install` writes a systemd
  user unit running `strike serve --auth --token-file ~/.strike/daemon.token`.
- Remotes are just other daemons you own. Bind stays loopback by default;
  `--allow-private-bind` (requires `--auth`) additionally allows RFC1918 and
  CGNAT/Tailscale (100.64.0.0/10) addresses. Public binds are rejected; there
  is no built-in multi-user login. The UI stores your daemon list (name +
  URL) in browser local storage and sends the per-daemon bearer token.
- The old JS `web/` UI is removed. `strike serve` is API-first and serves the
  Rust bundle from the same embed (`internal/frontend/server/static`).
- The TUI is deprecated, not deleted: bare `strike` prints a pointer,
  `strike tui [options]` (or flag form) still launches it with a notice.

## What exists in this slice

- `internal/persist/workspace`: durable workspace registry
  (`~/.strike/workspaces.json`, 0600, atomic write). Directories are
  validated on the daemon host; ids are stable path hashes.
- API: `GET/POST /v1/workspaces`, `POST /v1/workspaces/{id}/activate`,
  `DELETE /v1/workspaces/{id}` (token-gated like all `/v1/*` under `--auth`).
- `strike daemon install|uninstall|status|token` + systemd user unit.
- `strike serve --token-file` (stable 0600 token, minted once) and
  `--allow-private-bind`.
- `webui/`: minimal Leptos app — daemon connect (base URL + token in local
  storage), health check, saved remotes, workspace list/add/activate — plus
  PWA manifest, service worker (app shell only; API never cached), and icon.
- Server serves `/sw.js` and `/manifest.webmanifest` at the origin root and
  the bundle under `/assets/` (WASM-aware CSP and content types).

## Run it

```sh
nix develop -c make webui-build   # flake provides Go + Rust (fenix, wasm32) + trunk
nix develop -c make build
./strike daemon install     # or --addr <tailnet-ip>:8787 --allow-private-bind
systemctl --user enable --now strike-daemon
./strike daemon token       # paste into the UI
# open http://127.0.0.1:8787/ (or the tailnet URL) on desktop or phone
```

Foreground instead of systemd: `./strike serve --auth --token-file ~/.strike/daemon.token`.

## Not yet done (next slices, in order)

1. Engine root per workspace: opening a workspace currently records it as
   active; spawning/attaching a live engine rooted at that directory (today
   roots spawn in the daemon's cwd) is the next engine-plumbing slice.
2. Chat/session UI on `/v1/ws` + `/v1/ops` (transcript, composer, tool
   calls, permission asks) — the Cursor/kirocrew-shaped surface.
3. Session list/resume per workspace, file explorer, diffs.
4. Per-remote tokens in the UI (today one token field for the selected
   daemon), daemon discovery on the tailnet.
5. Tauri shell (optional), TUI removal decision, `docs/web.md` rewrite.
