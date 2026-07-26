# Architecture

Reference for agents modifying strike-cli. States what exists in the code;
verify against source before relying on a detail here, since this file can
drift.

## Dataflow

```
cmd/strike/wire.go (run) — composition root
│
├── builds internal/engine, then reads/writes it on two channels:
│     Ops()    chan<- protocol.Op     ◄── internal/tui submits UserInput,
│                                          PermissionReply, Interrupt,
│                                          SelectModel, SelectAgent, SetEffort
│     Events() <-chan protocol.Event  ──► internal/tui renders TextDelta,
│                                          ToolCallBegin/End, PermissionAsked,
│                                          TurnCompleted, ModelSelected, EffortSelected, …
│
├── tees every Event through internal/session before it reaches the TUI:
│     for ev := range eng.Events() { _ = store.Append(ev); events <- ev }
│     store.Append persists one JSONL line per event (~/.strike/sessions/…)
│
├── builds host.Services via internal/host/local.New(authStore, historyStore,
│     memoryStore, issueStore, agentNames, skills) — wraps internal/{auth,config,
│     models,history,memory,issue} into host.Services{Auth, Catalog, Settings,
│     History, Memory, Issues, Agents, Skills}, then attaches host.Files
│     (local.NewFiles(workDir)) for frontend file reads
│
└── tui.New(eng.Ops(), events, services, tui.Options{...})
      internal/tui's entire view of the world: two protocol channels plus
      one host.Services bundle. Nothing else crosses the boundary.
```

The engine never touches a terminal and the TUI never touches a model API,
tool, or credential directly — `internal/protocol` is the only channel
between them, and `internal/host` is the only channel from the TUI to
anything host-side. A session transcript is fully reconstructable by
re-reading its JSONL log, since the log is a serialized copy of the exact
event stream the TUI rendered from (see `internal/protocol/codec.go`).

## Packages

| Package | Role | May import |
|---|---|---|
| `cmd/strike` | CLI entry (`main.go`: flags, usage, `strike auth` subcommand) + composition root (`wire.go`: assembles engine, host/local, session store, tui) | anything — the only package that wires the whole tree |
| `internal/version` | Build-time Version/Commit stamped via `-ldflags` | stdlib |
| `internal/update` | GitHub Releases self-update (check, download, sha256, atomic replace, re-exec) | `version`, stdlib, net/http |
| `internal/protocol` | Op/Event seam between engine and frontends; the JSONL envelope (`codec.go`) is the session persistence format | stdlib only |
| `internal/engine` | Headless agent runtime: turn loop, tool dispatch, permission/question integration, deferred agent switch | `protocol`, `provider`, `tool`, `permission`, `question`, `memory`, `config` |
| `internal/provider` | LLM provider abstraction: `Provider` interface, normalized `StreamEvent`s | stdlib |
| `internal/provider/base` | Shared HTTP/JSON/SSE/auth client concrete adapters embed | `provider`, stdlib, net/http |
| `internal/provider/{anthropic,openaicompat,chatgpt,echo}` | Concrete adapters (openaicompat covers both the OpenAI platform API and xAI; chatgpt is the ChatGPT-subscription backend; echo is the offline dev provider) | `provider`, `provider/base` (all but echo), stdlib |
| `internal/tool` | Tool contract (`Tool`, `Context`, `Result`) + built-ins: read/glob/grep/edit/write/apply_patch/bash/task/webfetch/todowrite/todoread/memory_write/memory_read/issue_write/issue_read/notebook_edit/sleep/skill/question/enter_plan_mode/exit_plan_mode/phase_done/toolsearch | `provider` (for `ToolSchema`), `memory`, `issue`, stdlib |
| `internal/memory` | Project-scoped durable key/value memory (JSON under `~/.strike/memory/`) | stdlib |
| `internal/issue` | Project-scoped durable issues (JSON under `~/.strike/issues/`) | stdlib |
| `internal/question` | User-question ask service: suspends a tool call until `QuestionReply` | `protocol`, stdlib |
| `internal/permission` | Ordered allow/ask/deny rulesets, last-match-wins; the ask service that suspends a tool call for user input | `protocol`, `tool` (for `AskRequest`), stdlib |
| `internal/session` | JSONL event-log persistence (append/replay) + concurrent Manager (multi-session open, durable list, event mux) | `protocol`, stdlib |
| `internal/auth` | Credential store (0600 `auth.json`) + OAuth/PKCE/device flows | stdlib, net/http |
| `internal/config` | Layered JSON config (defaults → global → project) + agents/skills markdown loading | `permission` (Ruleset is a config field), stdlib |
| `internal/models` | models.dev catalog client, 24h cache with stale fallback | stdlib, net/http |
| `internal/history` | Project-scoped prompt history | stdlib |
| `internal/project` | Stable filesystem identity for project-scoped state (git-aware) | stdlib, os/exec |
| `internal/host` | **Frozen contract**: the services a frontend needs from its host process | stdlib only — enforced by the boundary test |
| `internal/host/local` | Real `host.Services` implementation; wraps auth/config/models/history/memory/issue/files for the frontend | `auth`, `config`, `history`, `host`, `issue`, `memory`, `models` |
| `internal/tui` | Bubble Tea frontend: app model, layout, transcript cells, modals, composer | `protocol`, `host`, `tui/...` only — enforced by the boundary test |
| `internal/tui/theme` | Resolved design tokens: adaptive color roles, terminal background, glyphs, border/spacing tokens, and precomputed styles | lipgloss, stdlib |
| `internal/tui/ui` | Reusable component library (Panel, Dialog, Badge, KeyHints, StatusBar, List, Notice, Card/Bento, OverlayCenter, Canvas, Logo) | stdlib, lipgloss, bubbles, charmbracelet/x/ansi, `tui/theme` |

