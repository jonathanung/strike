# THROWAWAY — Phase 0 NixOS microVM spike

Not a Strike product surface. Nothing here is wired into `cmd/strike`, the
engine, or `strike serve`. Safe to delete after [#1290](https://github.com/jonathanung/strike/issues/1290)
chooses a direction.

The guest is a NixOS system built with the `microvm.nix` module
(`4876e2ae`, input `flake = false` so the spike does not evaluate that
project's other inputs). It runs `strike rpc --provider echo` and mounts
**one** host directory over virtiofs. There is no guest NIC.

The host driver (`driver/`) starts `virtiofsd-run` and `microvm-run`,
checks the generated QEMU command, and relays one JSON-RPC turn.

## Do not enable KVM here

On this class of cloud guest, `KVM_CREATE_VCPU` has oopsed the kernel
(`kernel BUG at arch/x86/kvm/x86.c:702`). The guest forces TCG
(`microvm.cpu = "qemu64"`, `accel = "tcg"`, wrapper refuses `-enable-kvm`).
TCG timings are wiring evidence only. They are not a boot-time budget.

The measured passing boot on the research VM used TCG with `pit=on`,
`pic=on`, `notsc`, and `lpj=2500000`. Those clocks are not a KVM budget.
See `docs/design/sandbox/phase0-report.md`. A bubblewrap stand-in is also
recorded there; it is not the microVM:

```sh
CGO_ENABLED=0 go build -o /tmp/strike-phase0-bin ./cmd/strike
go run ./spike/sandbox/driver run-fallback --strike /tmp/strike-phase0-bin
```

That stand-in is a mount and network namespace. It is not a microVM.

`/dev/vhost-vsock` is absent and the host kernel is booted with
`nomodule`, so the session bus in this spike is a virtio-serial port, not
VSOCK. The architecture note still wants VSOCK on a host that has it.

## Run

```sh
./scripts/phase0.sh
```

Requires Nix flakes and a Linux host that can run QEMU TCG. The image
build embeds `strike` from this repo (`CGO_ENABLED=0`, checks off).
The fixed state directory is `/var/tmp/strike-phase0` because those paths
are baked into the runner.
