//go:build linux

package telemetry

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func readHostCPU() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	// cpu user nice system idle iowait irq softirq steal guest guest_nice
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var vals []uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		vals = append(vals, v)
	}
	// idle = idle + iowait (indices 3, 4)
	idle = vals[3]
	if len(vals) > 4 {
		idle += vals[4]
	}
	for _, v := range vals {
		total += v
	}
	return idle, total, true
}

func readProcessCPUTime(pid int) (ns int64, ok bool) {
	// /proc/self/stat field 14 utime, 15 stime in clock ticks.
	path := "/proc/self/stat"
	if pid > 0 {
		path = "/proc/" + strconv.Itoa(pid) + "/stat"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	// Comm may contain spaces/parens; split after last ')' of comm.
	s := string(b)
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+2 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(s[idx+2:])
	// After comm: state ppid ... utime(12) stime(13) zero-based from state.
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	// CLOCKS_PER_SEC is 100 on Linux.
	const ticksPerSec = 100
	sec := float64(utime+stime) / ticksPerSec
	return int64(sec * 1e9), true
}

func readMemory() (used, cached, total uint64, ok, cachedOK bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, false, false
	}
	defer f.Close()
	var memTotal, memAvailable, memCached, sReclaimable uint64
	var haveTotal, haveAvail, haveCached, haveSReclaim bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			memTotal, haveTotal = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			memAvailable, haveAvail = parseMeminfoKB(line)
		case strings.HasPrefix(line, "Cached:"):
			memCached, haveCached = parseMeminfoKB(line)
		case strings.HasPrefix(line, "SReclaimable:"):
			sReclaimable, haveSReclaim = parseMeminfoKB(line)
		}
		if haveTotal && haveAvail && haveCached && haveSReclaim {
			break
		}
	}
	if !haveTotal || memTotal == 0 {
		return 0, 0, 0, false, false
	}
	total = memTotal
	// MemAvailable already excludes reclaimable cache/buffers (like Activity Monitor used).
	if haveAvail && memAvailable <= memTotal {
		used = memTotal - memAvailable
	} else {
		// Fallback: MemTotal - MemFree - Buffers - Cached is less accurate.
		used = memTotal
	}
	if haveCached {
		cached = memCached
		if haveSReclaim {
			cached += sReclaimable
		}
		if cached > total {
			cached = total
		}
		cachedOK = true
	}
	return used, cached, total, true, cachedOK
}

func readSwap() (used, total uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	var swapTotal, swapFree uint64
	var haveTotal, haveFree bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "SwapTotal:") {
			swapTotal, haveTotal = parseMeminfoKB(line)
		} else if strings.HasPrefix(line, "SwapFree:") {
			swapFree, haveFree = parseMeminfoKB(line)
		}
		if haveTotal && haveFree {
			break
		}
	}
	if !haveTotal {
		return 0, 0, false
	}
	total = swapTotal
	if haveFree && swapFree <= swapTotal {
		used = swapTotal - swapFree
	}
	return used, total, true
}

func parseMeminfoKB(line string) (uint64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, false
	}
	kb, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return kb * 1024, true
}

func readDisk(root string) (used, total, free uint64, ok bool) {
	if root == "" {
		return 0, 0, 0, false
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		return 0, 0, 0, false
	}
	// Prefer Bsize; Frsize is fundamental block size on some FS.
	bsize := uint64(st.Bsize)
	if st.Frsize > 0 {
		bsize = uint64(st.Frsize)
	}
	total = st.Blocks * bsize
	// Bavail is free for unprivileged; Bfree is free for root.
	free = st.Bavail * bsize
	if free > total {
		free = total
	}
	used = total - free
	return used, total, free, true
}
