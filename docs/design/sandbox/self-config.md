# Sandbox self-config

**Angle:** how an agent safely configures its own dev environment inside a
NixOS microVM. Parent RFC:
[#1290](https://github.com/jonathanung/strike/issues/1290). Sibling angles
(`architecture`, `runtime`, `security`, `product`) own placement, the
hypervisor, credentials, and the UI shell. This page owns the environment
the agent is allowed to change, and how a person reviews that change.

**Verdict: go with conditions.**

The agent configures a **project shell** (a locked `flake.nix` devShell, or a
guest profile when the repo has no Nix). It does not configure the guest
NixOS system. System rebuilds, substituters, `nixConfig`, direnv, and nix-ld
stay behind an approval card that still asks under `yolo`. The guest base
module is sealed: the agent user is not in `wheel`, and a rebuild that would
drop the credential proxy, the firewall, or that user boundary is refused
before switch.

| | |
|---|---|
| Effort (this angle, full) | **L** |
| MVP slice | **M** |
| Verdict | **go with conditions** |

## Evidence

Read-only source study. No VM was booted and no flake was evaluated. This
cloud session has `/dev/kvm` and does not have `nix` on `PATH`.

| Source | Revision | What was read |
|---|---|---|
| [peasant-labs/infra](https://github.com/peasant-labs/infra) | `6f3b987af7026aef195c29f1e6627297068aef42` (2026-10-03) | `flake.nix`, `AGENTS.md`, `modules/nixos/services/github-runner/default.nix`, `modules/nixos/services/runner-dispatcher/default.nix`, `runner-image/Containerfile`, `runner-image/entrypoint.sh`, `docs/runner-vm-spike-report.md`, `docs/runner-pool.md` |
| [dayvidpham/nix-openclaw-vm](https://github.com/dayvidpham/nix-openclaw-vm) | `909b8a6cbf4ad50c12ff9773413a6161168cce45` (2026-05-17) | `flake.nix`, `AGENTS.md`, `README.md`, `ARCHITECTURE.md`, `.envrc`, `modules/openclaw-vm/guest.nix`, `modules/openclaw-vm/default.nix`, `modules/credential-proxy/guest.nix`, `credential-proxy/flake.nix`, `credential-proxy/.envrc` |
| strike `main` | `f47dcb26165b602f88ba534ebb9ec1a2f0cd228c` | `flake.nix`, `docs/nix.md`, `docs/container.md`, `docs/isolation.md`, `docs/config.md`, `docs/webui-daemon.md`; issues [#1266](https://github.com/jonathanung/strike/issues/1266), [#1269](https://github.com/jonathanung/strike/issues/1269) |

### What those trees actually do

Peasant and OpenClaw already separate **immutable machine config** from
**per-project shells**. Strike should keep that split.

**The runner image is a pinned artifact, not a live NixOS the job edits.**
`runner-image/Containerfile` pins the Ubuntu base digest, a dated apt
snapshot (`UBUNTU_SNAPSHOT`), exact apt versions, and SHA-256 of the runner
and docker tarballs. `AGENTS.md` states the publishing rule in one line:
consumers deploy, they never rebuild. `github-runner/default.nix` pulls
`imageRef` by digest and runs `cosign verify` before any container starts; a
stamp file skips the registry on reboot. `imageRef` and `imageSigner` move
together or the pull fails closed. The image grants the CI user passwordless
sudo (`runner ALL=(ALL) NOPASSWD: ALL`). That is acceptable for an ephemeral,
digest-pinned CI job. It is the wrong default for a long-lived agent.

**Caches are shared. Stores and credentials are not.** The container pool
bind-mounts one host `stateDir` and points `GOMODCACHE`, `GOCACHE`, and
`AGENT_TOOLSDIRECTORY` at `${stateDir}/cache`. The VM spike
(`docs/runner-vm-spike-report.md`) puts the runner image in the guest as a
tarball, the JIT config on a per-slot **read-only** virtiofs share, and
module/build caches on a **shared** virtiofs mount. The container store is a
6 GiB **per-slot** ext4 volume because a tmpfs store filled memory. A warm
Go test went from 25 s (six module downloads) to 6–7 s with an empty download.
The spike's isolation claim is "no host socket or App key in the guest; only
the cache and JIT shares." The PAT is unset before the listener starts
(`entrypoint.sh`). Host `/nix/store` is not mounted into the runner VM.

**OpenClaw's guest is a sealed NixOS module. Dev mode is a human hatch.**
`modules/openclaw-vm/guest.nix` declares system packages in the module
(`openclaw`, `curl`, `jq`, `git`, `bun`, `uv`, `opencode`). The interactive
`openclaw` user is not root. Polkit returns `NO` for systemd and journal
reads by `openclaw`, `openclaw-gateway`, and `opencode-server`. Passwordless
sudo is off unless `dangerousDevMode` is on (`security.sudo.wheelNeedsPassword
= !cfg.dangerousDevMode.enable`). That option's own description says it
should never be used in production: serial autologin and the QEMU guest
agent. The host module copies the same warning. `flake.nix` turns
`dangerousDevMode` on only for `test-vm` and `test-vm-boot`. Production
shape keeps the agent out of `wheel` and keeps `services.qemuGuest` off.

**`useVirtiofs` is an explicit store-sharing switch, default off.** The guest
option mounts the host's `/nix/store` at the guest's `/nix/store` and sets
`storeOnDisk = false`. The comment in `guest.nix` says this enables instant
rebuilds and requires the host store at runtime; portable and CI builds keep
the store inside an erofs image. `test-vm` sets `useVirtiofs = true`. virtiofs
follows host permissions. Nix store paths are mode `0555`, so a guest that
can mount the host store can read every realized output on that host,
including other projects' build products.

**`nix develop` in these repos is how humans and agents build the repo, on
the host, not how the guest OS is mutated.** `credential-proxy/flake.nix`
exposes `devShells.x86_64-linux.default` (`gopls`, `gotools`, `go-tools`,
`delve`, `temporal-cli`, `tlaplus18`) with `inputsFrom` the Go package.
`credential-proxy/.envrc` and the repo `.envrc` are both `use flake`.
`AGENTS.md` tells the agent `cd credential-proxy && nix develop` (or
`direnv allow`). `nix flake check` builds
`checks.x86_64-linux.eval-test-vm`, which evaluates the module system and
does not boot. Strike's own root `flake.nix` is the same pattern: locked
inputs, a small `devShells.default`, `GOTOOLCHAIN=local`, and `GOROOT` pinned
to the Nix `go`. `docs/nix.md` already says the flake is the source of truth
and that shell hooks must not install ad-hoc tooling.

**Neither reference enables nix-ld.** Foreign binaries in the runner image
come from apt and two verified tarballs. Foreign binaries in the OpenClaw
guest are Nix packages (`bun`, `uv`). There is no `programs.nix-ld` and no
`nix.settings.substituters` block in the guest module. Credential material
reaches the guest as placeholders and fw_cfg files
(`credential-proxy/guest.nix`); `HTTP_PROXY` forces egress through a VSOCK
socat bridge. A guest rebuild that dropped that bridge would bypass the
proxy. The host lockdown table drops new VM TCP except DNS (and Tailscale
when enabled).

**Strike already has the review pieces, unconnected to Nix.** [#1266](https://github.com/jonathanung/strike/issues/1266)
specifies approval cards with a command/diff preview and allow once / always /
deny, plus the rule that deny still applies under yolo. [#1269](https://github.com/jonathanung/strike/issues/1269)
specifies a viewer/editor that is read-only by default, saves through the
daemon with hash/mtime conflict detection, and shows a diff of what the
agent changed. `permissionMode` `accept-edits` allows file edits and still
asks for shell and network (`docs/webui-daemon.md`). `isolation.md` already
has one ask-even-under-yolo rule (`sensitive_read_egress`). Container detect
(`docs/container.md`) already recognizes `go.mod`, `package.json`, Python
markers, `Cargo.toml`, `flake.nix`, `shell.nix`, and `Makefile`.

## Declaration

A repo declares its environment with the Nix file it already has, plus an
optional Strike policy file. A second flake named `strike.nix` is rejected:
it would drift from `flake.nix`, and both reference repos (and strike itself)
already use `devShells.default`.

### `flake.nix` (the shell)

When the repo has a flake, the shell is `devShells.default` unless the policy
file names another `devShells.<name>` attribute. `flake.lock` is required
before the harness will enter the shell. An unlocked flake is a proposal:
the agent runs `nix flake lock` only after the card in
[Approval](#approval) shows the new lock.

`shell.nix` / `default.nix` are accepted as a legacy entry (`nix-shell`) and
always raise a card on first use, because they are not locked.

### `.strike/env.json` (optional policy)

Absent file means `enter: "auto"`. JSON matches `.strike/container.json` and
can be diffed without a Nix evaluator. The Nix expression stays in
`flake.nix`, where `nix flake check` and `nix develop` already work.

```json
{
  "version": 1,
  "enter": "auto",
  "flake": {
    "path": ".",
    "shell": "devShells.default"
  },
  "nixLd": "off",
  "systemRebuild": "deny",
  "substituters": []
}
```

| Field | Values | Meaning |
|---|---|---|
| `version` | `1` | Reject unknown versions. |
| `enter` | `auto`, `flake`, `shell-nix`, `profile`, `none` | How the harness builds `PATH` for tool bash. |
| `flake.path` / `flake.shell` | relative path, attribute | Used when `enter` is `flake` or when `auto` resolves to a flake. `path` must stay inside the workspace. |
| `nixLd` | `off`, `fixed` | `fixed` asks for the sealed library set. There is no `unrestricted` value. |
| `systemRebuild` | `deny`, `ask` | `ask` permits a proposal of `.strike/guest.nix`. `deny` refuses to draft a rebuild. |
| `substituters` | list of `{ "url", "publicKey" }` | Empty inherits guest policy (cache.nixos.org only). Non-empty is always a card. |

`auto` resolution order:

1. `flake.nix` with `devShells.default` (or exactly one `devShells` attribute) → `flake`.
2. `shell.nix` or `default.nix` → `shell-nix` (card on first entry).
3. A language marker from container detect (`go.mod`, `Cargo.toml`, `package.json`, `pyproject.toml` / `requirements.txt`, `Makefile`) and no Nix file → `profile`.
4. Otherwise → `none` (guest base `PATH` only: `git`, `curl`, `jq`, and the Nix CLI).

The file is project policy, committed when the user accepts the diff. It does
not pin nixpkgs. The project pin is `flake.lock`. The guest image pin is the
Strike guest flake, recorded separately (below).

### `.strike/guest.nix` (optional, privileged)

A NixOS module **snippet** the privileged helper appends to the sealed guest
base. It is how an agent proposes a system package, a nix-ld library, or a
sysctl that the project shell cannot express. It is not the live
configuration. The agent user cannot copy it into `/etc/nixos` and cannot
run `nixos-rebuild`.

### `.envrc` (not a declaration)

direnv files are executable policy. The harness does not source them until a
card records the sha256 of the file, the same role as `direnv allow`. A
changed hash invalidates the allow. After allow, the direnv diff is applied
on top of the flake environment. `use flake` is redundant with `enter: flake`
and should be left for humans; the harness enters the flake itself so the
command is an auditable `nix develop` argv.

### Session record

Each session meta block (field names for the architecture angle to place on
the wire) records:

| Field | Value |
|---|---|
| `guestSystem` | store path or nar hash of the running NixOS generation |
| `projectFlakeLock` | sha256 of `flake.lock`, or empty |
| `envJsonHash` | sha256 of `.strike/env.json`, or empty |
| `envrcHash` | sha256 of the allowed `.envrc`, or empty |
| `enter` | the resolved mode (`flake`, `profile`, …) |

## Flows

```text
repo files                         harness                         person
──────────                         ───────                         ──────
flake.nix + flake.lock ─┐
.strike/env.json        ├─ resolve enter ─ nix develop / profile ─ shell
.strike/guest.nix       │         │
.envrc                  │         └─ env-definition diff ─ card (#1266)
                        │                                    │
                        └─ editor (#1269) opens the same diff ┘
                                 allow once | always | deny
```

### Entering a shell

On session start the harness resolves `enter` and wraps tool bash.

| Resolved | Command the harness runs | Card |
|---|---|---|
| `flake`, lock present, `nixConfig` empty, lock hash previously accepted or committed by the user on `HEAD` | `nix develop <path>#<shell> --command -- <bash>` | no |
| `flake`, lock missing or lock hash new | stop before develop | yes, lock diff |
| `flake` with a `nixConfig` block, or any `--accept-flake-config` | stop | yes, and this class still asks under `yolo` |
| `shell-nix` | `nix-shell --run <bash>` | yes the first time per file hash |
| `profile` | a login with the guest profile's packages already on `PATH` | no |
| `none` | guest base `PATH` | no |

`nix develop` and `nix shell` use the guest Nix daemon. They do not pass
`--accept-flake-config`. Flake `nixConfig` can add substituters; accepting it
silently is a trust-boundary change, so it is the same card as editing
`substituters`.

`GOTOOLCHAIN=local` is set for Go shells the harness generates. The strike
root flake already does this so the compiler cannot download another toolchain.
Generated Rust shells do not install rustup.

### Ad hoc packages (`nix shell`)

| Request | Behavior |
|---|---|
| `nix shell <guest-pin>#<attr>` where `<guest-pin>` is the guest's locked nixpkgs | Allowed. The attr must exist in that pin. The package disappears with the shell. Nothing is written to the repo or the system profile. |
| `nix shell nixpkgs#<attr>` (flake registry, unlocked) | Card. The registry is not a lock. |
| `nix profile install` | Card. This mutates the user profile and survives the session. Prefer a devShell edit. |
| `curl \| sh`, `apt-get`, `sudo` | Denied by the guest image (no sudo, no apt) and by action-fact policy. The card, if the model asks for nix-ld or a rebuild to obtain the binary, recommends a Nix package first. |

A package the project needs twice is a one-line addition to `devShells` and
follows the diff flow. A package needed once stays in `nix shell`.

### Proposing a change as a diff

Env-definition paths:

- `flake.nix`, `flake.lock`, `shell.nix`, `default.nix`
- `.envrc`, `.envrc.*`
- `.strike/env.json`, `.strike/guest.nix`

The agent edits them with the ordinary write/edit tools. [#1269](https://github.com/jonathanung/strike/issues/1269)
is the surface: the file opens read-only until the person chooses to edit,
save goes through the daemon, and a hash/mtime mismatch is a conflict
against the agent's write. The same unified diff is the preview on the
[#1266](https://github.com/jonathanung/strike/issues/1266) card (that issue
already requires a command/diff preview and allow once / always / deny).

Two different permissions:

| Permission | What it allows | `accept-edits` | `yolo` |
|---|---|---|---|
| Edit the file | The bytes land in the worktree | allowed (it is a file edit) | allowed unless a hard deny matches |
| Enter the shell built from those bytes | `nix develop` / direnv / rebuild | still asks when the env-definition hash changed | still asks for the privileged class below |

`accept-edits` allowing a write must not imply running the new `shellHook`.
A `shellHook` is arbitrary code at the same trust level as `.envrc`.

Deny leaves the files in the worktree (so a careful edit is not destroyed)
and keeps the running shell on the last accepted hashes. Revert is an
explicit editor or git action. Allow always stores the accepted hashes as a
project permission rule for that path. It never stores "any future
`flake.nix`".

Privileged class (`env_privilege`), modeled on `sensitive_read_egress` which
already asks under `yolo` (`docs/isolation.md`):

- non-empty flake `nixConfig`, or `--accept-flake-config`
- `flake.lock` content change, `nix flake update`, `nix flake lock` of an unlocked input
- `.envrc` hash not yet allowed
- `.strike/env.json` changes to `nixLd`, `systemRebuild`, or `substituters`
- any substituter or `trusted-public-keys` change
- nix-ld enable or library-set change
- `nixos-rebuild`, `nix-collect-garbage`, writing `/etc/nix/nix.conf`
- reboot, `nixos-rebuild boot`, changing the default generation

These names go on `permissionAutoApproveExclude` so soft-approve's countdown
cannot fire them either. Deny rules still win, including under yolo, which
[#1266](https://github.com/jonathanung/strike/issues/1266) already requires.

Allow once on a privileged card runs the one command shown. Allow always is
offered only for "re-enter this exact `flake.nix` + `flake.lock` hash". It is
not offered for substituters, nix-ld, or rebuilds.

### direnv

1. Discover `.envrc` while resolving `enter`.
2. Card shows the full file (not a summary) and its sha256.
3. Allow once exports the direnv environment for the current command. Allow
   always binds that hash in the project rules.
4. The harness does not run `direnv allow` inside the guest as the agent
   user. The allow is the Strike decision, so a later guest process cannot
   widen it by touching `~/.local/share/direnv`.

### Guest module edit and rebuild

The production guest follows OpenClaw's non-dev posture:

- agent user is a normal user, not in `wheel`, no passwordless sudo
- polkit denies systemd and journal for that user
- `services.qemuGuest.enable` is false (`dangerousDevMode` stays a human
  serial-console hatch and is off in the Strike image)
- the agent has no write access to `/etc/nixos` or the Nix daemon socket's
  trusted-user list

Rebuilds go through a host-side helper. The guest reaches the helper only if
the architecture places Strike's control plane on the host. If the harness
itself runs in the guest ("run the harness in the VM" in #1290), the helper
is still a root service the agent user cannot signal except by asking the
host. The guest must not be able to approve its own rebuild. QEMU guest
agent is the wrong channel: OpenClaw enables it only under
`dangerousDevMode`, and a host daemon that can `guest-exec` as root is a
larger capability than a narrow rebuild RPC.

Helper protocol, each step a separate card except dry-build:

1. Agent writes `.strike/guest.nix` (editor diff).
2. Helper evaluates `sealed base` + snippet. `nixos-rebuild dry-build` (or
   `nix build` of the toplevel) runs as the helper, not as the agent.
3. Post-eval assertions, fail closed, no switch:
   - credential-proxy guest module still enabled (VSOCK bridge, `HTTP_PROXY`)
   - agent user still absent from `wheel`; sudo still requires a password the
     agent does not have
   - polkit deny for systemd/journal still present
   - `substituters` ⊆ guest policy ∪ card-approved keys
   - `programs.nix-ld` is off, or `nixLd` is `fixed` and the library list
     equals the sealed set
   - no `dangerousDevMode`, no qemu-ga, no password autologin
   - the snippet did not `mkForce` the base module's security options (the
     base sets those with a priority the snippet cannot override; the
     assertion checks the merged value, so a clever override still fails)
4. Card shows `nix store diff-closures` between the running generation and
   the candidate: added/removed store paths (truncated with a count), closure
   size delta, new systemd units, whether nix-ld or substituters flipped,
   and the `.strike/guest.nix` diff.
5. Allow once runs `nixos-rebuild switch` (test activation). It does not run
   `nixos-rebuild boot`.
6. The helper waits for the guest heartbeat (Strike control channel, plus
   the VSOCK proxy still answering). Timeout or a failed assertion on the
   running system runs `nixos-rebuild switch --rollback`.
7. `boot` / reboot is a second card with the generation id that would become
   default. Default recommendation on that card is deny.

`systemRebuild: "deny"` skips steps 2–7 and tells the agent to stay inside
the devShell.

### nix-ld

MVP leaves `programs.nix-ld.enable = false`.

Later, `nixLd: "fixed"` turns on nix-ld with a library set owned by the
sealed base (glibc, `stdenv.cc.cc.lib`, zlib, openssl, and similar). Adding
a library is a `.strike/guest.nix` diff plus the rebuild flow. The agent
cannot edit `ld.so` configuration or install apt packages. A missing `.so`
diagnostic names the Nix attribute to add to the devShell first, and offers
the nix-ld diff second.

The Ubuntu runner image is the documented alternative for workloads that
need a real FHS (service containers, `.deb` browsers). That is a second
image class for the runtime angle, digest-pinned the way `imageRef` is,
not an apt escape inside the NixOS guest.

### Binary caches and store sharing

| Layer | Default | Why |
|---|---|---|
| Guest Nix store | private, on the persistent virtio-blk state volume | OpenClaw already persists `/var/lib/openclaw` on `openclaw-state.img` (default 16 GiB). A store needs its own budget. Recommended volume: 32 GiB. |
| Host `/nix/store` via virtiofs | off | OpenClaw's `useVirtiofs` is opt-in and default false. Sharing it gives the guest every world-readable store path on the host. |
| Download / NAR cache | shared virtiofs, content-addressed, one host directory | Peasant's shared module/build cache is what made the second Go test cheap. Content-addressed NARs are safe to share across VMs as long as guests cannot add trusted keys. |
| Language caches | shared virtiofs subdirs, per-repo prefix | Same shape as `GOMODCACHE`, `GOCACHE`, `AGENT_TOOLSDIRECTORY` under the runner `stateDir`. Cargo and npm caches follow the same pattern. A repo prefix stops one project from poisoning another's module cache with a replaced checksum only if the tool itself verifies checksums (Go and Cargo do). |
| Substituters | `https://cache.nixos.org` only | Extra caches (Cachix, a private binary cache) are `env_privilege` cards. The helper writes `nix.conf`. The agent does not. |
| Container store inside the guest | per-VM ext4, not tmpfs | The spike's 6 GiB volume exists because tmpfs filled RAM during `podman load`. |

`storeShare: "host-ro"` may exist as a developer opt-in matching
`useVirtiofs`, for a single-user desktop where the host store is already
that user's. It stays off for any VM that runs untrusted agent code against
a host that builds other projects. It is read-only; builds still write to
the guest store. "Instant rebuild" in OpenClaw means the host has already
built the closure.

Garbage collection is the helper's job (`nix-collect-garbage` keeping the
current and previous system generations, and generations newer than 14 days).
The agent user cannot run GC. Peasant's failure mode was a full store taking
down the job; the helper should refuse a rebuild when free space on the state
volume is below 5 GiB and surface that as the card error.

### Generations and rollback

NixOS `switch` appends a generation. The previous generation stays bootable.
The helper keeps the last 5 generations (same idea as
`boot.loader.grub.configurationLimit`) and always retains the generation that
was current when the VM was created by Strike.

| Failure | Rollback |
|---|---|
| `switch` exits non-zero | no activation; running generation unchanged |
| heartbeat lost after switch | helper runs `nixos-rebuild switch --rollback` |
| person rejects the result | card action "roll back", which is allow-by-default (it returns to a generation they already ran) |
| bad devShell | `git` revert of `flake.nix` / `flake.lock`. System generations do not contain project files |

Project-shell rollback and system rollback are different buttons. The card
copy names which one will run.

### Repos with no Nix

`enter: auto` selects `profile`. The guest profile is baked from the guest
flake and is enough to compile the detected language:

| Marker | Profile packages |
|---|---|
| `go.mod` | `go` (major.minor from the `go` line, nearest nixpkgs build in the **guest** lock), `gopls`, `gnumake`, `git` |
| `Cargo.toml` | stable `cargo`, `rustc`, `rustfmt`, `clippy` via the same fenix pattern as strike's root flake (pure Nix, no rustup) |
| `package.json` | `nodejs` of the major in `engines` or `.nvmrc`, falling back to the guest's default node; corepack enabled |
| Python markers | `python3`, `uv` (OpenClaw's guest already ships `uv` for this reason) |
| `Makefile` only | `gnumake`, `gcc`, `pkg-config`, `git` |
| none | base `PATH` |

The harness does not write a flake just because a marker exists. Writing
`flake.nix` is a proposal the person accepts in the editor. Until they do,
the profile is ephemeral session state: nothing lands in the repo, and
another machine reproduces it only to the extent the guest image matches.

A `rust-toolchain.toml` or `GOTOOLCHAIN` line that the guest pin cannot
satisfy is a card offering a locked flake input (fenix, or a newer nixpkgs),
not a rustup or `curl \| sh` install.

### Lockfiles and reproducibility

| File | Role |
|---|---|
| Project `flake.lock` | Reproduces the devShell. Required before a quiet `nix develop`. |
| Guest Strike flake.lock | Reproduces the image, the profile, and the default nixpkgs used by `nix shell <guest-pin>#…`. Independent of the project lock. |
| `go.sum`, `Cargo.lock`, `package-lock.json` | Reproduces language dependencies inside the shell. Ordinary repo files, ordinary edit review. They are not `env_privilege` unless the command also changes `flake.lock`. |
| `.strike/env.json` | Reproduces Strike's choice of which shell to enter. Small, reviewed, not a pin. |

`docs/nix.md` already tells humans to bump pins with `nix flake update` on
purpose. The agent follows the same rule: an update is a lock diff on a
card, never a side effect of entering the shell.

## Worked example (design only)

**This example was not executed.** It is a design walkthrough of the flows
above, using the shape of strike's root `devShells.default` and OpenClaw's
`credential-proxy` devShell. No `nix develop`, no VM boot, no file was
written in a guest.

### Go repo with no Nix

Repo `~/src/widget`: a git checkout, `go.mod` says `go 1.26.0`, no
`flake.nix`, no `.strike/env.json`. The workspace is mounted in the guest at
`/work/widget`. The agent uid is `strike`, not in `wheel`.

1. **Resolve.** No env file, no flake, no `shell.nix`. Marker `go.mod` →
   `enter: profile`. Session meta records `enter=profile`, empty
   `projectFlakeLock`, and the current `guestSystem` hash.
2. **Shell.** The harness does not write files. Tool bash sees `go`,
   `gopls`, `gnumake`, `git` from the guest profile. The wrapper exports
   `GOTOOLCHAIN=local`, `GOROOT` from that `go`, `GOCACHE` and `GOMODCACHE`
   under the shared cache virtiofs at a `widget/` prefix, matching the
   runner module's `GOCACHE` / `GOMODCACHE` split. The agent runs
   `go test ./...`. Module download is an egress decision (security angle);
   the second test run hits the shared cache the way the spike's warm Go
   suite did.
3. **One-off tool.** The agent needs `golangci-lint` for a single command.
   It runs `nix shell <guest-pin>#golangci-lint`. The attr exists in the
   guest lock, so there is no card and no repo edit.
4. **Make it reproducible.** The person asks to commit an environment. The
   agent proposes two files. The editor ([#1269](https://github.com/jonathanung/strike/issues/1269))
   shows them read-only; the card ([#1266](https://github.com/jonathanung/strike/issues/1266))
   shows the same diff. `flake.lock` is included, so the card is
   `env_privilege` and still asks under `yolo`.

```nix
# DESIGN EXAMPLE — proposed flake.nix, not a file in this repo
{
  description = "widget";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };
    in {
      devShells.${system}.default = pkgs.mkShell {
        packages = [ pkgs.go pkgs.gopls pkgs.gnumake pkgs.git pkgs.golangci-lint ];
        shellHook = ''
          export GOROOT="${pkgs.go}/share/go"
          export GOTOOLCHAIN=local
          export GOPATH="$PWD/.nix-go/gopath"
          export GOCACHE="$PWD/.nix-go/gocache"
          export GOENV="$PWD/.nix-go/env"
          mkdir -p "$GOPATH" "$GOCACHE"
        '';
      };
    };
}
```

```json
{
  "version": 1,
  "enter": "flake",
  "flake": { "path": ".", "shell": "devShells.default" },
  "nixLd": "off",
  "systemRebuild": "deny",
  "substituters": []
}
```

5. **Allow once.** The daemon saves both files (hash check against the
   proposal). The next bash is `nix develop .#devShells.default --command --
   bash`. `guestSystem` does not change. Rollback of a bad shell hook is
   `git checkout -- flake.nix flake.lock .strike/env.json`, not a NixOS
   generation switch.
6. **Rejected detour.** The agent tries `sudo apt-get install gcc` to get
   cgo headers. The guest has no apt and the user is not in `wheel`. The
   model asks for a rebuild that adds `pkgs.gcc` to the **devShell**, which
   is a normal env-definition diff, not a `.strike/guest.nix` rebuild,
   because `systemRebuild` is `deny`.

### Rust repo with a toolchain file

Repo has `Cargo.toml` and `rust-toolchain.toml` pinning a dated nightly. No
flake.

1. `enter: profile` would select fenix stable, the same set strike's root
   flake uses (`cargo`, `rustc`, `rustfmt`, wasm std only if a marker says
   so). The nightly pin does not match.
2. The card offers one action: write a flake whose fenix input is locked to
   that nightly, and a `devShell` built with `fenix.packages.${system}.combine`.
   It does not offer rustup.
3. On allow, the flow is the Go flow from step 4. `CARGO_HOME` and
   `CARGO_TARGET_DIR` sit under the shared cache prefix, the same way
   strike's shell hook scopes them to `.nix-rust/`.

### What a system rebuild looks like in this example

The widget agent later needs a kernel feature the shell cannot provide
(illustrative: `/dev/kvm` inside the guest for a nested test). That is out
of scope for the devShell.

1. The agent can draft `.strike/guest.nix` only if someone sets
   `systemRebuild` to `ask` (itself an `env_privilege` card).
2. The helper dry-builds. If the snippet tries to set
   `security.sudo.wheelNeedsPassword = false` or disable the credential-proxy
   module, the assertion fails and no switch runs.
3. A clean snippet that only adds a documented option the base already
   allows would reach the closure-diff card. Allow once switches. A missed
   heartbeat rolls back. The project flake is untouched.

## MVP slice

Ship the project shell. Leave the guest sealed and immutable, the way the
runner image is immutable.

In:

- `enter: auto` resolution and `.strike/env.json` version 1 (`nixLd` forced
  `off`, `systemRebuild` forced `deny`, `substituters` forced `[]`)
- quiet `nix develop` for a locked flake with empty `nixConfig`
- profile fallback for `go.mod` and `Cargo.toml` (Go and Rust first; Node
  and Python in the next slice)
- `nix shell <guest-pin>#attr` with no card
- env-definition diff on the [#1266](https://github.com/jonathanung/strike/issues/1266)
  card and the [#1269](https://github.com/jonathanung/strike/issues/1269)
  editor, including the edit-vs-enter split
- `env_privilege` ask-even-under-yolo for lockfile changes, `nixConfig`, and
  `.envrc`
- guest-local store, no host store mount, shared NAR + `GOCACHE`/`GOMODCACHE`
  virtiofs
- session meta hashes (`guestSystem`, `projectFlakeLock`, `envJsonHash`, `enter`)

Out of MVP:

- `.strike/guest.nix`, the helper, generations, automatic rollback
- nix-ld
- extra substituters
- direnv execution (MVP shows `.envrc` and asks the person to delete it or
  wait; it does not source it)
- Node, Python, and Makefile profiles
- `storeShare: "host-ro"`

MVP acceptance: a Go module with no flake runs `go test` inside the guest
profile; proposing the example flake raises a card whose preview is the
diff; allowing it enters `nix develop`; `yolo` does not skip that card when
`flake.lock` is new; a `sudo` or `nixos-rebuild` from the agent user fails
without a generation change.

## Effort

| Slice | Size | What makes it that size |
|---|---|---|
| MVP above | **M** | Path class + one ask-even-under-yolo rule, a bash wrapper, JSON parse, card payload reused from #1266/#1269. No hypervisor work. |
| Full page (rebuild helper, assertions, generations, nix-ld fixed set, substituters, direnv hash allow, Node/Python profiles) | **L** | Privileged helper and sealed-module assertions are the bulk. They sit on the trust boundary (tier C once code exists). |
| Host-store sharing as a supported multi-tenant mode, FHS image class, arbitrary guest modules | **XL** | That is a second product. Runtime and security angles should own it. This page recommends not building it. |

## Risks

| Risk | What we saw | Mitigation in this design |
|---|---|---|
| Flake `nixConfig` adds a malicious substituter | Nix applies flake nixConfig only when flake config is accepted. Silent accept would make a repo file a trust root. | Never pass `--accept-flake-config`. Same card as `substituters`. |
| `.envrc` / `shellHook` is shell code | Both reference repos use `use flake`. OpenClaw's agent instructions tell the agent to `direnv allow`. | Hash allow. `accept-edits` does not enter the new shell. |
| Copying the runner image's `NOPASSWD: ALL` | `Containerfile` adds it so CI jobs can `sudo`. Jobs are ephemeral and the image is digest-pinned and cosign-verified. | Agent user is not in `wheel`. Apt is not installed. |
| `dangerousDevMode` generalized "so the agent can rebuild" | Guest option enables autologin, qemu-ga, and passwordless sudo, and the option text forbids production. | Hatch stays off. Rebuilds are a host helper with assertions. |
| Host store virtiofs leaks other builds | `useVirtiofs` mounts `/nix/store`, mode `0555` paths, default false, on in `test-vm`. | Default off. |
| Shared cache poisoning | Spike shares the cache across concurrent VMs and saw no errors. Go checksums make the module cache safe; a writable NAR cache is safe only while guests cannot add keys. | Content-addressed NAR cache. Keys are helper-owned. |
| Rebuild drops the credential proxy | Guest egress is `HTTP_PROXY` to a socat VSOCK bridge. Host nftables drops other new outbound. A module edit can delete that service. | Post-eval assertion before switch; heartbeat rollback after. |
| GC or a full disk kills the guest | Spike: tmpfs container store filled RAM; fix was a 6 GiB ext4 volume. | Agent cannot GC. Helper refuses rebuilds under 5 GiB free. Per-VM ext4 store. |
| Person confuses git rollback with generation rollback | Project files are not in the system closure. | Cards name which rollback runs. |
| Guest approves itself | If the harness runs in the guest, a local "allow" is the agent talking to itself. | Approval decisions are host-side only. |

## Open questions

Recommended defaults are what an implementation should ship if #1290 does
not say otherwise.

| # | Question | Recommended default |
|---|---|---|
| 1 | Declaration file: `strike.nix`, `.strike/env.json`, or flake only? | Optional `.strike/env.json`. The shell stays `flake.nix`. No `strike.nix`. |
| 2 | Write a flake into every non-Nix repo automatically? | No. Use the profile. Propose a flake only when the person asks or a package will not fit the profile. |
| 3 | Does `accept-edits` enter a shell whose `shellHook` the agent just wrote? | No. Edit and enter are separate. Enter after an env-definition hash change always asks. |
| 4 | Can `yolo` skip lockfile, substituter, direnv, nix-ld, and rebuild cards? | No. `env_privilege` asks under yolo, same pattern as `sensitive_read_egress`. |
| 5 | Who may press allow on a rebuild? | The person at the host UI only. The guest cannot answer its own card. |
| 6 | Share the host Nix store? | Off. Guest-local store. Shared content-addressed NAR cache and language caches. |
| 7 | State volume size? | 32 GiB. Revisit when a real closure is measured. |
| 8 | nix-ld in the first guest image? | Off until a repo has a binary with no Nix build. Then `fixed` library set, via the rebuild helper. |
| 9 | direnv in the MVP? | Detect and refuse to source. Execute only after the hash card, post-MVP. |
| 10 | Toolchain downloads (`GOTOOLCHAIN=auto`, rustup)? | Forbidden in generated shells. A missing nightly is a locked fenix input on a card. |
| 11 | Where does the privileged helper live if the harness moves into the guest? | Host process, reached from the guest only by a VSOCK request that the host turns into a card. Not qemu-ga. |
| 12 | Node and Python in the MVP profile? | No. Go and Rust first, because strike's own flake already pins those. |

## Out of scope here

Hypervisor choice, boot time, KVM, macOS/Windows fallback, credential proxy
internals, and the VM status page belong to the other #1290 angles. This
page assumes a Linux guest with a Nix daemon, a persistent state volume, a
virtiofs cache share, and a host that can show [#1266](https://github.com/jonathanung/strike/issues/1266)
cards and the [#1269](https://github.com/jonathanung/strike/issues/1269)
editor. Container launch (`docs/container.md`) remains the path for repos
that already have a Dockerfile; self-config does not replace it.
