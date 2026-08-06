package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestPatchCollabSubmitPreviewRejectApply(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Engine{opts: Options{SessionID: "lead", WorkDir: dir}}
	e.team = NewTeam("lead", "orch")
	if !e.team.Enroll(TeamMember{SessionID: "child", ParentSessionID: "lead", Persona: "build", Depth: 1}) {
		t.Fatal("enroll")
	}
	// Act as child for submit.
	e.opts.SessionID = "child"

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		"-old",
		"+new",
		"*** End Patch",
	}, "\n")

	sub, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action:  "submit",
		Patch:   patch,
		Title:   "flip a",
		WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Patch == nil || sub.Patch.ID != "p1" || sub.Patch.Status != "pending" {
		t.Fatalf("submit = %#v", sub)
	}
	if len(sub.Files) != 1 || sub.Files[0] != "a.txt" {
		t.Fatalf("files = %v", sub.Files)
	}
	// Disk unchanged after submit.
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "old\n" {
		t.Fatalf("disk mutated on submit: %q", data)
	}

	// Lead previews.
	e.opts.SessionID = "lead"
	prev, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action:  "preview",
		ID:      "p1",
		WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prev.Preview == nil || !prev.Preview.Valid {
		t.Fatalf("preview = %#v", prev)
	}
	if prev.Conflict {
		t.Fatalf("unexpected conflict: %#v", prev.Conflicts)
	}

	// Reject without apply.
	rej, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action:  "reject",
		ID:      "p1",
		Reason:  "style",
		WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rej.Patch == nil || rej.Patch.Status != "rejected" {
		t.Fatalf("reject = %#v", rej)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "old\n" {
		t.Fatalf("disk mutated on reject: %q", data)
	}
}

func TestPatchCollabApplyAndFilesChanged(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old\n"), 0o644)
	e := &Engine{opts: Options{SessionID: "lead", WorkDir: dir}}
	e.team = NewTeam("lead", "")
	if !e.team.Enroll(TeamMember{SessionID: "child", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll")
	}
	e.opts.SessionID = "child"
	e.turnDiff = &tool.TurnDiff{}

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		"-old",
		"+new",
		"*** End Patch",
	}, "\n")
	sub, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action: "submit", Patch: patch, WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.opts.SessionID = "lead"
	app, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action: "apply", ID: sub.Patch.ID, WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.Conflict || app.Patch == nil || app.Patch.Status != "applied" {
		t.Fatalf("apply = %#v", app)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(data) != "new\n" {
		t.Fatalf("content = %q err=%v", data, err)
	}
	// Engine tracked mutation for handoff files_changed.
	tracked := e.mutatedPathsSnapshot()
	if len(tracked) != 1 || tracked[0] != "a.txt" {
		t.Fatalf("mutated = %v", tracked)
	}
	// Ownership graph recorded.
	own := e.team.Ownership()
	if own == nil {
		t.Fatal("nil ownership")
	}
	snap := own.Snapshot()
	found := false
	for _, c := range snap.Claims {
		if strings.Contains(c.Path, "a.txt") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ownership claims missing a.txt: %#v", snap.Claims)
	}
}

func TestPatchCollabConflictDetectBlocksApply(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644)
	e := &Engine{opts: Options{SessionID: "lead", WorkDir: dir}}
	e.team = NewTeam("lead", "")
	if !e.team.Enroll(TeamMember{SessionID: "c1", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll c1")
	}
	if !e.team.Enroll(TeamMember{SessionID: "c2", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll c2")
	}

	pBody := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		"-x",
		"+y",
		"*** End Patch",
	}, "\n")
	e.opts.SessionID = "c1"
	s1, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action: "submit", Patch: pBody, WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.opts.SessionID = "c2"
	// Second pending touches same path.
	pBody2 := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		"-x",
		"+z",
		"*** End Patch",
	}, "\n")
	s2, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action: "submit", Patch: pBody2, WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s1.Patch.ID == s2.Patch.ID {
		t.Fatal("expected distinct ids")
	}

	e.opts.SessionID = "lead"
	conf, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action: "conflicts", WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !conf.Conflict || conf.Conflicts == nil || !conf.Conflicts.HasConflict {
		t.Fatalf("expected conflicts: %#v", conf)
	}

	app, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action: "apply", ID: s1.Patch.ID, WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !app.Conflict {
		t.Fatalf("apply should refuse overlapping pending: %#v", app)
	}
	// Disk unchanged.
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "x\n" {
		t.Fatalf("disk = %q", data)
	}
}