## Dependency rules

Verbatim from the refactor spec (`.plan/refactor-agents-ui.md`):

- `internal/host` (contract pkg only, not local/): stdlib imports only.
- `internal/tui/...`: no `internal/*` imports except `protocol`, `host`,
  `tui/...`.
- No backend package imports `internal/tui/...`.

These are enforced mechanically, not just by convention: `internal/tui/boundary_test.go`
(`TestArchitectureBoundaries`) walks every non-test `.go` file in the module
with `go/parser` and fails, naming the offending file and import, on any
violation. Run it like any other test (`go test ./internal/tui/...`); there
is no way to silently cross the boundary.

## TUI pane routing and layout

Key routing is deliberately ordered: quit, then modal, then completion-owned
left-pane keys, pane actions, global actions (including scroll/jump and split
orientation), and finally the focused component. `paneFocus` is a private
aggregate `left`/`right` value: left owns the transcript, notice, completion,
and composer as one pane, while right owns the active window. Modal ownership
remains `m.modal`; this is not a unified F2-style enum for separate transcript,
composer, and modal focus.

The right pane is TUI-local. Its private, value-oriented `window` interface
has identity/title, initialization, update, resize, and view methods; updates
and resizes return replacement values so model copies do not share mutable
state. The registry holds five windows: named session panes (`context` for
setup summary and `activity` for subagent status, recent parent tools, or idle
tips), a `files` explorer (lazy tree via `host.Files.ListDir`), a `markdown`
reader opened via `/md-read`, and an `editor` PTY window for `/vim`. It exposes
only the active window and has no close state or plugin mechanism. File bytes
and directory listings reach the markdown and files windows through
`host.Files`, not direct disk I/O from the TUI. Window input and resize updates
stay inside `internal/tui`: no protocol Op or Event was added for this pane
infrastructure. Composer input treats Enter as send and
Shift+Enter (normalized to Alt+Enter) as newline via a stdin wrapper and
enhanced keyboard modes; bare Escape from CSI-u is normalized to `0x1b`.

