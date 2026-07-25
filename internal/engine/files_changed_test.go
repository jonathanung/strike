package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestFilesChangedInvalidatesAndEmitsEvent(t *testing.T) {
	const sessionID = "files-session"
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Scripted provider: first turn reads the file, second turn tries to edit
	// after FilesChanged — edit must fail with stale guidance.
	readArgs, _ := json.Marshal(map[string]any{"filePath": "note.txt"})
	editArgs, _ := json.Marshal(map[string]any{
		"filePath":  "note.txt",
		"oldString": "v1",
		"newString": "v2",
	})
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "c1", Name: "read", Args: readArgs}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "c2", Name: "edit", Args: editArgs}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)

	reg := tool.NewRegistry(tool.NewRead(), tool.NewEdit())
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
		InitialProvider: "scripted",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "model", nil
		},
		Registry: reg,
		WorkDir:  dir,
		Rules:    []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "read it"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		end, ok := ev.(protocol.ToolCallEnd)
		return ok && end.CallID == "c1" && !end.IsError
	})
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})

	// External edit + FilesChanged signal.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng.Ops() <- protocol.FilesChanged{Paths: []string{"note.txt"}, Reason: "external_editor"}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.FilesInvalidated)
		return ok
	})
	inv := event.(protocol.FilesInvalidated)
	if inv.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("correlation = %#v", inv.Correlation)
	}
	if len(inv.Paths) != 1 || inv.Paths[0] != "note.txt" || inv.Reason != "external_editor" {
		t.Fatalf("FilesInvalidated = %#v", inv)
	}

	// Without re-read, edit must surface a tool error to the model.
	eng.Ops() <- protocol.UserInput{Text: "edit it"}
	endEv := waitForEvent(t, eng, func(ev protocol.Event) bool {
		end, ok := ev.(protocol.ToolCallEnd)
		return ok && end.CallID == "c2"
	})
	end := endEv.(protocol.ToolCallEnd)
	if !end.IsError || !strings.Contains(end.Output, "modified externally") {
		t.Fatalf("edit end = %#v", end)
	}
}

func TestFilesChangedDedupesAndAcceptsAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "x.go")
	if err := os.WriteFile(abs, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Options{
		SessionID: "dedupe",
		Select: func(string) (provider.Provider, string, error) {
			return &fastRecordingProvider{}, "m", nil
		},
		InitialProvider: "rec",
		Registry:        tool.NewRegistry(),
		WorkDir:         dir,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.FilesChanged{
		Paths:  []string{"x.go", abs, "x.go", "  ", filepath.Join(dir, "x.go")},
		Reason: "external_editor",
	}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.FilesInvalidated)
		return ok
	})
	inv := event.(protocol.FilesInvalidated)
	if len(inv.Paths) != 1 || inv.Paths[0] != "x.go" {
		t.Fatalf("paths = %#v, want single relative x.go", inv.Paths)
	}
}

func TestFilesChangedEmptyPathsNoEvent(t *testing.T) {
	eng := engine.New(engine.Options{
		SessionID: "empty",
		Select: func(string) (provider.Provider, string, error) {
			return &fastRecordingProvider{}, "m", nil
		},
		InitialProvider: "rec",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Drain startup AgentSelected.
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})

	eng.Ops() <- protocol.FilesChanged{Paths: []string{"", "  "}}
	select {
	case ev := <-eng.Events():
		if _, ok := ev.(protocol.FilesInvalidated); ok {
			t.Fatalf("unexpected FilesInvalidated: %#v", ev)
		}
	case <-time.After(150 * time.Millisecond):
	}
}
