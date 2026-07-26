# strike-cli

An agentic coding TUI in Go/Bubble Tea. The engine emits protocol events; the
TUI consumes them. Sessions are JSONL event logs.

Architecture is informed by deep-dives into
[opencode](https://github.com/sst/opencode) and
[codex](https://github.com/openai/codex). Full package map and dependency
rules: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Install

```sh
curl -fsSL https://strike.jonathanung.ca/install | bash
```

That URL redirects to the install script on GitHub; binaries come from
[GitHub Releases](https://github.com/jonathanung/strike-cli/releases). Details,
PATH, upgrade, and uninstall: [docs/install.md](docs/install.md).

```sh
strike version
strike --upgrade    # or /upgrade in the TUI
```

## Quickstart (from source)

Requires Go 1.26+.

```sh
make setup          # one-time: ~/.strike (config + example agent/skill)
make build          # builds ./strike (stamps version via git describe)
make run-echo       # offline dev loop — no API key
make run            # real agent with your configured provider
```

```sh
export ANTHROPIC_API_KEY=sk-ant-…   # or: ./strike auth login anthropic
./strike                            # or: --provider <p> --model <m> --effort <level>
./strike --continue                 # resume last root session
./strike exec "summarize this repo" # headless one-shot → stdout
```

In the TUI: `/provider`, `/model`, `/auth`, `/theme`, `/session`, `/init`,
`/help`, `/upgrade`. `/init` bootstraps project `AGENTS.md` (confirms before
overwrite). Enter sends; Shift+Enter newline; `esc` interrupts; `ctrl+t` jumps
to latest output; `ctrl+c` quits. `@path` attaches project files. See
[docs/keybinds.md](docs/keybinds.md) and [docs/usage.md](docs/usage.md).

## Docs

| Topic | |
|---|---|
| [Install & build](docs/install.md) | curl install, releases, upgrade, make targets |
| [Usage](docs/usage.md) | slash commands, `@file`, resume/fork, panes |
| [Keybinds](docs/keybinds.md) | keyboard reference (`f1` / `/keys` in-app) |
| [Auth & providers](docs/auth.md) | credentials, OAuth, billing routing |
| [Config](docs/config.md) | JSON, permissions, custom providers, `vimMode` |
| [Agents & skills](docs/agents-skills.md) | personas, skills, workflows / autonomy |
| [Architecture](docs/ARCHITECTURE.md) | packages, seams, recipes |
| [Contributing](docs/contributing.md) | layout, verification, doc check |