`View()` composes the full-width header first; its body is a horizontal
left|right split by default, or a vertical top/bottom split when
`splitOrientation` is vertical (`ctrl+;` or `/layout`). The left stack is
transcript, notice, completion, composer (in that order); then full-width
hints and the optional danger banner. `ui.Canvas` is the final full-screen
operation. With the default one-column gutter, horizontal split mode begins at
width 93: left is at least 60 columns, gutter is 1, and right is at least 32.
In a horizontal split, the canonical widths are
`right = max(32, (width-gutter)/3)` and
`left = width-gutter-right`. At or below 92 columns, only the active pane
uses the full width. With a custom gutter `g`, the split threshold is
`60 + g + 32`. Vertical split keeps full width and divides body height when
there is room; focus/cycle key chords swap so focus stays on the
cross-axis pair.

Transcript anchoring: `refreshViewport` calls `GotoBottom` only when the
viewport was already `AtBottom` before `SetContent`; otherwise it restores
`YOffset`. Child lifecycle events (`ChildStarted` / `ChildCompleted`) update
activity-pane state and never append transcript cells; other child-correlated
events remain filtered except permissions and questions.

Color themes load from bundled JSON plus `~/.strike/themes` and
`./.strike/themes`; `/theme` opens a picker (or `/theme <id>` applies one)
and `config.theme` / ctrl+d persists the choice. Session-local appearance
(`/theme dark|light|auto`) still calls `lipgloss.SetHasDarkBackground` for
forced modes and restores the initially detected background for auto.

## Why a host-services seam

`internal/host` exists so `internal/tui` has exactly one door to everything
that isn't the model turn loop: credentials, the model catalog, saved
defaults, prompt history, and the agent/skill listings. The TUI talks to
`host.Auth`, `host.Catalog`, `host.Settings`, and `host.History` — never to
`internal/auth`, `internal/config`, `internal/models`, or `internal/history`.

Two things fall out of that:

1. **Backend work can stage invisibly.** Adding a new host service (or a new
   `internal/protocol` event) and its `internal/host/local` implementation
   needs no TUI change to build, vet, or pass tests — the feature simply
   isn't called from any view yet. A later phase wires it up when it's ready
   to be user-facing. This is how "add a service without touching the
   frontend" works in practice, not just in principle.
2. **The frontend develops and tests against fakes.** `internal/tui/testsupport_test.go`
   defines scriptable fakes for every `host.Services` capability
   (`fakeAuth`, `fakeCatalog`, `fakeSettings`, `fakeHistory`) plus the
   `New(...)`-wrapping helpers the test suite builds models with. No TUI test
   touches a real credential file, the network, or project history on disk —
   see that file before writing a new TUI test.

