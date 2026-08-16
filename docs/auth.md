# Auth & providers

Credentials live in `~/.strike/auth.json` (0600). Environment
variables (`ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `XAI_API_KEY` /
`GEMINI_API_KEY` or `GOOGLE_API_KEY` / `KIMI_API_KEY` / `DEEPSEEK_API_KEY`)
always take precedence over stored credentials.

The Google AI Studio provider id is **`google`** (model ids stay `gemini-*`).
The shipped name **`gemini`** is accepted as an alias for login, logout,
config `provider`, and `--provider`, and is stored/listed as `google`.

## OpenAI billing routing

A ChatGPT OAuth login routes requests to the ChatGPT backend
(`chatgpt.com/backend-api/codex`, Responses API, streamed) — billed to your
ChatGPT Plus/Pro subscription, using the access token + `ChatGPT-Account-Id`
header. An explicit API key (`OPENAI_API_KEY` or `/auth openai key`) routes to
`api.openai.com` instead — billed to your platform API account. Subscription
mode supports the Codex model set (`gpt-5.5`, `gpt-5.4`, …), not `-pro`
models.

## Login

Log in either inside the TUI (`/auth <provider>`, see [usage.md](usage.md)) or
from the shell:

```sh
strike auth login openai            # OAuth "Sign in with ChatGPT" (browser);
                                    # also exchanges the id_token for a real
                                    # API key usable on api.openai.com
strike auth login xai               # xAI Grok OAuth (browser, PKCE)
strike auth login xai --device      # RFC 8628 device flow for headless/SSH
strike auth login google --api-key  # Google AI Studio (paste API key; alias: gemini)
strike auth login kimi --api-key    # Kimi (paste API key)
strike auth login deepseek --api-key # DeepSeek (paste API key)
strike auth login <provider> --api-key   # paste a key instead (any provider)
strike auth status
strike auth logout <provider>
```

Both OAuth integrations reuse public CLI client registrations (Codex CLI's
for OpenAI, Grok-CLI's for xAI — the same approach opencode ships), so the
loopback callback ports are fixed: `localhost:1455` for OpenAI,
`127.0.0.1:56121` for xAI. OAuth access tokens auto-refresh ~2 minutes
before expiry, and rotated refresh tokens are persisted.

Provider selection happens in-app with `/provider`; `--provider` on the
command line just pre-selects (and validates credentials eagerly). Defaults
when a provider is chosen without a model: `claude-sonnet-5` (Anthropic),
`gpt-5.5` (OpenAI), `grok-4.5` (xAI), `gemini-2.5-pro` (Google),
`moonshot-v1` (Kimi), `deepseek-chat` (DeepSeek).

Custom/self-hosted providers (`.strike/providers.jsonc` or `/settings`) use
env refs (`{env:NAME}`, `$NAME`) and/or a stored API key. Logging out of a
**custom** provider deletes its definition and credentials; built-in logout
only clears credentials. Details: [config.md](config.md#custom-providers).

## Egress redaction and secret refs

Session JSONL, `/export`, tool results, and diagnostic paths scrub
credential-shaped spans via `internal/trust/secret`. Tools may hold
`secret://env/NAME` refs resolved only at exec time. Full policy (what is
redacted vs preserved, bash env injection): [secrets.md](secrets.md).
