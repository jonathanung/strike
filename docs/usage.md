# Usage

## Slash commands

strike launches without any provider configured. Pick one inside the TUI:

```
/provider                      # centered picker: providers + auth status;
                               # selecting an unauthenticated one starts its
                               # login and switches once it succeeds;
                               # ctrl+x logs out (y/n confirm)
/provider anthropic            # direct switch (or openai, xai, echo)
/provider openai gpt-5.5       # optional explicit model
/model                         # centered model picker for the current
                               # provider (live models.dev catalog, cached
                               # 24h; type to filter)
/model grok-4.5                # direct switch on the current provider
/effort                        # centered picker for reasoning effort
                               # (active level shows on the top status bar)
/effort xhigh                  # off | low | medium | high | xhigh | max
/autonomy                      # exit-gate policy picker
/autonomy supervised           # supervised | agent | checks
/mode                          # permission posture picker (Shift+Tab cycles)
/mode soft-approve             # default | plan | soft-approve | accept-edits | yolo
/agent                         # centered agent picker
/agent plan                    # direct switch (build, plan, explore, …)
/agents                        # focus the agents right pane
/activity                      # focus the activity right pane
/files                         # focus the files right pane
/visualizer                    # focus the visualizer right pane
/system                        # focus the system right pane (needs /telemetry on)
/telemetry                     # toggle local system metrics pane (CPU/RAM/disk)
/telemetry on|off|status       # opt in/out or report; off by default (also --telemetry)
/session                       # browse past root sessions for this workspace
                               # (auto-titles); ctrl+a shows all workspaces
/session <id>                  # resume a specific session by id (any workspace)
/rename                        # rename the current session (editor)
/rename <title>                # rename the current session immediately
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
/settings                      # defaults (theme, editor, mode) + custom providers
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
/rewind                        # fork from a previous turn (picker; keeps original)
/rewind <n>                    # fork keeping completed turns 1..n
/export [path] [--open]        # write the transcript to markdown (default
                               # .strike/exports/… or $TMPDIR); --open hands
                               # the file to $EDITOR / $VISUAL
/copy                          # copy last assistant response to clipboard
                               # (OSC52; also alt+y)
/vim [path|@path[:line]]       # open file in editor (embedded/modal/takeover)
                               # or $EDITOR; @path like composer mentions
                               # (e.g. /vim @internal/foo.go)
/nano [path|@path[:line]]      # open file in nano (embedded/modal/takeover;
                               # @path ok; see nanoMode in config.md)
/md-read <path|@path>          # open markdown (embedded right pane or modal;
                               # @path ok; see mdReadMode in config.md)
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
/loop <interval> <job>         # recurring LLM job (session-only; see loop.md)
/loop list                     # list active loops
/loop stop [id]                # stop one loop or all
/context                       # context doctor modal (prompt layer breakdown)
/effective-prompt              # alias of /context
/cost                          # session token totals and estimated USD cost
/keys                          # filterable keybind cheatsheet (also f1)
/legend                        # UI icons, status glyphs, and chrome meanings
/help                          # list commands
/upgrade                       # install latest GitHub Release and restart
/init                          # create or update project AGENTS.md (confirm
                               # before replacing an existing file)
 /mcp                           # MCP status; retry/disable servers
/exit                          # quit strike (same as ctrl+c)
/quit                          # alias of /exit
# Keybind mirrors (same actions as chords; see keybinds.md and /keys):
/focus-left /focus-right       # focus panes (ctrl+h / ctrl+l)
/window-next /window-prev      # cycle right-pane windows (ctrl+o / ctrl+p)
/scroll-up /scroll-down        # transcript scroll
/jump-bottom                   # jump to latest output (ctrl+t)
/palette                       # command palette (ctrl+k)
/interrupt                     # interrupt running turn (esc)
/save-defaults                 # save defaults (ctrl+d)
/leave-editor                  # leave embedded editor (ctrl+g)
/edit-prompt                   # external editor for prompt (ctrl+e)
/agent-next                    # cycle agent persona (tab)
/mode-next                     # cycle permission mode (shift+tab)
/tool-prev /tool-next          # select tool cells (alt+[ / alt+])
/tool-expand /tool-copy        # expand or copy selected cell
/tool-review /tool-apply       # review/apply selected edit
/subagent /parent              # enter first child / return to parent
/subagent-next /subagent-prev  # sibling subagent cycle
/root-new /root-open           # concurrent roots (agents pane n / enter)
/root-interrupt /root-hide     # interrupt or hide selected root
/root-filter                   # cycle agents pane filter (f)
```

