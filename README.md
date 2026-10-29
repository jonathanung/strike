# strike

Website: https://strike.jonathanung.ca/

An agentic coding harness in Go. The engine emits protocol events; the web UI
is the primary client and the terminal UI is deprecated (`strike tui`).
Sessions are JSONL event logs.

Architecture is informed by deep-dives into
[opencode](https://github.com/sst/opencode) and
[codex](https://github.com/openai/codex). Full package map and dependency
rules: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

**Supported platforms:** macOS, Linux, and Windows via WSL2. Native Windows
is not supported in v1.

## Install

```sh
curl -fsSL https://strike.jonathanung.ca/install | bash
```

That URL redirects to the install script on GitHub; binaries come from
[GitHub Releases](https://github.com/jonathanung/strike/releases). Details,
PATH, upgrade, and uninstall: [docs/install.md](docs/install.md).

```sh
strike version
strike --upgrade    # or /upgrade in the TUI
strike daemon install --start
```

`strike daemon install` registers a per-user daemon and prints the matching
start commands. `--start` runs them. Linux writes a systemd user unit under
`~/.config/systemd/user/` (`$XDG_CONFIG_HOME` when set). macOS writes
`~/Library/LaunchAgents/ca.jonathanung.strike-daemon.plist` and uses
`launchctl`. Native Windows is unsupported; run `strike serve` inside WSL2.
The API token is `~/.strike/daemon.token` (mode 0600). Then open
http://127.0.0.1:8787/.

## Quickstart (from source)

Requires Go 1.26+.

```sh
make setup          # one-time: ~/.strike (config + example agent/skill)
make restore        # repair missing/corrupt ~/.strike (or: strike restore)
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

### Nix dev shell

Requires Nix with flakes enabled. Pins the exact Go 1.26, gopls, make, and git.

```sh
nix profile install github:jonathanung/strike
# or run without installing:
nix run github:jonathanung/strike

# development:
nix develop
make build
```

Details: [docs/nix.md](docs/nix.md).

In the TUI: `/provider`, `/model`, `/auth`, `/theme`, `/session`, `/init`,
`/ftue`, `/help`, `/upgrade`. `/init` bootstraps project `AGENTS.md` (confirms
before overwrite). `/ftue` opens the setup wizard (provider, model, optional
init, feature tour, scheduler presets, first prompt); clean installs auto-open
it once until finish or dismiss. Enter sends; Shift+Enter newline; `esc` interrupts;
`ctrl+t` jumps to latest output; `ctrl+c` quits. `@path` attaches project
files. See [docs/keybinds.md](docs/keybinds.md) and [docs/usage.md](docs/usage.md).

## Web UI (`strike serve`)

The primary client is the Rust/Leptos bundle served by the Go daemon.
`strike serve` is API-first (`/health`, `/v1/*`, `/v1/ws`) and embeds that
bundle. The old JavaScript `web/` app is gone. Bind stays loopback unless
`--allow-private-bind` is set with `--auth`. Details:
[docs/webui-daemon.md](docs/webui-daemon.md).

```sh
make serve
# or install the per-user daemon (systemd on Linux, launchd on macOS):
./strike daemon install --start
curl -s http://127.0.0.1:8787/health
```

## Docs

| Topic | |
|---|---|
| [Changelog](CHANGELOG.md) | release history and upgrade notes |
| [Install & build](docs/install.md) | curl install, releases, upgrade, make targets |
| [Usage](docs/usage.md) | slash commands, `@file`, resume/fork, panes |
| [Loop](docs/loop.md) | `/loop` recurring session jobs (vs `/goal`) |
| [Nix dev env](docs/nix.md) | Nix philosophy, best practices, reference projects |
| [Keybinds](docs/keybinds.md) | keyboard reference (`f1` / `/keys` in-app) |
| [Auth & providers](docs/auth.md) | credentials, OAuth, billing routing |
| [Config](docs/config.md) | JSON, permissions, custom providers, `vimMode`/`nanoMode` |
| [Agents & skills](docs/agents-skills.md) | personas, skills, workflows / autonomy |
| [Plugins](docs/plugins.md) | Agent Plugins 1.0.0 contract (skills, MCP, `com.strike.cli`, trust) |
| [Web UI](docs/webui-daemon.md) | Rust client + `strike serve` daemon |
| [Phone](docs/mobile.md) | tailnet reachability and the phone layout |
| [Web cockpit contract](docs/web-cockpit-contract.md) | progressive modes, parity matrix, attach-only (#1069) |
| [Go SDK](docs/sdk.md) | `pkg/sdk` client over `pkg/protocol` |
| [Architecture](docs/ARCHITECTURE.md) | packages, seams, recipes |
| [Contributing](docs/contributing.md) | layout, verification, doc check |

## License

Licensed under the [Apache License, Version 2.0](LICENSE).

Free to use, modify, and distribute. You must retain the copyright notice and
attribution (see `LICENSE` and `NOTICE`). Copyright 2026 Jonathan Ung.
