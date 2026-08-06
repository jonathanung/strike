package replay_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/replay"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func sampleBundle() *protocol.ContextBundle {
	return &protocol.ContextBundle{
		Goal:          "ship feature",
		Acceptance:    []string{"tests pass"},
		AllowedPaths:  []string{"internal/foo/**"},
		RequiredPaths: []string{"docs/a.md"},
		Constraints:   []string{"no secrets"},
		Artifacts:     []protocol.ArtifactRef{{ID: "art-1", Type: "findings"}},
		Items: []protocol.ContextBundleItem{
			{ID: "goal", Kind: "goal", Text: "ship feature"},
			{ID: "path-a", Kind: "path", Path: "docs/a.md"},
		},
		FilePins: []protocol.ContextFilePin{{Path: "docs/a.md", Hash: "abc"}},
	}
}

func TestBuildStartSnapshotFieldsAndRedaction(t *testing.T) {
	fixed := time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC)
	snap := replay.BuildStartSnapshot(replay.SnapshotOptions{
		SnapshotID:      "snap-1",
		DelegationID:    "del-9",
		ParentSessionID: "parent-1",
		ChildSessionID:  "child-1",
		LeadSessionID:   "lead-1",
		Prompt:          "fix bug with token sk-ant-api03-SECRETVALUE1234567890abcd",
		Agent:           "build",
		Name:            "worker-a",
		RouteReason:     "specialty=build",
		Settings: replay.SettingsDigest{
			Provider:       "echo",
			Model:          "echo",
			Effort:         "low",
			PermissionMode: "default",
		},
		ToolAllowList:  []string{"bash", "read", "edit"},
		ProtocolBundle: sampleBundle(),
		Config: replay.ConfigDigest{
			LeanCode:        "lite",
			MaxChildDepth:   2,
			BudgetMaxTokens: 1000,
		},
		Repo: &replay.RepoIdentity{
			Commit:     "deadbeef",
			Dirty:      true,
			Worktree:   "/tmp/wt",
			ProjectKey: "/proj",
		},
		VerifyGates: []replay.GateSpecSnapshot{
			{Name: "unit", Kind: "cmd", Value: "go test ./..."},
		},
		Criteria: []string{"done when green"},
		Clock:    fixedClock(fixed),
	})

	if snap.SchemaVersion != replay.RunSnapshotSchemaVersion {
		t.Fatalf("schema = %q", snap.SchemaVersion)
	}
	if snap.Phase != replay.SnapshotPhaseStart {
		t.Fatalf("phase = %q", snap.Phase)
	}
	if !snap.Redacted || snap.SnapshotID != "snap-1" {
		t.Fatalf("snap = %+v", snap)
	}
	if snap.CapturedAt != fixed {
		t.Fatalf("capturedAt = %v", snap.CapturedAt)
	}
	if snap.DelegationID != "del-9" || snap.ParentSessionID != "parent-1" || snap.ChildSessionID != "child-1" {
		t.Fatalf("ids = %+v", snap)
	}
	if snap.Agent != "build" || snap.Name != "worker-a" {
		t.Fatalf("agent/name = %q %q", snap.Agent, snap.Name)
	}
	if snap.PromptDigest == "" {
		t.Fatal("expected promptDigest")
	}
	if strings.Contains(snap.Prompt, "SECRETVALUE") {
		t.Fatalf("secret leaked in prompt: %q", snap.Prompt)
	}
	if snap.Settings.Provider != "echo" || snap.Settings.ToolsDigest == "" {
		t.Fatalf("settings = %+v", snap.Settings)
	}
	if len(snap.ToolAllowList) != 3 || snap.ToolAllowList[0] != "bash" {
		// sorted: bash, edit, read
		t.Fatalf("tools = %v", snap.ToolAllowList)
	}
	if snap.ContextBundle == nil || snap.ContextBundle.Digest == "" || snap.ContextBundle.Goal != "ship feature" {
		t.Fatalf("bundle = %+v", snap.ContextBundle)
	}
	if len(snap.ContextBundle.ItemIDs) != 2 || snap.ContextBundle.ArtifactIDs[0] != "art-1" {
		t.Fatalf("bundle ids = %+v", snap.ContextBundle)
	}
	if snap.Config.Digest == "" || snap.Config.LeanCode != "lite" {
		t.Fatalf("config = %+v", snap.Config)
	}
	if snap.Repo == nil || snap.Repo.Commit != "deadbeef" || !snap.Repo.Dirty {
		t.Fatalf("repo = %+v", snap.Repo)
	}
	if len(snap.VerifyGates) != 1 || snap.VerifyGates[0].Kind != "cmd" {
		t.Fatalf("gates = %+v", snap.VerifyGates)
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRETVALUE") {
		t.Fatalf("secret leaked into snapshot JSON: %s", raw)
	}
}

