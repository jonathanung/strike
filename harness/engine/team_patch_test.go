package engine

import (
	"errors"
	"strings"
	"testing"
)

func TestTeamPatchSubmitRejectApply(t *testing.T) {
	team := NewTeam("lead", "orchestrator")
	if team == nil {
		t.Fatal("nil team")
	}
	if !team.Enroll(TeamMember{SessionID: "child", ParentSessionID: "lead", Persona: "builder", Depth: 1}) {
		t.Fatal("enroll failed")
	}

	item, err := team.SubmitPatch("fix a", "*** Begin Patch\n*** End Patch", "child", []string{"a.go"}, "art1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "p1" || item.Status != PatchStatusPending || item.Version != 1 {
		t.Fatalf("item = %#v", item)
	}
	if item.ArtifactID != "art1" || item.ArtifactVersion != 2 {
		t.Fatalf("artifact link = %#v", item)
	}

	list := team.PatchesByStatus(PatchStatusPending)
	if len(list) != 1 {
		t.Fatalf("pending = %#v", list)
	}

	// Reject with wrong CAS.
	_, err = team.RejectPatch("p1", "lead", "nope", 99)
	if err == nil {
		t.Fatal("expected CAS conflict")
	}
	var conf *PatchConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("err = %v", err)
	}

	rej, err := team.RejectPatch("p1", "lead", "needs tests", 1)
	if err != nil {
		t.Fatal(err)
	}
	if rej.Status != PatchStatusRejected || rej.RejectReason != "needs tests" || rej.Version != 2 {
		t.Fatalf("rej = %#v", rej)
	}

	// Cannot apply rejected.
	_, err = team.MarkPatchApplied("p1", "lead", "sum", nil, 0)
	if err == nil {
		t.Fatal("expected reject gate")
	}
}

func TestTeamPatchApplyHappy(t *testing.T) {
	team := NewTeam("lead", "")
	if !team.Enroll(TeamMember{SessionID: "child", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll")
	}
	item, err := team.SubmitPatch("", "patch-body", "child", []string{"x.go"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := team.MarkPatchApplied(item.ID, "lead", "Success", []string{"x.go", "y.go"}, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != PatchStatusApplied || !strings.Contains(applied.AppliedSummary, "Success") {
		t.Fatalf("applied = %#v", applied)
	}
	if len(applied.Files) != 2 {
		t.Fatalf("files = %v", applied.Files)
	}
	// Idempotent re-apply.
	again, err := team.MarkPatchApplied(item.ID, "lead", "other", nil, 0)
	if err != nil || again.Status != PatchStatusApplied {
		t.Fatalf("again = %#v err=%v", again, err)
	}
}

func TestTeamPatchClearedOnDissolve(t *testing.T) {
	team := NewTeam("lead", "")
	if !team.Enroll(TeamMember{SessionID: "c", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll")
	}
	if _, err := team.SubmitPatch("t", "body", "c", nil, "", 0); err != nil {
		t.Fatal(err)
	}
	team.Dissolve()
	if len(team.Patches()) != 0 {
		t.Fatalf("patches after dissolve = %#v", team.Patches())
	}
	if _, err := team.SubmitPatch("t", "body", "lead", nil, "", 0); err == nil {
		t.Fatal("expected submit fail on dissolved team")
	}
}

func TestTeamPatchForeignActor(t *testing.T) {
	team := NewTeam("lead", "")
	if _, err := team.SubmitPatch("t", "body", "stranger", nil, "", 0); err == nil {
		t.Fatal("expected foreign submit fail")
	}
}
