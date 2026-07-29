# Investigation: token inefficiency vs peer harnesses (#432)

| Field | Value |
| --- | --- |
| Issue | [jonathanung/strike#432](https://github.com/jonathanung/strike/issues/432) |
| Title | investigate token inefficiencies compared to other harnesses such as claude code and open code |
| Issue hypothesis | "unnecessary input of every single tool call" |
| Status | **Wave 1 complete** (prune, Anthropic cache, schemas, caps, compaction, deferTools). Continue under epic [#466](https://github.com/jonathanung/strike/issues/466) |
| Date | 2026-07-27 (inventory refresh 2026-07-28) |

## Verdict (primary root cause)

Strike re-sends the **full model-facing history on every provider `Stream`**, including **every past tool call and the full tool-result body**, until continuous prune and/or coarse whole-history compaction shrink that payload.

Peer harnesses (Claude Code, OpenCode) **continuously shrink or blank old tool results** (microcompact / prune) long before full compaction, and mark **prompt-cache breakpoints** so the stable system + tools prefix is cheap on cache hits.

**The issue hypothesis is correct about history replay of tool I/O.** It is **not** primarily about "invoking tools too often." Tool-loop frequency amplifies cost only because each round re-bills the entire accumulated tool I/O.

**Wave 1** shipped OpenCode-shaped prune, earlier threshold compaction, tighter produce-time caps, compact always-on schemas + slim guidance, optional `deferTools`, and Anthropic request-side `cache_control`. Remaining gaps are observability, non-Anthropic cache, and prune tunability — tracked as children of epic #466.

---

## How Strike builds each model request

### Turn loop: append-only history, full replay each stream

In `internal/engine/turn.go`, the default turn loop:

1. Appends the user message to `e.messages`.
2. Repeatedly:
   - runs tool-result prune (`maybePruneToolResults`) then optional threshold compaction;
   - calls `streamModel` → `consumeStream`;
   - appends the assistant message (text + tool calls);
   - for each tool call, appends the full tool-result message from `execToolCall`;
   - loops until the model returns no tool calls.

Critical request assembly in `consumeStream`:

```go
// internal/engine/turn.go (consumeStream)
stream, err := e.prov.Stream(ctx, provider.Request{
    Model:     e.model,
    System:    system,
    Messages:  e.messages,  // entire in-memory history (after prune)
    Tools:     tools,       // effectiveToolSchemas()
    MaxTokens: e.opts.MaxTokens,
    // ...
})
```

**Mitigated:** `maybePruneToolResults` blanks older tool-result bodies before `Stream` (see Wave 1 shipped). Coarse compaction remains the heavier whole-history rewrite.

### Compaction: threshold + overflow, whole-history

`internal/engine/compaction.go` defaults (config knobs in `docs/config.md`):

| Constant | Default | Role |
| --- | --- | --- |
| `defaultCompactionThreshold` | `0.70` | Fire auto-compact when estimated occupancy ≥ 70% of known window (`compactionThreshold`) |
| `defaultKeepUserTurns` | `2` | Preserve last 2 real user-turn starts (+ their tails) |
| `defaultCompactionBuffer` | `4096` | Output headroom so threshold fires before hard exhaustion |
| Strategies | `trim` \| `summarize` | Drop older messages to a marker, or replace with a model summary |

Manual `/compact` and threshold/overflow paths are live. Continuous prune runs **under** this ceiling so full compact is rarer and cheaper when it fires (tuned from historical ~0.80 in #435 / #444).

### Tool output caps: produce-time

Tools bound output when results are produced (defaults after #439 / #442):

| Tool / helper | Constant | Default |
| --- | --- | --- |
| bash | `bashMaxOutput` | 16_000 bytes |
| process (shared runner) | `processDefaultMaxOutput` | 16_000 bytes |
| read | `readDefaultLimit` / `readMaxLineLen` | 500 lines / 1000 chars |
| grep | `grepMaxMatches` | 100 matches |
| glob | `globMaxResults` | 100 paths |
| webfetch | `webfetchMaxOutputRunes` / `webfetchMaxBody` | 30_000 runes / 2 MiB download |

These caps prevent a single call from being unbounded. They do **not** shrink results already stored in model history — prune (#433) blanks older results. Tighter produce-time caps reduce **fresh** entry size between prune cycles.

### Tool schemas and guidance every request

- `effectiveToolSchemas()` (`internal/engine/prompt_tools.go`) sends registry tools minus hard-denied tools on **every** stream (including turn 1), with **compacted descriptions** (`tool.CompactSchemaDescription`: short purposes; skill keeps available-skills list). Full InputSchema is unchanged; `Registry.Schemas()` keeps full prose for `toolsearch`.
- Subsetting by agent/phase hard deny (explore/reviewer/plan posture) still drops tools the model cannot call.
- The same effective set feeds additive system-prompt guidance via `tool.BuildGuidance` (`internal/tool/guidance.go` / `prompt_tools.go`) — **usage policy / when-to-use only** (schemas own names/descriptions; catalog restatement removed in #437 / #443).
- Built-in surface is on the order of ~27 tools (read/glob/grep/edit/write/apply_patch/bash/task family/webfetch/todo/memory/issue/notebook/sleep/skill/question/plan mode/toolsearch/…).
- `toolsearch` (`internal/tool/toolsearch.go`) searches full registry schemas. With config `deferTools: on` (#438 / #445; **default off**), non-core/MCP tools are omitted from provider Tools until toolsearch discovers them (core coding tools stay always-on).

### Prompt cache

**Anthropic (shipped #434 / #440):** `internal/provider/anthropic/anthropic.go` attaches ephemeral `cache_control` breakpoints on the stable prefix — last system text block, last tool definition, and last eligible message content block (skips thinking / redacted_thinking). Response-side `cache_read` / `cache_creation` still map onto usage events.

**Other providers:** `openaicompat`, Google, and ChatGPT do **not** yet set deliberate request-side cache markers. Follow-up: epic child [#491](https://github.com/jonathanung/strike/issues/491).

### System prompt size (order of magnitude)

Embedded prompts under `internal/engine/prompt/`:

- `shared.txt` ≈ 3.4KB
- Full embedded prompt tree ≈ 12.6KB (shared + provider overlays + lean/plan variants, not all stacked at once)

Not the dominant cost versus multi-round tool-result accumulation, but part of the fixed per-request prefix. `/context` exposes system-prompt **layer** char breakdown; full request-slice attribution is still open ([#490](https://github.com/jonathanung/strike/issues/490)).

### Subagents: ruled out as primary cause

Child engines (`internal/engine/child.go`) are constructed **without** copying parent `InitialMessages` / parent history. Children start fresh with their own session, registry subset, and compaction opts. Parent-history duplication is **not** the primary token leak.

---

## Peer patterns (local `.plan/` reference trees)

Paths below are relative to the strike monorepo research checkout (`.plan/`), as documented in `docs/peer-ecosystem.md`. They are behavioral reference bars, not copy targets.

### Claude Code (`.plan/cc/`)

**Pre-API pipeline** in `src/query.ts` (order matters):

1. `applyToolResultBudget` — bound oversized individual results
2. snip (optional feature) — history snip before microcompact
3. **microcompact** — clear old compactable tool results
4. **autocompact** — full compaction when still over threshold

**Microcompact** (`src/services/compact/microCompact.ts`):

- `COMPACTABLE_TOOLS`: read, shell family, grep, glob, web_search/web_fetch, edit, write
- Clears older results to `[Old tool result content cleared]` (see `TIME_BASED_MC_CLEARED_MESSAGE`)
- Keeps a recent tail; time-based and cached-microcompact variants also exist

**Prompt cache** (`src/services/api/claude.ts`):

- Heavy use of `cache_control` / `getCacheControl` on system, tools, and message breakpoints so the stable prefix is reusable

**Deferred tools** (`src/utils/toolSearch.ts`, `src/Tool.ts`):

- MCP / searchable tools can be sent with `defer_loading: true` and discovered via tool search rather than full schema payload every turn

### OpenCode (`.plan/opencode/`)

**Continuous prune** (`packages/opencode/src/session/compaction.ts`):

| Constant | Value | Meaning |
| --- | --- | --- |
| `PRUNE_PROTECT` | `40_000` | Protect ~40k tokens of recent tool output walking backward |
| `PRUNE_MINIMUM` | `20_000` | Only prune if more than ~20k tokens would be freed |

Older tool parts are marked `time.compacted`; protected tools (e.g. `skill`) can be skipped.

**Render path** (`packages/opencode/src/session/message-v2.ts`):

- Compacted tool outputs render as `[Old tool result content cleared]` for the model projection

**Caching** (`packages/opencode/src/provider/transform.ts` — `applyCaching`):

- Ephemeral cache control on first system messages + last non-system messages (Anthropic, OpenRouter, Bedrock, OpenAI-compatible, Copilot, Alibaba variants)

**Context model** (`CONTEXT.md`):

- **Context Epoch** — stable system-context baseline for provider cache until compaction or incompatible transition
- **Model Tool Output** — bounded projection of tool results into session history (registry-enforced size), distinct from full host-side retention

### Prior Strike audit

`.plan/CORE_AGENT_RUNTIME_DISCREPANCIES.md` already flagged:

- Unbounded (until compact) model history growth
- Tool output caps ≠ compaction / history hygiene
- OpenCode-style prune as a reference bar for context pressure

**Update:** Wave 1 closed the "no prune / no Anthropic cache / late-only compact" gaps. Remaining peer delta is mostly **non-Anthropic cache**, **request-slice telemetry**, and **operator knobs** for prune — not absence of microcompact.

---

## Ranked causes

| Rank | Severity | Cause | Status |
| ---: | --- | --- | --- |
| 1 | **CRITICAL** | Full tool-result history retransmitted on every stream | **Mitigated** — continuous prune (`internal/engine/prune.go`, #433) |
| 2 | **HIGH** | No request-side prompt cache breakpoints | **Partial** — Anthropic shipped (#434 / #440); other providers open ([#491](https://github.com/jonathanung/strike/issues/491)) |
| 3 | **MEDIUM** | Coarse compaction late (~0.80) | **Mitigated** — default threshold 0.70 + config knobs (#435 / #444); prune under ceiling |
| 4 | **MEDIUM** | All tool schemas + guidance duplication every request | **Reduced** — compact descriptions (#441), slim guidance (#443), optional `deferTools` (#445) |
| 5 | **MEDIUM** | Large per-call caps accumulate | **Reduced** — produce-time defaults tightened (#439 / #442) |
| 6 | **LOW / ruled out as primary** | Subagent parent-history duplication; "calling tools too often" alone | Unchanged |

---

## Illustrative cost model (simple math)

Assume 10 tool rounds, each producing a **5KB** tool result (well under bash's produce-time cap). Ignore system/tools fixed cost for a moment.

**Strike (no prune):** each stream re-sends all prior tool results:

| After round | Tool-result bytes in this request |
| ---: | ---: |
| 1 | 5 KB |
| 2 | 10 KB |
| 3 | 15 KB |
| … | … |
| 10 | 50 KB |

Cumulative tool-result input billed across the 10 streams:

\[
5 + 10 + 15 + \ldots + 50 = 275 \text{ KB}
\]

(plus system prompt + tool schemas on **every** stream).

**With OpenCode-shaped prune** (protect ~40k tokens ≈ rough order of ~160KB of recent tool text, blank older):

- Early rounds similar
- Once protect budget is full, older results become a short placeholder
- Curve flattens; cumulative re-billing of ancient tool I/O stops

Claude Code's microcompact is the same economic idea with a tool-allowlist and keep-N / time-based variants.

Real billing also depends on tokenizer, cache hits, and whether the provider charges cached input at a discount — which is why peer `cache_control` / Context Epoch design compounds the prune win.

---

## What is NOT broken

- **Tool loop correctness** — assistant tool_use + tool_result pairing in `turn.go` is sound.
- **Per-tool output caps** — exist at produce time (bash/read/grep/glob/webfetch/…).
- **Coarse compaction** — trim/summarize + threshold/overflow paths exist and work when the context window is known.
- **Continuous prune** — OpenCode-shaped blanking of old tool results before each stream.
- **Anthropic prompt cache** — request-side ephemeral breakpoints + response usage mapping.
- **Child agents** — start fresh sessions; they do not duplicate parent history.
- **Issue framing** — "unnecessary input of every single tool call" correctly points at **replay of tool I/O in history**, not a spurious extra tool invocation bug.

---

## Wave 1 shipped (closed under #432 and follow-ups)

| Work | Issue / PR | Notes |
| --- | --- | --- |
| Investigation | #432 | This document |
| Tool-result prune / microcompact | #433 | `internal/engine/prune.go`; protect 40k / minimum 20k / keep 2 user turns; protect `skill` |
| Anthropic `cache_control` | #434 / #440 | System + last tool + last eligible message block |
| Compact always-on schema descriptions | #436 / #441 | `tool.CompactSchemaDescription` in effective schemas |
| Slim tool guidance | #437 / #443 | Additive tips only; no catalog restatement |
| Produce-time output caps | #439 / #442 | bash/process/read/grep/glob/webfetch defaults |
| Earlier auto-compaction + knobs | #435 / #444 | threshold 0.70; `compactionThreshold` / `compactionBuffer` / `keepUserTurns` |
| Deferred tool schemas via toolsearch | #438 / #445 | config `deferTools: on\|off` (default **off**) |

### Prune behavior (reference)

Wired before each provider stream in `internal/engine/turn.go` (default + harness loops):

- OpenCode-shaped constants: `PRUNE_PROTECT=40_000`, `PRUNE_MINIMUM=20_000` (hardcoded; config knobs → [#492](https://github.com/jonathanung/strike/issues/492))
- Walk backward; skip tool results inside the last 2 real user turns; protect ~40k tokens of newer eligible tool output; blank older bodies to `[Old tool result content cleared]` when savings exceed 20k tokens
- Preserves tool_use/tool_result pairing; protects `skill` tool output; skips already-cleared results
- In-memory model-facing history only (JSONL restore still has full outputs; prune re-applies on subsequent streams)

---

## Remaining work (epic #466)

Parent: [Epic #466](https://github.com/jonathanung/strike/issues/466) — **continue** token efficiency. Epic stays open; **child issues own implementation PRs** (`Fixes #<child>`, not `Fixes #466`).

| Priority | Child | Summary | Tier hint |
| ---: | --- | --- | --- |
| 1 (implement first) | [#490](https://github.com/jonathanung/strike/issues/490) | Request token attribution — system / tools / messages / tool_result slices; surface in `/cost` and/or `/context` | B (C if protocol wire changes) |
| 2 | [#491](https://github.com/jonathanung/strike/issues/491) | Cross-provider prompt-cache breakpoints (non-Anthropic paths with real API support) | C |
| 3 | [#492](https://github.com/jonathanung/strike/issues/492) | Configurable prune knobs (`pruneProtect` / `pruneMinimum` / keep turns / protect tools) | B |

### Optional later (not filed)

- Flip default `deferTools` to `on` after more field evidence (MCP-heavy sessions).
- MCP / registry schema budget beyond defer + compact descriptions.
- Time-based or tool-allowlist microcompact variants (Claude Code) if OpenCode-shaped prune proves insufficient.

### Non-goals for #466 children

- Replacing the turn loop or tool-calling protocol
- Perfect cross-provider tokenizer parity
- Billing product / invoices
- Closing #466 from a docs-only PR

---

## Evidence index

### Strike

| Topic | Path |
| --- | --- |
| Turn loop + history append | `internal/engine/turn.go` |
| Full `Messages: e.messages` on Stream | `internal/engine/turn.go` (`consumeStream`) |
| Compaction defaults / strategies | `internal/engine/compaction.go` |
| Tool-result prune | `internal/engine/prune.go` |
| Effective tool schemas + guidance layer | `internal/engine/prompt_tools.go`, `internal/tool/guidance.go` |
| Deferred tools | `internal/tool/defer.go`, config `deferTools` |
| Anthropic prompt cache + usage | `internal/provider/anthropic/anthropic.go` |
| Bash / read caps | `internal/tool/bash.go`, `internal/tool/read.go` |
| toolsearch | `internal/tool/toolsearch.go` |
| Child engines (no parent history copy) | `internal/engine/child.go` |
| Embedded prompts | `internal/engine/prompt/` (`shared.txt` ~3.4KB; tree ~12.6KB) |
| Config (compaction, deferTools) | `docs/config.md` |

### Peers (`.plan/`)

| Topic | Path |
| --- | --- |
| CC query pipeline | `.plan/cc/src/query.ts` |
| CC microcompact | `.plan/cc/src/services/compact/microCompact.ts` |
| CC cache_control | `.plan/cc/src/services/api/claude.ts` |
| CC defer_loading / tool search | `.plan/cc/src/utils/toolSearch.ts`, `.plan/cc/src/Tool.ts` |
| OpenCode prune constants | `.plan/opencode/packages/opencode/src/session/compaction.ts` |
| OpenCode cleared tool render | `.plan/opencode/packages/opencode/src/session/message-v2.ts` |
| OpenCode applyCaching | `.plan/opencode/packages/opencode/src/provider/transform.ts` |
| OpenCode context vocabulary | `.plan/opencode/CONTEXT.md` |
| Prior Strike runtime audit | `.plan/CORE_AGENT_RUNTIME_DISCREPANCIES.md` |

---

## Conclusion

Token inefficiency vs Claude Code / OpenCode was dominated by **full retransmission of historical tool results on every model stream**, with **late-only compaction** and **no engineered prompt-cache breakpoints**. Wave 1 addressed the primary mechanism (prune), Anthropic cache, schema/guidance weight, produce-time caps, earlier compaction, and optional schema deferral.

**Continue under epic #466:** make costs **observable** ([#490](https://github.com/jonathanung/strike/issues/490)), extend cache discipline beyond Anthropic ([#491](https://github.com/jonathanung/strike/issues/491)), and let operators **tune prune** without rebuilds ([#492](https://github.com/jonathanung/strike/issues/492)).
