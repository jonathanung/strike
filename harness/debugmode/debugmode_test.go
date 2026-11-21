package debugmode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func twoHypotheses() []Hypothesis {
	return []Hypothesis{
		{ID: "h1", Statement: "cache key ignores locale", Confirms: "two locales share a key", Refutes: "keys differ"},
		{ID: "h2", Statement: "race on refresh"},
	}
}

func TestSetHypothesesValidation(t *testing.T) {
	tests := []struct {
		name    string
		hs      []Hypothesis
		wantErr string
	}{
		{name: "too few", hs: twoHypotheses()[:1], wantErr: "need 2–5"},
		{name: "too many", hs: make([]Hypothesis, 6), wantErr: "need 2–5"},
		{name: "empty id", hs: []Hypothesis{{ID: " ", Statement: "a"}, {ID: "b", Statement: "b"}}, wantErr: "empty id"},
		{name: "duplicate id", hs: []Hypothesis{{ID: "a", Statement: "a"}, {ID: "a", Statement: "b"}}, wantErr: "duplicate"},
		{name: "empty statement", hs: []Hypothesis{{ID: "a", Statement: "a"}, {ID: "b"}}, wantErr: "empty statement"},
		{name: "ok", hs: twoHypotheses()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().SetHypotheses(tt.hs)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSetHypothesesAssignsRankAndOpen(t *testing.T) {
	s := New()
	in := twoHypotheses()
	in[0].Status = StatusConfirmed // caller-supplied status is ignored
	if err := s.SetHypotheses(in); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	for i, h := range snap.Hypotheses {
		if h.Rank != i+1 || h.Status != StatusOpen {
			t.Fatalf("hypothesis %d = rank %d status %s", i, h.Rank, h.Status)
		}
	}
}

func TestLifecycleWithEvidence(t *testing.T) {
	s := New()
	if err := s.SetHypotheses(twoHypotheses()); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlockFix(""); err == nil {
		t.Fatal("UnlockFix without confirmed hypothesis or override should fail")
	}
	if err := s.Mark("h1", StatusConfirmed); err == nil {
		t.Fatal("confirming without evidence should fail")
	}
	if err := s.Attach("h1", Evidence{Source: "repro", Text: "key=en for fr request"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Attach("nope", Evidence{Text: "x"}); err == nil {
		t.Fatal("attach to unknown hypothesis should fail")
	}
	if err := s.Attach("h1", Evidence{Text: "  "}); err == nil {
		t.Fatal("empty evidence should fail")
	}
	if err := s.Mark("h1", StatusConfirmed); err != nil {
		t.Fatal(err)
	}
	if err := s.Mark("h2", StatusRefuted); err != nil {
		t.Fatal(err)
	}
	if err := s.Mark("h2", Status("maybe")); err == nil {
		t.Fatal("invalid status should fail")
	}
	if err := s.MarkFixed(); err == nil {
		t.Fatal("MarkFixed before fix phase should fail")
	}
	if err := s.UnlockFix(""); err != nil {
		t.Fatal(err)
	}
	if got := s.Phase(); got != PhaseFix {
		t.Fatalf("phase = %s, want fix", got)
	}
	if s.Snapshot().Override != "" {
		t.Fatal("override must stay empty when evidence satisfied the gate")
	}
	if err := s.SetHypotheses(twoHypotheses()); err == nil {
		t.Fatal("SetHypotheses after fix unlock should fail")
	}
	if _, err := s.End(t.TempDir()); err == nil {
		t.Fatal("End before MarkFixed should fail")
	}
	if err := s.MarkFixed(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.End(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := s.Phase(); got != PhaseClosed {
		t.Fatalf("phase = %s, want closed", got)
	}
	if err := s.Mark("h1", StatusOpen); err == nil {
		t.Fatal("Mark after close should fail")
	}
}

func TestUnlockFixOverrideRecorded(t *testing.T) {
	s := New()
	if err := s.SetHypotheses(twoHypotheses()); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlockFix("  user: known typo, skip repro  "); err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Override; got != "user: known typo, skip repro" {
		t.Fatalf("override = %q", got)
	}
	if err := s.UnlockFix("again"); err == nil {
		t.Fatal("second UnlockFix should fail")
	}
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	s := New()
	if err := s.SetHypotheses(twoHypotheses()); err != nil {
		t.Fatal(err)
	}
	if err := s.Attach("h1", Evidence{Text: "a"}); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	snap.Hypotheses[0].Evidence[0].Text = "mutated"
	snap.Hypotheses[0].Status = StatusConfirmed
	again := s.Snapshot()
	if again.Hypotheses[0].Evidence[0].Text != "a" || again.Hypotheses[0].Status != StatusOpen {
		t.Fatal("snapshot mutation leaked into session")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheckToolCallHypothesisPhase(t *testing.T) {
	tag := "log.Printf(\"" + Marker + " key=%v\", k)"
	tests := []struct {
		name  string
		tool  string
		args  any
		allow bool
	}{
		{name: "read passes", tool: "read", args: map[string]string{"filePath": "a.go"}, allow: true},
		{name: "bash passes gate", tool: "bash", args: map[string]string{"command": "go test"}, allow: true},
		{name: "edit adds tagged line", tool: "edit", args: map[string]string{
			"oldString": "k := key(r)\n", "newString": "k := key(r)\n" + tag + "\n"}, allow: true},
		{name: "edit removes tagged line", tool: "edit", args: map[string]string{
			"oldString": "k := key(r)\n" + tag + "\n", "newString": "k := key(r)\n"}, allow: true},
		{name: "edit adds blank and tagged", tool: "edit", args: map[string]string{
			"oldString": "a\n", "newString": "a\n\n" + tag}, allow: true},
		{name: "edit changes code", tool: "edit", args: map[string]string{
			"oldString": "k := key(r)", "newString": "k := key(r, locale)"}, allow: false},
		{name: "edit removes code", tool: "edit", args: map[string]string{
			"oldString": "a\nb\n", "newString": "a\n"}, allow: false},
		{name: "edit bad args", tool: "edit", args: "not an object", allow: false},
		{name: "write all tagged", tool: "write", args: map[string]string{"content": tag + "\n\n" + tag + "\n"}, allow: true},
		{name: "write untagged", tool: "write", args: map[string]string{"content": "package main\n" + tag}, allow: false},
		{name: "patch tagged", tool: "apply_patch", args: map[string]string{"patch": "*** Begin Patch\n*** Update File: a.go\n@@\n k := key(r)\n+" + tag + "\n*** End Patch\n"}, allow: true},
		{name: "patch untagged", tool: "apply_patch", args: map[string]string{"patch": "*** Begin Patch\n*** Update File: a.go\n@@\n-k := key(r)\n+k := key(r, l)\n*** End Patch\n"}, allow: false},
		{name: "patch delete", tool: "apply_patch", args: map[string]string{"patch": "*** Begin Patch\n*** Delete File: a.go\n*** End Patch\n"}, allow: false},
		{name: "patch move", tool: "apply_patch", args: map[string]string{"patch": "*** Begin Patch\n*** Update File: a.go\n*** Move to: b.go\n*** End Patch\n"}, allow: false},
		{name: "delete denied", tool: "delete", args: map[string]string{"path": "a.go"}, allow: false},
		{name: "move denied", tool: "move", args: map[string]string{"from": "a", "to": "b"}, allow: false},
	}
	s := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.CheckToolCall(tt.tool, mustJSON(t, tt.args))
			if tt.allow {
				if err != nil {
					t.Fatalf("want allow, got %v", err)
				}
				return
			}
			var ge *GateError
			if !errors.As(err, &ge) {
				t.Fatalf("want *GateError, got %v", err)
			}
			if ge.Tool != tt.tool || ge.Reason == "" {
				t.Fatalf("gate error = %+v", ge)
			}
		})
	}
}

func TestCheckToolCallFixPhaseAllowsAll(t *testing.T) {
	s := New()
	if err := s.SetHypotheses(twoHypotheses()); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlockFix("override"); err != nil {
		t.Fatal(err)
	}
	err := s.CheckToolCall("edit", mustJSON(t, map[string]string{"oldString": "a", "newString": "b"}))
	if err != nil {
		t.Fatalf("fix phase should not gate edits: %v", err)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestSweepRemovesTaggedLines(t *testing.T) {
	root := t.TempDir()
	tag := "\tlog.Println(\"" + Marker + "\", k)"
	writeFile(t, filepath.Join(root, "a.go"), "package a\n"+tag+"\nfunc A() {}\r\n"+tag, 0o640)
	writeFile(t, filepath.Join(root, "sub", "clean.go"), "package sub\n", 0o644)
	writeFile(t, filepath.Join(root, ".git", "HEAD"), Marker+"\n", 0o644)
	writeFile(t, filepath.Join(root, "bin.dat"), "\x00"+Marker+"\n", 0o644)

	hits, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Path != "a.go" || hits[0].Line != 2 || hits[1].Line != 4 {
		t.Fatalf("scan hits = %+v", hits)
	}

	rep, err := Sweep(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 2 || len(rep.Remaining) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	got, err := os.ReadFile(filepath.Join(root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package a\nfunc A() {}\r\n" {
		t.Fatalf("a.go after sweep = %q", got)
	}
	info, err := os.Stat(filepath.Join(root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", info.Mode().Perm())
	}
	// .git and binary files are left alone.
	if b, _ := os.ReadFile(filepath.Join(root, ".git", "HEAD")); !strings.Contains(string(b), Marker) {
		t.Fatal(".git must not be swept")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "bin.dat")); !strings.Contains(string(b), Marker) {
		t.Fatal("binary files must not be swept")
	}
}

func TestEndSweepsAndCloses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.py"), "x = 1\nprint('"+Marker+"', x)\n", 0o644)
	s := New()
	if err := s.SetHypotheses(twoHypotheses()); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlockFix("user override"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFixed(); err != nil {
		t.Fatal(err)
	}
	rep, err := s.End(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].Path != "x.py" {
		t.Fatalf("removed = %+v", rep.Removed)
	}
	if hits, _ := Scan(root); len(hits) != 0 {
		t.Fatalf("tagged lines remain: %+v", hits)
	}
}

func TestEndMissingRootKeepsSessionOpen(t *testing.T) {
	s := New()
	if err := s.SetHypotheses(twoHypotheses()); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlockFix("override"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFixed(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.End(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("End on missing root should fail")
	}
	if got := s.Phase(); got != PhaseFix {
		t.Fatalf("phase = %s, want fix after failed sweep", got)
	}
}

func TestMarkerNotInOwnSource(t *testing.T) {
	// The sweep must never strip this package's own source if run on the repo.
	for _, f := range []string{"debugmode.go", "gate.go", "sweep.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), Marker) {
			t.Fatalf("%s contains the literal marker", f)
		}
	}
}
