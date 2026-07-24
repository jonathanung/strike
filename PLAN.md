# strike-cli — Architecture & Plan

An agentic coding TUI in Go/BubbleTea. Design synthesized from deep-dives into
**opencode** (sst/opencode: TS core + client/server split) and **codex**
(openai/codex: Rust core + ratatui TUI). This doc records the decisions and the
build order; the scaffold in this repo implements Phase 0.

## Core architectural decisions

### 1. Engine/UI split behind an Op/Event protocol — from day one, in-process

Both references converge on the same seam:

- codex uses an SQ/EQ (submission queue / event queue) pattern: the TUI has
  *no dependency on the core engine crate*, only on protocol types. Even
  in-process, it runs the same message processor over bounded channels.
- opencode runs a full HTTP + SSE server; the TUI is a pure view over
  server-persisted state.

strike copies this: `internal/protocol` defines `Op` (client → engine:
`UserInput`, `PermissionReply`, `Interrupt`) and `Event` (engine → client:
`TextDelta`, `ToolCallBegin/End`, `PermissionAsked`, `TurnCompleted`, …).
The TUI and engine communicate **only** through two Go channels carrying these
types. Payoffs:

- The TUI can never reach into engine internals.
- A headless `strike exec` batch mode, a daemon/server mode, or an alternate
  frontend is a new transport, not a rewrite.
- **The event stream is the transcript**: persistence is a JSONL log of
  events (codex "rollout" pattern), so resume/replay/fork come nearly free.

### 2. Tool results separate "what the model sees" from "what the UI renders"

opencode's tool contract is `{title, output, metadata}` — `output` goes back
to the LLM (auto-truncated), `title` is the one-line transcript summary,
`metadata` is typed data the UI uses to render rich views (diffs, excerpts)
without re-parsing model-facing text. strike's `tool.Result` mirrors this.
Retrofitting this later is painful; it costs nothing now.

### 3. Permissions: ordered allow/ask/deny rulesets + suspend/resume asks

From opencode (the most complete model of the two):

- A ruleset is an ordered list of `{permission, pattern, action}`;
  **last matching rule wins**, layers concatenate (defaults → global config →
  project config → session "always" grants).
- "Ask" suspends the tool's goroutine on a channel; the TUI renders the prompt
  inline; the reply resolves it (`once` / `always` / `reject`).
- **Reject carries optional feedback** fed back to the model as the tool
  result, so the model course-corrects instead of just seeing "denied".
- "Always" grants add session rules and retroactively resolve matching
  pending asks; a reject cascades to sibling pending asks.

codex adds ideas for later: model-initiated escalation with a required
`justification`, prefix rules ("remember for `git *`"), and OS sandboxing
(seatbelt/landlock) — sandboxing is Phase 4+; the approval UX comes first.

### 4. One provider interface, normalized stream events

Everything downstream of the provider sees one event vocabulary
(`TextDelta | ToolCall | Done | Error`) regardless of vendor — opencode's
normalized `LLMEvent` union, codex's event mapping. First adapter: Anthropic
Messages API. An `echo` dev provider exercises the full loop (including tool
calls and permission prompts) offline.

### 5. TUI: cell transcript + bottom-pane modal stack

From codex's ratatui design, which maps cleanly onto BubbleTea:

- **Event loop**: one `Update` fed by terminal input and an engine-event
  channel command — the Go analogue of codex's four-way `tokio::select!`.
  Keep engine events (`protocol.Event`) and UI-internal messages as distinct
  message types.
- **History cells**: the transcript is an ordered list of self-rendering
  blocks (user text, assistant markdown, tool call, error) — one type per
  kind, finalized cells are cheap to re-render.
- **Bottom-pane modals**: anything that takes over input (permission prompt,
  pickers, palette) implements one small `modal` interface and swaps into the
  composer slot — codex's `BottomPaneView` trait. Esc always means cancel.
- **Streaming (later)**: stable/tail split — commit finalized markdown to
  scrollback once, redraw only the mutable tail; hold back reflow-prone
  structures (tables) until complete. Naive full re-render is fine for v1.
