# Feature Checkpoints

Dependency breakdown for the big-ticket features in [features.md](features.md):
what has to exist *before* each feature is buildable, split into checkpoints
you can hit (and test) one at a time. Ordered so earlier checkpoints unlock
later ones.

Current baseline this assumes (as of 2026-07-24): single-column bubbletea TUI
(`internal/tui/app.go` — viewport + composer + modal overlays), in-process
Op/Event protocol (`internal/protocol`), one engine/one session/one agent
(`internal/engine/engine.go`), tool registry + permission layer, prompt-history
store. No layout manager, no focus system, no PTY dep, no glamour, no DB.

## Shared foundations

These unlock multiple features — build them once, not per-feature.

- [ ] **F1. Keymap table** — pull hardcoded key handling out of `Update` into
  a single keymap (action → key) so ctrl+j/ctrl+l/ctrl+k are defined in one
  place and later remappable from config. Unlocks: panes, palette, vim.
- [ ] **F2. Focus model** — an explicit focus enum on the TUI `Model`
  (composer / transcript / right-pane / modal) with one router that sends key
  msgs to the focused component only. Today `updateComposer` implicitly owns
  keys; modals intercept ad hoc. Unlocks: panes, vim, agent tree.
- [ ] **F3. Layout manager** — `View()`/`reflow()` currently assume full
  width. Introduce a region-based layout: left column (transcript+composer),
  optional right column, with width budgeting and resize handling.
  Unlocks: right pane, everything hosted in it.
- [ ] **F4. Reusable tree widget** — expand/collapse tree component with
  cursor, scrolling, and lazy children. Used twice: file explorer and the
  agent multiplexing tree. Build it as a standalone bubble.
- [ ] **F5. Engine event log (JSONL)** — append every `protocol.Event` (plus
  op echoes) to a per-session JSONL file with timestamps. Cheap, and it is
  the substrate for hooks observability, session resume, and the agent tree
  transcripts. Do this early — it's mostly plumbing on the existing
  `emit()` path.
- [ ] **F6. Tool-result feedback path** — a uniform way for the engine to
  return a synthetic tool result message to the model ("permission denied
  because X"). The permission layer partially has this; formalize it.
  Unlocks: phase bounces, reject-with-feedback, hook block messages.

## Right pane & window system

Depends on: F1, F2, F3.

- [ ] **R1. Two-column layout renders** — right pane opens/closes (even
  empty), left column reflows correctly, narrow terminals collapse to one
  column. Exit test: resize storm doesn't corrupt layout.
- [ ] **R2. Focus switching** — ctrl+j toggles left/right, visible focus
  indicator (border/title tint), keys route to the focused side only.
- [ ] **R3. Window interface + registry** — `Window` interface
  (Init/Update/View/Title/Resize), a registry the pane hosts, ctrl+l cycles
  registered windows. Exit test: a trivial placeholder window cycles in/out.
- [ ] **R4. File explorer window** — F4 tree over the project dir,
  .gitignore-aware (reuse `internal/project` scoping), enter on a file
  dispatches an "open" request that other windows can claim (vim, md
  reader). Lazy-load directories.
- [ ] **R5. Markdown reader window** — add glamour; render file at pane
  width, re-render on resize; `/md-read <fpath>` command wired in
  `commands.go`. Mermaid-to-ASCII and HTML dump are later, separate
  checkpoints — don't block R5 on them.

## Embedded vim

Two modes (pane / modal overlay), user-configurable. The takeover variant is
nearly free and should ship first; true embedding is the hard part.

- [ ] **V1. Full-screen takeover `/vim`** — `tea.ExecProcess` with
  vim/nvim, resume TUI on exit, `$EDITOR` fallback. No pane system needed.
  This alone kills a lot of IDE-hopping; ship it before anything below.
- [ ] **V2. File-changed signal** — after editor exit, engine learns the
  file changed (new Op) so the agent re-reads instead of trusting stale
  reads. Also used by post-edit review later.
- [ ] **V3. PTY runner** — add a PTY dep (creack/pty), spawn nvim attached
  to it, manage lifecycle (resize, SIGWINCH, exit). No rendering yet — exit
  test: bytes flow both ways, clean shutdown.
- [ ] **V4. Terminal screen emulation** — parse the PTY byte stream into a
  renderable screen grid (vt10x or similar). This is the risk item; spike
  it before committing. Exit test: nvim renders correctly in a fixed-size
  offscreen grid, including colors and cursor.
- [ ] **V5. Terminal window** — host V4's grid as a right-pane window (needs
  R1–R3) and as a modal overlay (reuse `overlay.go`); keys pass through
  when focused; session keeps streaming behind it.
- [ ] **V6. Mode setting** — config key choosing pane vs overlay; `/vim`
  respects it.

## Hooks, phases & workflows

Layered: event log → declarative rules → shell hooks → phases → workflow
files → gates. Each layer works standalone.

