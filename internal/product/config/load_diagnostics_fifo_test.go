//go:build unix

package config

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLoadSkillsReportRejectsFIFO guards #1855: a FIFO named like a skill
// is reported as unreadable instead of blocking the load on open.
func TestLoadSkillsReportRejectsFIFO(t *testing.T) {
	_, work := isolateLoadHome(t)
	writeMD(t, filepath.Join(work, ".strike", "skills", "ok.md"), "---\ndescription: fine\n---\nBody.\n")
	pipe := filepath.Join(work, ".strike", "skills", "pipe.md")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	type result struct {
		skills []Skill
		diags  []LoadDiagnostic
		err    error
	}
	done := make(chan result, 1)
	go func() {
		s, d, err := LoadSkillsReport(work)
		done <- result{s, d, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("LoadSkillsReport blocked on a FIFO")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	d, ok := diagFor(r.diags, "pipe.md", DiagUnreadable)
	if !ok || d.Severity != "error" || !strings.Contains(d.Message, "not a regular file") {
		t.Fatalf("pipe diagnostic = %+v (all %v)", d, r.diags)
	}
	found := false
	for _, s := range r.skills {
		found = found || s.Name == "ok"
	}
	if !found {
		t.Fatal("regular skill next to the FIFO must still load")
	}
}
