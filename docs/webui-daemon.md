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
  a named directory plus an optional rules file, a permission mode, and
  extra roots. It is not a session folder. A root session records
  `workspaceId`. Web-created sessions use that directory. Activating a
  workspace applies its stored permission mode to the active live root.
  `PATCH /v1/workspaces/{id}` sets `rules` (a regular file inside the
  directory; symlinks that escape are rejected), `permissionMode`
  (`default`, `plan`, `soft-approve`, `accept-edits`, `yolo`), `name`, and
  `extraRoots` (existing directories, not the filesystem root; symlinks are
  stored resolved). Empty strings clear rules and the mode. An empty
  `extraRoots` array clears the extras.
- API: `GET/POST /v1/workspaces`, `GET /v1/workspaces/browse`,
  `POST /v1/workspaces/mkdir`, `POST /v1/workspaces/{id}/activate`,
  `PATCH /v1/workspaces/{id}`, `DELETE /v1/workspaces/{id}` (token-gated
  like all `/v1/*` under `--auth`). Browse and mkdir stay inside the
  allowlist: the daemon user's home, the process directory, registered
  workspace paths, and extra roots. A symlink that resolves outside that
  set is omitted. Add, once the allowlist is configured, rejects a path
  outside it.
- Per-child git worktrees already exist and stay off unless asked.
  `session.childIsolation` `off` or `shared` (the default) keeps the
  parent's directory. `worktree`, or a spawn `isolation: "worktree"`,
  checks out `<repo>/.strike/worktrees/<child-id>/`. There is no second
  flag. Outside a git repository the engine stays on the shared directory.
  A bound session rejects a delegated child whose working directory
  resolves outside the workspace and its extra roots. An unbound session
  (empty registry, or a launch directory that matched nothing) does not
  run that check.
- `strike daemon install|uninstall|status|token` + systemd user unit.
- `strike serve --token-file` (stable 0600 token, minted once) and
  `--allow-private-bind`.
- `webui/`: Leptos client. The shell is a session list, a transcript, and
  a side panel. Daemon connect, workspace policy, and saved remotes stay on
  the Daemon view.
  - Session list merges live roots (`/v1/roots`) with saved sessions.
    A gutter marks the canonical status (running, waiting, blocked, failed,
    done, stopped, idle) and an unread pip. Chips filter Unread, In progress,
    and Pinned. Search matches title, id, or a delegated child's title.
    Older rows fold after six, keeping pinned, busy, and the open session.
    Delegated children (`GET /v1/sessions/{id}/children`, team state when
    the roster has them) nest under the open session. Unread is stored on
    the daemon (`POST /v1/sessions/{id}/seen`, `docs/webui-status.md`).
    Pins stay in browser storage (`strike.pins`).
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
    all-sessions skip. The model chip (Alt+M) opens a filterable menu
    grouped by provider. Pins stay in this browser. Enter posts
    `select.model` for this session; saving a default patches
    `/v1/settings` and does not by itself switch the session. Effort is
    shown when the catalog reports reasoning or variants, or when the
    session already has an effort (`set.effort`). The context meter uses
    `contextUsed`, `contextLimit`, and `contextFit` from the live status,
    and the catalog window only when the daemon has not named a limit.
    Send is held on a critical fit or a full known window. A catalog
    `GET /v1/models` that fails stays "catalog unavailable" and does not
    invent models. `select.model` and `set.effort` are rejected while a
    turn is running.
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

## Several workspaces, one daemon

One `strike serve` process can hold several live roots at once. Each root
keeps its own engine, transcript, and working directory. Creating a root
from the web client uses the active workspace: that directory is the engine
working directory, the rules file is appended to that session's prompt
(capped at 64KiB), and the stored permission mode is the starting posture.
Switching the active root points file listings and diffs at that root's
directory. Children spawned from a bound session must stay inside the
workspace directory or an extra root; a symlink out is rejected. Worktrees
remain the existing opt-in and are rejected when they land outside those
roots.

Isolated per workspace: the session's directory, its rules text, its
starting permission mode, and the child working-directory allowlist.

Shared by every workspace in the process: the bearer token, provider
credentials, the session log directory, MCP and LSP processes started from
the launch tree, and project memory, history, plans, artifacts, and the
ledger (still keyed by the launch project). An empty registry changes
none of this: the engine stays in the process directory and child
directories are not checked.

The picker is not a session-folder tree. Session folders, when they exist,
only organise chats. They do not select a directory.

## Not yet done (next slices, in order)

1. Permission asks in the transcript. The composer can set the mode;
   an in-flight ask is still only the protocol event.
2. Session folders that organise chats, separate from workspace
   directories. The list already shows which workspace a chat is bound
   to. It does not hide chats from other workspaces.
3. Per-remote tokens in the UI (today one token field for the selected
   daemon), daemon discovery on the tailnet.
4. Tauri shell (optional), TUI removal decision, `docs/web.md` rewrite.
