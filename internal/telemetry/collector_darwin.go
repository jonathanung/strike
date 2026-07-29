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

func readMemory() (used, cached, total uint64, ok, cachedOK bool) {
	// hw.memsize for total physical RAM.
	memsize, err := unix.SysctlUint64("hw.memsize")
	if err != nil || memsize == 0 {
		return 0, 0, 0, false, false
	}
	total = memsize

	pageSize := uint64(4096)
	if ps, err := unix.SysctlUint64("hw.pagesize"); err == nil && ps > 0 {
		pageSize = ps
	}

	// Page counts via sysctl. free is required; others degrade gracefully.
	freePages, err := unix.SysctlUint32("vm.page_free_count")
	if err != nil {
		return 0, 0, 0, false, false
	}
	active := sysctlPages("vm.page_active_count")
	inactive := sysctlPages("vm.page_inactive_count")
	speculative := sysctlPages("vm.page_speculative_count")
	wired := sysctlPages("vm.page_wire_count")
	purgeable := sysctlPages("vm.page_purgeable_count")
	// compressor_page_count is "Pages occupied by compressor" (still used RAM).
	compressor := sysctlPages("vm.compressor_page_count")

	used, cached = darwinMemPressure(pageSize, total,
		uint64(freePages), active, inactive, speculative, wired, purgeable, compressor)
	// Cache metric is meaningful when we read at least one reclaimable class.
	cachedOK = inactive > 0 || speculative > 0 || purgeable > 0 || cached > 0
	// If wired/active sysctls failed entirely, fall back so we still show something.
	if wired == 0 && active == 0 && compressor == 0 {
		// Legacy-ish: total − free − inactive − speculative (still excludes file cache).
		reclaim := (uint64(freePages) + inactive + speculative) * pageSize
		if reclaim > total {
			reclaim = total
		}
		used = total - reclaim
		cached = (inactive + speculative) * pageSize
		cachedOK = cached > 0
	}
	return used, cached, total, true, cachedOK
}

func sysctlPages(name string) uint64 {
	v, err := unix.SysctlUint32(name)
	if err != nil {
		return 0
	}
	return uint64(v)
}

// xswUsage matches struct xsw_usage in sys/sysctl.h (darwin).
type xswUsage struct {
	Total     uint64
	Avail     uint64
	Used      uint64
	Pagesize  int32
	Encrypted bool
}

func readSwap() (used, total uint64, ok bool) {
	raw, err := unix.SysctlRaw("vm.swapusage")
	if err != nil || len(raw) < int(unsafe.Sizeof(xswUsage{})) {
		return 0, 0, false
	}
	swap := *(*xswUsage)(unsafe.Pointer(&raw[0]))
	if swap.Total == 0 && swap.Used == 0 {
		// No swap configured — still a valid measurement.
		return 0, 0, true
	}
	used = swap.Used
	total = swap.Total
	if used > total && total > 0 {
		used = total
	}
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
