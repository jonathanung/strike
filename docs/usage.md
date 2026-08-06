# Usage

## Slash commands

strike launches without any provider configured. Pick one inside the TUI:

```
/provider                      # centered picker: providers + auth status;
                               # selecting an unauthenticated one starts its
                               # login and switches once it succeeds;
                               # ctrl+x logs out (y/n confirm)
/provider anthropic            # direct switch (or openai, xai, google, kimi, deepseek, echo)
/provider openai gpt-5.5       # optional explicit model
/model                         # centered model picker for the current
                               # provider (live models.dev catalog, cached
                               # 24h; type to filter)
/model grok-4.5                # direct switch on the current provider
/effort                        # centered picker for reasoning effort
                               # (active level shows on the top status bar)
/effort xhigh                  # off | low | medium | high | xhigh | max
/autonomy                      # exit-gate policy picker
/autonomy supervised           # supervised | agent | checks | skip-all
/mode                          # permission posture picker (Shift+Tab cycles)
/mode soft-approve             # default | plan | soft-approve | accept-edits | yolo
/agent                         # centered agent picker
/agent plan                    # direct switch (build, plan, explore, …)
/agents                        # focus the agents right pane
/activity                      # focus the activity right pane
/files                         # focus the files right pane
/visualizer                    # focus the visualizer right pane
/system                        # focus the system right pane (needs telemetry on)
/telemetry                     # toggle local system metrics pane (CPU/RAM/disk)
/telemetry on|off|status       # show/hide or report; on by default (also --telemetry)
/pets                          # focus the pets right pane (ASCII companions)
/pets cat|dog|panda|fish       # pick a pet and focus the pane
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
/settings                      # defaults, compaction/prune dials, custom providers
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
/timeline                      # collapsed structured run timeline
/timeline export [path]        # versioned redacted JSON/JSONL trace export
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
/queue                         # browse/edit prompts queued while a turn runs
                               # (reorder, promote, delete, edit text, or
                               # interrupt to run the next item now)
 /issues [list|add|get|close|export|import] …
                               # project-scoped issue tracker; export/import
                               # portable JSON (default strike-issues.json).
                               # import merges by id; --replace wipes first.
                               # Relative export/import paths stay under the
                               # project root (no path escape).
/loop <interval> <job>         # recurring LLM job (session-only; see loop.md)
/loop list                     # list active loops
/loop stop [id]                # stop one loop or all
/workflow                      # list loaded workflows (source · valid · phases)
/workflow inspect <name>       # phases, gates, fingerprint, grants
/workflow start <name>         # preview phase grants, then activate
/workflow stop                 # clear active phase (keeps session history)
/context                       # context doctor (layers + ~tok by source)
/context pin|exclude <kind>    # pin or exclude a system-prompt layer kind
/context include|unpin <kind>  # undo exclude/pin
/context clear                 # clear pin/exclude sets
/effective-prompt              # alias of bare /context
/cost                          # session token totals and estimated USD cost
/keys                          # filterable keybind cheatsheet (also f1)
/legend                        # UI icons, status glyphs, and chrome meanings
/help                          # list commands
/upgrade                       # install latest GitHub Release and restart
/init                          # create or update project AGENTS.md (confirm
                               # before replacing an existing file)
 /ftue                          # setup wizard: provider, model, optional
                               # /init, feature tour, scheduler presets,
                               # first prompt (manual)
 /mcp                           # MCP status; retry/disable servers
 /lsp                           # language server status; retry/disable
 /diagnostics                   # focus the diagnostics right pane
/exit                          # quit strike (same as ctrl+c)
/quit                          # alias of /exit
# Keybind mirrors (same actions as chords; see keybinds.md and /keys):
/focus-left /focus-right       # focus panes (ctrl+h / ctrl+l)
/window-next /window-prev      # cycle right-pane windows (ctrl+o / ctrl+p)
/group-next /group-prev        # cycle right-pane stack groups (ctrl+shift+o / ctrl+shift+p)
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
| `/export` | dump the visible transcript to markdown (user/assistant/tool summaries); redacts credentials via `pkg/redact` (see [secrets.md](secrets.md)); default path under `.strike/exports/` or tmp; `--open` launches `$EDITOR` |
| `/timeline` | collapsed structured run timeline (turns/tools/provider attempts/children with durations); `/timeline export [path]` writes versioned redacted JSON (or `.jsonl`). Complements session JSONL and agent roster/budget fields — not a second full transcript |
| `/copy` | copy plain text of the last assistant response (not tool output) to the system clipboard via OSC52; same as `alt+y`; notice on success/failure |
| `/compact` | ask the engine to compact model history |
| `/memory` | bare = list browser (focuses memory pane); `list [tag]`, `get <key>`, `set <key> <value>`, `rm <key>`, `export [path]`, `import <path> [--replace]` (portable JSON; relative paths stay under project root) |
| `/queue` | browse prompts buffered while a turn runs: reorder (`shift+↑/↓` or `K`/`J`), promote (`p`), in-place edit (`enter`), load into composer (`e`), delete (`d`), clear (`c`), or interrupt and run the FIFO head next (`x`). Empty-composer `bksp` still pops the last item; idle `esc` clears the whole queue |
| `/issues` | bare = list browser (focuses issues pane); `list [open\|closed]`, `add <title>`, `get <id>`, `close <id>`, `export [path]`, `import <path> [--replace]` (same portable rules as memory) |
| `/agents` `/activity` `/files` `/diagnostics` `/visualizer` `/system` `/pets` | jump focus to the named right pane (`/agent` remains persona select; `/system` needs telemetry on; `/pets [name]` picks cat/dog/panda/fish) |
| `/telemetry [on\|off\|status]` | local system metrics pane (CPU/RAM/disk); **on by default** (~1 Hz sampler). Disable with `/telemetry off` |
| `/loop` | schedule a recurring prompt (`15m`, `2h`, …); session-only; `/loop list`, `/loop stop [id]` — see [loop.md](loop.md). Distinct from [`/goal`](goal.md) |
| `/workflow` | list/inspect/start/stop loaded workflows; start previews phase permission grants; palette expands actions |
| `/context` | context doctor modal: system-prompt layers with **per-source ~token estimates**, history msg count, request token attribution (system / tools / messages / tool_results; local ~4 chars/token, labeled `estimated`), pin/exclude/shed state, oversized warnings (previews redacted). Subcommands: `pin`/`unpin`/`exclude`/`include` `<kind>`, `clear`. Fit warnings also appear on the timeline before hard overflow when the window is known. Kind examples: `project_memory`, `lean_code`, `instruction`, `persona` |
| `/cost` | session input/output/cache totals from usage events; est. USD when catalog rates known; unknown stays explicit |
| `/init` | light local scan → write `AGENTS.md`; confirms before overwrite |
| `/ftue` | setup wizard composing provider connect, model pick, optional `/init`, a skippable feature tour (panes, agents, permissions, autonomy, keys, commands), optional scheduler build-system presets (checkbox catalog with rule/limit preview; apply writes global `scheduler.presets` atomically and preserves custom limits/rules), and first-prompt guidance; opening does not change settings; tour copy uses live keybinds and omits unavailable surfaces; Finish focuses the composer; esc dismisses. Finish/dismiss acknowledge global onboarding so auto-open does not repeat; manual `/ftue` stays available. Child pickers/tour/presets return to the same wizard step |
| `/mcp` | MCP status (`up`/`down`/`error`/`disabled`); `/mcp retry [name]`, `/mcp disable <name>` (see [config.md](config.md#mcp-servers-stdio--http)) |
| `/lsp` | language server status; `/lsp retry [name]`, `/lsp disable <name>` (see [config.md](config.md#language-servers-lsp)) |
| `/diagnostics` | focus the diagnostics right pane (live language-server findings) |

### Agent teams

**Team = same session tree by default.** When a lead session spawns `task`
children, those agents (lead + children) form an implicit team — no separate
team-create step. Concurrent roots are independent teams.

| Capability | How |
|---|---|
| Spawn teammates | `task` with optional `name` (stable alias) and `agent` persona |
| List roster | `agent_roster` |
| Peer message | `agent_message` (`to` = `session_id` or `name`) |
| Contracts | `agent_message` with `task_id` / `urgency` / `kind=request`+`require_ack`; read via `agent_thread`; ack with `kind=ack` |
| Fan-out | `agent_broadcast` (all other teammates; use sparingly) |
| Parent steer only | `task_message` (owned child; not peer chat) |
| Finish signal | `[child.completed]` on the lead (structured handoff JSON) |

Messages land at tool-round / idle boundaries (safe injection). Defaults allow
in-team messaging; out-of-team targets fail closed; permission deny rules still
apply. Parent-only flows that never call `agent_*` tools are unchanged. Prefer
contracts (task-bound threads, ack TTL, urgency) over chatty status ping-pong.

**Example — parallel explore + implement with one peer handoff:**

1. Lead: `task(name=explorer, agent=explore, …)` and
   `task(name=implementer, agent=general, …)` in the same turn.
2. Explorer: `agent_message(to="implementer", body="change X in path Y; tests in Z", task_id=…, kind="request")`.
3. Implementer acks (`kind=ack`, `in_reply_to`) and acts; lead synthesizes from
   `[child.completed]` structured handoff + inbox / `agent_thread`.

Full coordination semantics: [agents-skills.md](agents-skills.md#agent-teams).

### Autonomy & workflows

`/workflow` lists, inspects, starts, and stops loaded workflows (builtin,
global `~/.strike/workflows`, project `.strike/workflows`, and plugin sources).
Bare `/workflow` lists the catalog with source and validation state. Start
always previews phase-0 permission grants before mutating engine state; only
validated workflows can activate. Stop clears phase state without rewriting
session history. The command palette expands list/start/stop/inspect actions.

`/autonomy` sets the session exit-gate policy for multi-phase workflows
(see [agents-skills.md](agents-skills.md)). The dial is **authoritative** for
every phase exit (`phase_done`, `exit_plan_mode`); workflow-authored exit
types are not. Distinct from `/mode` (tool permissions) and from `--auto`.

| Mode | Behavior |
|---|---|
| `supervised` (default) | you approve every phase exit |
| `agent` | model clears gates via `phase_done` / `exit_plan_mode` |
| `checks` | phase check commands must exit 0 (trust-gated `phase_check`) |
| `skip-all` | bypass workflow/plan approval only — tool permissions unchanged; plan handoff records `approvalSource=skip-all` |

### Permission mode dial

`/mode` (or **Shift+Tab**) cycles the session **tool-permission posture**. This
is distinct from `/autonomy` (exit gates) and from the **sandbox** dial (OS
isolation — what bash is allowed to touch). The header always shows `mode …`;
yolo also paints a danger banner. Mode changes are accepted **mid-turn**: the
new posture applies to subsequent tool permission checks in the same turn
(in-flight permission asks are rejected so the model retries under the new
rules).

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

### OS sandbox dial

`sandbox` in [config.md](config.md) (or `--sandbox`) sets OS process isolation
for bash: `off` | `read-only` | `workspace-write` (default). This is **what is
possible**; `permissionMode` is **when you get asked**. `/sandbox` prints the
effective policy and backend; `/sandbox explain` shows the generated profile
(including write-deny globs and network posture compiled from permissions).
`yolo` with `sandbox: off` requires `--i-know`.

Built-in skills also appear as slash commands: `/commit`, `/push`, `/pr`,
`/ship`, `/review`, `/learn`, `/deslop`, `/verify` (plus custom skills under
discovery roots). See [agents-skills.md](agents-skills.md) and
[peer-ecosystem.md](peer-ecosystem.md).

### Composer: `!` shell escape

Prefix a line with `!` to run a local bash command in the session work
directory without starting a model turn (for example `!pwd`, `!git status`).
Output appears in the transcript as a bash tool cell. Empty `!` is ignored
with a notice. Destructive commands that target paths outside the workspace
are checked by the same best-effort path guard as the bash tool.

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
./strike --telemetry             # ensure local system metrics pane (on by default)
./strike exec "fix the flaky test"   # one-shot headless turn → stdout
./strike exec -                  # read prompt from stdin
./strike exec --json "…"         # single result object on stdout
./strike exec --output-format stream-json "…"  # protocol Event envelopes (JSONL)
./strike rpc --provider echo     # stdio JSON-RPC Op/Event bridge (NDJSON)
./strike acp --provider echo     # Agent Client Protocol agent (Zed / Devin)
./strike mcp-serve --provider echo --auto   # MCP server (stdio) for hosts
```

