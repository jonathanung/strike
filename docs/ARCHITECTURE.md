# Architecture

Reference for agents modifying strike-cli. States what exists in the code;
verify against source before relying on a detail here, since this file can
drift.

## Dataflow

```
cmd/strike session_lifecycle.go (`run`) — composition root
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
│     Onboarding, History, Memory, Issues, Agents, Skills}, then attaches
│     host.Files (local.NewFiles(workDir)) for frontend file reads and
│     diff-viewer apply writes (ApplyEdit / ApplyPatch)
│
└── tui.New(eng.Ops(), events, services, tui.Options{...})
      internal/tui's entire view of the world: two protocol channels plus
      one host.Services bundle. Nothing else crosses the boundary.
```

The engine never touches a terminal and the TUI never touches a model API,
tool, or credential directly — `pkg/protocol` (re-exported as
`internal/protocol` for in-tree compatibility) is the only channel between
them, and `internal/host` is the only channel from the TUI to anything
host-side. A session transcript is fully reconstructable by re-reading its
JSONL log, since the log is a serialized copy of the exact event stream the
TUI rendered from (see `pkg/protocol/codec.go`).

## Packages

| Package | Role | May import |
|---|---|---|
| `cmd/strike` | CLI entry (`main.go`) + composition root (`wire.go` stub; `assemble_tools.go`, `session_lifecycle.go`, `exec.go`, `rpc.go`, `acp.go`, `serve.go`, `multiroot.go`) | anything — the only package that wires the whole tree |
| `internal/rpc` | Stdio JSON-RPC 2.0 bridge for Op/Event (`strike rpc`: NDJSON ops in, event envelopes out) | `protocol`, stdlib |
| `internal/acp` | Agent Client Protocol (ACP) agent adapter (`strike acp`: session/prompt/tool-call ↔ Op/Event) | `protocol`, stdlib |
| `internal/server` | Experimental read-only HTTP attach: `/health`, SSE session event tail, minimal attach page (`strike serve`) | `session`, `version`, `protocol` (via session JSONL), stdlib |
| `internal/version` | Build-time Version/Commit stamped via `-ldflags` | stdlib |
| `internal/update` | GitHub Releases self-update (check, download, sha256, atomic replace, re-exec) | `version`, stdlib, net/http |
| `pkg/protocol` | **Public** Op/Event wire schema between engine and frontends; JSONL envelopes (`codec.go` / `op_codec.go`) are the session persistence + transport format (includes harness/verification events and `scheduler.queued` / `admitted` / `canceled`). Semver via `Version`; unknown event types decode as `UnknownEvent` (forward-compat). Consumer contract: [protocol.md](protocol.md) | stdlib only |
| `pkg/redact` | **Public** shared credential-shaped string scrubbing (`String`, `ScrubToolOutput`, `JSON`, `Error`) for exports, inspect, timeline traces, diagnostic bundles, and engine tool I/O | stdlib only |
| `pkg/timeline` | **Public** structured run timeline builder + versioned redacted JSON/JSONL export derived from protocol events (complements session JSONL and #774 roster/budget; not a second transcript). Storage bounds (#810): preview caps, optional content-addressed blob spill (`blob:sha256:` refs, no fsync), in-memory entry prune, Observe latency metrics, dir retention helpers for traces trees | `pkg/protocol`, `pkg/redact`, stdlib |
| `pkg/diag` | **Public** prompt/config diagnostic bundle builder + versioned redacted JSON export (layer map, effective dials, digests; complements `/context` and timeline) | `pkg/protocol`, `pkg/redact`, stdlib |
| `pkg/sdk` | **Public** thin Go client over `pkg/protocol`: in-process channel client, JSONL encode/decode, `RunTurn`, session JSONL replay. Does not embed the engine (engine stays `internal/`). Consumer docs: [sdk.md](sdk.md) | `pkg/protocol`, stdlib only |
| `internal/protocol` | Compatibility re-export of `pkg/protocol` (type aliases + thin forwards). Prefer `pkg/protocol` for new code | `pkg/protocol` only |
| `internal/engine` | Headless agent runtime: built-in turn loop, task-subagent function harnesses, tool dispatch, permission/question integration, deferred agent switch; implicit session-scoped agent **team** (lead + children roster + shared task board + patch collaboration board + delegation lifecycle + path ownership/overlap in `team.go` / `team_board.go` / `team_patch.go` / `delegation.go` / `ownership.go`); model-stream and bash admission via shared `scheduler`; scrubs tool I/O via `secret`/`pkg/redact`; emits `artifact.updated` on typed artifact mutations and `ledger.updated` on decision-ledger mutations; auto-loads active ledger slice into system prompt | `protocol`, `provider`, `harness`, `tool`, `permission`, `question`, `memory`, `artifact`, `ledger`, `config`, `sandbox`, `scheduler`, `secret`, `pkg/redact` |
| `internal/harness` | Function-harness contract and named function registry; model calls return completed responses | `provider`, stdlib |
| `internal/harness/external` | Private JSONL subprocess adapter from configured commands to `harness.Func` | `harness`, `provider`, stdlib, os/exec |
| `internal/provider` | LLM provider abstraction: `Provider` interface, normalized `StreamEvent`s | stdlib |
| `internal/provider/base` | Shared HTTP/JSON/SSE/auth client concrete adapters embed | `provider`, stdlib, net/http |
| `internal/provider/{anthropic,openaicompat,chatgpt,google,echo}` | Concrete adapters (openaicompat covers OpenAI platform API, xAI, Kimi, DeepSeek; chatgpt is the ChatGPT-subscription backend; google is Google AI Studio generateContent; echo is the offline dev provider) | `provider`, `provider/base` (all but echo), stdlib |
| `internal/sandbox` | OS-primitive process sandbox: `Wrap(argv, Policy)` via Linux `bwrap` / macOS `sandbox-exec`; Policy carries mode, write denials, `NoNetwork` (host net on by default), and optional `NetworkAllow` host/CIDR list (webfetch; shared shape for future container net); `Explain`/`ProfileText` for `/sandbox explain`; graceful degrade + startup warning when unavailable | stdlib only |
| `internal/scheduler` | Fair cancellable named-pool admission (process/build/test/model/container): context-aware acquire, atomic multi-pool leases, observer snapshots; layered limits + ordered command classification (`Compile` / `CompileWithPresets` → `Effective`); versioned build-system presets (`Catalog`, expand into ordinary limits/rules) | stdlib only |
| `internal/tool` | Tool contract (`Tool`, `Context`, `Result`, `CodedError`) + built-ins: read/glob/grep/edit/write/apply_patch/bash/task/task_status/task_read/task_message/task_interrupt/delegate/wait/agent_roster/agent_ownership/agent_message/agent_broadcast/agent_thread/team_task/patch_collab/webfetch/todowrite/todoread/memory_write/memory_read/issue_write/issue_read/plan_write/plan_read/plan_delegate/artifact_write/artifact_read/ledger_write/ledger_read/context_bundle/notebook_edit/sleep/skill/question/enter_plan_mode/exit_plan_mode/phase_done/toolsearch; FS tx safety (`FileState` freshness + optional `baseHash`, atomic temp+rename writes, `TurnDiff` create/update/delete); `PathOwnership` multi-agent path claims; `patch_collab` reuses apply_patch envelopes for submit/preview/reject/apply with path-overlap conflict detection; bash acquires scheduler pools after Ask; file tools call `FileSync` + `CollectDiagnostics` after mutations; plan tools use `RootSessionID` for ownership; `plan_delegate` correlates sections to task children with content CAS apply; artifact tools use session+root for owner/team access; ledger tools append/invalidate/supersede shared decisions; `context_bundle` reads sealed spawn context (goal/paths/artifacts/constraints); progressive `task` (create + get/status/message/wait/transition/cancel; compat shims: delegate/task_*/wait) accepts `context_bundle`; `RunProcess` checks `fault.ProcessAfterStart` for chaos tests | `provider` (for `ToolSchema`), `memory`, `issue`, `plan`, `artifact`, `ledger`, `sandbox`, `scheduler`, `fault`, stdlib |
| `internal/mcp` | MCP client (stdio + streamable HTTP) + session manager; bridges tools onto `tool.Registry` as `mcp_<server>_<tool>`; retry/disable; tools-only stdio **server** (`Server`) for `strike mcp-serve` | `tool`, stdlib, net/http |
| `internal/lsp` | LSP client (JSON-RPC 2.0 over stdio, Content-Length framing) + manager; extension→server registry; didOpen/didChange/didClose from file tools; collect `publishDiagnostics`; inject formatted diagnostics into file-tool Results (`CollectForPaths`); navigation + workspace `diagnostics` query tools (definition/references/symbols/diagnostics) for deferred tools; crash isolation | stdlib, os/exec |
| `internal/memory` | Project-scoped durable key/value memory (JSON under `~/.strike/memory/`) | stdlib |
| `internal/issue` | Project-scoped durable issues (JSON under `~/.strike/issues/`) | stdlib |
| `internal/goal` | Loop harness: goals, JSONL iterations/events, guards, critic, hooks | stdlib |
| `internal/plan` | Project-scoped root-session-owned structured plans (sections, lifecycle, CAS versions, section→child delegate correlation; JSON under `~/.strike/plans/`) | stdlib |
| `internal/artifact` | Shared typed multi-agent artifacts (findings/patch/test_report/contract/plan-blob) with CAS versions, owner|team access, project|session scope; JSON under `~/.strike/artifacts/`. Not memory (untyped KV), issues (tickets), or structured plans (use `internal/plan`) | stdlib |
| `internal/ledger` | Shared decision/assumption/constraint ledger (append/invalidate/supersede; history preserved); JSON under `~/.strike/ledger/`. Active slice auto-loads into system prompt; `ledger.updated` on mutations. Prefer over burying critical assumptions only in chat | stdlib |
| `internal/verify` | Independent completion gates (cmd/schema/path) and claim-vs-verified result type shared by delegation (#780) and solo/harness paths (#806); implementer cannot self-certify | stdlib |
| `internal/fault` | Test-only failure injection points (`Arm`/`Check`) for harness chaos tests (#808); unarmed `Check` is a no-op. See `docs/chaos.md` | stdlib only |
| `internal/question` | User-question ask service: suspends a tool call until `QuestionReply` (1–4 prompts per batch; TUI walks them, one reply with all answers) | `protocol`, stdlib |
| `internal/permission` | Ordered allow/ask/deny rulesets, last-match-wins; ask service; **Explain** (matched rule + layer); scoped grants with TTL; shipped presets (`read-only`/`dev`/`yolo-with-sandbox`); `permission.decided` audit events; `CompileSandbox` maps write/edit denials + webfetch/mcp network posture into `sandbox.Policy` | `protocol`, `tool` (for `AskRequest`), `sandbox`, stdlib |
| `internal/session` | JSONL event-log persistence (append/replay) + concurrent Manager (multi-session open, durable list, event mux). Sidecar `*.meta.json` stores `projectKey` (workspace folder) first for `/session` scoping. `Append` redacts via `secret.RedactEvent`, writes a complete line + fsync (`fault.SessionSync` injectable), and prefixes new logs with a `session.header` schema version. Replay skips trailing crash residue and fails closed on interior corruption or newer schema. Portable redacted export/import packages, `Fork`/`ForkAt` lineage (`meta.forkedFrom`), session retention (`ApplyRetention` / config `session.retention*`), and trace/run sidecar retention (`ApplyTraceRetention`, `ApplyRetentionWithSidecars`, config `session.traceRetention*` / `timeline*`) live here (#803/#810). Complementary: markdown `/export` (#221), checkpoint stack across `--continue` (#573) | `protocol`, `secret`, `fault`, `pkg/timeline`, stdlib |
| `internal/replay` | Offline eval harness (E3) + #791 recording + #782 multi-agent run snapshots + **#807 harness regression pack**: golden JSONL echo replay, tool-sequence diffs, prompt-regression metrics (`make prompt-reg`), harness eval themes correctness/safety/recovery/latency-cost (`make harness-eval`; CI report soft/non-blocking first; tests also exercise `sandbox`/`pkg/timeline`/`tool` contracts offline), versioned run recordings (settings digests, nondeterministic markers, handoff/gate/bundle fields), **RunSnapshot** (spawn+completion identity for delegated runs; session-scoped under `~/.strike/runs/`; complements JSONL, does not replace it), structured compare (`CompareRecordings` / `CompareRunSnapshots`), branch-from-event (session.ForkAt, no live side effects), offline `ReplayRunSnapshot` via echo. Composes with epic #459; does not own SWE-bench (#561) or chaos injection (#808) | `engine`, `session`, `protocol`, `provider`/`echo`, `tool`, `permission`, `secret`, `pkg/redact`, stdlib |
| `internal/secret` | Secret-ref env indirection (`ParseRef`/`Resolve`/`MergeEnv`) + `RedactEvent` for session JSONL; string scrubbing delegates to `pkg/redact` | `protocol`, `pkg/redact`, stdlib |
| `internal/auth` | Credential store (0600 `auth.json`) + OAuth/PKCE/device flows | stdlib, net/http |
| `internal/config` | Layered JSON config (defaults → global → project → managed/MDM) + agents/skills markdown loading; managed deny ceiling provenance on `Config.Managed` | `permission` (Ruleset is a config field), `protocol`, `sandbox` (sandbox dial parse), `scheduler` (limits + presets + command rules), stdlib |
| `internal/models` | models.dev catalog client, 24h cache with stale fallback | stdlib, net/http |
| `internal/history` | Project-scoped prompt history | stdlib |
| `internal/project` | Stable filesystem identity + optional per-session git worktrees under `.strike/worktrees/` | stdlib, os/exec |
| `internal/host` | **Frozen contract**: the services a frontend needs from its host process (includes `SchedulerPresets` catalog + global apply, `Permissions` explain/presets for `/permission`) | stdlib only — enforced by the boundary test |
| `internal/host/local` | Real `host.Services` implementation; wraps auth/config/models/history/memory/issue/plan/goal/files/mcp/scheduler presets for the frontend | `auth`, `config`, `history`, `host`, `issue`, `plan`, `goal`, `mcp`, `memory`, `models`, `sandbox`, `scheduler`, `tool` (composer `!` shell) |
| `internal/tui` | Bubble Tea frontend: app model, layout, cells, modals, composer. Sources under `_src/<group>/`, flattened by `go generate` | `protocol`, `host`, `tui/...` only (+ `pkg/redact` / `pkg/protocol`) — enforced by the boundary test |
| `internal/tui/theme` | Resolved design tokens: adaptive color roles, surfaces, chrome mode (soft\|solid\|bordered), terminal background, glyphs, border/spacing tokens, and precomputed styles | lipgloss, stdlib |
| `internal/tui/common` | Pure formatting helpers (ThemedSpace, DotJoin, compact durations) | `tui/theme`, stdlib |
| `internal/tui/term` | PTY + vt10x for embedded editors | stdlib + pty/vt10x |
| `internal/tui/ui` | Reusable component library (Panel, Dialog, Badge, KeyHints, StatusBar, List, Notice, Card/Bento, OverlayCenter/Scrim, Canvas, Logo) | stdlib, lipgloss, bubbles, charmbracelet/x/ansi, `tui/theme` |

## Dependency rules

Verbatim from the refactor spec (`.plan/refactor-agents-ui.md`):

- `internal/host` (contract pkg only, not local/): stdlib imports only.
- `internal/tui/...`: no `internal/*` imports except `protocol`, `host`,
  `tui/...`. (`pkg/protocol`, `pkg/redact` are also allowed — not under
  `internal/`.)
- No backend package imports `internal/tui/...`.
- `pkg/protocol`: stdlib only (public wire surface).
- `pkg/redact`: stdlib only (public scrubbing helper).
- `pkg/timeline`: stdlib + `pkg/protocol` + `pkg/redact` only.
- `pkg/diag`: stdlib + `pkg/protocol` + `pkg/redact` only.
- `pkg/sdk`: stdlib + `pkg/protocol` only (public client over the wire schema).

These are enforced mechanically, not just by convention: `internal/tui/boundary_test.go`
(`TestArchitectureBoundaries`) walks every non-test `.go` file in the module
with `go/parser` and fails, naming the offending file and import, on any
violation. Run it like any other test (`go test ./internal/tui/...`); there
is no way to silently cross the boundary.

## Cancellation, deadlines, and backpressure

Harness-wide cancel/deadline behavior (see also #794). Compose with the
scheduler pools — do not invent a second admission system.

### Cancel propagation

```
Interrupt op / Run parent ctx / turn deadline
        │
        ▼
  turnCtx cancel  ──► provider Stream select on ctx.Done (drain leftover)
        │
        ├── execToolCall: skip Execute or settle ToolCallEnd
        │     errorCode=canceled|timeout; partial stdout preserved + incomplete marker
        ▼
  tool.Execute(ctx) ──► bash RunProcess (process-group SIGKILL on unix)
                    ──► scheduler Acquire (waiter → scheduler.canceled)
                    ──► task_interrupt → child Ops Interrupt (child turn only)
```

| Path | Behavior |
|---|---|
| **User `interrupt`** | Cancels the **parent** turn only. Non-blocking children keep running until they finish or receive `task_interrupt`. |
| **`task_interrupt`** | Sends `Interrupt` to one owned child session; parent turn is unaffected. Child tools/processes observe the child turn ctx. |
| **Scheduler pool cancel** | Waiting `Acquire` returns `ctx.Err()`; engine emits `scheduler.canceled` (`reason` `canceled`\|`closed`). In-flight leases are released on tool/stream exit (defer), not force-revoked mid-critical-section. |
| **Bash / `RunProcess`** | Unix: own process group + `SIGKILL` on the group within `WaitDelay` (~2s) so grandchildren cannot hang `Wait`. Partial stdout/stderr retained; `ToolCallEnd.errorCode` = `canceled` or `timeout`. |
| **External harness** | Same process-group kill on cancel as `RunProcess` (unix). |
| **Provider stream** | `consumeStream` selects on `ctx.Done`, drains the stream in the background, ends the turn with `stopReason=interrupted` (user cancel) or `timeout` (turn deadline). |

### Deadlines

| Scope | Mechanism | Timeline / codes |
|---|---|---|
| Per-tool (bash) | `timeoutMs` (default 120s, max 600s); starts **after** scheduler admission | `process.exited` status `timeout`; `tool.end` `errorCode=timeout`, `IsError` |
| Per-tool mem/CPU | optional `ProcessSpec.Limits` (`RLIMIT_AS` / `RLIMIT_CPU` via prlimit) | **Linux only**; no-op elsewhere — see [isolation.md](isolation.md) |
| Per-tool OS sandbox deny | bwrap/seatbelt blocks classified after exit | `tool.end` `errorCode=sandbox_denied` + reason; timeline `errorCode` |
| Per-turn | `engine.Options.TurnTimeout` (zero = off) | `EngineError` code `timeout` + `turn.completed` `stopReason=timeout` |
| Provider HTTP | request ctx only (no separate client timeout on streaming adapters) | surfaces as stream/turn cancel |

### Backpressure (bounded queues)

| Queue | Capacity | Full behavior |
|---|---|---|
| `Ops` channel | 16 | **Block** sender (TUI/host should not spin unbounded ops). |
| `Events` channel | 256 | **Block** `emit`. `ToolCallBegin` is emitted from `Run` so `Interrupt` stays serviceable while the buffer is full. |
| Mid-turn user input | 32 (`maxPendingUserInputs`) | **Reject** with `EngineError` `code=queue_full` (does not block Ops). Survives Interrupt; drained FIFO when idle. |
| Scheduler waiters | unbounded waiter list per pool | Cancel via ctx → `scheduler.canceled`; capacity is the pool limit, not a second queue cap. |

Stable codes used here: `canceled`, `timeout`, `queue_full`, `sandbox_denied`
(`pkg/protocol` `ErrorCode*`). Broader tool contract codes (#793) share the same
vocabulary. Isolation layers: [isolation.md](isolation.md).

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
state. The registry holds right-pane windows: named session panes (`context` for
setup summary and `activity` for subagent status, recent parent tools, or an
empty-state line), an `agents` multi-root tree, a `visualizer` for the selected node's
status/tokens/cost/tokens-per-turn sparkline, a `files` explorer (lazy tree via
`host.Files.ListDir`), a `diagnostics` browser (live language-server findings
via `host.LSP`), `memory` and `issues` browsers, a `markdown` reader
opened via `/md-read`, an `editor` PTY window for `/vim`/`/nano`, and a `pets`
ASCII companion pane (`/pets`). Windows are organized into stack **groups**
(session: context+activity; agents: agents+visualizer; files: files+diagnostics;
project: memory+issues; singles: markdown/editor/pets).
When the right pane is large enough, multi-member groups render as a paired
split (vertical in a side column, horizontal when the body split is a bottom
bar); otherwise only the focused member is shown. Focus cycle walks members
within the active group, then the next group. The registry exposes the active
window index and has no close state or plugin mechanism. File bytes and
directory listings reach the markdown and files windows through `host.Files`,
not direct disk I/O from the TUI. Window input and resize updates stay inside
`internal/tui`: no protocol Op or Event was added for this pane infrastructure.
Composer input treats Enter as send and `ctrl+j` / Shift+Enter / Alt+Enter
as newline (Shift+Enter CSI normalizes to Alt+Enter; enhanced ctrl+j to
Alt+j via a stdin wrapper). Bare LF (`KeyCtrlJ`) is also newline. Pane focus is `ctrl+h`/`ctrl+l`
(primary vs secondary, orientation-independent); secondary-pane cycle is
`ctrl+o`/`ctrl+p`; stack-group cycle is `ctrl+shift+o`/`ctrl+shift+p`;
command palette is `ctrl+k` (when kill-to-end does not delete). Bare Escape
from CSI-u is normalized to `0x1b`.

`View()` composes the full-width header first. **Pre-first-prompt home**
(empty root transcript): a thin full-width context bar, centered STRIKE
wordmark + centered prompt box, optional recent-history line, and a
composer-oriented footer — no multi-pane split. After the first transcript
cell, the normal multi-pane layout takes over. That body is a horizontal
left|right split by default, or a vertical top/bottom split when
`splitOrientation` is vertical (`ctrl+;` or `/layout`). The left stack is
transcript, notice, completion, composer (in that order); then full-width
**context-sensitive** hints (composer vs right-pane nav) and the optional
danger banner. Stacked right-pane groups size sparse members (context,
system telemetry) to content and flex the remainder into activity (#680).
`ui.Canvas` is the final full-screen operation. With the default one-column
gutter, horizontal split mode begins at width 93: left is at least 60
columns, gutter is 1, and right is at least 32. In a horizontal split, the
canonical widths are `right = max(32, (width-gutter)/3)` and
`left = width-gutter-right`. At or below 92 columns, only the active pane
uses the full width. With a custom gutter `g`, the split threshold is
`60 + g + 32`. Vertical split keeps full width and divides body height when
there is room; focus/cycle chords stay fixed (not swapped by orientation).

Transcript anchoring: `refreshViewport` calls `GotoBottom` only when the
viewport was already `AtBottom` before `SetContent`; otherwise it restores
`YOffset`. Child lifecycle events (`ChildStarted` / `ChildCompleted`) update
activity-pane state and never append transcript cells; other child-correlated
events remain filtered except permissions and questions.

Color themes load from bundled JSON plus `~/.strike/themes` and
`./.strike/themes`; `/theme` opens a picker (or `/theme <id>` applies one)
and `config.theme` / ctrl+d persists the choice. Session-local appearance
(`/theme dark|light|auto`) is model state that feeds theme resolution; terminal
background comes from Bubble Tea `BackgroundColorMsg` (not a pre-program OSC 11
query). Forced dark/light override detection; auto uses the last detected bg.
Default chrome is **soft** (surface-filled rounded cards, Family-style);
themes may opt into `chrome: "solid"` or `chrome: "bordered"`. See [theme.md](theme.md).


## TUI file map

`internal/tui` is one Go package (shared unexported `Model` / `modal` / `window` /
`cell`). Go cannot split a package across directories, so sources are grouped
under `_src/<group>/` for traversability and flattened into `internal/tui/*.go`
by `go generate ./internal/tui` (`make test` / `make build` run this first).

| `_src` group | Concern |
|---|---|
| `app/` | Model, event apply, key routing, slash commands |
| `layout/` | View composition, splits, welcome, chrome |
| `modal/` | Overlay dialogs and pickers |
| `window/` | Right-pane windows and registry |
| `cell/` | Transcript cells and export |
| `input/` | Composer, completion, mouse, editor launch |
| `session/` | Session navigation |
| `hostui/` | MCP, project data, terminal notify, links |
| `util/` | Shims to `common/` |
| `test/` | Cross-cutting tests |

**Edit `_src/` only**, then `go generate ./internal/tui`. Flattened
`internal/tui/*.go` copies are gitignored and regenerated by make/CI; editing
them is silently discarded (`TestSrcFlattenInSync`). Independent real
packages: `theme/`, `ui/`, `term/`, `common/`.

Charm module paths (enforced by `TestCharmImportPaths` in
`_src/test/boundary_test.go`): v1 uses `github.com/charmbracelet/…`; v2 uses
the vanity domain `charm.land/…` (e.g. `charm.land/bubbletea/v2`). The path
`github.com/charmbracelet/…/v2` is rejected. When migrating to Charm v2,
update import path constants (including `lipglossPath` in
`style_boundary_test.go`) **in the same commit as the import rewrites**.


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
2. Wire construction into the `selectProvider` closure in `cmd/strike/assemble_tools.go`
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
   Prefer also implementing `Contract() tool.Contract` (side-effect class +
   idempotency; see `internal/tool/contract.go`). Registry helpers
   `Contract`/`Contracts` document these fields; tools without `Contract`
   default to `external` + `conditional`. The engine applies
   `tool.DecideRetry` (error code × idempotency) so only `safe-retry` tools
   auto-retry on `transient`/`timeout`; mutative tools never double-apply
   (`internal/tool/retry.go`, config `toolRetry`).
2. Register it in the `tool.NewRegistry(...)` call in `cmd/strike/assemble_tools.go`.
  3. If it mutates state or has side effects, call
     `tc.Ask(ctx, tool.AskRequest{Permission: "yourperm", Patterns: []string{...}})`
     inside `Execute`, and add a default rule for `"yourperm"` to
     `permission.Defaults()` in `internal/permission/permission.go` (Allow for
     read-only, Ask for anything mutating — see the existing defaults; reuse
     `edit`/`write`/`bash` when the new tool is the same class of action).
     Prefer structured failures via `tool.ErrInvalidArgs` /
     `tool.ErrPrecondition` / … so the engine can settle stable
     `protocol.ToolResultError` codes on `ToolCallEnd` and
     `provider.ToolResult.ErrorCode`.
  4. No `internal/tui` change is needed for a generic tool: tool calls render
    from `protocol.ToolCallBegin`/`ToolCallEnd` via `toolCell` in
    `internal/tui/cells.go` (name, title, output preview, ok/err glyph).
    Edit-shaped `Metadata` (`oldString`/`newString`) is consumed by the TUI
    via `ui.DiffPreview` in the permission modal and completed tool cells;
    from a selected tool cell, `a` confirms and re-applies the shown edit
    (or `apply_patch` envelope) into the active worktree through
    `host.Files.ApplyEdit` / `ApplyPatch`. Other tools can keep emitting
    metadata without a TUI change until a frontend renderer is added for them.

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
   `autonomy`, `auth`, `settings`, `agent`, `agents`, `activity`, `files`,
   `visualizer`, `system`, `telemetry`, `fast`, `vim`, `nano`, `md-read`,
   `theme`, `layout`, `split`, `compact`, `fork`, `undo`, `rewind`, `session`,
   `export`, `copy`, `help`, `keys`, `legend`, `memory`, `issues`, `goal`, `loop`,
   `workflow`, `context`,
   `effective-prompt`, `cost`, `upgrade`, `init`, `mcp`, `exit`, `quit`, and
   keybind-backed action mirrors such as `focus-left`, `palette`,
   `interrupt`, `agent-next`, `tool-copy`, `subagent`, `root-new`, …) are
   rejected by `config.ValidateSkillName` before they ever reach the frontend.
   See `keybindSlashPrimary` in `internal/tui/keybind_slash.go` for the full
   keybind→slash map. `/init` is a builtin that writes project
   `AGENTS.md` via `host.ProjectInit` (confirm before overwrite). `/ftue` opens
   the setup wizard (provider → model → optional init → feature tour →
   optional scheduler presets → first prompt) without writing settings on
   open. The feature tour is read-only, uses live keybind labels, and omits
   unavailable surfaces. Scheduler presets use `host.SchedulerPresets` (catalog
   + atomic global apply; custom limits/commands preserved). Finish/dismiss
   acknowledge `host.Onboarding` (`~/.strike/onboarding.json`) so interactive
   TUI auto-opens once for clean installs. PR URLs from successful `gh pr` bash
   output are stored via `protocol.SessionMeta` and `session` sidecar
   metadata. `/vim` embeds nvim/vim/nano in the right-pane `editor` window by
   default (PTY + vt10x via `internal/tui/term`). Config key `vimMode`
   selects `pane`/`embedded` (default), `overlay`/`modal` (large scrim
   popout), or `takeover` (full-screen `tea.ExecProcess`). `/nano` is the
   first-class nano command (same presentation via `nanoMode`; nano on PATH
   only). `/md-read` uses the same presentation vocabulary via `mdReadMode`
   (`embedded` default, or `modal`). `/vim` editor resolution: `$VISUAL` →
   `$EDITOR` → nvim/vim/vi/nano on PATH. GUI `$EDITOR` values always take over.
- **Builtin command (code).** Add a `commandSpec` to `builtinCommandSpecs`
  in `internal/tui/commands.go`, a `case "/yourcmd":` arm in
  `Model.handleCommand` (`internal/tui/command_dispatch.go`), and — if it's a primary
  action — a hint in `hintsView` (`internal/tui/view.go`).

## TUI source map (selected)

Same package `internal/tui`; split for reviewability only (no subpackages).

| File | Responsibility |
|---|---|
| `app.go` | `Model`, `New`/`Init`/`Update` switchboard |
| `apply_event.go` | protocol event → transcript cells / child activity / notices |
| `update_keys.go` | `handleKeyMsg` focus-aware key routing |
| `composer.go` | composer input, completion, history browse |
| `transcript_keys.go` | tool/file selection, cell copy, viewport refresh |
| `layout_app.go` | reflow, orientation, permission auto-approve arming |
| `app_state.go` | usage/context/agents/visualizer snapshots and notices |
| `render.go` | `View` / `renderFrame` / compact helpers |
| `commands.go` | slash command specs + completion metadata |
| `command_dispatch.go` | `handleCommand` and slash handler implementations |
| `cells.go` | transcript cell types and rendering helpers |
| `keymap.go` / `keys.go` | keybind table and binding ids |
| `keybind_slash.go` | keybind→slash registry + action mirrors |
| `session_nav.go` | session list / child transcript projection |
| `root_switch.go` | multi-root apply helpers |
| `view.go` | header/hints and non-root view fragments |

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
    `Auth`/`Catalog`/`Settings`/`History`/`Memory`/`Issues`/`Plans`/`Goals`/`Workflows`/`Files` for the shape: small,
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


## Filesystem transaction safety (tools)

Mutating file tools (`edit` / `write` / `apply_patch` / `notebook_edit`) share one
stack — do not fork a second file-state system:

| Layer | Package | Role |
|---|---|---|
| Freshness / base hash | `tool.FileState`, `CheckBaseHash`, `CheckContentUnchanged` | Per-agent read snapshots (mtime/size/hash); optional `baseHash` / `baseHashes` args fail closed with `precondition_failed` (#793 codes) |
| Atomic commit | `workspaceWriteFile` → `atomicWriteFile` | Same-dir temp + `rename` on local POSIX; refuses symlink leaves |
| Per-turn diff | `tool.TurnDiff` → `protocol.TurnCompleted.Files` | create/update/delete summary for timeline/UI + `/undo` path preview |
| Undo snapshots | `tool.CheckpointStore` (#540) | Pre-mutation bytes per turn for `/undo` restore; multi-file restore is path-sorted |
| Uncovered mutations | `CheckpointStore.MarkUncovered` → `TurnCompleted.Uncovered` / `SessionRewound.Uncovered` | bash (and similar) mark the turn so undo warns instead of silent full success (#801); full bash snapshot coverage remains #572 |
| Multi-agent overlap | `tool.PathOwnership` (#772) | Cross-agent path claims / leases; orthogonal to per-tool freshness |

`FileState` answers “did *this* agent re-read after an external change?”;
ownership answers “is another active worker claiming this path?”; checkpoints
answer “what bytes do we restore on undo?”. Checkpoint stack is process-lifetime
only today (`--continue` does not reload bytes — #573).

## Engine source map (selected)

Same package `internal/engine`; split for reviewability only.

| File | Responsibility |
|---|---|
| `engine.go` | `Engine`/`Options`, `New`, `Run`, begin/turn lifecycle glue |
| `ops.go` | `handleOp` and select/agent/effort/autonomy/permission-mode setters |
| `turn.go` | turn queue, `runTurn`, provider stream, tool exec, hooks, usage |
| `child.go` | subagent child sessions |
| `compaction.go` | history compaction |
| `restore.go` | session restore |
| `phase.go` | multi-phase workflows |
| `prompt.go` | system prompt assembly |


## Composition root source map (`cmd/strike`)

| File | Responsibility |
|---|---|
| `wire.go` | package doc only (composition root entrypoint names) |
| `assemble_tools.go` | `assemble`: providers, tools, MCP, hooks, host services |
| `session_lifecycle.go` | `run` / `runSession` / resume / worktree bind / `runExec` |
| `main.go` | flags, usage, subcommand dispatch |
| `exec.go` / `serve.go` / `mcp_serve.go` / `multiroot.go` / `auth.go` | already-split surfaces |

## TUI theme and style boundary

`theme.Theme.Resolve` is the runtime normalization point for partial themes.
It fills unset colors (including surfaces), chrome mode, icons, border style,
and spacing from the stock theme; `Background` is a `lipgloss.TerminalColor`,
with `theme.NoBackground()` the only explicit transparent value.
`(Theme).S()` derives the shared semantic styles from those resolved roles.
`ui.Canvas` preserves nested surface backgrounds and restores the application
background only after SGR clears.

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
