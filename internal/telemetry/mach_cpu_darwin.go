//go:build darwin

package telemetry

import (
	"sync"
	"syscall"
	"unsafe"
	_ "unsafe" // for go:linkname
)

// Host CPU load on macOS comes from host_statistics(HOST_CPU_LOAD_INFO), a Mach
// RPC. There is no sysctl equivalent — kern.cp_time is BSD-only and macOS does
// not export it, which is why the CPU row read "unavailable" on every Mac
// (#602). Release builds are CGO_ENABLED=0, so cgo would be compiled out of
// every shipped binary; instead we reach libSystem the same way
// golang.org/x/sys/unix does, via cgo_import_dynamic plus an asm trampoline.
//
// Implemented in the runtime package (runtime/sys_darwin.go).
//
//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:cgo_import_dynamic libc_mach_host_self mach_host_self "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_host_statistics host_statistics "/usr/lib/libSystem.B.dylib"

var (
	libc_mach_host_self_trampoline_addr  uintptr
	libc_host_statistics_trampoline_addr uintptr
)

// hostCPULoadInfo is HOST_CPU_LOAD_INFO from mach/host_info.h.
const hostCPULoadInfo = 3

// machHostPort caches the host port for the process lifetime. mach_host_self
// hands out a send right per call, so sampling at 1 Hz without caching would
// leak a port reference every tick.
var machHostPort = sync.OnceValue(func() uintptr {
	port, _, _ := syscall_syscall(libc_mach_host_self_trampoline_addr, 0, 0, 0)
	return port
})

// machHostCPUTicks returns cumulative CPU ticks per state across all cores.
func machHostCPUTicks() (ticks [cpuStateMax]uint32, ok bool) {
	host := machHostPort()
	if host == 0 {
		return ticks, false
	}
	// host_statistics takes the element count as an in/out parameter and writes
	// back how many it filled.
	count := uint32(cpuStateMax)
	ret, _, _ := syscall_syscall6(libc_host_statistics_trampoline_addr,
		host,
		hostCPULoadInfo,
		uintptr(unsafe.Pointer(&ticks[0])),
		uintptr(unsafe.Pointer(&count)),
		0, 0)
	// KERN_SUCCESS is 0. A short write means the struct is not the shape we
	// expect, so treat it as unavailable rather than reading partial ticks.
	if ret != 0 || count != cpuStateMax {
		return ticks, false
	}
	return ticks, true
}