func TestCompleteRunSnapshotHandoffAndRecording(t *testing.T) {
	fixed := time.Date(2026, 8, 5, 16, 0, 0, 0, time.UTC)
	start := replay.BuildStartSnapshot(replay.SnapshotOptions{
		SnapshotID:      "snap-c",
		DelegationID:    "del-1",
		ParentSessionID: "p",
		ChildSessionID:  "c",
		Prompt:          "do work",
		Agent:           "build",
		Clock:           fixedClock(fixed),
	})
	completed := protocol.ChildCompleted{
		Correlation: protocol.Correlation{
			SessionID:       "c",
			ParentSessionID: "p",
			Depth:           1,
		},
		Status:       protocol.ChildStatusCompleted,
		DelegationID: "del-1",
		Handoff: protocol.CompletionHandoff{
			Summary:      "child done",
			FilesChanged: []string{"a.go"},
			Findings:     []string{"note"},
			Provenance:   []string{"goal"},
		},
		Verification: &protocol.VerificationReport{
			Passed:   true,
			Claimed:  true,
			Verified: true,
			Summary:  "ok",
			Checks: []protocol.VerificationCheck{
				{Name: "unit", Kind: "cmd", Value: "go test", Passed: true},
			},
		},
	}
	// Real child JSONL stamps ParentSessionID/Depth — recording must still extract tools.
	childCorr := protocol.Correlation{SessionID: "c", ParentSessionID: "p", Depth: 1, TurnID: "t1"}
	childEvs := []protocol.Event{
		protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "c", ParentSessionID: "p", Depth: 1}, Provider: "echo", Model: "echo"},
		protocol.UserMessage{Correlation: childCorr, Text: "do work"},
		protocol.ToolCallBegin{Correlation: childCorr, CallID: "x", Name: "bash", Args: json.RawMessage(`{"command":"echo hi"}`)},
		protocol.ToolCallEnd{Correlation: childCorr, CallID: "x", Title: "bash", Output: "hi\n"},
		protocol.TurnCompleted{Correlation: childCorr, StopReason: "end_turn"},
	}

	done := replay.CompleteRunSnapshot(start, completed, childEvs, replay.SnapshotOptions{Clock: fixedClock(fixed)})
	if done.Phase != replay.SnapshotPhaseComplete {
		t.Fatalf("phase = %q", done.Phase)
	}
	if done.ExitStatus != string(protocol.ChildStatusCompleted) {
		t.Fatalf("exit = %q", done.ExitStatus)
	}
	if done.Handoff == nil || done.Handoff.Summary != "child done" {
		t.Fatalf("handoff = %+v", done.Handoff)
	}
	if done.Verification == nil || !done.Verification.Passed {
		t.Fatalf("verification = %+v", done.Verification)
	}
	if done.Recording == nil || len(done.Recording.ToolCalls) != 1 {
		t.Fatalf("recording tools empty (child lineage not promoted?): %+v", done.Recording)
	}
	if done.Recording.ToolCalls[0].Name != "bash" {
		t.Fatalf("tool = %+v", done.Recording.ToolCalls)
	}
	if len(done.Recording.UserInputs) != 1 || done.Recording.UserInputs[0] != "do work" {
		t.Fatalf("inputs = %v", done.Recording.UserInputs)
	}
	if done.Settings.Provider != "echo" {
		t.Fatalf("settings merged = %+v", done.Settings)
	}
	if done.CompletedAt != fixed {
		t.Fatalf("completedAt = %v", done.CompletedAt)
	}
}

func TestWriteLoadPersistRunSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	snap := replay.BuildStartSnapshot(replay.SnapshotOptions{
		SnapshotID:      "rt-1",
		ParentSessionID: "parent-rt",
		ChildSessionID:  "child-rt",
		Prompt:          "hello",
		Agent:           "general",
		Clock:           fixedClock(fixed),
	})
	path := filepath.Join(dir, "one.json")
	if err := replay.WriteRunSnapshot(path, snap); err != nil {
		t.Fatal(err)
	}
	got, err := replay.LoadRunSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotID != snap.SnapshotID || got.Prompt != snap.Prompt || got.Agent != snap.Agent {
		t.Fatalf("got %+v want %+v", got, snap)
	}

	// Persist under runs layout
	runs := filepath.Join(dir, "runs")
	p2, err := replay.PersistRunSnapshot(runs, snap)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := replay.SnapshotPath(runs, "parent-rt", "rt-1")
	if p2 != wantPath {
		t.Fatalf("path = %q want %q", p2, wantPath)
	}
	if _, err := os.Stat(p2); err != nil {
		t.Fatal(err)
	}
	got2, err := replay.LoadRunSnapshot(p2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.ChildSessionID != "child-rt" {
		t.Fatalf("got2 = %+v", got2)
	}
}

