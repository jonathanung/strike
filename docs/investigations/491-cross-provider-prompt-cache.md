# Cross-provider prompt-cache breakpoints (#491)

Parent: epic #466. Anthropic request-side `cache_control` shipped in #434 / #440.
This note records which Strike adapters can set **request-side** cache controls
today, what landed for non-Anthropic paths, and when cache misses are expected.

## Provider matrix (Strike adapters)

| Adapter | Request-side control | Wire fields | Usage breakout mapped? |
| --- | --- | --- | --- |
| **anthropic** | Yes — engineered breakpoints | `cache_control: { "type": "ephemeral" }` on last system block, last tool, last eligible message content block (max 3 of Anthropic’s 4) | Yes — `cache_read_input_tokens` → `CacheReadTokens`, `cache_creation_input_tokens` → `CacheCreationTokens` |
| **openaicompat** (OpenAI platform, xAI, custom OpenAI-compat hosts) | Yes — stable session cache key | `prompt_cache_key` on chat-completions and Responses bodies | Yes — `prompt_tokens_details.cached_tokens` / `cache_write_tokens` (chat) and `input_tokens_details.*` (Responses) → `CacheReadTokens` / `CacheCreationTokens` |
| **chatgpt** (subscription Responses at chatgpt.com) | Yes — stable session cache key (Codex-shaped) | `prompt_cache_key` on body; `include: ["reasoning.encrypted_content"]`; **never** `prompt_cache_retention` (backend 400s) | Yes — `input_tokens_details.cached_tokens` / `cache_write_tokens` when present |
| **google** (Gemini `generateContent`) | No | Google’s explicit context-cache API is a different product (create/cachedContents + `cachedContent` name); not the same as per-request breakpoints | `usageMetadata` has no cache breakout in our wire types |
| **echo** | N/A | Offline test double | Synthetic usage only |

### Why openaicompat uses `prompt_cache_key` (not Anthropic-style `cache_control`)

- **OpenAI** automatic prompt caching is prefix-based for eligible models
  (`gpt-4o` and newer, prompts ≥ ~1024 tokens). Routing affinity improves when
  requests that share a long prefix also share
  [`prompt_cache_key`](https://platform.openai.com/docs/guides/prompt-caching).
  GPT-5.6+ adds optional `prompt_cache_breakpoint` / `prompt_cache_options`;
  those fields **400 on older models**, so Strike does not emit them yet.
- **xAI** documents the same `prompt_cache_key` on `/v1/chat/completions` and
  `/v1/responses` (sticky routing / `x-grok-conv-id`) and reports
  `prompt_tokens_details.cached_tokens`.
- OpenCode peers set `promptCacheKey` / `prompt_cache_key` to the session id for
  `@ai-sdk/openai`, `@ai-sdk/xai`, etc. Strike mirrors that: engine stamps
  `provider.Request.CacheKey` with `SessionID`.
- OpenRouter-shaped message-level `cache_control: { type: "ephemeral" }` is
  useful when a gateway forwards **Anthropic** models. Strike’s openaicompat
  path is OpenAI/xAI chat-completions and platform Responses — attaching
  Anthropic-only markers would be a pure no-op or a rejection risk. Skipped.

### Usage accounting (OpenAI/xAI subsets)

Vendors report **total** prompt/input tokens with cache counts as **subsets**,
not Anthropic-style additive parts. Engine occupancy is:

```text
used = InputTokens + CacheReadTokens + CacheCreationTokens + OutputTokens
```

So openaicompat normalizes:

```text
CacheReadTokens     = cached_tokens
CacheCreationTokens = cache_write_tokens   // 0 when vendor omits
InputTokens         = prompt/input − cached − write
```

Parts still sum to the vendor’s prompt+completion totals. Display/cost can
still surface cache hits via existing `UsageReported.cacheRead` /
`cacheCreation`.

## Tradeoffs / expected cache misses

| Change | Effect |
| --- | --- |
| System / agent rewrite | Prefix hash changes → miss (intentional) |
| Tool registry change (add/remove/rename tools, schema edit, `toolsearch` defer load) | Tools participate in the cached prefix → miss |
| Compaction / prune that rewrites earlier history | Shared prefix shortens or changes → partial or full miss until the new prefix is rewritten |
| New session id | New `prompt_cache_key` → no cross-session sticky routing (by design) |
| Short prompts (&lt; ~1024 tokens on OpenAI) | `cached_tokens` stays 0 even with a key |
| High RPM on one key | OpenAI may shed sticky routing (~15 rpm/key guidance); rare for interactive Strike sessions |
| Provider switch mid-session | Different host/model → independent caches |

### Why chatgpt also sends `prompt_cache_key`

Codex CLI and OpenClaw both attach `prompt_cache_key` (session id) and
`include: ["reasoning.encrypted_content"]` on
`chatgpt.com/backend-api/codex/responses`. Strike mirrors that so the
subscription backend gets the same sticky-routing + reasoning-resume shape.
Do **not** send `prompt_cache_retention` or platform-only `metadata` — peers
strip those because the ChatGPT backend rejects them.

## Non-goals (this issue)

- Re-implement Anthropic `cache_control` (already done).
- Fake markers on Google without a real wire contract.
- GPT-5.6-only `prompt_cache_breakpoint` (follow-up when Strike’s default
  OpenAI models require explicit mode).
- Changing prune or compaction behavior.

## Implementation map

| Piece | Location |
| --- | --- |
| Request field | `provider.Request.CacheKey` |
| Engine stamp | `internal/engine/turn.go`, `compaction.go` (`SessionID`) |
| Chat wire + usage | `internal/provider/openaicompat/openaicompat.go` |
| Responses wire + usage | `internal/provider/openaicompat/responses.go` |
| ChatGPT subscription wire + usage | `internal/provider/chatgpt/chatgpt.go` |
| Tests | `openaicompat_test.go`, `chatgpt_test.go` (`prompt_cache_key` golden body + usage tables) |
