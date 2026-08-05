package main

import (
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/echo"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/tool"
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
	hub := newMultiRootHub(first, makeSlot, files, local.NewShell(dir, ""))
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
	if lives[0] != secondID {
		t.Fatalf("active should be first in LiveIDs: %v", lives)
	}

	if err := hub.Activate(first.id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if hub.ActiveID() != first.id {
		t.Fatalf("active after activate = %q", hub.ActiveID())
	}

	select {
	case hub.Ops() <- protocol.Interrupt{}:
	case <-time.After(2 * time.Second):
		t.Fatal("ops blocked")
	}
}
