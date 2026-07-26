package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestRewindRestoresFileCheckpoint(t *testing.T) {
	dir := initTempGitRepo(t)
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("pre-turn\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	editArgs, _ := json.Marshal(map[string]any{
		"filePath":  "note.txt",
		"oldString": "pre-turn",
		"newString": "post-turn",
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
		SessionID:       "ckpt-restore",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		Registry:        tool.NewRegistry(tool.NewEdit()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{{
			{Permission: "edit", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "edit note"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		end, ok := ev.(protocol.ToolCallEnd)
		return ok && end.CallID == "e1" && !end.IsError
	})
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "post-turn\n" {
		t.Fatalf("after edit = %q", got)
	}

	eng.Ops() <- protocol.Rewind{RestoreFiles: true}
	var rewound protocol.SessionRewound
	deadline := time.After(3 * time.Second)
	for rewound.Removed == 0 {
		select {
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.SessionRewound:
				rewound = e
			case protocol.EngineError:
				t.Fatalf("EngineError: %s", e.Message)
			}
		case <-deadline:
			t.Fatal("timeout rewind")
		}
	}
	if !rewound.RestoreFiles || rewound.FilesRestored != 1 {
		t.Fatalf("SessionRewound = %+v", rewound)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "pre-turn\n" {
		t.Fatalf("after restore = %q, want pre-turn", got)
	}
	if len(eng.Messages()) != 0 {
		t.Fatalf("messages after rewind = %#v", eng.Messages())
	}
}

func TestRewindChatOnlyKeepsDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeArgs, _ := json.Marshal(map[string]any{
		"filePath": "note.txt",
		"content":  "v2\n",
	})
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "w1", Name: "write", Args: writeArgs}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:       "ckpt-chat",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{{
			{Permission: "write", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "write"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})

	eng.Ops() <- protocol.Rewind{RestoreFiles: false}
	rewound := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.SessionRewound)
		return ok
	}).(protocol.SessionRewound)
	if rewound.RestoreFiles || rewound.FilesRestored != 0 {
		t.Fatalf("SessionRewound = %+v", rewound)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2\n" {
		t.Fatalf("chat-only undo mutated disk: %q", got)
	}
}

func TestRewindEmptyTurnDoesNotRestorePrior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("a0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	editArgs, _ := json.Marshal(map[string]any{
		"filePath":  "a.txt",
		"oldString": "a0",
		"newString": "a1",
	})
	// Turn 1: edit. Turn 2: chat only. Undo turn 2 with restore must keep a1.
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "e1", Name: "edit", Args: editArgs}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:       "ckpt-align",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		Registry:        tool.NewRegistry(tool.NewEdit()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{{
			{Permission: "edit", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "edit"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	eng.Ops() <- protocol.UserInput{Text: "just chat"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})

	eng.Ops() <- protocol.Rewind{RestoreFiles: true}
	rewound := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.SessionRewound)
		return ok
	}).(protocol.SessionRewound)
	if rewound.FilesRestored != 0 {
		t.Fatalf("empty turn restored files: %+v", rewound)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "a1\n" {
		t.Fatalf("prior turn file restored early: %q", got)
	}
}

func initTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=strike",
			"GIT_AUTHOR_EMAIL=strike@test",
			"GIT_COMMITTER_NAME=strike",
			"GIT_COMMITTER_EMAIL=strike@test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "strike@test")
	run("config", "user.name", "strike")
	return dir
}
