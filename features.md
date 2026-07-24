# UX Features

Backlog of UX improvements, roughly grouped. Items marked ★ are the ones
that most shaped how good opencode/codex feel and are worth doing early.

## Composer & input

- ★ **Slash-command autocomplete** — typing `/` opens a filterable popup of
  commands + skills (with descriptions), tab/enter to complete, fuzzy match
  (`/pr` → `/provider`). Same dropdown surfaces skill args hints.
- ★ **`@file` mentions** — typing `@` opens a fuzzy file finder scoped to the
  project; the selected path is inserted and attached as context to the
  message (read server-side at submit time).
- **Multi-line editing** — alt+enter (or shift+enter where the terminal
  supports it) inserts a newline; enter still sends. Auto-grow the composer
  up to ~8 lines.
- **History recall** — up/down at an empty composer cycles previous prompts
  (per project, persisted in ~/.strike).
- **`ctrl+e` external editor** — open the current composer content in
  $EDITOR (vim/nvim) in a full-screen takeover, resume the TUI on save —
  for long prompts that outgrow a textarea.
- **Paste detection** — large multi-line pastes get collapsed into an
  attachment-style block ("[pasted 120 lines]") instead of flooding the
  composer; expandable before send.
- **Kill/yank & word-wise navigation** — readline keys (ctrl+w, alt+b/f,
  ctrl+u/k) in the composer.

## Transcript & rendering

- ★ **Markdown rendering** (glamour) for assistant text, with the
  stable/tail streaming split from codex: committed lines never re-render,
  only the mutable tail redraws; hold back partial tables.
- ★ **Rich diff cells** — edit/write tool calls render a proper diff
  (gutter line numbers, +/- colors from the theme, syntax highlighting)
  instead of a text summary; toggle unified/split; `d` on a permission
  prompt expands the full diff.
- **Collapsible tool cells** — enter on a tool cell expands full output;
  bursts of read/glob/grep collapse into one "exploring…" cell (codex's
  ExecCell grouping) with a count.
- **Live bash output** — long-running commands stream a bounded tail
  (last ~5 lines) into the tool cell while running.
- **Mouse support** — wheel scroll in the viewport, click to expand tool
  cells, click links (OSC 8 hyperlinks for file paths and URLs).
- **Copy affordances** — `y` on a cell copies its content (code block,
  command, diff) to the clipboard; code blocks get a subtle "copied" flash.
- **Timestamps + token/cost meter** — subtle per-turn stats; running
  context-usage bar in the status line with a warning as compaction nears.

## Editor integration

- **`/vim <fpath>` split editor** — open vim/nvim in a right-hand split
  (or full-screen takeover on narrow terminals) for quick edits without
  leaving the session; on save/quit, strike notes the file changed so the
  agent re-reads it. Fallback to $EDITOR.
- **Open-at-line** — file:line references in the transcript are actionable:
  enter/click opens the editor at that exact line.
- **Post-edit review** — after the agent edits files, `v` on the tool cell
  opens the touched file in the editor at the first changed hunk.

## Commands, palette & discovery

- ★ **Command palette** (ctrl+k) — fuzzy-searchable list of every action
  (switch provider/model/agent, toggle theme, open session picker, copy
  last message…), so features are discoverable without memorizing keys.
- **Contextual help footer** — one-line key hints that change with focus
  (composer vs modal vs scrolling), like codex's bottom-pane hints.
- **`/keys`** — cheat-sheet modal of all keybindings.
- **Keybinding config** — remappable keys in ~/.strike/config.

## Sessions & continuity

- ★ **Session picker / resume** — `strike --continue` and a `/sessions`
  modal listing recent sessions (title, age, message count) built from the
  JSONL logs; enter replays the transcript and continues.
- **Auto-titling** — name sessions from the first user message (or ask the
  model for a 5-word title) instead of timestamps.
- **Fork & rewind** — `/fork` duplicates the session at the current point;
  `/undo` rewinds the last turn (the event log already supports this).
- **`strike exec` headless mode** — one-shot prompt → stream to stdout,
  same engine over the same protocol; makes strike scriptable.

## Agents, models & providers

- **Agent picker modal** — the Tab cycle is fast, but a centered picker
  (like /provider) showing each agent's description/model pin helps once
  there are >3 agents; ctrl+d there sets defaultAgent.
- **Model metadata in the picker** — context window, input/output cost, and
  reasoning support from models.dev next to each model id.
- **Catalog-driven defaults** — derive per-provider default models from
  models.dev instead of hardcoding (they drift).
- **Provider health indicator** — status-line dot when the provider errors
  or auth is about to expire; `/auth` prompt preemptively on expiry.

## Permissions & safety UX

- ★ **Reject with feedback** — the permission prompt's reject option opens
  a one-line input whose text is sent back to the model as the tool result
  (the engine already supports Message; the TUI doesn't collect it yet).
- **Diff preview in permission prompts** — show the actual change for
  edit/write asks inline (metadata already carries old/new).
- **Remember-with-scope choices** — "always for `git *`", "always in this
  project" (writes a project .strike/config rule), not just session-wide.
- **Auto-approve countdown mode** — optional yolo-lite: asks auto-approve
  after N seconds with a visible countdown, esc to veto.

## Polish

- **Theme files** — themes as JSON in ~/.strike/themes; light/dark
  auto-detect from the terminal; /theme picker with live preview.
- **Terminal title + notification** — set the tab title to the session
  topic; ring the bell / OSC 9 notify when a long turn finishes or a
  permission ask arrives while unfocused.
- **Spinner variety & elapsed time** — "working (12s · 3 tool calls)" in
  the status line instead of a bare spinner.
- **Graceful narrow-terminal layout** — collapse gutters and wrap the
  status line below ~80 cols instead of truncating.
- **First-run onboarding** — on a fresh ~/.strike, walk through provider
  login and defaults inline (a guided version of make setup).
