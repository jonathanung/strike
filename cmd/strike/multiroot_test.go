package main

import (
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/provider/echo"
	"github.com/jonathanung/strike-cli/harness/sandbox"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/persist/session"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestMultiRootHubSpawnAndActivate(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	files := local.NewFiles(dir)

	makeSlot := func(resumeID string) (*rootSlot, error) {
		var (
			id    string
			bound session.Bound
		)
		if resumeID == "" {
			info, err := mgr.Create(session.CreateOptions{ProjectKey: "proj"})
			if err != nil {
				return nil, err
			}
			id = info.ID
			bound, err = mgr.Bind(id)
			if err != nil {
				_ = mgr.Close(id)
				return nil, err
			}
		} else {
			opened, err := openResumeSession(mgr, resumeID)
			if err != nil {
				return nil, err
			}
			id, bound = opened.id, opened.bound
		}
		eng := engine.New(engine.Options{
			SessionID: id,
			Select: func(string) (provider.Provider, string, error) {
				return echo.New(), "echo", nil
			},
			WorkDir:         dir,
			InitialProvider: "echo",
			Registry:        tool.NewRegistry(),
			QuietStartup:    true,
		})
		return &rootSlot{id: id, eng: eng, bound: bound, workDir: dir}, nil
	}

	first, err := makeSlot("")
	if err != nil {
		t.Fatal(err)
	}
	hub := newMultiRootHub(first, makeSlot, files, local.NewShell(dir, sandbox.Policy{
		Mode: sandbox.ModeWorkspaceWrite, WorkDir: dir,
	}))
	defer func() { _ = hub.Close() }()

	if got := hub.ActiveID(); got != first.id {
		t.Fatalf("active = %q, want %q", got, first.id)
	}
	if n := len(hub.LiveIDs()); n != 1 {
		t.Fatalf("live = %d, want 1", n)
	}

	secondID, err := hub.Spawn()
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if secondID == "" || secondID == first.id {
		t.Fatalf("second = %q", secondID)
	}
	if hub.ActiveID() != secondID {
		t.Fatalf("active after spawn = %q, want %q", hub.ActiveID(), secondID)
	}
	lives := hub.LiveIDs()
	if len(lives) != 2 {
		t.Fatalf("live = %v, want 2", lives)
	}
	// Spawn/open insertion order: first root stays ahead of the newly spawned one.
	if lives[0] != first.id || lives[1] != secondID {
		t.Fatalf("LiveIDs = %v, want spawn order [%s %s]", lives, first.id, secondID)
	}

	if err := hub.Activate(first.id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if hub.ActiveID() != first.id {
		t.Fatalf("active after activate = %q", hub.ActiveID())
	}
	// Activate must not reshuffle the agents list (#865).
	after := hub.LiveIDs()
	if len(after) != 2 || after[0] != first.id || after[1] != secondID {
		t.Fatalf("LiveIDs after Activate = %v, want stable [%s %s]", after, first.id, secondID)
	}

	select {
	case hub.Ops() <- protocol.Interrupt{}:
	case <-time.After(2 * time.Second):
		t.Fatal("ops blocked")
	}
}

// TestMultiRootHubLiveIDsStableAcrossActivate covers switching among ≥3 roots
// without the active session jumping to the front of LiveIDs.
func TestMultiRootHubLiveIDsStableAcrossActivate(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	files := local.NewFiles(dir)

	makeSlot := func(resumeID string) (*rootSlot, error) {
		info, err := mgr.Create(session.CreateOptions{ProjectKey: "proj"})
		if err != nil {
			return nil, err
		}
		bound, err := mgr.Bind(info.ID)
		if err != nil {
			_ = mgr.Close(info.ID)
			return nil, err
		}
		eng := engine.New(engine.Options{
			SessionID: info.ID,
			Select: func(string) (provider.Provider, string, error) {
				return echo.New(), "echo", nil
			},
			WorkDir:         dir,
			InitialProvider: "echo",
			Registry:        tool.NewRegistry(),
			QuietStartup:    true,
		})
		return &rootSlot{id: info.ID, eng: eng, bound: bound, workDir: dir}, nil
	}

	first, err := makeSlot("")
	if err != nil {
		t.Fatal(err)
	}
	hub := newMultiRootHub(first, makeSlot, files, local.NewShell(dir, sandbox.Policy{
		Mode: sandbox.ModeWorkspaceWrite, WorkDir: dir,
	}))
	defer func() { _ = hub.Close() }()

	secondID, err := hub.Spawn()
	if err != nil {
		t.Fatalf("Spawn second: %v", err)
	}
	thirdID, err := hub.Spawn()
	if err != nil {
		t.Fatalf("Spawn third: %v", err)
	}
	want := []string{first.id, secondID, thirdID}
	got := hub.LiveIDs()
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("LiveIDs after spawns = %v, want %v", got, want)
	}

	// Activate middle, then last, then first — order must stay fixed.
	for _, id := range []string{secondID, thirdID, first.id} {
		if err := hub.Activate(id); err != nil {
			t.Fatalf("Activate(%s): %v", id, err)
		}
		got = hub.LiveIDs()
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("LiveIDs after Activate(%s) = %v, want %v", id, got, want)
		}
		if hub.ActiveID() != id {
			t.Fatalf("ActiveID = %q, want %q", hub.ActiveID(), id)
		}
	}
}
