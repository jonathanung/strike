//go:build darwin

package telemetry

import (
	"encoding/binary"
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

	// Page counts via sysctl. free is required; the rest degrade gracefully.
	// Note macOS does not export vm.page_active_count / vm.page_inactive_count /
	// vm.page_wire_count / vm.compressor_page_count at all — those are Mach
	// host_statistics64 fields, not sysctls — so the metric is built from the
	// OIDs that do exist.
	freePages, ok := sysctlPageCount("vm.page_free_count")
	if !ok {
		return 0, 0, 0, false, false
	}
	pages := darwinVMPages{Free: freePages}
	// File-backed ("Cached Files") pages. Older kernels expose only the
	// pageable subset under a different name.
	if v, ok := sysctlPageCount("vm.vm_page_external_count"); ok {
		pages.External, pages.ExternalOK = v, true
	} else if v, ok := sysctlPageCount("vm.page_pageable_external_count"); ok {
		pages.External, pages.ExternalOK = v, true
	}
	pages.Purgeable, pages.PurgeableOK = sysctlPageCount("vm.page_purgeable_count")
	pages.Speculative, pages.SpeculativeOK = sysctlPageCount("vm.page_speculative_count")

	used, cached, cachedOK = darwinMemUsage(pageSize, total, pages)
	return used, cached, total, true, cachedOK
}

// sysctlPageCount reads a VM page-count sysctl. macOS exports these as either
// 32-bit or 64-bit integers depending on the OID (vm.page_free_count is 32-bit,
// vm.vm_page_external_count is 64-bit), so decode by the width the kernel
// actually returned — unix.SysctlUint32 fails outright on the 64-bit ones.
func sysctlPageCount(name string) (uint64, bool) {
	raw, err := unix.SysctlRaw(name)
	if err != nil {
		return 0, false
	}
	// darwin is little-endian on every supported arch.
	switch len(raw) {
	case 4:
		return uint64(binary.LittleEndian.Uint32(raw)), true
	case 8:
		return binary.LittleEndian.Uint64(raw), true
	default:
		return 0, false
	}
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
