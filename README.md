# strike-cli

An agentic coding TUI in Go/Bubble Tea. The engine emits protocol events; the
TUI consumes them. Sessions are JSONL event logs.

Architecture is informed by deep-dives into
[opencode](https://github.com/sst/opencode) and
[codex](https://github.com/openai/codex). Full package map and dependency
rules: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quickstart

Requires Go 1.26+.

```sh
make setup          # one-time: ~/.strike (config + example agent/skill)
make build          # builds ./strike
make run-echo       # offline dev loop — no API key
make run            # real agent with your configured provider
```

```sh
export ANTHROPIC_API_KEY=sk-ant-…   # or: ./strike auth login anthropic
./strike                            # or: --provider <p> --model <m> --effort <level>
```

In the TUI: `/provider`, `/model`, `/auth`, `/help`. Enter sends; Shift+Enter
newline; `esc` interrupts; `ctrl+c` quits. See [docs/keybinds.md](docs/keybinds.md).

## Docs

| Topic | |
|---|---|
| [Install & build](docs/install.md) | setup, make targets, CLI flags |
| [Usage](docs/usage.md) | slash commands, UI layout, dashboard |
| [Keybinds](docs/keybinds.md) | keyboard reference |
| [Auth & providers](docs/auth.md) | credentials, OAuth, billing routing |
| [Config](docs/config.md) | layered JSON, permissions, effort |
| [Agents & skills](docs/agents-skills.md) | personas, prompt layers, skills |
| [Architecture](docs/ARCHITECTURE.md) | packages, seams, recipes |
| [Contributing](docs/contributing.md) | layout, verification, conventions |
