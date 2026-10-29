# Phase 0 report: NixOS microVM sandbox

Throwaway spike for [#1290](https://github.com/jonathanung/strike/issues/1290).
The prototype lives in `spike/sandbox/` and is not wired into `cmd/strike`.
Machine-readable copies of the driver reports are in
[`phase0-evidence.json`](phase0-evidence.json).

## Verdict

**Keep, with conditions.** A NixOS microVM guest reached userspace on this
research VM, with the network off, one workspace shared, and one echo
`strike rpc` turn completed. The clocks below are QEMU TCG software
emulation. They are not a KVM boot budget and they are not a reason to
stop. The next measurement has to be on a host where `KVM_CREATE_VCPU`
works. This spike did not call that ioctl.

## What this host is

| Fact | Value |
|---|---|
| OS | Ubuntu 24.04.5 LTS, Linux 6.12.94+ x86_64 |
| CPUs | 4 |
| MemTotal | 16387280 kB |
| `/dev/kvm` | present, `crw-rw----` root:103 |
| `/dev/vhost-vsock` | absent |
| `/dev/vhost-net` | absent |

Opening `/dev/kvm` as the unprivileged user returned `permission denied`.
With sudo, `KVM_GET_API_VERSION` (`0xae00`) returned **12**.
`KVM_CREATE_VM` and `KVM_CREATE_VCPU` were not called.
`dmesg` printed no `BUG:` or `Oops` line during these runs.
The host is itself a KVM guest (`kvm-clock` on the command line). An
earlier runtime note said `KVM_CREATE_VCPU` oopses this kernel; that was
not repeated.

Nix 2.35.4 was installed single-user for the build. It was not on the
image at the start of the spike.

## What ran

Passing guest, QEMU TCG only (`accel=tcg`, no `-enable-kvm`):

- microvm.nix `4876e2ae`, machine `microvm`, CPU `qemu64`, 1 vCPU
- `pit=on`, `pic=on`, kernel `nokaslr notsc lpj=2500000`
- store disk is erofs (`165568` blocks of `4096` bytes, `678166528` bytes), not a host `/nix/store` share
- one virtiofs share, tag `workspace`, source `/var/tmp/strike-phase0/workspace`, mount `/workspace`
- no guest NIC
- `strike rpc --provider echo --auto` on virtio-serial port `strike.rpc` (VSOCK is impossible here: no `/dev/vhost-vsock`)
- host driver `spike/sandbox/driver` relays one initialize and one `user.input`

`lpj=2500000` is a boot flag so the delay loop returns. It is not a measured bogomips value.
`guest_mem_configured_mb` **1024** is configuration, not a measurement.

The driver reported `pass: true` for that run. Shutdown did not exit by itself: the log line is `phase0: shutdown: signal: killed` (the driver's 45s shutdown context). The driver then reaped the QEMU process group.

### Isolation, from the guest probe file

- hostname `strike-phase0`
- `non_lo_ifaces` empty, `default_route=absent`
- `net_ping`, `net_tcp`, `net_metadata`, `net_dns` all `blocked`
- host canary, host `secrets/auth.json`, and `/home/ubuntu` `absent`
- workspace parent escape `absent`, workspace marker `present`
- no environment names matching key, token, secret, password, or api
- `run_secrets=absent` (NixOS still mounts an empty systemd `/run/credentials`; the probe fails only when a regular file is there, and it does not print names)
- `mount /workspace virtiofs workspace`
- `/nix/store` is `erofs` on `/dev/vda`
- guest MemTotal **987400** kB, MemAvailable **735804** kB

Echo stop reason `end_turn`. The text began `You said: phase0 hello`.

### Clocks and memory for that passing TCG run

`timing_representative` is false.

| Sample | Value |
|---|---|
| virtiofsd socket ready | 100 ms |
| qemu start to probe marker and probe file | 39836 ms |
| qemu start to `initialize` ok | 48221 ms |
| qemu start to `turn.completed` | 49671 ms |
| qemu VmRSS and VmHWM at the turn | 882100 KiB |
| virtiofsd tree VmRSS | 9084 KiB |
| host MemAvailable at qemu start | 6991716 KiB |
| host MemAvailable after the turn | 6245464 KiB |

The guest console also printed `kvm_amd: Nested Virtualization enabled` while the guest was booting its own modules. `/dev/kvm` was not passed into the guest. The host `dmesg` did not gain a BUG or Oops from that line.

## What could not run

**KVM.** Not used, on purpose. No vCPU was created.

**TCG without a PIT.** An earlier image (`qemu64`, `accel=tcg`, `pit=off`, `pic=off`, `nokaslr tsc=reliable`) printed `tsc: Marking TSC unstable due to could not calculate TSC khz` and then produced no further kernel lines. virtiofsd's socket was ready in **100** ms. A `ps` sample about one minute after qemu start showed pid 42028, RSS **237136** kB, **99.7%** CPU, etime `01:01`. The saved qemu log is **8699** bytes; the last **168** bytes are the driver's SIGTERM line, so the kernel text is **8531** bytes and ends on that TSC line. The driver printed waiting lines through **12m17s**. Its error string says `guest probe timed out after 20m0s` because that build printed the configured deadline, not a separate measured elapsed time. No `PHASE0_PROBE_DONE`, no probe file. Host MemAvailable at the start of that run was **11297012** KiB. QEMU RSS at exit was not sampled.

**VSOCK.** The session bus in the passing run is virtio-serial.

**bubblewrap is not the architecture.** It was also run, because the PIT-off guest had not reached userspace yet. That run passed the same checks in a user, mount, and network namespace (`bwrap` 0.9.0, `--unshare-all`, one writable bind of the workspace, no `--share-net`). Probe **35** ms, initialize **66** ms, turn **362** ms, sandbox process-tree VmRSS **36952** KiB at initialize. `namespace_mem_total_kib` **16387280** equals host MemTotal. Those numbers are not a microVM boot. Ping inside that namespace failed with `Operation not permitted` (no `CAP_NET_RAW`); TCP to `1.1.1.1:443` and to `169.254.169.254:80` failed with `Network is unreachable`.

One PIT-on boot completed the echo turn (probe **38591** ms, turn **47083** ms, qemu VmRSS **852092** KiB) and then the checker rejected the probe script: bare `awk` was missing, `findmnt`'s tree glyphs hid `/workspace`, and an empty systemd `/run/credentials` was treated as a secret. The probe script was fixed and the passing run above is the one that counts.

## What the MVP needs next

1. Repeat the boot on a Linux host where `KVM_CREATE_VCPU` succeeds, and replace every TCG clock in this file with that measurement before anyone sets a budget.
2. Use VSOCK for Ops and Events where `/dev/vhost-vsock` exists. virtio-serial was only the substitute on this host.
3. Keep the guest NIC off until a host egress proxy exists. Keep using the echo provider until then. Provider keys stay on the host. This spike's guest HOME is `/tmp/strike-home` inside the guest; the product split still wants session JSONL and auth on the host, with `~/.strike` not exported.
4. Keep one virtiofs workspace and an erofs store disk. Do not share the host Nix store.
5. `microvm-shutdown` did not finish inside 45s under TCG. The MVP should shut the guest down through QMP on a host where that path is timed.
6. microvm's virtiofsd supervisord config sets `user=root` and exits when the driver is not root. This spike execs the inner virtiofsd instead.