### Session, memory, issues

| Command | Notes |
|---|---|
| `/session` | picker of past **root** sessions for the **current workspace** only (auto-titles; resume reloads model history). `ctrl+a` toggles all workspaces. Legacy sessions without a stored folder path appear only in all-workspaces mode |
| `/session <id>` | resume that root session by id (works across workspaces; list filter does not apply) |
| `/rename [title]` | rename the current session (brief auto-titles; agents pane `r` too) |
| `/fork` | copy the current session JSONL into a new id (idle only) |
| `/undo` | undo last turn in place (idle only); bare opens picker; `chat` keeps disk; `files` restores per-file checkpoints from that turn (never `git reset --hard`) |
| `/rewind` | fork a **new** session from a completed turn (idle only); original session stays listable; bare opens turn picker; `/rewind n` keeps turns 1..n. Workspace file revert is not part of rewind (use `/undo files` on the live session) |
| `/export` | dump the visible transcript to markdown (user/assistant/tool summaries); redacts common API-key shapes; default path under `.strike/exports/` or tmp; `--open` launches `$EDITOR` |
| `/copy` | copy plain text of the last assistant response (not tool output) to the system clipboard via OSC52; same as `alt+y`; notice on success/failure |
| `/compact` | ask the engine to compact model history |
| `/memory` | bare = list browser (focuses memory pane); `list [tag]`, `get <key>`, `set <key> <value>`, `rm <key>`, `export [path]`, `import <path> [--replace]` (portable JSON; relative paths stay under project root) |
| `/issues` | bare = list browser (focuses issues pane); `list [open\|closed]`, `add <title>`, `get <id>`, `close <id>`, `export [path]`, `import <path> [--replace]` (same portable rules as memory) |
| `/agents` `/activity` `/files` `/visualizer` `/system` | jump focus to the named right pane (`/agent` remains persona select; `/system` needs telemetry on) |
| `/telemetry [on\|off\|status]` | local system metrics pane (CPU/RAM/disk); **off by default** (no 1 Hz sampler). Launch with `--telemetry` or `/telemetry on` |
| `/loop` | schedule a recurring prompt (`15m`, `2h`, …); session-only; `/loop list`, `/loop stop [id]` — see [loop.md](loop.md). Distinct from [`/goal`](goal.md) |
| `/context` | context doctor modal: layer sizes, history msg count, oversized warnings (previews redacted) |
| `/cost` | session input/output/cache totals from usage events; est. USD when catalog rates known; unknown stays explicit |
| `/init` | light local scan → write `AGENTS.md`; confirms before overwrite |
| `/mcp` | MCP status (`up`/`down`/`error`/`disabled`); `/mcp retry [name]`, `/mcp disable <name>` (see [config.md](config.md#mcp-servers-stdio--http)) |

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
| `soft-approve` | visible 15s countdown then allow once; veto anytime |
| `accept-edits` | auto-allow edit/write; bash/network still ask |
| `yolo` | skip permission asks; explicit deny rules still apply |

Persists per session in the JSONL log. Optional default for **new** sessions:
`permissionMode` in [config.md](config.md), or **ctrl+d** in the `/mode`
picker (and global ctrl+d) to save the current posture as that default.
Resume restores the session log, not the config default.

Built-in skills also appear as slash commands: `/commit`, `/push`, `/pr`,
`/ship`, `/review`, `/learn`, `/deslop`, `/verify` (plus custom skills under
discovery roots). See [agents-skills.md](agents-skills.md) and
[peer-ecosystem.md](peer-ecosystem.md).

### Composer: `!` shell escape

Prefix a line with `!` to run a local bash command in the session work
directory without starting a model turn (for example `!pwd`, `!git status`).
Output appears in the transcript as a bash tool cell. Empty `!` is ignored
with a notice. Destructive commands that target paths outside the workspace
are blocked by the same sandbox as the bash tool.

### Composer: `@file` / `@folder` mentions

Type `@` then a path fragment for fuzzy project-file completion (needs
`host.Files`). Matching uses basename and full relative path. Directories
appear as `@path/`. An exact typed path is always offered when it exists under
the project root, even if it was outside the fuzzy top results.

File-taking slash commands accept the same `@path` form: `/vim @internal/foo.go`,
`/nano @notes.txt`, `/md-read @README.md`. Plain paths still work without `@`.
Bare or invalid mentions (for example `/vim @` or `/vim @../secret`) error.

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
OpenAI-/Anthropic-compatible endpoints: `/settings`, `.strike/providers.jsonc`,
or config `providers` — see [config.md](config.md).

## CLI session resume & headless exec

```sh
./strike --continue              # resume most recent root session
./strike --session <id>          # resume a specific root session by id
./strike --telemetry             # show local system metrics pane (CPU/RAM/disk)
./strike exec "fix the flaky test"   # one-shot headless turn → stdout
./strike exec -                  # read prompt from stdin
```

System telemetry (local host CPU/RAM/disk only — not cloud analytics) is **off
by default** so no 1 Hz sampler runs. Enable with `--telemetry` or
`/telemetry on`; disable with `/telemetry off`.

`--continue` and `--session` cannot be combined. `strike exec` accepts the
same `--provider` / `--model` / `--effort` /
`--auto` / `--dangerously-skip-permissions` flags as the TUI. Permission and
question prompts cannot be answered interactively in exec; asks are rejected
unless `--auto` or `--dangerously-skip-permissions` is set
(configured/agent denies still apply). Full flag list:
[install.md](install.md) or `strike --help`.

## UI

The screen has a full-width header, footer hints, and danger banner when
needed. Its left pane is one aggregate stack: `session` transcript, reserved
notice line, slash-command completion, and `prompt ❯` composer. The right
slot hosts one active window from the registry:

| Window | Role |
|---|---|
| `context` | setup summary (provider, model, agent, …) |
| `activity` | tools/tips / subagent status |
| `agents` | multi-root session/agent tree (concurrent roots + children) |
| `visualizer` | selected-node status, tokens/cost, activity sparkline |
| `files` | workspace file tree (`host.Files`) |
| `memory` | project memory browser |
| `issues` | project issue browser |
| `markdown` | markdown reader (`/md-read <path|@path>`; or modal via `mdReadMode`) |
| `editor` | embedded nvim/vim/nano PTY for `/vim` or `/nano` (modal via `vimMode`/`nanoMode`) |

Related right-pane windows stack as **groups** when the pane is tall/wide
enough: session (`context`+`activity`), agents (`agents`+`visualizer`), and
project (`memory`+`issues`) share a 50/50 split (vertical in a side column,
horizontal in a bottom bar). `files`, `markdown`, and `editor` stay full-height
singles. Compact or narrow terminals collapse each group to one pane.

### Concurrent root sessions

Focus the `agents` pane to run several parent sessions side by side. The pane
footer (and empty state) list the controls; `/keys` / `f1` with the agents
pane focused leads with those rows under **Current focus**, then the full
cheatsheet (including **Agents** when not focused):

| Key | Action |
|---|---|
| `n` | spawn a new concurrent root |
| `enter` | activate the selected root (or open a child transcript) |
| `x` | interrupt the selected root or child |
| `j` / `k` | move |
| `f` | cycle filters (all / needs you / working / ready / roots) |

Do not confuse these with:

- **`Tab`** — cycles **agent personas** (build, plan, explore, …) for the
  active composer, not root sessions.
- **`ctrl+x` leader chords** — navigate **child/subagent** transcripts nested
  under a parent; they do not create or switch concurrent roots.

Worktree isolation for second+ roots is configured via `worktree` in
[config.md](config.md). Full chord table: [keybinds.md](keybinds.md).

Pane keys (orientation-independent): `ctrl+h` / `ctrl+l` focus the left
(primary transcript) or right (secondary pane column); `ctrl+o` / `ctrl+p`
cycle focus within the active stack group then to the next group. `ctrl+;`
(or `/layout` / `/split`) toggles a vertical top/bottom split without swapping
those chords. `ctrl+k` opens the command palette (when kill-to-end does not
delete); `f1` (or `/keys`) opens a filterable keybind cheatsheet. Enter sends;
`ctrl+j`, Shift+Enter, or Alt+Enter inserts a newline (Shift+Enter CSI
rewrites to Alt+Enter).
`pgup`/`pgdn` (and `ctrl+up`/`ctrl+down`)
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
