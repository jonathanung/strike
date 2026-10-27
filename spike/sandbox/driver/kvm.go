package main

import (
	"fmt"
	"os"
	"syscall"
)

// kvmGetAPIVersion is Linux _IO(KVMIO, 0x00) with KVMIO 0xAE.
// This spike calls only this ioctl. KVM_CREATE_VCPU (0xAE41) oopses the
// kernel on this cloud image (arch/x86/kvm/x86.c) and must not be added.
const kvmGetAPIVersion = 0xae00

func kvmAPIVersion() (int, error) {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	v, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(kvmGetAPIVersion), 0)
	if errno != 0 {
		return 0, errno
	}
	if int(v) < 0 {
		return 0, fmt.Errorf("kvm api version %d", int(v))
	}
	return int(v), nil
}
