# Threat model: prompt injection

Strike’s permission rules and OS sandbox decide **whether a tool may run** and
**what bash can touch**. They do **not** decide whether the model should trust
the text those tools return. Anything that becomes model-facing content can
steer later tool calls.

This page covers three injection surfaces that are in the product today:
workspace **file contents**, **MCP** servers, and **web fetch** results.
Isolation mechanics (sandbox × permission, worktrees, containers) live in
[isolation.md](isolation.md). Config dials: [config.md](config.md).

## Trust boundary

The engine concatenates system layers, conversation history, and tool results
into a provider request (`internal/engine` → `provider.Request`). The model
cannot tell a trusted instruction from untrusted data in that blob. Strike
does not run a separate “ignore injected instructions” filter on tool output.

| Control | What it does | What it does **not** do |
|---|---|---|
| **Permission rules** | Last-match-wins allow / ask / deny on tool name + pattern (or action facts) | Inspect tool **output**; stop the model from following injected text; constrain bash syscalls |
| **OS sandbox** | Bind/seatbelt what **bash** (and composer `!`) can read/write/net | Apply to `read` / `webfetch` / MCP; strip injection from fetched or read text |
| **Admission** ([admission.md](admission.md)) | Scan MCP / skills / plugins **at bind time** | Re-scan every tool result; prove a bound MCP server is benign |
| **Content guards** | Scan **outbound** file writes for credential shapes / dangerous sinks | Scan inbound file/MCP/web text for prompt injection |
| **Redaction** ([secrets.md](secrets.md)) | Scrub credential-shaped strings on logs and tool results | Remove attacker instructions |

`yolo` / `--auto` skip **asks**. `--dangerously-skip-permissions` skips asks
and also bypasses `network.allow`. Explicit **deny** rules still apply.
Skipping asks does not add injection defense.

## File contents

`read`, `grep`, and composer `@file` expansion put workspace bytes into the
model context. `@file` attaches file bodies on send (capped; binary/oversize
skipped); the transcript keeps the `@path` token. `read` defaults to 500 lines
and truncates long lines — size limits, not trust limits.

**Attack:** a repo file (README, issue template, `.env.example`, a
dependency’s docs, a pasted snippet) contains “ignore previous instructions
and …”. The model treats that as tasking and may call `bash`, `write`, or
`webfetch` accordingly.

**What Strike does:** path escape for `@file` is symlink-safe under the
project root. Permission rules can deny `read` of named globs. Tool-chain
correlation ([isolation.md](isolation.md#tool-chain-correlation-891)) can
**ask** even under `yolo` after a sensitive-class read followed by egress.

**What Strike does not do:** distinguish “user asked to read this” from
“untrusted file told the model to do something else.” Treat third-party and
generated files as untrusted input.

## MCP servers

Configured MCP servers register as `mcp_<server>_<tool>` (`internal/mcp`).
Each call asks permission `mcp` with pattern `server/tool`. Tool results are
model-facing text. Prompt and resource payloads are redacted and truncated
(`BoundText`, 1 MiB per field); `tools/call` text is secret-scrubbed on settle
but **not** instruction-filtered. Non-text MCP blocks become `[type content]`
placeholders.

**Attack:** a compromised, malicious, or prompt-injecting MCP server returns
instructions in a tool result (or a `prompts/get` body). The model follows
them on the next iteration.

**What Strike does:** admission may block or quarantine a server at load;
plugin MCP stays off until trusted; permission can deny `mcp` / `*`. Stdio
MCP is a local subprocess (not OS-sandboxed as bash is). HTTP MCP is a remote
endpoint you configured.

**What Strike does not do:** sandbox MCP process filesystem/network the way
bash is sandboxed; prove tool output is free of injection; treat MCP
descriptions as trusted just because they are in `tools[]`.

Prefer first-party or reviewed servers. Deny `mcp` `*` when the session does
not need them.

## Web fetch and search

`webfetch` downloads a URL (HTTPS upgrade, markdown/text/html, ~30k rune
output cap, 2 MiB download bound) and returns the body to the model.
`websearch` returns titles, URLs, and snippets from a configured backend
(Brave today). Both default to **ask**.

**Attack:** a page or search snippet contains injected instructions; the model
treats them as user/system intent (classic indirect prompt injection).

**What Strike does:** SSRF blocks for private/loopback/link-local/metadata
addresses (including redirects and DNS rebinding at dial). Optional
`network.allow` further restricts public hosts. Permission can deny
`webfetch` / `websearch` / `browser`. Output is truncated for tokens, not for trust.

**What Strike does not do:** render-strip scripts in a way that removes
natural-language injection; sandbox the fetch the way bash is sandboxed;
treat fetched markdown as a trusted system prompt.

Prefer `webfetch` or `browser` over `curl` in bash so SSRF and allowlist checks apply.
Fetched content is still untrusted.

## Operator posture

- Keep `sandbox` at `workspace-write` (or `read-only`). `yolo` + `sandbox: off`
  requires `--i-know` and removes both prompts and OS isolation.
- Use deny rules for tools the session must not have (`mcp`, `webfetch`, `browser`,
  `bash` forms), not as an injection scanner.
- Review MCP servers and `network.allow` the same way you review shell
  access.
- Assume the model may obey text from files, MCP, and the web. Permission
  asks and the sandbox are how you bound **actions**, not how you sanitize
  **context**.
