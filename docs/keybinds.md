# Keybinds

In-app cheatsheet: `f1` or `/keys` (filterable).

## Global

| Key | Action |
|---|---|
| `enter` | send prompt |
| `shift+enter` | newline (`ctrl+j` / bare LF; `alt+enter` after enhanced CSI) |
| `esc` | interrupt turn / reject permission / close modal |
| `ctrl+c` | quit |
| `ctrl+p` | command palette |
| `f1` | keybind cheatsheet (`/keys`) |
| `tab` | cycle agents (composer empty of `/` completion) |
| `ctrl+d` | save defaults (see [config.md](config.md)) |
| `ctrl+e` | open prompt in external `$EDITOR` |

## Transcript & panes

| Key | Action |
|---|---|
| `pgup` / `pgdn` | scroll transcript |
| `ctrl+up` / `ctrl+down` | scroll transcript |
| `ctrl+t` | jump to latest output |
| `ctrl+h` / `ctrl+l` | focus left / right pane (horizontal split) |
| `ctrl+j` / `ctrl+k` | cycle right-pane window next / previous (when right pane focused; left-focus `ctrl+j` is newline) |
| `ctrl+;` | toggle split orientation (`/layout`, `/split`) |

In a vertical split, focus and cycle chords swap: focus is `ctrl+j`/`ctrl+k`,
cycle is `ctrl+h`/`ctrl+l`.

Right-pane windows (cycle with the chords above): `context`, `activity`,
`files`, `markdown`, `editor`. See [usage.md](usage.md).

## Permission prompts

| Key | Action |
|---|---|
| `1` / `y` | allow once |
| `2` / `s` | allow for session |
| `3` / `p` | allow for project |
| `4` / `n` | reject (optional feedback) |
| `←`/`→` or `h`/`l` / tab | move choice |
| `enter` | confirm highlighted choice |
| `esc` | reject |

## Tool cells (composer empty)

| Key | Action |
|---|---|
| `alt+[` / `alt+]` | previous / next tool cell |
| `enter` | expand tool / open `file:line` |
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

## Lists & pickers

| Key | Action |
|---|---|
| `↑`/`↓` / `ctrl+p`/`ctrl+n` | move selection |
| `j`/`k` | move (pickers without filter) |
| `enter` | confirm |
| type | filter (when available) |
| `esc` | close |
| `ctrl+d` | save highlighted default |

UI layout and slash commands: [usage.md](usage.md).
