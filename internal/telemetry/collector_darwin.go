//go:build darwin

package telemetry

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func readHostCPU() (idle, total uint64, ok bool) {
	// kern.cp_time: user nice sys idle intr
	raw, err := unix.SysctlRaw("kern.cp_time")
	if err != nil || len(raw) < 5*8 {
		return 0, 0, false
	}
	// Each counter is a 64-bit (or long) value depending on arch.
	// On modern darwin, sysctl returns array of uint64/long.
	n := len(raw) / 8
	if n < 5 {
		// 32-bit longs
		if len(raw) < 5*4 {
			return 0, 0, false
		}
		vals := make([]uint64, 5)
		for i := 0; i < 5; i++ {
			vals[i] = uint64(*(*uint32)(unsafe.Pointer(&raw[i*4])))
		}
		idle = vals[3]
		for _, v := range vals {
			total += v
		}
		return idle, total, true
	}
	vals := make([]uint64, 5)
	for i := 0; i < 5; i++ {
		vals[i] = *(*uint64)(unsafe.Pointer(&raw[i*8]))
	}
	idle = vals[3]
	for _, v := range vals {
		total += v
	}
	return idle, total, true
}

func readProcessCPUTime(pid int) (ns int64, ok bool) {
	var usage syscall.Rusage
	// RUSAGE_SELF when pid matches current; otherwise best-effort self only.
	who := syscall.RUSAGE_SELF
	if err := syscall.Getrusage(who, &usage); err != nil {
		return 0, false
	}
	sec := usage.Utime.Sec + usage.Stime.Sec
	usec := usage.Utime.Usec + usage.Stime.Usec
	return sec*1e9 + int64(usec)*1e3, true
}

func readMemory() (used, total uint64, ok bool) {
	// hw.memsize for total
	memsize, err := unix.SysctlUint64("hw.memsize")
	if err != nil || memsize == 0 {
		return 0, 0, false
	}
	total = memsize

	// vm.page_free_count * vm.pagesize ≈ free; used ≈ total - free - speculative.
	pageSize := uint64(4096)
	if ps, err := unix.SysctlUint64("hw.pagesize"); err == nil && ps > 0 {
		pageSize = ps
	}
	freePages, err1 := unix.SysctlUint32("vm.page_free_count")
	inactive, err2 := unix.SysctlUint32("vm.page_inactive_count")
	if err1 != nil {
		// Total known but used unknown — still better than silent zero used.
		return 0, 0, false
	}
	free := uint64(freePages) * pageSize
	if err2 == nil {
		free += uint64(inactive) * pageSize
	}
	if free > total {
		free = total
	}
	used = total - free
	return used, total, true
}

func readDisk(root string) (used, total, free uint64, ok bool) {
	if root == "" {
		return 0, 0, 0, false
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		return 0, 0, 0, false
	}
	bsize := uint64(st.Bsize)
	total = st.Blocks * bsize
	free = st.Bavail * bsize
	if free > total {
		free = total
	}
	used = total - free
	return used, total, free, true
}
