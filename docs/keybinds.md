# Keybinds

In-app cheatsheet: `f1` or `/keys` (filterable). The list leads with a
**Current focus** section for the focused pane (composer/transcript on the
left; the active right-pane window such as `agents`, `files`, or `editor`),
then the remaining binds. The bottom footer is also **context-sensitive**:
composer focus shows send / newline / external editor; right-pane focus shows
select / open / next pane. Each command action also shows its slash mirror
(when one exists). Remap chords in `keybinds.jsonc` (or the config `keybinds`
object) — see [config.md](config.md). `/keys reset` restores session defaults.

Command actions (focus, panes, modals, interrupt, tool cells, roots, …) are
also invocable as slash commands for discoverability and scriptability. Pure
input editing (readline kill/yank, completion, enter/send) stays key-only.
Registry: `keybindSlashPrimary` / `keybindNoSlashReason` in
`internal/tui/app/_src/app/keybind_slash.go`.

## Global

| Key | Action | Slash |
|---|---|---|
| `enter` | send prompt | — |
| `ctrl+j` / `shift+enter` / `alt+enter` | newline (shift+enter CSI rewrites to `alt+enter`; enhanced ctrl+j → `alt+j`; bare LF is also `ctrl+j`; empty-composer `alt+enter` expands a tool cell instead) | — |
| `esc` | interrupt turn (cancels tools/LLM; notice “interrupted”, header **canceled**, transcript row) / reject permission / close modal | `/interrupt` |
| `ctrl+c` | quit | `/exit`, `/quit` |
| `ctrl+k` | command palette (when kill-to-end does not delete) | `/palette` |
| `f1` | keybind cheatsheet | `/keys` |
| `tab` | cycle agent personas (composer empty of `/` completion; not concurrent roots) | `/agent-next` |
| `shift+tab` | cycle permission mode (default → plan → soft-approve → accept-edits → yolo); works mid-turn | `/mode-next` |
| `ctrl+d` | save defaults (see [config.md](config.md)) | `/save-defaults` |
| `ctrl+e` | open prompt in external editor (`$VISUAL`/`$EDITOR`, else nvim/vim/vi/nano) | `/edit-prompt` |

## Transcript & panes

