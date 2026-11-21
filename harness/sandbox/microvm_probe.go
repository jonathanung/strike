package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
)

// kvmAPIVersion is the only stable KVM API version (KVM_GET_API_VERSION).
const kvmAPIVersion = 12

// kvmProber is the KVM ioctl sequence the microvm probe runs. Injectable so
// tests never touch a real /dev/kvm.
type kvmProber interface {
	// Open opens /dev/kvm read-write.
	Open() error
	APIVersion() (int, error)
	CreateVM() error
	CreateVCPU() error
	// Close releases every fd opened so far. Safe after a failed step.
	Close()
}

// microvmDriver reports whether a microvm driver (strike-sandboxd socket or
// microvm-run) is installed. No driver ships in this build; P1-c registers
// one. Returns a short description of what is missing when absent.
var microvmDriver = func() (string, bool) {
	return "no microvm driver installed (strike-sandboxd socket or microvm-run)", false
}

// probeMicroVMWith runs the static availability check: open /dev/kvm,
// KVM_GET_API_VERSION, KVM_CREATE_VM, driver present, then KVM_CREATE_VCPU.
// A device node alone is not proof: some nested hosts pass CREATE_VM and
// oops on CREATE_VCPU (docs/design/sandbox/runtime.md). The vCPU step runs
// last, only once a driver is installed, so hosts that never set up the
// microvm runtime are not exposed to that kernel bug.
func probeMicroVMWith(k kvmProber, driver func() (string, bool)) MicroVMStatus {
	if err := k.Open(); err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return MicroVMStatus{Reason: "/dev/kvm not found (KVM not enabled on this host)"}
		case errors.Is(err, fs.ErrPermission):
			return MicroVMStatus{Reason: "cannot open /dev/kvm: permission denied (user not in the kvm group)"}
		default:
			return MicroVMStatus{Reason: fmt.Sprintf("cannot open /dev/kvm: %v", err)}
		}
	}
	defer k.Close()
	v, err := k.APIVersion()
	if err != nil {
		return MicroVMStatus{Reason: fmt.Sprintf("KVM_GET_API_VERSION failed: %v", err)}
	}
	if v != kvmAPIVersion {
		return MicroVMStatus{Reason: fmt.Sprintf("unsupported KVM API version %d (want %d)", v, kvmAPIVersion)}
	}
	if err := k.CreateVM(); err != nil {
		return MicroVMStatus{Reason: fmt.Sprintf("KVM_CREATE_VM failed: %v", err)}
	}
	if driver == nil {
		return MicroVMStatus{Reason: "no microvm driver installed"}
	}
	if missing, ok := driver(); !ok {
		return MicroVMStatus{Reason: missing}
	}
	if err := k.CreateVCPU(); err != nil {
		return MicroVMStatus{Reason: fmt.Sprintf("KVM_CREATE_VCPU failed: %v (this host cannot boot a guest)", err)}
	}
	return MicroVMStatus{Available: true}
}
