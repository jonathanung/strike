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
make cover          # statement coverage profile + total %
make cover-check    # cover + fail below COVER_MIN (default 75)
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
./strike --continue                  # resume the most recent root session
./strike --session <id>              # resume a specific root session by id
```

`--provider <provider>`, `--model <model>`, and `--effort <level>` may be
combined. `--continue` and `--session` cannot be combined.

To bypass permission checks for one invocation, use
`--dangerously-skip-permissions`.
**Warning:** this allows all tool calls without asks or denies. It applies
only to that process invocation, does not persist config or permission rules,
and is visibly marked as dangerous mode in the TUI. Agent profile denies still
apply. Run `strike --help` for the authoritative CLI usage and option list.

### Headless one-shot

```sh
./strike exec [options] <prompt...>
./strike exec [options] -            # read prompt from stdin
```

`strike exec` runs one prompt without the TUI and streams the assistant reply
to stdout. Options match the TUI (`--provider`, `--model`, `--effort`,
`--dangerously-skip-permissions`). Permission and question prompts cannot be
answered interactively; asks are rejected unless
`--dangerously-skip-permissions` is set.

Defaults when a provider is chosen without a model: `claude-sonnet-5`,
`gpt-5.5`, `grok-4.5`.

If you use the `strike` shell alias (points at this repo's built binary),
re-run `make build` after pulling changes to refresh it.

Credentials and provider login: [auth.md](auth.md).