`host.Services` fields may be nil or empty (a fake in a narrow test, a future
frontend that doesn't support one capability); every frontend call site
degrades gracefully instead of panicking — see `services.History != nil`
checks in `internal/tui/app.go` and `saveDefaultsThroughCmd`'s nil-`Settings`
branch in `internal/tui/view.go` for the pattern.

## Recipes

### Add a provider

1. Implement `provider.Provider` (`Name() string`; `Stream(ctx, Request) (<-chan StreamEvent, error)`,
   `internal/provider/provider.go`). For a real HTTP backend, embed
   `provider/base.Client` for transport/auth/JSON-SSE/error-shaping (see
   `internal/provider/anthropic/anthropic.go` or `openaicompat/openaicompat.go`);
   for something synthetic, see `internal/provider/echo/echo.go`.
2. Wire construction into the `selectProvider` closure in `cmd/strike/wire.go`
   (the `switch name { case "anthropic": ... }` block) — it returns the
   `provider.Provider`, its default model, and an error for missing
   credentials or an unknown name.
3. Add the provider's default model id to `config.DefaultModel` in
   `internal/config/config.go`.
4. If the frontend should offer login/selection for it, add an entry to
   `credentialProviders` in `internal/host/local/local.go` with its
   capability flags (`APIKey`/`OAuth`/`Device`), and add `BeginOAuth`/`BeginDevice`
   switch cases if it supports those flows (see `auth.OpenAIFlow`,
   `auth.XAIFlow`, `auth.XAIDeviceFlow` in `internal/auth` for the pattern
   each wraps). Skip this for an env-var-only or builtin provider (like echo).
5. No `internal/tui` change is needed: `/provider`, the provider picker, and
   `/auth` are entirely data-driven from `host.Auth.Statuses()`.

### Add a tool

1. Implement `tool.Tool` (`Name`, `Description`, `Schema`, `Execute`) in a new
   file under `internal/tool/` — `internal/tool/glob.go` is a minimal
   example; `edit.go`/`write.go`/`bash.go` show the permission-ask pattern.
2. Register it in the `tool.NewRegistry(...)` call in `cmd/strike/wire.go`.
  3. If it mutates state or has side effects, call
     `tc.Ask(ctx, tool.AskRequest{Permission: "yourperm", Patterns: []string{...}})`
     inside `Execute`, and add a default rule for `"yourperm"` to
     `permission.Defaults()` in `internal/permission/permission.go` (Allow for
     read-only, Ask for anything mutating — see the existing defaults; reuse
     `edit`/`write`/`bash` when the new tool is the same class of action).
  4. No `internal/tui` change is needed for a generic tool: tool calls render
    from `protocol.ToolCallBegin`/`ToolCallEnd` via `toolCell` in
    `internal/tui/cells.go` (name, title, output preview, ok/err glyph).
    Edit-shaped `Metadata` (`oldString`/`newString`) is consumed by the TUI
    via `ui.DiffPreview` in the permission modal and completed tool cells;
    other tools can keep emitting metadata without a TUI change until a
    frontend renderer is added for them.

### Add a slash command

Two different mechanisms, depending on whether it needs Go code:

- **Skill (no code).** Built-in shipping skills (`commit`, `push`, `pr`,
  `ship`) are embedded under `internal/config/skills/` and always load.
  Drop a markdown file with frontmatter into `~/.strike/skills/<name>.md`
  or `./.strike/skills/<name>.md` to add or override — see
  `LoadSkillsWithError` in `internal/config/agents.go` for the frontmatter
  format (`description:`) and `$ARGUMENTS` substitution. It becomes
  `/<name>` on the next launch automatically, through
   `host.Services.Skills`. Reserved names (`provider`, `model`, `effort`,
   `autonomy`, `auth`, `settings`, `agent`, `fast`, `vim`, `md-read`,
   `theme`, `layout`, `split`, `compact`, `fork`, `undo`, `rewind`,
   `session`, `help`, `keys`, `memory`, `issues`, `context`,
   `effective-prompt`, `upgrade`) are rejected by
   `config.ValidateSkillName` before
   they ever reach the frontend. PR URLs from successful `gh pr` bash
   output are stored via `protocol.SessionMeta` and `session` sidecar
   metadata. `/vim` embeds nvim/vim in the right-pane `editor` window by
   default (PTY + vt10x via `internal/tui/term`). Config key `vimMode`
   selects `pane` (default), `overlay`, or `takeover` (full-screen
   `tea.ExecProcess`). GUI `$EDITOR` values always take over.
- **Builtin command (code).** Add a `commandSpec` to `builtinCommandSpecs`
  in `internal/tui/commands.go`, a `case "/yourcmd":` arm in
  `Model.handleCommand` (`internal/tui/app.go`), and — if it's a primary
  action — a hint in `hintsView` (`internal/tui/view.go`).

### Add a UI component

1. Add `internal/tui/ui/yourname.go`. Imports are limited to stdlib,
   lipgloss, bubbles, `charmbracelet/x/ansi`, and `internal/tui/theme` (see
   the package doc in `internal/tui/ui/doc.go`). Meet its three guarantees:
   width-safe (never render wider than asked — check with `lipgloss.Width`),
   graceful at tiny widths (drop borders/truncate rather than panic), and
   zero-value tolerant (use `resolveIcons(th)` so a bare `theme.Theme{}`
   still renders, matching every existing component).
2. Give the exported function a doc comment with a short usage snippet (see
   `internal/tui/ui/badge.go` or `panel.go` for the format).
3. Add a rendered-string test at a few fixed widths in
   `internal/tui/ui/yourname_test.go` — assert structure (`lipgloss.Width`,
   substrings, line counts), not literal ANSI bytes; `panel_test.go` and
   `list_test.go` show the pattern.
4. Consume it from a view in `internal/tui/*.go`. Views never build a raw
   lipgloss box or list — that is what this package is for.

### Add a host service

1. Add the method/interface/field to `host.Services` in
   `internal/host/host.go`. This package is a stdlib-only contract — no
   importing `auth`, `config`, `models`, or `history` here, even for a type
   reference (the boundary test fails the build otherwise). Look at
  `Auth`/`Catalog`/`Settings`/`History`/`Memory`/`Issues`/`Files` for the shape: small,
  frontend-facing, `context`-aware when it may block.
2. Implement it in `internal/host/local/` (e.g. `local.go`, `files.go`),
  wrapping the real backend package. This package is the seam that is allowed
  to import both `internal/host` and the backend packages; keep new
  implementations here unless there's a reason to add another implementation
  package.
3. Consume it from `internal/tui` through `Model.services` (or pass the
   specific sub-interface into a modal constructor, as `providerModal` and
   `modelModal` do) — never import the backend package directly from
   `internal/tui`.
4. This is also the staging mechanism from the "why a host-services seam"
   section above: steps 1–2 alone (add the service, implement it, no TUI call
   site) leave `go build`/`go vet`/`go test ./...` green with zero visible
   change — the frontend wiring is a separate, later step.

### Add a theme token

1. Add the field to `theme.Theme` in `internal/tui/theme/theme.go` — an
   `lipgloss.AdaptiveColor` for a color role, a `lipgloss.TerminalColor` for
   the application background, a glyph on `theme.Icons`, or an appropriate
   border/spacing token — and give `Default()`/`DefaultIcons()` a value.
2. If most views will read it, add a precomputed field to `theme.Styles` and
   set it in `(Theme).S()` (`internal/tui/theme/styles.go`), so call sites
   write `th.S().YourField` instead of repeating
   `lipgloss.NewStyle().Foreground(th.YourField)`.
3. Resolve a supplied theme with `th = th.Resolve()` before consuming its
   fields. `Theme.Resolve` supplies every unset token from `Default()` while
   preserving `theme.NoBackground()` as the explicit transparent-background
   opt-out.
4. Never hardcode a color, glyph, border, spacing value, or emphasis modifier
   in a root view or component — every visual choice traces back to a resolved
   theme token, which is what makes a future palette or glyph swap a one-file
   edit.

### Dynamic agent-state coloring

Live session/agent status chrome uses `theme.AgentState` and the token map in
`internal/tui/theme/agent_state.go` (`Ready`→`Success`, `Working`→`AccentAlt`,
`Attention`→`Warning`, `Error`→`Error`, reserved `Dead`→`TextMuted`). The TUI
reduces protocol events into that state in `applyAgentStateEvent` /
`agentState` — views must not invent status from modal types. Multi-agent
tree nodes should reuse the same mapping when M5 lands; do not add a second
palette.

## TUI theme and style boundary

`theme.Theme.Resolve` is the runtime normalization point for partial themes.
It fills unset colors, icons, border style, and spacing from the stock theme;
`Background` is a `lipgloss.TerminalColor`, with `theme.NoBackground()` the
only explicit transparent value. `(Theme).S()` derives the shared semantic
styles from those resolved roles.

Root TUI views compose completed `theme.Styles` and `ui` components, then do
only structural work such as concatenation, joining, line selection, and
width/height allocation. Widget setup receives the resolved theme's foreground
and cursor/spinner styles but does not own a background. `ui.Canvas` owns the
application background: it is the final full-screen operation after the root
view and any overlay have composed, and paints every terminal cell unless the
theme explicitly selected `NoBackground`.

Review visual changes with this checklist:

- Root views use only completed theme styles/components plus structural ops.
- Every `ui` visual-modifier argument traces to a resolved theme value.
- Unknown or interprocedural modifier origins are reviewed manually.
- New colors, glyphs, borders, spacing, and emphasis live in `theme`.