System telemetry (local host CPU/RAM/disk only — not cloud analytics) is **on
by default** (~1 Hz sampler). Disable with `/telemetry off`; re-enable with
`/telemetry on` or launch with `--telemetry`.

`--continue` and `--session` cannot be combined. `strike exec` accepts the
same `--provider` / `--model` / `--effort` /
`--auto` / `--dangerously-skip-permissions` flags as the TUI, plus
`--output-format text|json|stream-json` (default `text`) and `--json`
(shorthand for `json`). Formats:

| Format | Stdout |
|---|---|
| `text` | plain assistant text (default) |
| `json` | one `{"type":"result",…}` object when the turn ends |
| `stream-json` | one `pkg/protocol` Event envelope per line (same shape as session JSONL) |

Exit codes: `0` turn ok, `1` turn/runtime error, `2` usage error. Permission
and question prompts cannot be answered interactively in exec; asks are
rejected unless `--auto` or `--dangerously-skip-permissions` is set
(configured/agent denies still apply). Full flag list:
[install.md](install.md) or `strike --help`.

`strike rpc` is a long-lived stdio bridge: newline-delimited JSON-RPC 2.0 on
stdin/stdout (same Op/Event envelopes as `pkg/protocol` and the serve
WebSocket). Ops go in (`user.input`, `permission.reply`, or method `op` with
an OpEnvelope); events come out as `event` notifications. Diagnostics stay on
stderr. See `strike rpc --help`.

