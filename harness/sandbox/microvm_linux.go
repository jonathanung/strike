//go:build linux

package sandbox

import (
	"errors"

	"golang.org/x/sys/unix"
)

// KVM ioctl numbers (_IO(KVMIO=0xAE, n)).
const (
	ioctlKVMGetAPIVersion = 0xAE00
	ioctlKVMCreateVM      = 0xAE01
	ioctlKVMCreateVCPU    = 0xAE41
)

func probeMicroVM() MicroVMStatus {
	return probeMicroVMWith(&linuxKVM{kvm: -1, vm: -1, vcpu: -1}, microvmDriver)
}

// linuxKVM runs the real ioctl sequence against /dev/kvm.
type linuxKVM struct {
	kvm, vm, vcpu int
}

func (k *linuxKVM) Open() error {
	fd, err := unix.Open("/dev/kvm", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	k.kvm = fd
	return nil
}

func (k *linuxKVM) APIVersion() (int, error) {
	v, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(k.kvm), ioctlKVMGetAPIVersion, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(v), nil
}

func (k *linuxKVM) CreateVM() error {
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(k.kvm), ioctlKVMCreateVM, 0)
	if errno != 0 {
		return errno
	}
	k.vm = int(fd)
	return nil
}

func (k *linuxKVM) CreateVCPU() error {
	if k.vm < 0 {
		return errors.New("no VM fd")
	}
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(k.vm), ioctlKVMCreateVCPU, 0)
	if errno != 0 {
		return errno
	}
	k.vcpu = int(fd)
	return nil
}

func (k *linuxKVM) Close() {
	for _, fd := range []*int{&k.vcpu, &k.vm, &k.kvm} {
		if *fd >= 0 {
			_ = unix.Close(*fd)
			*fd = -1
		}
	}
}
