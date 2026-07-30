//go:build darwin

package telemetry

import (
	"encoding/binary"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// hostCPUTicks widens the Mach tick counters across their 32-bit rollover. It
// has its own lock because readHostCPU runs before sampleCPU takes Host.mu, and
// Collect is documented safe for concurrent use.
var hostCPUTicks struct {
	mu sync.Mutex
	machTickWidener
}

func readHostCPU() (idle, total uint64, ok bool) {
	// Host CPU load is a Mach RPC on macOS — see mach_cpu_darwin.go. The old
	// kern.cp_time sysctl read here is BSD-only; macOS never exported it, so
	// this always reported unavailable (#602).
	ticks, ok := machHostCPUTicks()
	if !ok {
		return 0, 0, false
	}
	hostCPUTicks.mu.Lock()
	defer hostCPUTicks.mu.Unlock()
	idle, total = hostCPUTicks.add(ticks)
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

	// hw.pagesize alone is not trustworthy — see darwinPageSize. vm.pages is
	// the kernel page pool the counters are drawn from, so it cross-checks it.
	hwPageSize, _ := unix.SysctlUint64("hw.pagesize")
	vmPages, _ := sysctlPageCount("vm.pages")
	pageSize, ok := darwinPageSize(hwPageSize, memsize, vmPages)
	if !ok {
		return 0, 0, 0, false, false
	}

	// Page counts via sysctl. free is required; the rest degrade gracefully.
	// Note macOS does not export vm.page_active_count / vm.page_inactive_count /
	// vm.page_wire_count / vm.compressor_page_count at all — those are Mach
	// host_statistics64 fields, not sysctls — so the metric is built from the
	// OIDs that do exist.
	freePages, freeOK := sysctlPageCount("vm.page_free_count")
	if !freeOK {
		return 0, 0, 0, false, false
	}
	pages := darwinVMPages{Free: freePages}
	// File-backed ("Cached Files") pages. Both OIDs exist on current macOS;
	// the second is a narrower fallback that omits non-pageable external pages,
	// so prefer the full count and only fall back if it is missing.
	if v, ok := sysctlPageCount("vm.vm_page_external_count"); ok {
		pages.External, pages.ExternalOK = v, true
	} else if v, ok := sysctlPageCount("vm.page_pageable_external_count"); ok {
		pages.External, pages.ExternalOK = v, true
	}
	pages.Purgeable, pages.PurgeableOK = sysctlPageCount("vm.page_purgeable_count")

	used, cached, ok = darwinMemUsage(pageSize, total, pages)
	if !ok {
		return 0, 0, 0, false, false
	}
	return used, cached, total, true, true
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