func TestPatchCollabNonOverlappingSequentialApply(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	e := &Engine{opts: Options{SessionID: "lead", WorkDir: dir}}
	e.team = NewTeam("lead", "")
	if !e.team.Enroll(TeamMember{SessionID: "c1", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll c1")
	}
	if !e.team.Enroll(TeamMember{SessionID: "c2", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll c2")
	}

	pa := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		"-a",
		"+A",
		"*** End Patch",
	}, "\n")
	pb := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: b.txt",
		"@@",
		"-b",
		"+B",
		"*** End Patch",
	}, "\n")
	e.opts.SessionID = "c1"
	s1, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{Action: "submit", Patch: pa, WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	e.opts.SessionID = "c2"
	s2, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{Action: "submit", Patch: pb, WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	e.opts.SessionID = "lead"
	// No path overlap — both apply cleanly in sequence.
	a1, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{Action: "apply", ID: s1.Patch.ID, WorkDir: dir})
	if err != nil || a1.Conflict {
		t.Fatalf("apply1 = %#v err=%v", a1, err)
	}
	a2, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{Action: "apply", ID: s2.Patch.ID, WorkDir: dir})
	if err != nil || a2.Conflict {
		t.Fatalf("apply2 = %#v err=%v", a2, err)
	}
	da, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	db, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	if string(da) != "A\n" || string(db) != "B\n" {
		t.Fatalf("a=%q b=%q", da, db)
	}
}

func TestPatchCollabNoTeam(t *testing.T) {
	e := &Engine{opts: Options{SessionID: "solo", WorkDir: t.TempDir()}}
	_, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{Action: "list"})
	if err == nil || !strings.Contains(err.Error(), "no team") {
		t.Fatalf("err = %v", err)
	}
}

// Stale pending on an unrelated path must not block apply of a clean patch.
func TestPatchCollabApplyIgnoresUnrelatedStalePending(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	e := &Engine{opts: Options{SessionID: "lead", WorkDir: dir}}
	e.team = NewTeam("lead", "")
	if !e.team.Enroll(TeamMember{SessionID: "c1", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll")
	}
	if !e.team.Enroll(TeamMember{SessionID: "c2", ParentSessionID: "lead", Depth: 1}) {
		t.Fatal("enroll")
	}

	// p_stale targets b.txt with wrong old lines (invalid against base).
	stale := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: b.txt",
		"@@",
		"-nope",
		"+B",
		"*** End Patch",
	}, "\n")
	// Force onto board with precomputed files (bypass submit validation).
	if _, err := e.team.SubmitPatch("stale", stale, "c2", []string{"b.txt"}, "", 0); err != nil {
		t.Fatal(err)
	}

	good := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		"-a",
		"+A",
		"*** End Patch",
	}, "\n")
	e.opts.SessionID = "c1"
	s1, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action: "submit", Patch: good, WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.opts.SessionID = "lead"
	app, err := e.patchCollab(context.Background(), tool.PatchCollabRequest{
		Action: "apply", ID: s1.Patch.ID, WorkDir: dir,
	})
	if err != nil || app.Conflict {
		t.Fatalf("apply should succeed despite unrelated stale pending: %#v err=%v", app, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "A\n" {
		t.Fatalf("a.txt = %q", data)
	}
}