func TestExtractRunSnapshotsFromParentEvents(t *testing.T) {
	fixed := time.Date(2026, 8, 5, 17, 0, 0, 0, time.UTC)
	parent := []protocol.Event{
		protocol.ChildStarted{
			Correlation: protocol.Correlation{
				SessionID:       "child-a",
				ParentSessionID: "root",
				Depth:           1,
			},
			Agent:         "build",
			Prompt:        "task a token sk-ant-api03-SECRETVALUE1234567890abcd",
			Name:          "a",
			RouteReason:   "pin",
			ContextBundle: sampleBundle(),
		},
		protocol.ChildCompleted{
			Correlation: protocol.Correlation{
				SessionID:       "child-a",
				ParentSessionID: "root",
				Depth:           1,
			},
			Status:       protocol.ChildStatusBlocked,
			DelegationID: "d-a",
			Handoff: protocol.CompletionHandoff{
				Summary: "need context",
				MissingContext: []protocol.MissingContextEntry{
					{Kind: "path", Path: "docs/a.md"},
				},
			},
			Verification: &protocol.VerificationReport{
				Passed:  false,
				Claimed: true,
				Summary: "gate failed",
				Checks:  []protocol.VerificationCheck{{Name: "unit", Kind: "cmd", Passed: false}},
			},
		},
	}
	snaps := replay.ExtractRunSnapshots(parent, replay.ExtractOptions{
		LeadSessionID: "root",
		Config:        replay.ConfigDigest{LeanCode: "full", MaxChildDepth: 1},
		Repo:          &replay.RepoIdentity{Commit: "abc", Worktree: "/w"},
		Clock:         fixedClock(fixed),
	})
	if len(snaps) != 1 {
		t.Fatalf("snaps = %d %+v", len(snaps), snaps)
	}
	s := snaps[0]
	if s.Phase != replay.SnapshotPhaseComplete {
		t.Fatalf("phase = %q", s.Phase)
	}
	if s.DelegationID != "d-a" || s.ChildSessionID != "child-a" || s.LeadSessionID != "root" {
		t.Fatalf("ids = %+v", s)
	}
	if s.ExitStatus != string(protocol.ChildStatusBlocked) {
		t.Fatalf("exit = %q", s.ExitStatus)
	}
	if s.ContextBundle == nil || s.ContextBundle.Digest == "" {
		t.Fatalf("bundle = %+v", s.ContextBundle)
	}
	if s.Handoff == nil || len(s.Handoff.MissingContextKinds) != 1 {
		t.Fatalf("handoff = %+v", s.Handoff)
	}
	if s.Verification == nil || s.Verification.Passed {
		t.Fatalf("verification = %+v", s.Verification)
	}
	if strings.Contains(s.Prompt, "SECRETVALUE") {
		t.Fatalf("secret in extracted prompt: %q", s.Prompt)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "SECRETVALUE") {
		t.Fatalf("secret in JSON: %s", raw)
	}
}

