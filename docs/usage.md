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
/mode                          # permission posture picker (Shift+Tab cycles)
/mode yolo                     # default | plan | accept-edits | yolo
/agent                         # centered agent picker
/agent plan                    # direct switch (build, plan, explore, …)
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
/compact                       # compact model history (trim or summarize per config)
/fork                          # duplicate this conversation into a new session id
/undo                          # undo last turn (picker: chat only vs + files)
/undo chat                     # drop last turn from history only
/undo files                    # drop last turn and restore files edited then
/rewind                        # alias of /undo
/export [path] [--open]        # write the transcript to markdown (default
                               # .strike/exports/… or $TMPDIR); --open hands
                               # the file to $EDITOR / $VISUAL
/vim [path[:line]]             # open file in embedded editor (pane/overlay)
                               # or $EDITOR (see vimMode in config.md)
/md-read <path>                # open a markdown file in the right markdown pane
/memory [list|get|set|rm|export|import] …
                               # project-scoped durable key/value memory;
                               # export/import portable JSON (default path
                               # strike-memory.json). import merges by key;
                               # add --replace to wipe first
/issues [list|add|get|close|export|import] …
                               # project-scoped issue tracker; export/import
                               # portable JSON (default strike-issues.json).
                               # import merges by id; --replace wipes first.
                               # Relative export/import paths stay under the
                               # project root (no path escape).
/context                       # context doctor modal (prompt layer breakdown)
/effective-prompt              # alias of /context
/cost                          # session token totals and estimated USD cost
/keys                          # filterable keybind cheatsheet (also f1)
/help                          # list commands
/upgrade                       # install latest GitHub Release and restart
/init                          # create or update project AGENTS.md (confirm
                               # before replacing an existing file)
/mcp                           # list MCP servers (up/down) and tool names
```

### Session, memory, issues

| Command | Notes |
|---|---|
| `/session` | picker of past **root** sessions (auto-titles); resume reloads model history |
| `/session <id>` | resume that root session by id |
| `/fork` | copy the current session JSONL into a new id (idle only) |
| `/undo` / `/rewind` | undo last turn (idle only); bare opens picker; `chat` keeps disk; `files` restores per-file checkpoints from that turn (never `git reset --hard`) |
| `/export` | dump the visible transcript to markdown (user/assistant/tool summaries); redacts common API-key shapes; default path under `.strike/exports/` or tmp; `--open` launches `$EDITOR` |
| `/compact` | ask the engine to compact model history |
| `/memory` | bare = list browser; `list [tag]`, `get <key>`, `set <key> <value>`, `rm <key>`, `export [path]`, `import <path> [--replace]` (portable JSON; relative paths stay under project root) |
| `/issues` | bare = list browser; `list [open\|closed]`, `add <title>`, `get <id>`, `close <id>`, `export [path]`, `import <path> [--replace]` (same portable rules as memory) |
| `/context` | context doctor modal: layer sizes, history msg count, oversized warnings (previews redacted) |
| `/cost` | session input/output/cache totals from usage events; est. USD when catalog rates known; unknown stays explicit |
| `/init` | light local scan → write `AGENTS.md`; confirms before overwrite |
| `/mcp` | list configured MCP servers, status, and namespaced tool ids (see [config.md](config.md#mcp-servers-stdio-tools)) |

### Autonomy & workflows

`/autonomy` sets the session exit-gate policy for multi-phase workflows
(see [agents-skills.md](agents-skills.md)):

| Mode | Behavior |
|---|---|
| `supervised` (default) | you approve phase gates |
| `agent` | model clears gates via `phase_done` |
| `checks` | configured check commands must exit 0 |

### Permission mode dial

`/mode` (or **Shift+Tab**) cycles the session **tool-permission posture**. This
is distinct from `/autonomy` (exit gates). The header always shows `mode …`;
yolo also paints a danger banner.

| Mode | Behavior |
|---|---|
| `default` | normal ask rules (config + agent + phase) |
| `plan` | hard read-only write/edit deny; enters plan workflow |
| `accept-edits` | auto-allow edit/write; bash/network still ask |
| `yolo` | skip permission asks; explicit deny rules still apply |

Persists per session in the JSONL log. Optional default for new sessions:
`permissionMode` in [config.md](config.md).

Built-in skills also appear as slash commands: `/commit`, `/push`, `/pr`,
`/ship`, `/review`, `/learn`, `/deslop`, `/verify` (plus custom skills under
discovery roots). See [agents-skills.md](agents-skills.md) and
[peer-ecosystem.md](peer-ecosystem.md).

### Composer: `@file` / `@folder` mentions

Type `@` then a path fragment for fuzzy project-file completion (needs
`host.Files`). Matching uses basename and full relative path. Directories
appear as `@path/`. An exact typed path is always offered when it exists under
the project root, even if it was outside the fuzzy top results.

**Index:** prefers `git ls-files` (honors `.gitignore`); otherwise walks the
project root. Default skips include `.plan`, `node_modules`, `.git`, `vendor`,
build outputs, and similar noise. Add more basename skips (one per line) in
`.strike/file-index-skip`.

**Expand on send:** `@file` attaches file contents (capped; binary/oversize
skipped with a notice). `@folder/` attaches an **immediate child listing
only** (not a recursive multi-file dump). Transcript/history keep the `@path`
tokens. Emails (`user@host`) are not treated as mentions. Paths cannot escape
the project root (symlink-safe).

Submitting a prompt before selecting shows "No model selected" in the
notice line above the composer (your prompt stays in the input). Talking to
a real provider needs credentials — see [auth.md](auth.md).

Provider selection happens in-app with `/provider`; `--provider` on the
command line just pre-selects (and validates credentials eagerly). Custom
OpenAI-/Anthropic-compatible endpoints: `/settings` or config
`providers` — see [config.md](config.md).

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
| `agents` | multi-root session/agent tree |
| `visualizer` | selected-node status, tokens/cost, activity sparkline |
| `files` | workspace file tree (`host.Files`) |
| `memory` | project memory browser |
| `issues` | project issue browser |
| `markdown` | markdown reader (`/md-read <path>`) |
| `editor` | embedded nvim/vim PTY for `/vim` |

Related right-pane windows stack as **groups** when the pane is tall/wide
enough: session (`context`+`activity`), agents (`agents`+`visualizer`), and
project (`memory`+`issues`) share a 50/50 split (vertical in a side column,
horizontal in a bottom bar). `files`, `markdown`, and `editor` stay full-height
singles. Compact or narrow terminals collapse each group to one pane.

Vim-style pane keys (horizontal split): `ctrl+h` / `ctrl+l` focus the left
or right pane; `ctrl+j` / `ctrl+k` move focus within the active stack group
then to the next group (enhanced terminals distinguish `ctrl+j` from bare LF
newline). `ctrl+;` (or `/layout` / `/split`) toggles a vertical
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
bounded to fit (and a `/init` CTA when `AGENTS.md` is missing); first-run
onboarding also mentions `/init`. Agents and skills appear only when valid
configured entries exist; recent prompts only when prompt history exists. It
repacks to fit the terminal on resize and collapses to a single column when
narrow.

Full keyboard reference: [keybinds.md](keybinds.md).