- **Theming**: one struct of semantic colors (text, muted, accent, error,
  diff-added/removed, border…), themes as data, lipgloss styles built from the
  struct — never hardcoded colors in views (opencode's palette contract).

## Minimal tool set (v1)

Both research reports independently land on the same core:

| Tool | Purpose | Permission |
|---|---|---|
| `read` | Read file with offset/limit, line numbers | allow (default) |
| `glob` | Filename pattern matching (`**` supported) | allow |
| `grep` | Regex content search | allow |
| `edit` | Exact string-replacement edit | ask |
| `write` | Create/overwrite file | ask |
| `bash` | Shell execution with timeout + output truncation | ask |

Near-term additions (v1.x): `todo`/plan tool, `task` (subagents), `webfetch`,
`apply_patch` (codex's fuzzy-anchored patch grammar — only worth it for
GPT-family models; edit/write suffice for Claude).

Failure shape matters: unknown tool or bad args returns a *correctable error
as the tool result* (opencode's `invalid` tool), never a crash.

## Extensibility model

The tool registry + permission ruleset + event bus are the extension surface.
Everything below layers on without touching the core loop:

1. **Config** (now): JSON, global (`~/.strike/strike.json`) merged
   with project (`./strike.json`); permission rules concatenate so project
   overrides global (last-match-wins does the merging for us).
2. **MCP client** (Phase 3): stdio + streamable-HTTP servers declared in
   config; discovered tools register into the same registry, gated by the
   same permission system, namespaced per server.
3. **Custom tools/commands** (Phase 3): config-declared executable tools
   (JSON schema in, `{title,output,metadata}` out over stdio) and prompt-
   template slash commands.
4. **Hooks** (Phase 4): named lifecycle events (PreToolUse, PostToolUse,
   PermissionRequest, SessionStart, PreCompact…) running config-declared
   commands — codex has 11 of these; the emit points already exist because
   everything flows through the event bus.
5. **Agents/modes** (Phase 4): named `{prompt, model, permission-ruleset,
   tool-visibility}` bundles (plan mode = read-only ruleset).

## Build phases

- **Phase 0 — scaffold (this repo, now)**: package layout, protocol types,
  tool interface + six core tools, permission service, engine turn loop,
  JSONL session store, config loading, BubbleTea shell (composer, transcript
  cells, permission modal, spinner/status), echo provider proving the full
  loop offline, Anthropic provider (non-streaming first). Auth:
  `strike auth` credential store (0600 auth.json) with API keys for
  anthropic/openai/xai, OpenAI ChatGPT OAuth (PKCE + id_token→API-key
  exchange, reusing the public Codex CLI client), xAI Grok OAuth (PKCE
  browser flow + RFC 8628 device flow, reusing the public Grok-CLI client),
  auto-refresh with rotated-token persistence, and an OpenAI-compatible
  chat-completions provider serving both openai and xai.
- **Phase 1 — real agent loop**: Anthropic SSE streaming, retries, token
  accounting, interrupt handling, markdown rendering (glamour), bash output
  live-tailing.
- **Phase 2 — editing UX**: diff rendering for edit/write (theme-driven
  added/removed colors, syntax highlighting), reject-with-feedback UI,
  session resume picker (replay JSONL), `strike exec` headless mode over the
  same protocol.
- **Phase 3 — extensibility**: MCP client, custom tools, slash commands,
  command palette, themes as data files.
- **Phase 4 — maturity**: compaction (protect recent-token tail, summarize
  the rest, cap tool outputs — opencode's recipe), subagents/`task`, hooks,
  OS sandboxing (seatbelt/landlock), second provider (OpenAI + apply_patch).

## Package map

```
cmd/strike/            entrypoint, wiring, flags
internal/protocol/     Op/Event types + JSONL envelope codec   ← the seam
internal/engine/       session state, turn loop, tool dispatch
internal/provider/     Provider interface, normalized stream events
internal/provider/anthropic/, internal/provider/echo/
internal/tool/         Tool interface, Result{Title,Output,Metadata}, registry,
                       read/write/edit/glob/grep/bash
internal/permission/   rulesets, evaluate (last-match-wins), ask service
internal/session/      JSONL event-log store (transcript = event log)
internal/config/       default + global + project config merge
internal/tui/          BubbleTea app: cells, modal stack, composer, status
internal/tui/theme/    semantic color contract
```