func TestCompareRunSnapshotsEqualAndDiverge(t *testing.T) {
	fixed := time.Date(2026, 8, 5, 18, 0, 0, 0, time.UTC)
	mk := func(summary string, passed bool) replay.RunSnapshot {
		start := replay.BuildStartSnapshot(replay.SnapshotOptions{
			SnapshotID:      "cmp",
			ParentSessionID: "p",
			ChildSessionID:  "c",
			Prompt:          "same prompt",
			Agent:           "build",
			Settings:        replay.SettingsDigest{Provider: "echo", Model: "echo"},
			ToolAllowList:   []string{"read", "bash"},
			ProtocolBundle:  sampleBundle(),
			Config:          replay.ConfigDigest{LeanCode: "lite"},
			Clock:           fixedClock(fixed),
		})
		cc := protocol.ChildCompleted{
			Correlation: protocol.Correlation{SessionID: "c", ParentSessionID: "p", Depth: 1},
			Status:      protocol.ChildStatusCompleted,
			Handoff:     protocol.CompletionHandoff{Summary: summary, FilesChanged: []string{"x.go"}},
			Verification: &protocol.VerificationReport{
				Passed: passed, Claimed: true, Verified: passed, Summary: "g",
				Checks: []protocol.VerificationCheck{{Name: "u", Kind: "cmd", Passed: passed}},
			},
		}
		return replay.CompleteRunSnapshot(start, cc, nil, replay.SnapshotOptions{Clock: fixedClock(fixed)})
	}
	a := mk("done", true)
	b := mk("done", true)
	// SnapshotIDs differ but content matches.
	b.SnapshotID = "other"
	rep := replay.CompareRunSnapshots(a, b, replay.CompareOptions{})
	if !rep.Equal() {
		t.Fatalf("expected equal:\n%s", replay.FormatCompareReport(rep))
	}

	// Handoff diverge
	b2 := mk("different", true)
	rep = replay.CompareRunSnapshots(a, b2, replay.CompareOptions{})
	if rep.Equal() {
		t.Fatal("expected handoff divergence")
	}
	var sawHandoff bool
	for _, d := range rep.Divergences {
		if strings.Contains(d.Path, "handoff") {
			sawHandoff = true
		}
	}
	if !sawHandoff {
		t.Fatalf("divergences = %+v", rep.Divergences)
	}

	// Gate diverge
	b3 := mk("done", false)
	rep = replay.CompareRunSnapshots(a, b3, replay.CompareOptions{})
	if rep.Equal() {
		t.Fatal("expected gate divergence")
	}
	var sawGate bool
	for _, d := range rep.Divergences {
		if strings.Contains(d.Path, "verification") || strings.Contains(d.Path, "passed") {
			sawGate = true
		}
	}
	if !sawGate {
		t.Fatalf("divergences = %+v", rep.Divergences)
	}

	// Exit diverge
	b4 := a
	b4.ExitStatus = string(protocol.ChildStatusFailed)
	rep = replay.CompareRunSnapshots(a, b4, replay.CompareOptions{})
	if rep.Equal() || rep.ExitStatus == nil {
		t.Fatalf("expected exit divergence: %+v", rep)
	}
}

func TestReplayRunSnapshotEcho(t *testing.T) {
	snap := replay.BuildStartSnapshot(replay.SnapshotOptions{
		SnapshotID: "echo-1",
		Prompt:     "say hello",
		Agent:      "build",
		Settings:   replay.SettingsDigest{Provider: "echo", Model: "echo"},
		Clock:      fixedClock(time.Date(2026, 8, 5, 19, 0, 0, 0, time.UTC)),
	})
	res, err := replay.ReplayRunSnapshot(context.Background(), snap, replay.Options{
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.UserInputs) != 1 || res.UserInputs[0] != "say hello" {
		t.Fatalf("inputs = %v", res.UserInputs)
	}
	if res.Turns < 1 {
		t.Fatalf("turns = %d events=%d", res.Turns, len(res.Events))
	}
}

func TestRecordingFromSnapshotSynthetic(t *testing.T) {
	snap := replay.BuildStartSnapshot(replay.SnapshotOptions{
		Prompt: "p",
		Agent:  "build",
		Clock:  fixedClock(time.Date(2026, 8, 5, 20, 0, 0, 0, time.UTC)),
	})
	h := replay.HandoffSnapshot{Summary: "s", Status: "completed", FilesChanged: []string{"f.go"}}
	snap.Handoff = &h
	snap.ExitStatus = "completed"
	snap.Phase = replay.SnapshotPhaseComplete
	rec := replay.RecordingFromSnapshot(snap)
	if len(rec.Handoffs) != 1 || rec.Handoffs[0].Summary != "s" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.ExitStatus != "completed" {
		t.Fatalf("exit = %q", rec.ExitStatus)
	}
}

func TestCaptureRepoIdentity(t *testing.T) {
	// Worktree of this test process should be a git repo.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ri := replay.CaptureRepoIdentity(wd, "/proj-key")
	if ri == nil {
		t.Fatal("nil identity")
	}
	if ri.Worktree != wd || ri.ProjectKey != "/proj-key" {
		t.Fatalf("ri = %+v", ri)
	}
	// Commit may be empty if git missing; when present it should be hex-ish.
	if ri.Commit != "" && len(ri.Commit) < 7 {
		t.Fatalf("commit = %q", ri.Commit)
	}
}

func TestSnapshotPathSanitizes(t *testing.T) {
	p := replay.SnapshotPath("/runs", "a/b", "c/d")
	if strings.Contains(p, "a/b") || strings.Contains(filepath.Base(filepath.Dir(p)), "/") {
		// parent dir should not retain separators as path segments beyond join
	}
	if !strings.HasSuffix(p, ".json") {
		t.Fatalf("path = %q", p)
	}
	// Ensure no ".." survives
	p2 := replay.SnapshotPath("/runs", "..", "x")
	if strings.Contains(p2, "/../") {
		t.Fatalf("unsanitized: %q", p2)
	}
}
