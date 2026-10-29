package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseKiBAndStat(t *testing.T) {
	text := "Name:\tqemu\nVmRSS:\t  2048 kB\nVmHWM:\t4096 kB\n"
	rss, ok := parseKiBField(text, "VmRSS")
	if !ok || rss != 2048 {
		t.Fatalf("rss %d %v", rss, ok)
	}
	hwm, ok := parseKiBField(text, "VmHWM")
	if !ok || hwm != 4096 {
		t.Fatalf("hwm %d %v", hwm, ok)
	}
	ppid, ok := parseStatPPID("12 (qemu (tcg)) S 7 12 12 0")
	if !ok || ppid != 7 {
		t.Fatalf("ppid %d %v", ppid, ok)
	}
}

func TestTreeVmRSS(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, 1, 0, 100, "init")
	writeProc(t, root, 20, 1, 1000, "virtiofsd (main)")
	writeProc(t, root, 21, 20, 300, "virtiofsd-worker")
	writeProc(t, root, 30, 1, 9999, "other")
	sum, err := treeVmRSS(root, 20)
	if err != nil {
		t.Fatal(err)
	}
	if sum != 1300 {
		t.Fatalf("sum = %d, want 1300", sum)
	}
}

func writeProc(t *testing.T, root string, pid, ppid int, rss int, comm string) {
	t.Helper()
	dir := filepath.Join(root, itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := itoa(pid) + " (" + comm + ") S " + itoa(ppid) + " 0 0\n"
	status := "Name:\t" + comm + "\nVmRSS:\t" + itoa(rss) + " kB\nVmHWM:\t" + itoa(rss) + " kB\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
