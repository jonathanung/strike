# Config

`~/.strike/config` (global) merged with `./.strike/config` (project), both
JSON:

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-5",
  "effort": "high",
  "defaultAgent": "build",
  "permissions": [
    { "permission": "bash", "pattern": "go *", "action": "allow" },
    { "permission": "write", "pattern": "**/*.env", "action": "deny" }
  ]
}
```

Rules concatenate across layers; the last matching rule wins, so project
config overrides global, and session "always" grants override both.

**ctrl+d saves defaults**: on the main screen it persists the current
provider/model/agent/effort to `~/.strike/config`; in the provider picker it
saves the highlighted provider; in the model picker it saves provider + model;
in the effort picker it saves the highlighted level.

## Reasoning effort

`/effort` sets how much internal reasoning the model spends before answering.
The ladder is normalized across vendors and each adapter maps it to its own
wire fields — Anthropic to adaptive thinking plus `output_config.effort`, the
OpenAI family to a `reasoning_effort` string. With no level set, strike sends
no reasoning fields at all and each provider's own default applies.

The two ends of the ladder are requests, not guarantees, because the vendor
ladders differ in length: `off` disables thinking outright on Anthropic but
floors at `minimal` on the OpenAI family (which has no zero setting), and
`xhigh`/`max` clamp down to `high` there for the same reason.

| Level | Meaning |
|---|---|
| `off` | least reasoning the provider allows — fastest and cheapest |
| `low` | minimal reasoning for short, scoped tasks |
| `medium` | balanced reasoning for routine work |
| `high` | thorough reasoning — the provider default |
| `xhigh` | deeper reasoning, best for coding and agentic work |
| `max` | maximum reasoning when correctness beats cost |

Agents and skills (including permission rules on agents): [agents-skills.md](agents-skills.md).
