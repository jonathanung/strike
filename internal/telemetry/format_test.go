package telemetry

import (
	"strings"
	"testing"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		n    uint64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{10*1024*1024*1024 + 100*1024*1024, "10.1 GB"},
		{32 * 1024 * 1024 * 1024, "32 GB"},
		{1024 * 1024 * 1024 * 1024, "1 TB"},
	}
	for _, tt := range tests {
		if got := FormatBytes(tt.n); got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestFormatPercent(t *testing.T) {
	if got := FormatPercent(31.64); got != "31.6%" {
		t.Errorf("FormatPercent = %q", got)
	}
	if got := FormatPercent(-1); got != "0.0%" {
		t.Errorf("neg = %q", got)
	}
	if got := FormatPercent(150); got != "100.0%" {
		t.Errorf("clamp = %q", got)
	}
}

func TestFormatLinesUnavailable(t *testing.T) {
	var s Sample
	if got := FormatMemLine(s); got != Unavailable {
		t.Errorf("mem = %q", got)
	}
	if got := FormatCPULine(s, true); got != Unavailable {
		t.Errorf("cpu = %q", got)
	}
	if got := FormatDiskLine(s); got != Unavailable {
		t.Errorf("disk = %q", got)
	}
}

func TestFormatLinesKnown(t *testing.T) {
	s := Sample{
		CPUHostOK: true, CPUHostPct: 42.3,
		CPUProcOK: true, CPUProcPct: 1.2,
		MemOK: true, MemUsedBytes: 10*1024*1024*1024 + 100*1024*1024, MemTotalBytes: 32 * 1024 * 1024 * 1024,
		DiskOK: true, DiskUsedBytes: 287 * 1024 * 1024 * 1024, DiskTotalBytes: 494 * 1024 * 1024 * 1024,
		DiskFreeBytes: 207 * 1024 * 1024 * 1024,
	}
	mem := FormatMemLine(s)
	if !strings.Contains(mem, "used") || !strings.Contains(mem, "%") {
		t.Errorf("mem line = %q", mem)
	}
	cpu := FormatCPULine(s, true)
	if !strings.Contains(cpu, "42.3%") || !strings.Contains(cpu, "proc") {
		t.Errorf("cpu line = %q", cpu)
	}
	if got := FormatCPULine(s, false); strings.Contains(got, "proc") {
		t.Errorf("cpu without proc = %q", got)
	}
	disk := FormatDiskLine(s)
	if !strings.Contains(disk, "free") || !strings.Contains(disk, "used") {
		t.Errorf("disk line = %q", disk)
	}
}

func TestRatioAndLevel(t *testing.T) {
	if _, ok := Ratio(1, 0); ok {
		t.Error("total 0 should be unknown")
	}
	r, ok := Ratio(50, 100)
	if !ok || r != 0.5 {
		t.Errorf("ratio = %v %v", r, ok)
	}
	if LevelOf(0.5, true) != LevelNormal {
		t.Error("normal")
	}
	if LevelOf(0.7, true) != LevelWarning {
		t.Error("warn at 0.7")
	}
	if LevelOf(0.91, true) != LevelCritical {
		t.Error("crit")
	}
	if LevelOf(0, false) != LevelUnknown {
		t.Error("unknown")
	}
	pr, known := PercentRatio(42.3, true)
	if !known || pr < 0.42 || pr > 0.43 {
		t.Errorf("pct ratio = %v", pr)
	}
	if _, known := PercentRatio(0, false); known {
		t.Error("pct unknown")
	}
}

func TestSampleRatios(t *testing.T) {
	var s Sample
	if _, ok := s.MemRatio(); ok {
		t.Error("mem unknown")
	}
	s.MemOK = true
	s.MemUsedBytes = 25
	s.MemTotalBytes = 100
	if r, ok := s.MemRatio(); !ok || r != 0.25 {
		t.Errorf("mem ratio = %v %v", r, ok)
	}
	s.DiskOK = true
	s.DiskUsedBytes = 50
	s.DiskTotalBytes = 100
	if r, ok := s.DiskRatio(); !ok || r != 0.5 {
		t.Errorf("disk ratio = %v %v", r, ok)
	}
	s.CPUHostOK = true
	s.CPUHostPct = 80
	if r, ok := s.CPURatio(); !ok || r != 0.8 {
		t.Errorf("cpu ratio = %v %v", r, ok)
	}
}
