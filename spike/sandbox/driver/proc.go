package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func parseKiBField(text, key string) (int64, bool) {
	prefix := key + ":"
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// parseStatPPID reads the ppid field from /proc/pid/stat. comm may contain
// spaces and parentheses, so the parse starts after the last ')'.
func parseStatPPID(stat string) (int, bool) {
	i := strings.LastIndex(stat, ")")
	if i < 0 || i+2 >= len(stat) {
		return 0, false
	}
	fields := strings.Fields(stat[i+2:])
	if len(fields) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

type procSample struct {
	VmRSSKiB int64
	VmHWMKiB int64
}

func readProcSample(procRoot string, pid int) (procSample, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return procSample{}, err
	}
	rss, ok := parseKiBField(string(b), "VmRSS")
	if !ok {
		return procSample{}, fmt.Errorf("VmRSS missing for %d", pid)
	}
	hwm, ok := parseKiBField(string(b), "VmHWM")
	if !ok {
		return procSample{}, fmt.Errorf("VmHWM missing for %d", pid)
	}
	return procSample{VmRSSKiB: rss, VmHWMKiB: hwm}, nil
}

// treeVmRSS sums VmRSS of pid and its descendants under procRoot.
func treeVmRSS(procRoot string, root int) (int64, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, err
	}
	type node struct {
		ppid int
		rss  int64
	}
	nodes := map[int]node{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue
		}
		ppid, ok := parseStatPPID(string(stat))
		if !ok {
			continue
		}
		status, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "status"))
		if err != nil {
			continue
		}
		rss, ok := parseKiBField(string(status), "VmRSS")
		if !ok {
			continue
		}
		nodes[pid] = node{ppid: ppid, rss: rss}
	}
	if _, ok := nodes[root]; !ok {
		return 0, fmt.Errorf("pid %d not in %s", root, procRoot)
	}
	var sum int64
	seen := map[int]bool{}
	var walk func(int)
	walk = func(pid int) {
		if seen[pid] {
			return
		}
		seen[pid] = true
		n, ok := nodes[pid]
		if !ok {
			return
		}
		sum += n.rss
		for child, cn := range nodes {
			if cn.ppid == pid {
				walk(child)
			}
		}
	}
	walk(root)
	return sum, nil
}

func hostMemAvailableKiB() (int64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	n, ok := parseKiBField(string(b), "MemAvailable")
	if !ok {
		return 0, fmt.Errorf("MemAvailable missing")
	}
	return n, nil
}
