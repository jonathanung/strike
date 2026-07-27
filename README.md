# strike-cli

Website: https://strike.jonathanung.ca/

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

### Nix dev shell

Requires Nix with flakes enabled. Pins the exact Go 1.26, gopls, make, and git.

```sh
nix develop
make build
```

Details: [docs/nix.md](docs/nix.md).

In the TUI: `/provider`, `/model`, `/auth`, `/theme`, `/session`, `/init`,
`/help`, `/upgrade`. `/init` bootstraps project `AGENTS.md` (confirms before
overwrite). Enter sends; Shift+Enter newline; `esc` interrupts; `ctrl+t` jumps
to latest output; `ctrl+c` quits. `@path` attaches project files. See
[docs/keybinds.md](docs/keybinds.md) and [docs/usage.md](docs/usage.md).

## Experimental web cockpit (`strike serve`)

Opt-in browser cockpit: live composer/ops over WebSocket, permissions and
questions, status chrome, plus read-only SSE attach to any session JSONL.
Defaults to the offline `echo` provider. The TUI remains primary.

```sh
make serve
# or: ./strike serve --addr 127.0.0.1:8787 --token <secret> --provider echo
curl -s http://127.0.0.1:8787/health
# open http://127.0.0.1:8787/attach?token=<secret>

# LAN (phone/laptop on same network) — loud WARNING, no TLS:
./strike serve --expose --token <secret>
# optional: --allow-cidr 192.168.0.0/16
```

- `GET /health` — JSON `{ok, version, commit}` (no auth)
- `GET /attach` — cockpit page (composer, transcript, permission modal)
- `GET /v1/ws` — WebSocket ops in / events out (`?token=` or Bearer)
- `POST /v1/ops` — submit one op envelope
- `GET /v1/live/events`, `/v1/status`, `/v1/agents`, `/v1/sessions`
- `GET /v1/sessions/{id}/events` — SSE JSONL tail

Default bind is loopback-only. Non-loopback requires `--expose` (token + WARNING).
Optional Vite dev proxy lives in `web/`. Details and threat model:
[docs/web.md](docs/web.md).

## Docs

| Topic | |
|---|---|
| [Install & build](docs/install.md) | curl install, releases, upgrade, make targets |
| [Usage](docs/usage.md) | slash commands, `@file`, resume/fork, panes |
| [Nix dev env](docs/nix.md) | Nix philosophy, best practices, reference projects |
| [Keybinds](docs/keybinds.md) | keyboard reference (`f1` / `/keys` in-app) |
| [Auth & providers](docs/auth.md) | credentials, OAuth, billing routing |
| [Config](docs/config.md) | JSON, permissions, custom providers, `vimMode` |
| [Agents & skills](docs/agents-skills.md) | personas, skills, workflows / autonomy |
| [Web cockpit](docs/web.md) | experimental `strike serve` (live + RO) |
| [Architecture](docs/ARCHITECTURE.md) | packages, seams, recipes |
| [Contributing](docs/contributing.md) | layout, verification, doc check |

## License

Licensed under the [Apache License, Version 2.0](LICENSE).

Free to use, modify, and distribute. You must retain the copyright notice and
attribution (see `LICENSE` and `NOTICE`). Copyright 2026 Jonathan Ung.
