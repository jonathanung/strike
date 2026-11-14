# Runtime substrate for a NixOS agent sandbox

Status: feasibility note for [#1290](https://github.com/jonathanung/strike/issues/1290).
Date: 2026-10-26. Docs only. No Strike code changes.

This page answers the runtime question: which VM or sandbox substrate fits
Strike, on which platforms, and whether Peasant's Go VM drivers can be reused.
Placement (plugin vs engine, host vs guest process) belongs to the architecture
note. Threats and credential injection belong to the security note.

## Verdict

**Go with conditions.**

Use a warm Linux [microvm.nix](https://github.com/astro/microvm.nix) guest on
Cloud Hypervisor, with the host Nix store shared read-only over virtiofs and a
writable overlay on a block volume. QEMU is the alternate hypervisor when the
guest needs user-mode networking or `fw_cfg` credentials. Firecracker, Lima,
nsjail, and a cold per-job boot are the wrong defaults. Hosts where
`KVM_CREATE_VCPU` fails — including this cloud VM — stay on the existing
podman/docker manager or on `bwrap` / `sandbox-exec`. Strike defines its own
small driver. Peasant's `internal/vm` packages stay where they are.

## What was read

| Tree | Commit | What it actually contains |
|---|---|---|
| [peasant-labs/infra](https://github.com/peasant-labs/infra) | `6f3b987af7026aef195c29f1e6627297068aef42` (2026-10-03) | Go drivers under `runner-dispatcher/internal/vm/` (`driver.go` 42 lines, `systemdvm` 188, `subprocess` 211, `dryrun` 80). NixOS module `modules/nixos/services/runner-dispatcher` shells out to `systemctl start microvm@<slot>`. Container pool module `modules/nixos/services/github-runner` is rootless podman. `docs/runner-vm-spike-report.md` (2026-09-30) is the only boot measurement. The Cloud Hypervisor guest Nix (571 lines) is **not in this repo**; the spike says it lives in the maintainer's dotfiles. |
| [dayvidpham/nix-openclaw-vm](https://github.com/dayvidpham/nix-openclaw-vm) | `909b8a6cbf4ad50c12ff9773413a6161168cce45` (2026-05-17) | `modules/openclaw-vm/` host + guest. Guest sets `microvm.hypervisor = "qemu"`. Flake lock pins `astro/microvm.nix` `68c9f9c6ca91841f04f726a298c385411b7bfcd5`. `scripts/test-vm-boot.sh` expects `nix build .#test-vm-boot` then `microvm-run`. |
| [astro/microvm.nix](https://github.com/astro/microvm.nix) README on `main` | `4876e2ae2de6ce225f4ccf664ae441128aa3f03a` (same SHA as `microvm-nix/microvm.nix` HEAD when fetched) | Hypervisor matrix, default 512 MB RAM, store on erofs/squashfs or a host share plus optional writable overlay. No snapshot/restore section. |
| Strike today | `origin/main` at the time of writing | `harness/sandbox`: Linux `bwrap`, macOS `sandbox-exec`. `internal/integrate/container`: docker/podman CLI, no Moby SDK ([container.md](../../container.md)). `session.childIsolation` is `off` \| `shared` \| `worktree` only (`internal/product/config/config.go`). Nix in this repo is a dev shell ([nix.md](../../nix.md)), not a VM. |

Peasant's `runner-dispatcher/README.md` still says the hypervisor driver is
unwired. The NixOS module and the spike report are ahead of that README: the
wired production driver is `systemd`, which starts predeclared
`microvm@<slot>.service` units. There is no Cloud Hypervisor client in the Go
tree.

## What this host could and could not run

Host: Ubuntu 24.04, kernel `6.12.94+`, 4 vCPU, `MemTotal` 16387280 kB. `dmesg`
reports `Hypervisor detected: KVM`, ACPI OEM `FIRECK`, and
`Booting paravirtualized kernel on KVM`. This machine is itself a Firecracker
guest.

| Check | Result |
|---|---|
| `nix` on `PATH` | absent |
| `qemu-system-x86_64`, `cloud-hypervisor`, `firecracker`, `podman`, `docker`, `bwrap`, `nsjail`, `limactl` | absent |
| `/dev/kvm` | present, mode `crw-rw----`, group 103. uid 1000 is not in that group |
| `/sys/module/kvm_intel/parameters/nested` | `Y` |
| `/dev/vhost-vsock` | absent |
| unprivileged `open("/dev/kvm")` | `EACCES`. Five tries, 0–6 µs |
| root `KVM_GET_API_VERSION` | `12` |
| root `KVM_CREATE_VM` | success. Five tries: 467, 727, 423, 817, 778 µs |
| root `KVM_CREATE_VCPU` | process SIGSEGV. Kernel: `kernel BUG at arch/x86/kvm/x86.c:702!` then `Oops: invalid opcode` in `kvm_arch_vcpu_create` (`kvmprobe` PID 4360, then `kvmstep` PID 4390). Kernel tainted `DIE` |

No NixOS microvm, no QEMU guest, no container, and no bubblewrap run was
attempted. A second `KVM_CREATE_VCPU` would only oops the kernel again, so
boot time, guest RAM, and disk were not measured here. The numbers above are
ioctl timings, not VM boot times.

A `/dev/kvm` node plus `nested=Y` is not a usable nested hypervisor on this
image. Any Strike probe has to create a vCPU (or an equivalent "can this
hypervisor boot") and fail closed when that faults. Checking that the device
node exists is the wrong test.

## Comparison

Costs in the peasant column are from their spike on a NixOS desktop
(2026-09-30, run 36788926641), 4 slots × 6 vCPU × 4 GiB, idle slots stopped.
They are not measurements of Strike, and they are not measurements of this
cloud VM.

| Substrate | Hardware boundary | Nix store | Platforms | Boot / residency in the sources | Fits Strike |
|---|---|---|---|---|---|
| **microvm.nix + Cloud Hypervisor** | KVM | virtiofs (no 9p, per microvm README). Peasant shares JIT and caches this way | Linux with a working KVM vCPU. NixOS host in both references | Peasant: unit start → userspace ~10 s, then ~15 s `podman load`, runner takes the job at **+35 s** from queue. Idle RAM: none while slots are stopped. Under load: up to 4 × 4 GiB. Per-slot container disk: 6 GiB ext4 | **Default Linux guest.** Skip the inner `podman load`; that 15 s is a runner-image cost, not a NixOS-boot cost. Still expect on the order of the ~10 s userspace figure before calling it fast, and measure a Strike guest before publishing a number |
| **microvm.nix + QEMU** | KVM (Linux) or the process on macOS only via other hypervisors | 9p and virtiofs. User networking, TAP, VSOCK, `fw_cfg` | Linux. OpenClaw's guest hardcodes `hypervisor = "qemu"` | No boot timing in nix-openclaw-vm. Defaults: 8192 MiB, 2 vCPU, 16384 MiB state volume. `useVirtiofs` defaults **false** (store baked into erofs). Test VM in the flake sets 4096 MiB, 2 vCPU, `useVirtiofs = true` | **Alternate**, when the operator needs user networking (no bridge) or `fw_cfg` credential files. OpenClaw's VSOCK proxy also needs `/dev/vhost-vsock`, which this host does not have |
| **microvm.nix + Firecracker** | KVM | README restriction: **no 9p and no virtiofs** | Linux KVM | Not used by either reference for the agent guest. Peasant's hypervisor is Cloud Hypervisor | **Reject for v1.** A store that cannot be shared forces a full image rebuild for every environment change, which fights the self-config goal. This cloud host is a Firecracker guest whose nested vCPU ioctl bugs |
| **microvm.nix + vfkit** | Apple Virtualization.framework | virtiofs in the upstream flake example. No 9p, no tap/bridge | macOS only | Not exercised here (this host is Linux) | Later macOS path for people who already run Nix. Not the MVP |
| **crosvm / kvmtool / stratovirt / alioth** | KVM | README lists share and control-socket holes for each | Linux | Unused by both references | Leave them out of the driver matrix |
| **podman / docker** | Shared kernel (namespaces, cgroups) | Bind-mount or copy. No Nix required | Linux; Docker Desktop on macOS/Windows | Peasant container pool: job start **1–2 s** after queue when a slot is free, four runners resident the whole time. Strike already shells out to `docker` or `podman` | **Fallback** where KVM or Nix is missing. Same boundary Peasant kept beside the VM pool, and the boundary OpenClaw ships as the container mode |
| **bubblewrap** (`harness/sandbox`) | Shared kernel, user namespaces | None | Linux. Degrades when user namespaces are blocked | In-process wrap of one bash argv. No boot | **Current default** for bash on the host. Stays the backend when `runtime` is the host process. A microvm guest does not also need a host `bwrap` around the same command |
| **sandbox-exec** | Seatbelt, shared kernel | None | macOS | Shipped | macOS default until a vfkit guest exists |
| **nsjail** | Shared kernel, seccomp + namespaces | None | Linux | Unused in Strike and in both references | Do not add a second userspace sandbox beside `bwrap` |
| **Lima** | QEMU or Apple virt | Whatever the guest image contains | macOS and Linux | Unused in both references | Do not adopt. vfkit-through-microvm.nix is the Nix-native macOS VM, and Lima would be a third manager |
| **WSL2** | Already a Hyper-V VM | Depends on the distro | Windows | Neither reference targets it | Treat as "Linux, run the KVM vCPU probe". Absent or broken KVM uses the container or `bwrap` fallback inside the distro |
| **devcontainer** | The container runtime above | Image or a bind of the workspace | Wherever docker/podman runs | Strike E12 (`internal/integrate/container`) | Same fallback as podman/docker, not a separate substrate |

## Recommended substrate per platform

| Platform | Substrate | Why this one |
|---|---|---|
| Linux, Nix installed, `KVM_CREATE_VCPU` succeeds | Warm **microvm.nix / Cloud Hypervisor** workspace VM | Only combination that both references use for a hardware boundary **and** a shared Nix store (virtiofs). One VM per workspace, left running, matches OpenClaw's `microvm@openclaw-vm` better than Peasant's cold slot-per-job |
| Linux, Nix installed, KVM vCPU probe fails | **podman/docker** via `internal/integrate/container` | This cloud VM is the example: device node present, nested flag on, vCPU creation oopses. A microvm guest cannot boot |
| Linux, no Nix, KVM present or not | **container**, then **bwrap** if no container engine | A microvm guest without a Nix build is a prebuilt image pipeline Strike does not have. `bwrap` is already the bash sandbox |
| macOS | **sandbox-exec** now. **vfkit** microvm only after the Linux guest works and only when Nix is installed | microvm.nix's macOS hypervisor is vfkit, not KVM, and it has no tap/bridge |
| WSL2 | Same row as Linux after the vCPU probe | Nested KVM is a Windows host setting. Do not document WSL2 as a supported microvm platform until a probe on Windows nested virt is measured |
| Cloud VM without nested virt, or nested virt that faults | **container** or **bwrap** | Cursor's own agent VM, observed here, is in this bucket |
| CI ephemeral jobs | Peasant's cold slot model is for that workload, not for the TUI | Their accepted overhead is about **+33 s** versus the warm container pool, plus serialized boots (~21 s extra per additional VM in one planning cycle). An interactive Strike session should pay boot once per workspace |

`session.childIsolation` stays a filesystem placement (`shared` vs `worktree`).
A VM is a compute substrate and composes with a worktree (the workspace share
can be a worktree path). Recommended dial, for the architecture note to accept
or rename: `runtime.backend` = `process` \| `container` \| `microvm`.

## Boot, memory, disk

Peasant's healthy-path clocks (idle pool, run 36788926641):

| Step | Delta from job queued |
|---|---|
| Slot unit started (message, JIT, virtiofsd reset) | +7 s |
| Guest userspace | +17 s (~10 s of guest boot) |
| Baked image loaded, job unit started | +32 s (~15 s of that is `podman load`) |
| Runner takes the job | **+35 s** |
| Warm Go test suite finished | +41 s |

Boots in one planning cycle are serialized. The third of three jobs started
about 45 s after the first. That is the dispatcher waiting on each unit, not a
Cloud Hypervisor limit.

Strike's interactive guest can drop the `podman load` if the harness is in the
NixOS closure rather than a container tarball inside the guest. The remaining
~10 s to userspace is still their number for their guest, on their desktop.
Publish a Strike number only after a guest built from Strike's flake is timed
on a host where `KVM_CREATE_VCPU` works.

Memory and disk that the sources actually configure:

| Source | RAM | vCPU | Disk |
|---|---|---|---|
| microvm.nix README default | 512 MB, optional balloon | (per VM) | ro store image, or a share |
| Peasant spike | 4 GiB, released when the slot stops | 6 | 6 GiB ext4 per slot for the container store, after a tmpfs store filled RAM. Caches and the JIT file are virtiofs, not copies |
| OpenClaw option default | 8192 MiB | 2 | state volume 16384 MiB. Store embedded (erofs) unless `useVirtiofs` |
| OpenClaw `test-vm` / `test-vm-boot` | 4096 MiB | 2 | virtiofs store |

Recommended MVP size, chosen from those rows rather than from a new benchmark:
**4096 MiB, 2 vCPU** (Peasant's working RAM class and OpenClaw's test VM),
**6 GiB virtio-blk volume** for the writable store overlay (the size Peasant
needed once the store left tmpfs). The user's repository is a separate
virtiofs share, not a copy onto that volume. 512 MB is below a Go toolchain.
8192 MiB / 16 GiB is OpenClaw's gateway-plus-OpenCode guest, which is a larger
machine than a coding sandbox has to be. Peasant's idle pool holds no RAM
because the slots are stopped. A warm workspace VM holds its 4096 MiB until
the user stops it.

## Snapshot and restore

Neither reference implements snapshot/restore.

- Peasant's `vm.Driver` is `Boot`, `List`, `Kill`. Slots power off. The next
  job boots clean. Writable state that survives is the shared cache virtiofs
  and the per-slot ext4 volume.
- OpenClaw persists `/var/lib/openclaw` on `openclaw-state.img` and reboots
  the same VM. There is no save/restore API in the module.
- The microvm.nix README fetched for this note describes start, shares,
  volumes, and a balloon. It does not describe snapshot or restore.

A memory snapshot also does not capture a virtiofs host directory. Restoring
one without the host files is a split brain. MVP restore is: stop the process,
keep the block volume, start again. Snapshot/restore is a later spike on one
hypervisor, with a number attached, before it becomes a product control.

## Nix store sharing and binary caches

Two store layouts, both already options in microvm.nix and in OpenClaw:

| Layout | Mechanism | When |
|---|---|---|
| **Shared host store** | virtiofs of the host `/nix/store`, mounted read-only. OpenClaw `useVirtiofs`. Cloud Hypervisor can do this; Firecracker cannot (microvm README) | Host has Nix. Rebuilds of the guest config show up without rewriting an image. The guest cannot install into the host store through this mount |
| **Embedded store** | erofs (or squashfs) image, `storeOnDisk`. OpenClaw's default | Portable guest, CI image, host has no Nix at runtime. Any store change rebuilds the image. OpenClaw's comment on `useVirtiofs` states that tradeoff |

Guest installs (the self-config goal) need a **writable overlay**:

- `microvm.writableStoreOverlay` on a **virtio-blk volume** is the layout that
  has worked for years (`microvm.nix` issue
  [#43](https://github.com/astro/microvm.nix/issues/43) used a volume
  successfully with Cloud Hypervisor).
- A virtiofs directory as the overlay upperdir failed with
  `overlayfs: upper fs missing required features` on Cloud Hypervisor until
  microvm.nix PR
  [#419](https://github.com/microvm-nix/microvm.nix/pull/419) (issue closed
  2025-12-06). OpenClaw's lock (`68c9f9c6`) is later than that, but the volume
  path is still the one to ship first.
- Persist the Nix database (`/nix/var`) on the same volume. microvm.nix issue
  [#134](https://github.com/astro/microvm.nix/issues/134): after reboot the
  paths can still be on disk while the daemon re-downloads them, because the
  db lived on tmpfs.

Binary caches:

- Paths in the guest's system closure are read from the host store through
  the virtiofs lowerdir, so those paths are not copied into the overlay.
  Other host-store paths are not automatically registered in the guest Nix
  database; issue
  [#134](https://github.com/astro/microvm.nix/issues/134) is the same class
  of problem for paths the guest itself added.
- Paths the closure does not contain come from the guest's substituters
  (`cache.nixos.org`, or whatever the user's flake already uses) into the
  **overlay volume**, not into the host store.
- A host-side binary cache (nix-serve, or an HTTP cache on the bridge) is
  optional. VSOCK to a host cache is attractive and unmeasured; OpenClaw uses
  VSOCK for the gateway and the credential proxy, not for the Nix cache.
- Peasant's shared virtiofs is a **module and build cache**, plus a read-only
  JIT file, not a full `/nix/store` mount. Their runner image is a tarball
  baked into the guest. Copy that split (share caches, do not share the host's
  writable state) only for the cache directory. The OS closure still wants the
  store layout above.

virtiofsd operational constraint, already paid by Peasant: the daemon exits
with the VM and is not respawned, and restarting it **inside** the systemd
start transaction deadlocks on the `Before=` edge. The reset has to be its own
`systemctl try-restart microvm-virtiofsd@<slot>` before start. A subprocess
driver that launches `virtiofsd-run` itself (OpenClaw's
`scripts/test-vm-boot.sh`) has the same "socket must exist before the VMM"
ordering, without the systemd deadlock.

## Peasant's Go drivers: licence and API

Licence: `peasant-labs/infra` is Apache-2.0, the same licence as Strike. There
is no `NOTICE` file and the driver sources carry no copyright header. Copying
them into Strike would still require shipping the Apache text and stating the
changes (Apache-2.0 §4). `nix-openclaw-vm` is MIT (Copyright 2026 David Pham);
copying a Nix module requires keeping that notice.

Do **not** import the module.

- The packages live at
  `github.com/peasant-labs/infra/runner-dispatcher/internal/vm/...`. Go's
  `internal` rule rejects an import from `github.com/jonathanung/strike/...`.
- `BootSpec` is a GitHub Actions JIT record: `Name`, `JobID`, `JITConfig`,
  `Class`. `JITConfig` is written to a 0400 file the guest runner reads.
  Strike has no JIT config.
- `systemdvm` only starts names in a fixed slot list
  (`runner-vm-1..4` in the spike). It does not create a VM. The hypervisor,
  the bridges, and the shares are declared in Nix and, for the guest, in
  dotfiles this repo does not contain.
- `subprocess` is a process supervisor: templated argv
  `{name} {jit-file} {cache-dir} {job-id}`, process-group SIGTERM then SIGKILL
  after 10 s. That pattern is worth reimplementing (~200 lines). It is not
  worth a dependency.
- `dryrun` is an in-memory test double. Strike's existing `ExecFunc` fakes
  cover the same ground for CLI drivers.
- The dispatcher binary also requires a GitHub App key before it will select a
  driver (`cmd/runner-dispatcher/main.go`). It is not a library entry point.

Recommended shape Strike owns:

```text
Probe(ctx) error
Start(ctx, Spec) (Instance, error)
Stop(ctx, id) error
Status(ctx, id) (Instance, error)

Spec: Name, Workspace host path, MemoryMiB, VCPU, StoreMode
Instance: ID, State  // starting | running | stopped | failed
```

Backends:

| Backend | Wraps | Probe failure |
|---|---|---|
| `process` | today's `sandbox.Wrap` (`bwrap` or `sandbox-exec`) | existing degrade rules in [isolation.md](../../isolation.md) |
| `container` | today's `internal/integrate/container` Manager | `engine_not_found` / `engine_unavailable`, already defined |
| `microvm` | subprocess of `microvm-run` (and `virtiofsd-run`), process group killed like Peasant's `subprocess` driver | `/dev/kvm` not openable, or `KVM_CREATE_VCPU` fails, or `nix` / the runner binary is missing. Return that error. Do not silently start `bwrap` |

`microvm` fails closed when the user asked for it. Silent fallback hides the
difference between a KVM boundary and a shared kernel. A separate config flag
may allow downgrade to `container` (then `process`) and must say so in
`/sandbox explain`, the same way `sandboxAllowDegrade` is explicit today.

The systemd slot driver is a NixOS host-service option for a later machine
that wants Peasant's pool (fixed N, `systemctl`, virtiofsd units). It is a
poor fit for `strike` on a laptop, where the VM count follows workspaces and
NixOS is not the host OS. The subprocess driver talks to `microvm-run` the way
OpenClaw's boot script does, so the host can be any Linux with Nix and KVM,
not only NixOS.

## Effort

**L** for the Linux warm-microvm MVP below.

| Slice | Size | Why |
|---|---|---|
| `runtime.backend` dial, probe, fail-closed errors, tests | M | Config, engine, and `/sandbox explain`. Trust-boundary tier in [AGENTS.md](../../../AGENTS.md) (sandbox / session). The container and bwrap implementations already exist |
| One microvm.nix guest flake + subprocess driver + virtiofs store + 6 GiB volume + workspace share | L | Most of the time is the guest closure, virtiofsd ordering, and networking, which is where Peasant spent about two thirds of the spike defects (JIT names, notify timeout, virtiofsd deadlock, DNS, ext4 vs tmpfs) |
| macOS vfkit, WSL2, snapshot/restore, a NixOS systemd pool module | XL | Separate platforms and an unmeasured hypervisor feature. Out of the MVP |

## MVP slice

1. Document and implement `runtime.backend` with `process` as the default,
   matching today's behavior.
2. `microvm` probe: open `/dev/kvm`, `KVM_GET_API_VERSION`, `KVM_CREATE_VM`,
   `KVM_CREATE_VCPU`, close the fds, and check that a `microvm-run` built from
   the guest flake exists. Any failure is a typed error that names `process`
   and `container` as the available backends. The vCPU step is required; this
   host would pass a device-node check and then oops.
3. Guest flake (new, small, not a fork of OpenClaw's gateway image):
   `hypervisor = "cloud-hypervisor"`, 4096 MiB, 2 vCPU, virtiofs read-only
   host `/nix/store`, `writableStoreOverlay` on a 6 GiB volume with `/nix/var`
   persisted, workspace virtiofs read-write, VSOCK for a host control socket,
   no TAP until the security note picks an egress design. QEMU is a one-line
   hypervisor override for user networking and `fw_cfg`, not a second driver.
4. Go driver: start `virtiofsd-run`, wait until the sockets exist, start
   `microvm-run`, SIGTERM the process group, SIGKILL after a timeout. Unit
   tests with a fake argv, the way `subprocess` tests a fake command. No
   import of `peasant-labs/infra`.
5. One measured boot on a Linux host where the vCPU probe succeeds: time from
   `Start` to a guest `systemd` notify or an SSH/VSOCK banner, RSS of the VMM,
   and size of the store image vs the overlay volume. Those numbers replace
   the peasant figures in the user-facing docs. This cloud VM cannot produce
   them.
6. Leave snapshots, vfkit, WSL, and the systemd slot pool out of the slice.

## Risks

| Risk | Why it is real | Mitigation in the MVP |
|---|---|---|
| Nested KVM lies | This host: `nested=Y`, `/dev/kvm` present, `KVM_CREATE_VCPU` BUG at `arch/x86/kvm/x86.c:702`. Two oopses tainted the kernel (`DIE`) | Probe creates a vCPU. `microvm` refuses when it faults |
| Device permissions | uid 1000 cannot open `/dev/kvm` (group 103) | The driver runs where the kvm group or a NixOS `microvm` user can open the device. The TUI user does not need to be root if the host service is |
| virtiofsd lifecycle | Peasant deadlocked `systemctl start` by resetting virtiofsd inside the start transaction. OpenClaw's script races the sockets | Subprocess driver resets virtiofsd **before** the VMM, as its own step |
| Store overlay on virtiofs | Years of `upper fs missing required features` on Cloud Hypervisor | Volume-backed overlay only, until someone measures the post-#419 virtiofs upperdir |
| Nix db on tmpfs | Issue #134: reboot re-downloads paths that are still on disk | `/nix/var` on the volume |
| Boot latency mistaken for the container pool | Peasant: +35 s to job start vs 1–2 s warm containers, ~15 s of that a `podman load` | Warm workspace VM. Do not boot per turn. Do not put a container-image load on the guest boot path |
| Shared-kernel fallback presented as the same sandbox | `bwrap`, nsjail, podman, and devcontainers share the host kernel. OpenClaw's architecture note treats a container escape as host access, including to a credential-proxy key | Fail closed on `microvm`. A downgrade is a visible config flag. Security note owns whether that downgrade is offered at all |
| Workspace virtiofs is a host write channel | A read-write share of the repo is the point of a coding agent and is also a path onto the host filesystem | Share one workspace directory, not `/` and not `/nix/store` writable. Security note sets the mount policy |
| Guest Nix builds fill the volume | Peasant hit this with tmpfs | 6 GiB volume, and a disk-full error that stops the build rather than the host |
| macOS and Windows users get a different boundary | vfkit and WSL2 nested virt are unmeasured here | Docs and `/sandbox explain` name the backend that actually ran |
| Copying Peasant anyway | `internal/` will not compile as a dependency; the API is JIT-shaped; the guest Nix is not in the repo we would be depending on | Own the interface. Reimplement the process-group kill |
| Signed images | Peasant cosign-signs the **container** runner image (`docs/runner-pool.md`). The microvm guest is a Nix closure in dotfiles, not a signed OCI artifact in `peasant-infra` | MVP trust is the flake lock and the store hashes. Cosign stays on the container path Strike already has for ejected images |

## Open questions

Defaults if Jonathan does not pick otherwise:

| # | Question | Recommended default |
|---|---|---|
| 1 | Cloud Hypervisor, QEMU, or Firecracker? | **Cloud Hypervisor** on Linux. `hypervisor = "qemu"` when the operator needs user networking or `fw_cfg`. Firecracker is out of v1 |
| 2 | Cold VM per turn, cold VM per session, or warm VM per workspace? | **Warm per workspace.** Peasant's cold per-job boot is the right shape for CI and the wrong shape for the TUI (~10 s to userspace before their extra image load) |
| 3 | Import Peasant's driver? | **No.** Apache-2.0 would allow a copy with attribution; Go `internal` forbids the import; the API is a GitHub JIT slot. Reimplement a smaller `Probe/Start/Stop/Status` |
| 4 | What runs when KVM or Nix is missing? | **`container` if docker or podman is on PATH, else `process` (`bwrap` / `sandbox-exec`).** Only when the user set an explicit downgrade flag. A requested `microvm` without the flag is an error |
| 5 | Store layout? | **virtiofs read-only host `/nix/store` + writable overlay on a 6 GiB volume + `/nix/var` on that volume.** Embedded erofs only for a portable image with no host Nix |
| 6 | Binary cache? | **`cache.nixos.org` (and the user's existing substituters) into the overlay.** Host store paths come from virtiofs. No nix-serve in the MVP |
| 7 | Snapshots in v1? | **No.** Stop/start the same volume |
| 8 | Does `vm` become a `session.childIsolation` value? | **No.** Keep `off` \| `shared` \| `worktree`. Add `runtime.backend`. A worktree path may be the workspace share |
| 9 | systemd slots or one subprocess per VM? | **Subprocess of `microvm-run`.** systemd slots only if a later NixOS host module wants a fixed pool |
| 10 | RAM / CPU / disk defaults? | **4096 MiB, 2 vCPU, 6 GiB overlay volume**, from Peasant's 4 GiB working size and OpenClaw's test VM, and from Peasant's 6 GiB ext4. Not from a Strike benchmark |
| 11 | nsjail, Lima, WSL2 as targets? | **No** for nsjail and Lima. WSL2 follows the Linux probe and is unsupported as a microvm host until measured |
| 12 | Where does the Strike process run? | Runtime default for the MVP: **guest runs the toolchain and bash; host keeps the session log and the TUI.** Full harness-in-guest needs a transport (VSOCK is what OpenClaw uses) and is the architecture note's call. This host has no `/dev/vhost-vsock`, so that transport was not tested |

## Sources

- Peasant spike, including the latency table and the defect list:
  `docs/runner-vm-spike-report.md` at `6f3b987`.
- Peasant pool (rootless podman, cosign, 1–2 s job start):
  `docs/runner-pool.md` at the same commit.
- Driver contract: `runner-dispatcher/internal/vm/driver.go` and
  `cmd/runner-dispatcher/main.go` (`-vm-driver` = `dryrun` \| `subprocess` \|
  `systemd`).
- OpenClaw guest hypervisor, virtiofs flag, VSOCK, resources:
  `modules/openclaw-vm/guest.nix`, `modules/openclaw-vm/default.nix`,
  `flake.nix` (`test-vm` memory 4096), `scripts/test-vm-boot.sh`,
  `ARCHITECTURE.md` (container vs microvm vs full VM; VSOCK).
- microvm.nix hypervisor table and store model: README at `4876e2ae`.
  Overlay behavior: `nixos-modules/microvm/mounts.nix` on that tree, issues
  #43, #134, PR #419.
- This host's ioctl results: 2026-10-26, kernel `6.12.94+`, recorded in the
  table above. No guest was booted.
