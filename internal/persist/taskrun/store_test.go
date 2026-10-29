package taskrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func three(t *testing.T, s *Store) Run {
	t.Helper()
	run, err := s.Ensure("plan-1", "root-a", "Ship", "draft", []SectionInput{
		{ID: "s1", Title: "Write", Body: "the note"},
		{ID: "s2", Title: "Review", Body: "the note"},
		{ID: "s3", Title: "Publish", Body: "the note"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func section(run Run, id string) Section {
	for _, sec := range run.Sections {
		if sec.ID == id {
			return sec
		}
	}
	return Section{}
}

func TestEnsureChainsAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	run := three(t, s)
	if got := section(run, "s2").DependsOn; len(got) != 1 || got[0] != "s1" {
		t.Fatalf("s2 deps = %#v", got)
	}
	if got := section(run, "s3").DependsOn; len(got) != 1 || got[0] != "s2" {
		t.Fatalf("s3 deps = %#v", got)
	}
	if ready := ReadyIDs(run); len(ready) != 1 || ready[0] != "s1" {
		t.Fatalf("ready = %#v", ready)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok, err := s2.Get("plan-1")
	if err != nil || !ok {
		t.Fatalf("reopen ok=%v err=%v", ok, err)
	}
	if got.Version != run.Version || section(got, "s1").Title != "Write" || section(got, "s1").Body != "the note" {
		t.Fatalf("reopen = %#v", got)
	}
	if ready := ReadyIDs(got); len(ready) != 1 || ready[0] != "s1" {
		t.Fatalf("ready after restart = %#v", ready)
	}
}

func TestFailedSectionBlocksDependantsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	run := three(t, s)
	run, err = s.ClaimOne(run.PlanID, "s1")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.BindChild(run.PlanID, "s1", "child-a")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.ApplyChild(run.PlanID, "s1", Outcome{
		ChildID:       "child-a",
		ChildStatus:   "blocked",
		Incomplete:    true,
		HasVerify:     true,
		VerifyPassed:  false,
		VerifySummary: "schema:handoff failed",
		Summary:       "prose only",
		Checks:        []Check{{Kind: "schema", Name: "handoff", Passed: false, Error: "incomplete"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if section(run, "s1").Status != StatusFailed {
		t.Fatalf("s1 = %s", section(run, "s1").Status)
	}
	if section(run, "s1").Checkpoint == nil || !section(run, "s1").Checkpoint.Incomplete {
		t.Fatalf("checkpoint = %#v", section(run, "s1").Checkpoint)
	}
	if section(run, "s2").Status != StatusBlocked || section(run, "s3").Status != StatusBlocked {
		t.Fatalf("dependants = %s %s", section(run, "s2").Status, section(run, "s3").Status)
	}
	if ready := ReadyIDs(run); len(ready) != 0 {
		t.Fatalf("ready after failure = %#v", ready)
	}
	if _, err := s.ClaimOne(run.PlanID, "s2"); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("claim dependant = %v", err)
	}
	if _, err := s.ClaimOne(run.PlanID, "s3"); err == nil {
		t.Fatal("claim transitive dependant succeeded")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok, err := s2.Get("plan-1")
	if err != nil || !ok {
		t.Fatalf("reopen ok=%v err=%v", ok, err)
	}
	if section(got, "s1").Status != StatusFailed {
		t.Fatalf("restart s1 = %s", section(got, "s1").Status)
	}
	if section(got, "s2").Status != StatusBlocked || section(got, "s3").Status != StatusBlocked {
		t.Fatalf("restart dependants = %s %s detail=%q", section(got, "s2").Status, section(got, "s3").Status, section(got, "s2").Detail)
	}
	if ready := ReadyIDs(got); len(ready) != 0 {
		t.Fatalf("ready after restart = %#v", ready)
	}
	if _, err := s2.ClaimOne(got.PlanID, "s2"); err == nil {
		t.Fatal("restart claim of blocked dependant succeeded")
	}
	if cp := section(got, "s1").Checkpoint; cp == nil || cp.VerifySummary != "schema:handoff failed" || cp.ChildID != "child-a" {
		t.Fatalf("checkpoint lost across restart: %#v", cp)
	}
}

func TestCheckpointDoesNotContinueUntilApprove(t *testing.T) {
	s := openStore(t)
	run := three(t, s)
	run, err := s.ClaimOne(run.PlanID, "s1")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.BindChild(run.PlanID, "s1", "child-ok")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.ApplyChild(run.PlanID, "s1", Outcome{
		ChildID:      "child-ok",
		ChildStatus:  "completed",
		Summary:      "wrote the note",
		HasVerify:    true,
		VerifyPassed: true,
		FilesChanged: []string{"note.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if section(run, "s1").Status != StatusCheckpoint {
		t.Fatalf("s1 = %s", section(run, "s1").Status)
	}
	if section(run, "s2").Status != StatusPending {
		t.Fatalf("s2 = %s (want pending, not running)", section(run, "s2").Status)
	}
	if ready := ReadyIDs(run); len(ready) != 0 {
		t.Fatalf("ready before approve = %#v", ready)
	}
	run, err = s.Approve(run.PlanID, "s1", run.Version)
	if err != nil {
		t.Fatal(err)
	}
	if ready := ReadyIDs(run); len(ready) != 1 || ready[0] != "s2" {
		t.Fatalf("ready after approve = %#v", ready)
	}
	if section(run, "s3").Status != StatusPending {
		t.Fatalf("s3 = %s", section(run, "s3").Status)
	}
	if _, err := s.ClaimOne(run.PlanID, "s3"); err == nil {
		t.Fatal("claimed s3 while it still depends on s2")
	}
}

func TestSkipFailedUnblocksAndRetryRestores(t *testing.T) {
	s := openStore(t)
	run := three(t, s)
	run, err := s.ClaimOne(run.PlanID, "s1")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.BindChild(run.PlanID, "s1", "child-a")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.ApplyChild(run.PlanID, "s1", Outcome{ChildID: "child-a", ChildStatus: "failed", Summary: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.Skip(run.PlanID, "s1", run.Version)
	if err != nil {
		t.Fatal(err)
	}
	if section(run, "s1").Status != StatusSkipped {
		t.Fatalf("s1 = %s", section(run, "s1").Status)
	}
	if ready := ReadyIDs(run); len(ready) != 1 || ready[0] != "s2" {
		t.Fatalf("ready after skip = %#v", ready)
	}
	if section(run, "s3").Status == StatusBlocked {
		t.Fatal("s3 stayed blocked after s1 was skipped and s2 is pending")
	}

	run, err = s.Retry(run.PlanID, "s2", run.Version)
	if err == nil {
		t.Fatal("retry of a pending section succeeded")
	}
}

func TestRetryFailedClearsBlock(t *testing.T) {
	s := openStore(t)
	run := three(t, s)
	run = failFirst(t, s, run)
	run, err := s.Retry(run.PlanID, "s1", run.Version)
	if err != nil {
		t.Fatal(err)
	}
	if section(run, "s1").Status != StatusPending || section(run, "s1").ChildID != "" || section(run, "s1").Checkpoint != nil {
		t.Fatalf("retried = %#v", section(run, "s1"))
	}
	if section(run, "s2").Status != StatusPending {
		t.Fatalf("s2 = %s", section(run, "s2").Status)
	}
	if ready := ReadyIDs(run); len(ready) != 1 || ready[0] != "s1" {
		t.Fatalf("ready = %#v", ready)
	}
	if _, err := s.ClaimOne(run.PlanID, "s2"); err == nil {
		t.Fatal("dependant claimed while predecessor is only pending")
	}
}

func TestReorderDoesNotRouteAroundFailure(t *testing.T) {
	s := openStore(t)
	run := three(t, s)
	run = failFirst(t, s, run)
	run, err := s.Reorder(run.PlanID, []string{"s3", "s2", "s1"}, run.Version)
	if err != nil {
		t.Fatal(err)
	}
	if run.Sections[0].ID != "s3" {
		t.Fatalf("order = %#v", run.Sections)
	}
	if deps := section(run, "s2").DependsOn; len(deps) != 1 || deps[0] != "s1" {
		t.Fatalf("s2 deps = %#v", deps)
	}
	if section(run, "s2").Status != StatusBlocked || section(run, "s3").Status != StatusBlocked {
		t.Fatalf("after reorder = %s %s", section(run, "s2").Status, section(run, "s3").Status)
	}
	if ready := ReadyIDs(run); len(ready) != 0 {
		t.Fatalf("ready = %#v", ready)
	}
}

func TestReorderPendingRewritesChain(t *testing.T) {
	s := openStore(t)
	run := three(t, s)
	run, err := s.Reorder(run.PlanID, []string{"s3", "s1", "s2"}, run.Version)
	if err != nil {
		t.Fatal(err)
	}
	if deps := section(run, "s3").DependsOn; len(deps) != 0 {
		t.Fatalf("s3 deps = %#v", deps)
	}
	if deps := section(run, "s1").DependsOn; len(deps) != 1 || deps[0] != "s3" {
		t.Fatalf("s1 deps = %#v", deps)
	}
	if ready := ReadyIDs(run); len(ready) != 1 || ready[0] != "s3" {
		t.Fatalf("ready = %#v", ready)
	}
}

func TestUnboundClaimReturnsToPendingOnRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	run := three(t, s)
	if _, err := s.ClaimOne(run.PlanID, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok, err := s2.Get("plan-1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if section(got, "s1").Status != StatusPending {
		t.Fatalf("unbound claim status = %s", section(got, "s1").Status)
	}
}

func TestObjectiveMentionsHandoffGate(t *testing.T) {
	text := SectionObjective("Ship", Section{ID: "s1", Title: "Write", Body: "the note"})
	for _, want := range []string{"the note", "schema:handoff", "s1", "Write"} {
		if !strings.Contains(text, want) {
			t.Fatalf("objective missing %q:\n%s", want, text)
		}
	}
}

func TestOpenRejectsEmptyProject(t *testing.T) {
	if _, err := Open("", "p"); err == nil {
		t.Fatal("empty root")
	}
	if _, err := Open(t.TempDir(), ""); err == nil {
		t.Fatal("empty key")
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("stat")
	}
}

func failFirst(t *testing.T, s *Store, run Run) Run {
	t.Helper()
	var err error
	run, err = s.ClaimOne(run.PlanID, "s1")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.BindChild(run.PlanID, "s1", "child-a")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.ApplyChild(run.PlanID, "s1", Outcome{ChildID: "child-a", ChildStatus: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	return run
}
