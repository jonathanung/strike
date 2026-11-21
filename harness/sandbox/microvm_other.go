//go:build !linux

package sandbox

import "runtime"

func probeMicroVM() MicroVMStatus {
	return MicroVMStatus{Reason: "microvm runtime requires Linux with KVM (this host is " + runtime.GOOS + ")"}
}
