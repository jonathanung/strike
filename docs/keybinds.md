# Keybinds

In-app cheatsheet: `f1` or `/keys` (filterable). Remap chords in config
(`keybinds` object) — see [config.md](config.md). `/keys reset` restores
session defaults.

## Global

| Key | Action |
|---|---|
| `enter` | send prompt |
| `shift+enter` | newline (`alt+enter` after enhanced CSI; bare LF fallback) |
| `esc` | interrupt turn / reject permission / close modal |
| `ctrl+c` | quit |
| `ctrl+p` | command palette |
| `f1` | keybind cheatsheet (`/keys`) |
| `tab` | cycle agents (composer empty of `/` completion) |
| `shift+tab` | cycle permission mode (default → plan → accept-edits → yolo) |
| `ctrl+d` | save defaults (see [config.md](config.md)) |
| `ctrl+e` | open prompt in external `$EDITOR` |

## Transcript & panes

| Key | Action |
|---|---|
| `pgup` / `pgdn` | scroll transcript |
| `ctrl+up` / `ctrl+down` | scroll transcript |
| `ctrl+t` | jump to latest output |
| `ctrl+h` / `ctrl+l` | focus left / right pane (horizontal split) |
| `ctrl+j` / `ctrl+k` | cycle right-pane focus next / previous within the active stack group, then to the next group (enhanced `ctrl+j` is distinct from bare LF newline) |
| `ctrl+;` | toggle split orientation (`/layout`, `/split`) |

In a vertical split, focus and cycle chords swap: focus is `ctrl+j`/`ctrl+k`,
cycle is `ctrl+h`/`ctrl+l`.

Right-pane **stack groups** (related panes shown together when space allows):

| Group | Panes (split) |
|---|---|
| Session | `context` + `activity` |
| Agents | `agents` + `visualizer` |
| Project | `memory` + `issues` |
| Singles | `files`, `markdown`, `editor` (full height each) |

Focus cycle order is deterministic: top→bottom (or left→right in a bottom-bar
split) inside the group, then the next group. Narrow/compact terminals collapse
to a single pane and cycle the same order one at a time. See [usage.md](usage.md).

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

| Key | Action |
|---|---|
| `alt+[` / `alt+]` | previous / next tool cell |
| `enter` | expand / collapse tool output or large edit diff; else open `file:line` |
| `y` | copy cell (tool/explore, else latest assistant/user) |
| `v` | review edit in editor |

## Composer editing

| Key | Action |
|---|---|
| `ctrl+w` | kill word backward |
| `alt+b` / `alt+f` | word backward / forward |
| `ctrl+u` | kill to line start |
| `ctrl+k` | kill to line end (when it deletes; else pane cycle) |
| `ctrl+y` | yank |
| `↑` / `↓` | prompt history (when composer has no multiline cursor motion) |

## Subagent navigation

| Key | Action |
|---|---|
| `ctrl+x` then `↓` | enter first subagent transcript |
| `ctrl+x` then `↑` | return to parent session |
| `ctrl+x` then `←`/`→` | cycle sibling subagents |
| `↑`/`↓`/`←`/`→` | parent / child / siblings while viewing a subagent (composer empty) |
| `esc` | leave subagent view (when idle) / interrupt turn |

## Embedded editor (`/vim`)

| Key | Action |
|---|---|
| `ctrl+g` | leave editor pane / overlay focus |

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
`keybindCatalog` / `defaultKeyMap` in `internal/tui/keymap.go`.
