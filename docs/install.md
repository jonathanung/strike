# Install & build

## One-liner (recommended)

```sh
curl -fsSL https://strike.jonathanung.ca/install | bash
```

`https://strike.jonathanung.ca/install` is a **stable brand URL** that should
**redirect** (301/302) to the raw install script on GitHub, for example:

```
https://raw.githubusercontent.com/jonathanung/strike-cli/main/scripts/install.sh
```

Binaries are **not** hosted on the VPS long-term. The script resolves the
latest [GitHub Release](https://github.com/jonathanung/strike-cli/releases),
downloads the matching `strike_<tag>_<os>_<arch>.tar.gz`, verifies
`checksums.txt` (sha256), and installs to `~/.strike/bin/strike` (no root).

Optional:

```sh
# skip editing ~/.bashrc / ~/.zshrc
curl -fsSL https://strike.jonathanung.ca/install | bash -s -- --no-modify-path

# pin a tag
curl -fsSL https://strike.jonathanung.ca/install | bash -s -- --version=v0.1.0
# or: STRIKE_VERSION=v0.1.0 bash scripts/install.sh
```

After install, open a new shell (or `export PATH="$HOME/.strike/bin:$PATH"`)
and run:

```sh
strike version
strike
```

### Uninstall

```sh
rm -f ~/.strike/bin/strike
# optional: remove config/sessions (destructive)
# rm -rf ~/.strike
```

Remove any `PATH` line you added for `~/.strike/bin` from your shell rc.

## Upgrade

```sh
strike --upgrade
# or inside the TUI:
/upgrade
```

Self-update fetches the latest GitHub Release, verifies the archive checksum,
and atomically replaces the running binary. `strike upgrade` / `strike --upgrade`
exits back to the shell when done; `/upgrade` in the TUI restarts the app.
Config and sessions under `~/.strike` are never deleted. If the binary is not
writable (e.g. installed system-wide), re-run the install script or use your
package manager.

Windows self-update is unsupported in v1; re-download from Releases.

## Domain / DNS (ops)

Configure `strike.jonathanung.ca` with TLS and **redirect-only** rules:

| Public URL | Redirects to |
|---|---|
| `https://strike.jonathanung.ca/install` | raw `scripts/install.sh` on default branch |
| `https://strike.jonathanung.ca/` (optional) | this repo or docs |
| `https://strike.jonathanung.ca/latest` (optional) | GitHub Releases latest |

Smoke:

```sh
curl -fsSLI https://strike.jonathanung.ca/install
# expect a 301/302 chain ending at raw.githubusercontent.com/.../install.sh
```

## Build from source

Requires Go 1.26+ (`brew install go`).

```sh
make setup          # one-time: creates ~/.strike (config + example
                    # plan agent and commit skill); never overwrites
make build          # builds ./strike with version/commit ldflags
make run-echo       # offline dev loop — no API key needed. Type
                    # `run <command>` to exercise tool dispatch and the
                    # permission prompt.
make run            # real agent with your configured provider
make test           # go test ./...
make vet            # go vet ./...
```

`make build` stamps:

```text
-X …/internal/version.Version=$(git describe --tags …)
-X …/internal/version.Commit=$(git rev-parse --short HEAD)
```

Release CI (`.github/workflows/release.yml`) builds linux/darwin amd64+arm64
tarballs and `checksums.txt` on `v*` tags.

## Launch

strike launches without any provider configured. Pick one inside the TUI
(`/provider`, `/model`, `/auth` — see [usage.md](usage.md)) or pass flags:

```sh
export ANTHROPIC_API_KEY=sk-ant-…   # or: strike auth login anthropic
./strike                            # tries the config default silently;
                                    # otherwise select with /provider
./strike --provider <provider>       # anthropic, openai, xai, or echo;
                                    # fails loudly if no credentials
./strike --model <model>             # pre-select a model
./strike --effort <level>            # off, low, medium, high, xhigh, or max
./strike --version                   # stamped semver + commit
./strike --upgrade                   # self-update from GitHub Releases
```

`--provider <provider>`, `--model <model>`, and `--effort <level>` may be
combined. To bypass permission checks for one invocation, use
`--dangerously-skip-permissions`.
**Warning:** this allows all tool calls without asks or denies. It applies
only to that process invocation, does not persist config or permission rules,
and is visibly marked as dangerous mode in the TUI. Run `strike --help` for
the authoritative CLI usage and option list.

Defaults when a provider is chosen without a model: `claude-sonnet-5`,
`gpt-5.5`, `grok-4.5`.

If you use the `strike` shell alias (points at this repo's built binary),
re-run `make build` after pulling changes to refresh it.

Credentials and provider login: [auth.md](auth.md).
