package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestTurnCompletedEmitsFileDiff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "exist.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeArgs, _ := json.Marshal(map[string]any{
		"filePath": "fresh.txt",
		"content":  "brand new\n",
	})
	editArgs, _ := json.Marshal(map[string]any{
		"filePath":  "exist.txt",
		"oldString": "old",
		"newString": "new",
	})
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "w1", Name: "write", Args: writeArgs}},
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "e1", Name: "edit", Args: editArgs}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:       "turn-diff",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		Registry:        tool.NewRegistry(tool.NewWrite(), tool.NewEdit()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{{
			{Permission: "*", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "mutate files"}
	var completed protocol.TurnCompleted
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		tc, ok := ev.(protocol.TurnCompleted)
		if !ok {
			return false
		}
		completed = tc
		return true
	})
	byPath := map[string]string{}
	for _, f := range completed.Files {
		byPath[f.Path] = f.Kind
	}
	if byPath["fresh.txt"] != "create" {
		t.Fatalf("fresh.txt = %q in %#v", byPath["fresh.txt"], completed.Files)
	}
	if byPath["exist.txt"] != "update" {
		t.Fatalf("exist.txt = %q in %#v", byPath["exist.txt"], completed.Files)
	}
}

func TestEditBaseHashFailureSurfacesCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	editArgs, _ := json.Marshal(map[string]any{
		"filePath":  "a.txt",
		"oldString": "hello",
		"newString": "hi",
		"baseHash":  "0000000000000000000000000000000000000000000000000000000000000000",
	})
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "e1", Name: "edit", Args: editArgs}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:       "base-hash-fail",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		Registry:        tool.NewRegistry(tool.NewEdit()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{{
			{Permission: "*", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "edit"}
	var end protocol.ToolCallEnd
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		e, ok := ev.(protocol.ToolCallEnd)
		if !ok || e.CallID != "e1" {
			return false
		}
		end = e
		return true
	})
	if !end.IsError {
		t.Fatal("want tool error")
	}
	if !strings.Contains(end.Output, "precondition_failed") {
		t.Fatalf("output missing code: %q", end.Output)
	}
	// File unchanged.
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "hello\n" {
		t.Fatalf("disk = %q", got)
	}
}
