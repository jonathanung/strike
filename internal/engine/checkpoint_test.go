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

func TestRewindMultiFileRestoreOrderAndBashUncovered(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"z.txt", "a.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"-v0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	editZ, _ := json.Marshal(map[string]any{
		"filePath": "z.txt", "oldString": "z.txt-v0", "newString": "z.txt-v1",
	})
	editA, _ := json.Marshal(map[string]any{
		"filePath": "a.txt", "oldString": "a.txt-v0", "newString": "a.txt-v1",
	})
	bashArgs, _ := json.Marshal(map[string]any{"command": "true"})
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "e1", Name: "edit", Args: editZ}},
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "e2", Name: "edit", Args: editA}},
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "b1", Name: "bash", Args: bashArgs}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:       "ckpt-multi",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		Registry:        tool.NewRegistry(tool.NewEdit(), tool.NewBash()),
		WorkDir:         dir,
		CheckpointDir:   t.TempDir(),
		Rules: []permission.Ruleset{{
			{Permission: "edit", Pattern: "*", Action: permission.Allow},
			{Permission: "bash", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "edit both + bash"}
	var completed protocol.TurnCompleted
	deadline := time.After(5 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.TurnCompleted:
				completed = e
			case protocol.EngineError:
				t.Fatalf("EngineError: %s", e.Message)
			}
		case <-deadline:
			t.Fatal("timeout turn")
		}
	}
	// No-op bash is covered by shadow-git reconcile (#572).
	if len(completed.Uncovered) != 0 {
		t.Fatalf("TurnCompleted.Uncovered = %#v, want empty", completed.Uncovered)
	}
	if len(completed.Files) != 2 {
		t.Fatalf("TurnCompleted.Files = %#v", completed.Files)
	}

	eng.Ops() <- protocol.Rewind{RestoreFiles: true}
	var rewound protocol.SessionRewound
	deadline = time.After(3 * time.Second)
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
	if rewound.FilesRestored != 2 {
		t.Fatalf("SessionRewound = %+v", rewound)
	}
	if len(rewound.Uncovered) != 0 {
		t.Fatalf("SessionRewound.Uncovered = %#v, want empty", rewound.Uncovered)
	}
	// Restored paths are workspace-relative and sorted.
	if len(rewound.Files) != 2 || rewound.Files[0] != "a.txt" || rewound.Files[1] != "z.txt" {
		t.Fatalf("SessionRewound.Files = %#v, want sorted a.txt,z.txt", rewound.Files)
	}
	for _, name := range []string{"a.txt", "z.txt"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		want := name + "-v0\n"
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
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

func TestRewindRestoresBashMutation(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "shell.txt")
	if err := os.WriteFile(path, []byte("pre\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bashArgs, _ := json.Marshal(map[string]any{"command": "printf 'post\n' > shell.txt"})
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "b1", Name: "bash", Args: bashArgs}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	persist := t.TempDir()
	eng := engine.New(engine.Options{
		SessionID:       "ckpt-bash",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         dir,
		CheckpointDir:   persist,
		Rules: []permission.Ruleset{{
			{Permission: "bash", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "bash mutate"}
	completed := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	}).(protocol.TurnCompleted)
	if len(completed.Uncovered) != 0 {
		t.Fatalf("Uncovered=%#v", completed.Uncovered)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "post\n" {
		t.Fatalf("after bash = %q", got)
	}

	eng.Ops() <- protocol.Rewind{RestoreFiles: true}
	rewound := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.SessionRewound)
		return ok
	}).(protocol.SessionRewound)
	if rewound.FilesRestored != 1 {
		t.Fatalf("SessionRewound=%+v", rewound)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "pre\n" {
		t.Fatalf("after restore = %q", got)
	}
}

func TestContinueLoadsCheckpointStack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeArgs, _ := json.Marshal(map[string]any{
		"filePath": "note.txt",
		"content":  "v1\n",
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
	persist := t.TempDir()
	eng := engine.New(engine.Options{
		SessionID:       "ckpt-continue",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		CheckpointDir:   persist,
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
	cancel()

	// Simulate --continue: new engine, same CheckpointDir, seeded history.
	prov2 := newScriptedProvider()
	eng2 := engine.New(engine.Options{
		SessionID:       "ckpt-continue",
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov2, "model", nil },
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		CheckpointDir:   persist,
		InitialMessages: []provider.Message{
			{Role: provider.RoleUser, Text: "write"},
			{Role: provider.RoleAssistant, Text: "done"},
		},
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go eng2.Run(ctx2)

	eng2.Ops() <- protocol.Rewind{RestoreFiles: true}
	rewound := waitForEvent(t, eng2, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.SessionRewound)
		return ok
	}).(protocol.SessionRewound)
	if rewound.FilesRestored != 1 {
		t.Fatalf("SessionRewound after continue = %+v", rewound)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v0\n" {
		t.Fatalf("after continue restore = %q", got)
	}
}
