//go:build linux

package terminal

import (
	"os"
	"strconv"
	"strings"
)

func processCWD(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil || p == "" {
		return "", false
	}
	return p, true
}

func cwdUnseenIsEscape() bool { return true }

// foregroundInside reports whether every process in the leader's foreground
// process group is still inside root. seen is false when none of those cwds
// could be read. The walk exists only to find that group (bash behind bwrap,
// or a foreground child). Other children are not treated as the shell.
func foregroundInside(root string, leader int) (bool, bool) {
	pids := foregroundPIDs(leader)
	if len(pids) == 0 {
		return false, false
	}
	saw := false
	for _, pid := range pids {
		cwd, ok := processCWD(pid)
		if !ok {
			continue
		}
		saw = true
		resolved := cwd
		if real, err := canonicalDir(cwd); err == nil {
			resolved = real
		}
		if !inside(root, resolved) {
			return false, true
		}
	}
	if !saw {
		return false, false
	}
	return true, true
}

func foregroundPIDs(leader int) []int {
	if leader <= 0 {
		return nil
	}
	pgrp, tpgid, ok := procStat(leader)
	if !ok {
		return []int{leader}
	}
	target := tpgid
	if target <= 0 {
		target = pgrp
	}
	seen := map[int]struct{}{}
	var out []int
	var walk func(int, int)
	walk = func(pid, depth int) {
		if pid <= 0 || depth > 8 {
			return
		}
		if _, ok := seen[pid]; ok {
			return
		}
		seen[pid] = struct{}{}
		pg, _, stOK := procStat(pid)
		if stOK && (pid == target || pg == target) {
			out = append(out, pid)
		}
		for _, child := range childPIDs(pid) {
			walk(child, depth+1)
		}
	}
	walk(leader, 0)
	if len(out) == 0 {
		return []int{leader}
	}
	return out
}

func childPIDs(pid int) []int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/task/" + strconv.Itoa(pid) + "/children")
	if err != nil || len(b) == 0 {
		return nil
	}
	fields := strings.Fields(string(b))
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n <= 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}

// procStat reads pgrp and tpgid. comm may contain spaces, so the fields after
// the last ')' are what the kernel documents.
func procStat(pid int) (pgrp, tpgid int, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return 0, 0, false
	}
	fields := strings.Fields(s[i+2:])
	// state, ppid, pgrp, session, tty_nr, tpgid
	if len(fields) < 6 {
		return 0, 0, false
	}
	pgrp, err1 := strconv.Atoi(fields[2])
	tpgid, err2 := strconv.Atoi(fields[5])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return pgrp, tpgid, true
}
