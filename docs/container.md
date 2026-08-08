# Container runtime

Native containerization (epic
[#547](https://github.com/jonathanung/strike/issues/547)) lives in
`internal/container`. This page records the **E12.0** engine decision and the
package boundary. User-facing launch/attach/eject land in later E12 issues.

## Decision (E12.0 / #582): CLI shell-out

**Choice:** shell out to `docker` or `podman` via an injectable `ExecFunc`.
**Not chosen:** vendor the Moby/Docker Go SDK (`github.com/docker/docker/client`).

| Criterion | CLI shell-out | Moby SDK |
|---|---|---|
| Dependencies | stdlib `os/exec` only | large API + transitive tree |
| Podman | same argv when compatible | Docker API only (socket quirks) |
| API version negotiation | none (CLI handles it) | required |
| Matches strike practice | git worktrees, eval runners | new pattern |
| Interactive TTY attach | `docker exec -it` / PTY | hijack API (Zone still shelled out) |
| Unit tests | fake `ExecFunc` | mock generated client |

Zone's `internal/docker` used the SDK; the port (**E12.1 / #583**) rewrites
those call sites onto this CLI runtime rather than copying the SDK client.

Eval runners already shell out (`internal/eval/swebench.CLIRuntime`). **E12.10
/#592** should migrate them onto `internal/container` so one backend owns
create/exec/rm and scheduler pool leases.

## Package surface

```text
internal/container
  ExecFunc / DefaultExecFunc   injectable CLI runner
  Runtime (interface)          Available, Pull, Create, Start, Stop,
                               Remove, InspectID, CopyFrom/To, Exec
  CLI                          production Runtime (+ BuildImage, networks,
                               InspectContainer)
  Manager                      per-repo lifecycle: Build, Launch, Attach,
                               Exec, Stop, Restart, Destroy, Clean, Status,
                               ListManaged
  Config                       in-process config (JSON layering = E12.2)
  Cache                        <repo>/.strike/container/ (image/container ids,
                               config.hash)
  ContainerName / NetworkName  strike-<repo>-<sha256[:16]>
  Labels                       com.strike.* (was com.zone.*)
```

### Config (E12.2)

Layered JSON `container` block and optional `container.jsonc` (defaults →
global → project → managed). See [config.md](config.md#container-native-containerization-e12).
`config.ContainerConfig.ToRuntime` feeds `Manager`.

### Eject (E12.3)

```sh
strike container eject [--out Dockerfile.devcontainer] [--force] [--dockerfile path]
strike container drift [--dockerfile path]
```

Materializes `Dockerfile.devcontainer` (commit by default) with a
`# strike-config-hash:` header. Drift refuses overwrite unless `--force`.
`--dockerfile` uses a hand-edited file as the body while still stamping the hash.
`Manager` prefers the ejected file when present.

### Launch inside (E12.4)

```sh
strike --launch-inside-container
# or config: "container": { "execution": "container" }
```

Preflight codes: `already_inside_container`, `engine_not_found`,
`engine_unavailable`, `no_dockerfile`, `dockerfile_drift`, `required_env`.
On success: build/start Manager container, copy host `strike` binary, 
`docker exec -it` with workspace mount and credential env. Nested launch is
refused via `STRIKE_ISOLATION`.

### `/devcontainer` skill (E12.5)

Built-in skill that scaffolds project container config:

```sh
strike container detect          # human summary
strike container detect --json   # markers + suggested config fragment
```

Detection reads `go.mod`, `package.json` (+ lockfiles), `requirements.txt` /
`pyproject.toml` / `Pipfile` / `setup.py`, `Cargo.toml`, `flake.nix` /
`shell.nix`, and `Makefile`. The skill **always** asks via the `question` tool
(base image, deps, network posture, resources), writes `.strike/container.json`,
shows the Dockerfile diff, and only then runs `strike container eject`.
Config fields: `needsNode` / `nodeVersion`, `needsPython` / `pythonVersion`,
`needsGo` / `goVersion`, `needsRust` (plus existing `packages`, `network`,
`resources`).

### Attach live container (E12.6)

**Session model:** one managed container per repo path
(`ContainerName(repoPath)` → `strike-<repo>-<sha256[:16]>`). Multiple strike
sessions **attach** to that container rather than creating a second.

```sh
strike --launch-inside-container
# on stale live container (interactive TTY):
#   attach anyway [a] / rebuild [r] / cancel [c]
strike --launch-inside-container --container-attach-stale
strike --launch-inside-container --container-rebuild
strike --launch-inside-container --container-cancel   # non-interactive refuse

strike container ls            # this repo mapping + live state
strike container ls --all      # every com.strike.managed container
strike container status        # running + config-hash compatibility
```

`Manager.LaunchWithResult` returns mode `attached` | `started` | `restarted` |
`rebuilt`. Launch prints e.g. `strike: attached to existing container …`.
Discovery, creation, and explicit rebuild are serialized per repository across
processes; a concurrent launch re-inspects and joins the compatible live winner.
Stale config raises `*StaleContainerError` (unwraps `ErrConfigDrift`) with
question options attach/rebuild/cancel for CLI or the `question` tool.

### Isolation indicator (E12.7)

Posture ladder (descriptive, not graded):

`host+yolo` → `host+default` → `host+sandbox` → `container` → `container+no-network`

- Injected as `STRIKE_ISOLATION` at process/container launch (never `/.dockerenv`).
- Header badge (muted) next to the permission dial; `/container` full view;
  `/legend` entries; context right-pane row.
- `session.meta.isolation` for reproducibility (protocol 1.15+).

Still later: broader test suite (E12.8).


## Binary selection

1. Explicit `CLI.Binary` if set (`docker`, `podman`, or absolute path).
2. Else first of `docker`, `podman` on `PATH`.
3. `Available` runs `info --format {{.ServerVersion}}` and maps failures to
   `ErrEngineNotFound` / `ErrEngineUnavailable`.

## Architecture boundary

- `internal/tui` **must not** import `internal/container` (enforced by
  architecture boundary tests once wired).
- Status and isolation posture reach the UI through `host.Services` and
  protocol events (E12.7).
- Credentials are forwarded as env at launch time — never baked into images
  (E12.4).

## Zone archive (E12.9)

[`peasant-community/zone`](https://github.com/peasant-community/zone) is
**archived**. Containerization lives only in strike. Provenance and MIT
attribution are recorded in the repository root `NOTICE`.

## Related

- [isolation.md](isolation.md) — how containers compose with OS sandbox and worktrees
- [ARCHITECTURE.md](ARCHITECTURE.md) — package table
- [config.md](config.md) — future `container` / `execution` keys (E12.2, E12.4)
