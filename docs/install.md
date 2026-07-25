# Install & build

Requires Go 1.26+ (`brew install go`).

```sh
make setup          # one-time: creates ~/.strike (config + example
                    # plan agent and commit skill); never overwrites
make build          # builds ./strike        (or: go build -o strike ./cmd/strike)
make run-echo       # offline dev loop — no API key needed. Type
                    # `run <command>` to exercise tool dispatch and the
                    # permission prompt.
make run            # real agent with your configured provider
make test           # go test ./...
make vet            # go vet ./...
```

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