| Key | Action | Slash |
|---|---|---|
| `pgup` / `pgdn` | scroll transcript | `/scroll-up`, `/scroll-down` |
| `ctrl+up` / `ctrl+down` | scroll transcript | `/scroll-up`, `/scroll-down` |
| `ctrl+t` | jump to latest output | `/jump-bottom` |
| `alt+y` | copy last assistant response (OSC52) | `/copy` |
| `ctrl+h` / `ctrl+l` | focus left (primary transcript) / right (secondary pane column) — **orientation-independent**; on the lean home screen, `ctrl+l` also opens the multi-pane workspace (launch left, panels right) | `/focus-left`, `/focus-right` |
| `ctrl+p` / `ctrl+o` | cycle right-pane focus next / previous within the active stack group, then to the next group | `/window-next`, `/window-prev` |
| `ctrl+shift+o` / `ctrl+shift+p` | cycle right-pane **stack groups** next / previous (lands on the group's first pane) | `/group-next`, `/group-prev` |
| `ctrl+;` | toggle split orientation | `/layout`, `/split` |

Focus and cycle chords do **not** swap when the split is stacked top/bottom:
left/right mean primary transcript vs secondary pane column in either layout.

Right-pane **stack groups** (related panes shown together when space allows):

| Group | Panes (split) |
|---|---|
| Session | `context` + `activity` (+ `telemetry` when enabled) |
| Agents | `agents` + `visualizer` |
| Files | `files` + `diagnostics` |
| Project | `memory` + `issues` + `plans` |
| Singles | `markdown`, `editor` (full height each) |
| Plugin | installed `pane/1` contributions (shared stack; `/pane <id>`) |

Focus cycle order is deterministic: top→bottom (or left→right in a bottom-bar
split) inside the group, then the next group. Narrow/compact terminals collapse
to a single pane and cycle the same order one at a time. Group cycle
(`ctrl+shift+o`/`ctrl+shift+p`) skips remaining members of the current group
and jumps to the first pane of the next/previous group. See [usage.md](usage.md).

## Permission prompts

| Key | Action |
|---|---|
| `1` / `y` | allow once |
| `2` / `s` | allow for session |
| `3` / `p` | allow for project |
| `4` / `n` | reject (optional feedback) |
| `←`/`→` or `h`/`l` / tab | move choice |
| `enter` | confirm highlighted choice |
| `d` | expand / collapse large edit diff |
| `esc` | reject |

## Tool cells (composer empty)

| Key | Action | Slash |
|---|---|---|
| `alt+[` / `alt+]` | previous / next tool cell | `/tool-prev`, `/tool-next` |
| `alt+enter` | expand / collapse tool output or large edit diff; else open `file:line` (composer empty only; with text, `alt+enter` is newline) | `/tool-expand` |
| `y` | copy cell (tool/explore, else latest assistant/user) | `/tool-copy` |
| `alt+y` | copy last assistant response only (skips tool cells) | `/copy` |
| `v` | review edit in editor | `/tool-review` |
| `alt+a` | apply shown edit/patch into the active worktree (confirm; not bare `a`, which types in the composer) | `/tool-apply` |

## Composer editing

| Key | Action |
|---|---|
| `ctrl+w` | kill word backward |
| `alt+b` / `alt+f` | word backward / forward |
| `ctrl+u` | kill to line start |
| `ctrl+k` | kill to line end (when it deletes; else command palette) |
| `ctrl+y` | yank |
| `↑` / `↓` | prompt history (when composer has no multiline cursor motion) |

## Queue pane (prompts, loops, pool waits)

| Key / command | Action |
|---|---|
| `/queue` | focus the queue right pane (buffered prompts, `/loop` jobs, scheduler waits) |
| empty composer + `bksp` | pop last queued prompt into the composer |
| idle `esc` | clear the whole queue (when no turn is running) |
| queue pane `↑`/`↓` / `j`/`k` | move selection |
| queue pane `shift+↑`/`shift+↓` or `K`/`J` | reorder selected prompt |
| queue pane `enter` | edit selected prompt text (opens overlay editor) |
| queue pane `e` | load selected prompt into composer |
| queue pane `p` | promote selected prompt to next (FIFO head) |
| queue pane `d` / `delete` / `bksp` | remove selected prompt or stop selected loop |
| queue pane `c` | clear all buffered prompts |
| queue pane `x` | interrupt running turn (if any) and run the FIFO head next |
| queue pane `m` | open full-screen queue overlay browser |
| queue modal `esc` / `q` | close overlay |

## Agents pane (concurrent roots)

When the `agents` right pane is focused (or shown in the agents stack group),
these controls manage **concurrent root sessions**:

| Key | Action | Slash |
|---|---|---|
| `n` | new concurrent root session | `/root-new` |
| `enter` | activate selected root (or open a child transcript) | `/root-open` |
| `x` | interrupt the selected root or child | `/root-interrupt` |
| `r` | rename the selected root or child (persists across resume) | `/rename` |
| `j` / `k` | move cursor | — |
| `f` | cycle view filter (all → needs you → working → ready → roots) | `/root-filter` |
| `d` | hide selected root from pane | `/root-hide` |

`Tab` switches **agent personas** (build/plan/explore/…), not root sessions.
`ctrl+x` leader chords navigate **child/subagent** transcripts only — use the
agents pane (`n` / `enter` / `x`) for concurrent roots.

## Subagent navigation

Child sessions spawned by tools (not concurrent roots):

| Key | Action | Slash |
|---|---|---|
| `ctrl+x` then `↓` | enter first subagent transcript | `/subagent` |
| `ctrl+x` then `↑` | return to parent session | `/parent` |
| `ctrl+x` then `←`/`→` | cycle sibling subagents | `/subagent-prev`, `/subagent-next` |
| `↑`/`↓`/`←`/`→` | parent / child / siblings while viewing a subagent (composer empty) | same as above |
| `esc` | leave subagent view (when idle) / interrupt turn | `/interrupt` |

## Embedded editor (`/vim`, `/nano`)

| Key | Action | Slash |
|---|---|---|
| `ctrl+g` | leave editor pane / close modal overlay | `/leave-editor` |

## Markdown reader modal (`/md-read` when `mdReadMode=modal`)

| Key | Action |
|---|---|
| `esc` / `q` / `ctrl+g` | close modal |
| `↑`/`↓` / pgup/pgdn | scroll |

## Completion (slash / `@file`)

| Key | Action |
|---|---|
| `↑` | previous candidate |
| `↓` / `ctrl+n` | next candidate |
| `tab` / `enter` | accept |
| `esc` | dismiss |

## Lists & pickers

| Key | Action |
|---|---|
| `↑`/`↓` / `ctrl+p`/`ctrl+n` | move selection |
| `j`/`k` | move (pickers without filter) |
| `enter` | confirm |
| type | filter (when available) |
| `ctrl+x` | log out highlighted provider (confirm y/n; provider picker) |
| `esc` | close |
| `ctrl+d` | save highlighted default |

UI layout and slash commands: [usage.md](usage.md). Source of truth in code:
`keybindCatalog` / `defaultKeyMap` in `internal/tui/app/_src/app/keymap.go`; slash mapping
in `internal/tui/app/_src/app/keybind_slash.go`.
