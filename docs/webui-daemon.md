# Web-first strike: Rust UI + Go daemon

Status: walking skeleton on branch `worktree-rust-webui-daemon`. Not merged.

## Decisions

- The web UI is the primary client. One responsive Leptos (Rust/WASM) app,
  installable as a PWA, serves desktop browser, web, and phone. A Tauri
  desktop shell can wrap the same bundle later; it is not required.
- The Go engine stays separate and mostly unchanged. The UI talks to it only
  through the existing daemon API (`/health`, `/v1/*`, `/v1/ws`, `/v1/ops`,
  protocol Ops/Events). No engine rewrite. The bind, token, and remote-access
  rules are [gateway.md](gateway.md).
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
  validated on the daemon host; ids are stable path hashes. A workspace is
  a named directory plus an optional rules file and permission mode. It is
  not a session folder. Sessions do not store a workspace id yet, and
  activate does not change the engine working directory or the live
  permission mode. `PATCH /v1/workspaces/{id}` sets `rules` (a regular file
  inside the directory; symlinks that escape are rejected) and
  `permissionMode` (`default`, `plan`, `soft-approve`, `accept-edits`,
  `yolo`). Empty strings clear the field.
- API: `GET/POST /v1/workspaces`, `POST /v1/workspaces/{id}/activate`,
  `PATCH /v1/workspaces/{id}`, `DELETE /v1/workspaces/{id}` (token-gated
  like all `/v1/*` under `--auth`).
- Per-child git worktrees already exist and stay off unless asked.
  `session.childIsolation` `off` or `shared` (the default) keeps the
  parent's directory. `worktree`, or a spawn `isolation: "worktree"`,
  checks out `<repo>/.strike/worktrees/<child-id>/`. There is no second
  flag. Outside a git repository the engine stays on the shared directory.
  Delegated child working directories are not yet checked against the
  workspace registry; that waits until a session actually runs in a
  registered directory ([webui-planned.md](webui-planned.md)).
- `strike daemon install|uninstall|status|token` + systemd user unit.
- `strike serve --token-file` (stable 0600 token, minted once) and
  `--allow-private-bind`.
- `webui/`: Leptos client. The shell is a session list, a transcript, and
  a side panel. Daemon connect, workspace policy, and saved remotes stay on
  the Daemon view.
  - Session list merges live roots (`/v1/roots`) with saved sessions.
    A gutter marks in progress or unread. Chips filter Unread, In progress,
    and Pinned. Search matches title, id, or a delegated child's title.
    Older rows fold after six, keeping pinned, busy, and the open session.
    Delegated children (`GET /v1/sessions/{id}/children`, team state when
    the roster has them) nest under the open session. Unread and pinned
    live in browser storage (`strike.seen`, `strike.pins`); the server does
    not store them.
  - The top bar has a command field (Ctrl+K or Cmd+K) for a new session,
    stop-all, the Chat and Daemon views, Steer/Queue, and the five approval
    modes. A gateway chip follows `/health`. Back and Forward walk the
    sessions opened in this tab. Focus hides the side columns.
  - The composer posts `user.input` when the root is idle. While it is
    busy, Steer posts `steer` and Queue posts `user.input`. That choice is
    `strike.enterMode` (default Queue, the same send as before). The
    approval chip posts `set.permission_mode` with Strike's real modes:
    `default` (ask on the normal rules), `plan` (writes denied; stricter
    than read-only shell), `soft-approve` (15s countdown then allow once),
    `accept-edits` (file edits allowed; shell and network still ask),
    `yolo` (this session skips asks that are not hard-denied; other
    sessions stay as they are). There is no read-only-shell mode and no
    all-sessions skip. The model chip shows the live provider and model
    from `/v1/bootstrap`. A catalog `GET /v1/models` that fails stays
    "catalog unavailable" and does not invent models. `select.model` is
    rejected while a turn is running.
  - Empty sessions rotate a short headline and offer six prompts that send
    as ordinary text. A failed load shows the error and Retry.
  - The side panel is Activity (team snapshot: status, elapsed, stop,
    retry, expand, including `blocked` handoffs), Files (`/v1/files`),
    Changes (`/v1/changed-files`), Memory, Knowledge, and Artifacts.
    Clicking or dragging a file inserts `@path` as text. The web composer
    does not expand that token the way the TUI does. Memory mode
    (normal, incognito, temporary) is a composer chip and a palette command.
    Open on a file reads it in the center document pane
    (`GET /v1/file/view`). The pane is read-only until Edit. Save is
    `PUT /v1/file` with the sha256 and mtime from the read; a mismatch is
    409 and does not write. Plan mode and an edit/write deny rule reject
    the save. Symlinks, binaries, oversize files, and paths outside the
    work root (or an explicit allowed-root ceiling) are rejected. Markdown
    can preview. Find-in-file is local. Agent diff reuses
    `GET /v1/changed-files` for the open path. Transcript `path:line` and
    `strike-file:path#Lline` open the same pane.
  - Goals (`/v1/goals`, the Goals view) define a verification command,
    budgets, and an autopilot posture. Deny rules still apply. See
    [goal.md](goal.md).
  - Side chat is a read-only drawer (Activity, an Activity row, the composer,
    or Alt+S). `POST /v1/sidechat/ask` answers from the session transcript
    with a separate model call and no tools. `POST /v1/sidechat/ops` refuses
    `permission.reply` and `user.input`. Turns are not written to the session
    JSONL.
  - Schedule, the task runner, onboarding, and a multi-gateway hub are
    documented in [webui-planned.md](webui-planned.md), not drawn as empty
    panels. Memory, knowledge, and artifacts are drawn from the live routes.
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
2. Permission asks in the transcript. The composer can set the mode;
   an in-flight ask is still only the protocol event.
3. Session list and resume follow the active workspace. Files and diffs
   follow the daemon working directory, which is not the workspace path.
4. Per-remote tokens in the UI (today one token field for the selected
   daemon), daemon discovery on the tailnet.
5. Tauri shell (optional), TUI removal decision, `docs/web.md` rewrite.