- [ ] **H1. Hook dispatch points** — define hook events and fire them from
  the engine: pre/post tool call (`execToolCall` is the choke point), turn
  start/end, session start/end, later phase transitions. Internally just a
  synchronous dispatcher.
- [ ] **H2. Declarative rules in config** — schema: event matcher (event
  type + tool name glob) → action (log / block / notify). Evaluate in the
  permission layer. Blocked calls use F6 to explain themselves to the
  model. Exit test: a rule that blocks `write` bounces with a message.
- [ ] **H3. Observability log** — a "log" rule action that records matched
  events (piggybacks on F5); `strike` can pretty-print a session's log for
  review.
- [ ] **H4. Shell-command hooks** — event JSON on stdin, exit code decides
  allow/block, stdout optionally injected via F6. Needs timeouts, and a
  decision on trust (project-local hook definitions execute code — gate
  first run like a permission ask).
- [ ] **H5. Phase state in engine** — engine carries a current-phase value;
  each phase maps to a permission profile + rule set; transitions emit an
  event (so hooks/H1 see them) and render in the status line.
- [ ] **H6. Workflow file format** — JSON/YAML schema: ordered phases, each
  with context payload (ctx1…), tool/permission profile, hooks, exit gate.
  Parser + validation with good error messages. Ship the built-in plan-mode
  workflow (read-only plan phase → implement phase) as the default and
  proof of the schema.
- [ ] **H7. Context swap on transition** — `system()` currently rebuilds the
  system prompt statically; make per-phase context injectable and removable
  on phase change without corrupting conversation history. This is the
  subtle engine work in the whole stack — checkpoint it separately.
- [ ] **H8. Exit gates** — three gate types, in order of effort:
  `agent` (a `phase_done` tool the agent calls — default), `check` (run a
  command, exit 0 advances), `user` (TUI approval prompt — reuse the
  permission-ask UI). Configurable per phase in H6's schema.

## Project-local memory / issue DB

Mostly independent — only the browser window needs the pane system.

- [ ] **D1. Storage decision + layer** — pick pure-Go storage (bbolt or
  modernc sqlite; avoid cgo). Project-scoped path via `internal/project`
  (`.strike/` in-repo vs hashed path under `~/.strike` — decide, considering
  gitignore/commit implications). Exit test: two projects never see each
  other's data.
- [ ] **D2. Memory schema + agent tools** — key/value-with-tags memory
  entries; `memory_write`/`memory_read` tools in the registry.
- [ ] **D3. Memory auto-load** — relevant memory injected at session start
  (touches `system()` — coordinate with H7's context injection so there's
  one mechanism, not two).
- [ ] **D4. Issue schema + agent tools** — open/close/list/update issues
  with ids and status; agent files issues as it works.
- [ ] **D5. User commands** — `/memory`, `/issues` in `commands.go` with
  list/add/close verbs.
- [ ] **D6. DB browser window** — right-pane window (needs R3) listing
  issues/memory with expand-to-detail.

## Agent pane multiplexing

Longest pole; almost everything above feeds it. Order matters: the engine
work (M1–M3) is the real feature — the tree UI is the easy last mile.

- [ ] **M1. Session as first-class object** — session id on every event
  (protocol change), per-session history and event stream, engine no longer
  assumes one conversation. F5's log becomes per-session naturally.
- [ ] **M2. Concurrent sessions** — a session manager owning N engine
  sessions, event mux tagged by session id, per-session interrupt. TUI still
  shows one at a time (flat switcher is the exit test — tmux-window style).
- [ ] **M3. Subagent spawning** — a tool that spawns a child session with
  its own agent/profile; parent/child ids in events; child completion
  reported back to the parent as a tool result.
- [ ] **M4. Reusable transcript renderer** — cell rendering (`cells.go`)
  currently lives in the single `Model`; extract it so any session's event
  stream can render in any pane. Prereq for viewing a selected node.
- [ ] **M5. Agent tree window** — F4 tree: top-level = concurrent sessions,
  children = spawned subagents; select a node → M4 renders its live
  transcript; launch/interrupt from the tree. The "reeve" view.
- [ ] **M6. Visualizer window** — whatever richer visualization comes after
  M5 (status glyphs, token/cost per node, activity sparkline). Deliberately
  last.

## Suggested overall order

1. **F5 event log + H1–H3 hooks/rules** — small, immediately useful
   (observability), no UI risk.
2. **V1 takeover vim + V2** — biggest UX win per line of code.
3. **F1–F3 + R1–R4** — the pane system and file explorer.
4. **D1–D5** — memory/issues (browser window D6 whenever).
5. **R5 markdown reader**, **H4–H8 workflows/phases** — in parallel tracks.
6. **V3–V6 embedded vim** — after the spike (V4) proves viable.
7. **M1–M6 multiplexing** — last; it leans on nearly everything else.