`strike acp` speaks the [Agent Client Protocol](https://agentclientprotocol.com/)
so editors like Zed and Devin Desktop can embed strike as an ACP agent. It maps
`session/new` / `session/prompt` / `session/cancel` onto Op/Event
(`user.input`, `interrupt`, tool and text events as `session/update`, permission
asks as `session/request_permission`). Stdout is pure ACP JSON-RPC; diagnostics
on stderr. See `strike acp --help`.

### MCP server mode (`strike mcp-serve`)

Exposes strike as a tools-only [MCP](https://modelcontextprotocol.io) server on
stdio so hosts (Claude Code, Codex, …) can delegate work via a `strike_task`
tool. Each call runs one headless turn (same engine path as `strike exec`) and
returns the assistant summary. Wire traffic is stdout; diagnostics go to
stderr. Same provider/model/effort/sandbox/`--auto` flags as exec. Example host
config:

```json
{
  "mcpServers": {
    "strike": {
      "command": "strike",
      "args": ["mcp-serve", "--provider", "anthropic", "--auto"]
    }
  }
}
```

## UI

**Before the first prompt**, the screen is a centered home layout: header,
thin context bar, STRIKE wordmark, focused prompt box (mode: chat / shell /
command), optional recent-history line, and a short composer-oriented footer.
`ctrl+l` (or `/focus-right` / a pane jump like `/agents`) opens the multi-pane
workspace early: the launch stack (empty transcript + composer) becomes the
left pane and the right pane column fills with session panels. After the first
message, the multi-pane session layout takes over either way.

That layout has a full-width header, **context-sensitive** footer hints
(composer vs right-pane navigation), and a danger banner when needed. Its left
pane is one aggregate stack: `session` transcript, reserved notice line,
slash-command completion, and mode-titled `chat ❯` / `shell ❯` / `command ❯`
composer. The right slot hosts one active window from the registry:

| Window | Role |
|---|---|
| `context` | setup summary (provider, model, agent, …) |
| `activity` | tools / subagent status / empty-state |
| `agents` | multi-root session/agent tree (concurrent roots + children) |
| `visualizer` | selected-node status, tokens/cost, tokens/turn sparkline |
| `files` | workspace file tree (`host.Files`) |
| `memory` | project memory browser |
| `issues` | project issue browser |
| `markdown` | markdown reader (`/md-read <path|@path>`; or modal via `mdReadMode`) |
| `editor` | embedded nvim/vim/nano PTY for `/vim` or `/nano` (modal via `vimMode`/`nanoMode`) |

Related right-pane windows stack as **groups** when the pane is tall/wide
enough: session (`context`+`activity`[+`system` telemetry]), agents
(`agents`+`visualizer`), and project (`memory`+`issues`). Sparse panes
(context, system) size to their content; activity (and other flex members)
absorb the remainder so empty bordered voids stay small. `files`, `markdown`,
and `editor` stay full-height singles. Compact or narrow terminals collapse
each group to one pane.

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

Session worktree isolation defaults to `off` (launch cwd); set
`session.worktree` to `auto` or `always` in [config.md](config.md) for
per-root git worktrees. Full chord table:
[keybinds.md](keybinds.md).

Pane keys (orientation-independent): `ctrl+h` / `ctrl+l` focus the left
(primary transcript) or right (secondary pane column); `ctrl+o` / `ctrl+p`
cycle focus within the active stack group then to the next group;
`ctrl+shift+o` / `ctrl+shift+p` jump to the next/previous stack group (first
pane). `ctrl+;` (or `/layout` / `/split`) toggles a vertical top/bottom split
without swapping those chords. `ctrl+k` opens the command palette (when
kill-to-end does not delete); `f1` (or `/keys`) opens a filterable keybind
cheatsheet. Enter sends;
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
onboarding also mentions `/init`. On a clean install the interactive TUI
auto-opens `/ftue` once until you finish or dismiss it (state in
`~/.strike/onboarding.json`). Re-run the full guided setup anytime with
`/ftue` (provider → model → optional project init → feature tour → optional scheduler presets → first prompt). Agents and
skills appear only when valid configured entries exist; recent prompts only
when prompt history exists. It repacks to fit the terminal on resize and
collapses to a single column when narrow.

Full keyboard reference: [keybinds.md](keybinds.md).
