# Usage

## Slash commands

strike launches without any provider configured. Pick one inside the TUI:

```
/provider                      # centered picker: providers + auth status;
                               # selecting an unauthenticated one starts its
                               # login and switches once it succeeds
/provider anthropic            # direct switch (or openai, xai, echo)
/provider openai gpt-5.5       # optional explicit model
/model                         # centered model picker for the current
                               # provider (live models.dev catalog, cached
                               # 24h; type to filter)
/model grok-4.5                # direct switch on the current provider
/effort                        # centered picker for reasoning effort
/effort xhigh                  # off | low | medium | high | xhigh | max
/autonomy                      # exit-gate policy picker
/autonomy supervised           # supervised | agent | checks
/agent                         # centered agent picker
/agent plan                    # direct switch (built-ins: build, plan)
/session                       # browse past root sessions (auto-titles) and
                               # resume one with full model history
/session <id>                  # resume a specific session by id
/fast                          # toggle OpenAI priority tier (~2×, lower
                               # latency). Sticky session preference; no-op
                               # on Anthropic, xAI, ChatGPT subscription, or
                               # models without a fast mode. /fast on|off
/think                         # show or hide model chain-of-thought when the
                               # provider streams it. Default hidden. /think on|off
/auth                          # same picker as /provider
/auth openai                   # OAuth login in the browser (async — the TUI
                               # keeps working; result shows in the notice line)
/auth xai device               # RFC 8628 device flow for headless machines
/auth anthropic                # masked API-key input (also: /auth <p> key)
/auth status                   # anthropic: none · openai: oauth+key · …
/auth logout <provider>
/settings                      # manage custom providers and settings
/theme                         # centered color-theme picker (bundled +
                               # ~/.strike/themes + ./.strike/themes)
/theme <id>                    # apply a theme by id
/theme dark|light|auto         # session appearance (forced / restore detect)
/layout                        # toggle horizontal/vertical pane split
/split                         # alias of /layout
/compact                       # compact model history (keep recent turns)
/vim [path[:line]]             # open file in embedded editor (pane/overlay)
                               # or $EDITOR (see vimMode in config.md)
/md-read <path>                # open a markdown file in the right markdown pane
/memory [list|get|set|rm] …    # project-scoped durable key/value memory
/issues [list|add|get|close] … # project-scoped issue tracker
/keys                          # filterable keybind cheatsheet (also f1)
/help                          # list commands
```

Built-in skills also appear as slash commands: `/commit`, `/push`, `/pr`,
`/ship` (and any custom skills under `~/.strike/skills` or `./.strike/skills`).
See [agents-skills.md](agents-skills.md).

Submitting a prompt before selecting shows "No model selected" in the
notice line above the composer (your prompt stays in the input). Talking to
a real provider needs credentials — see [auth.md](auth.md).

Provider selection happens in-app with `/provider`; `--provider` on the
command line just pre-selects (and validates credentials eagerly).

## CLI session resume & headless exec

```sh
./strike --continue              # resume most recent root session
./strike --session <id>          # resume a specific root session by id
./strike exec "fix the flaky test"   # one-shot headless turn → stdout
./strike exec -                  # read prompt from stdin
```

`--continue` and `--session` cannot be combined. `strike exec` accepts the
same `--provider` / `--model` / `--effort` /
`--dangerously-skip-permissions` flags as the TUI. Permission and question
prompts cannot be answered interactively in exec; asks are rejected unless
`--dangerously-skip-permissions` is set (configured/agent denies still
apply). Full flag list: [install.md](install.md) or `strike --help`.

## UI

The screen has a full-width header, footer hints, and danger banner when
needed. Its left pane is one aggregate stack: `session` transcript, reserved
notice line, slash-command completion, and `prompt ❯` composer. The right
slot hosts one active window from the registry:

| Window | Role |
|---|---|
| `context` | setup summary (provider, model, agent, …) |
| `activity` | tools/tips / subagent status |
| `files` | workspace file tree (`host.Files`) |
| `markdown` | markdown reader (`/md-read <path>`) |
| `editor` | embedded nvim/vim PTY for `/vim` |

Vim-style pane keys (horizontal split): `ctrl+h` / `ctrl+l` focus the left
or right pane; `ctrl+j` / `ctrl+k` cycle the active right-pane window
next/previous. `ctrl+;` (or `/layout` / `/split`) toggles a vertical
top/bottom split and swaps those chords (focus becomes `ctrl+j`/`ctrl+k`,
cycle becomes `ctrl+h`/`ctrl+l`). `ctrl+p` opens the command palette; `f1`
(or `/keys`) opens a filterable keybind cheatsheet. Enter sends; Shift+Enter
(or Alt+Enter) inserts a newline. `pgup`/`pgdn` (and `ctrl+up`/`ctrl+down`)
scroll the transcript; `ctrl+t` jumps to the latest output. The transcript
sticks to the bottom while you are already anchored, and keeps your scroll
offset when you have scrolled up. Pickers, the command palette, and
permission prompts render as centered dialogs in the same panel style.
`/theme` opens the color-theme picker; `/theme <id>` applies one; `/theme
dark|light|auto` sets session appearance only.

The default horizontal split appears at 93 columns and above, with a minimum
60-column left pane, one-column gutter, and 32-column right pane. At 92
columns and below, only the active pane fills the full width. For a custom
gutter of width `g`, the split threshold is `60 + g + 32`. Vertical split uses
the full width and divides body height when there is room. Below 60 columns or
20 rows panels drop their borders ("compact mode") instead of clipping or
garbling. There is no window close state or plugin mechanism for panes.

A fresh session with an empty transcript shows a dashboard of fixed-height
cards in place of a blank viewport; when space allows, a Logo band sits above
the cards and the header still owns the compact brand. The dashboard always
shows keybindings. It shows get-started provider rows only when no provider is
selected or the selected provider needs authentication, with provider rows
bounded to fit; agents and skills only when valid configured entries exist;
and recent prompts only when prompt history exists. It repacks to fit the
terminal on resize and collapses to a single column when narrow.

Full keyboard reference: [keybinds.md](keybinds.md).
