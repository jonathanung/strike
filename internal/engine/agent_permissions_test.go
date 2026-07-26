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

func writeToolCall(id, path, content string) provider.ToolCall {
	args, _ := json.Marshal(map[string]any{
		"filePath": path,
		"content":  content,
	})
	return provider.ToolCall{ID: id, Name: "write", Args: args}
}

func reviewerDenyWriteEdit() permission.Ruleset {
	return permission.Ruleset{
		{Permission: "write", Pattern: "*", Action: permission.Deny},
		{Permission: "edit", Pattern: "*", Action: permission.Deny},
	}
}

// TestReviewerAgentDeniesWrite is the AG1 exit criterion: a reviewer agent
// profile with write/edit deny hard-rejects write tool calls even when base
// rules would allow them.
func TestReviewerAgentDeniesWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	// Pre-create so we can assert content is unchanged (write never runs).
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := writeToolCall("w1", "out.txt", "pwned\n")
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("done after deny"),
	)

	// Base allows write so a missing agent layer would succeed; agent deny must win.
	baseAllowWrite := permission.Ruleset{
		{Permission: "write", Pattern: "*", Action: permission.Allow},
		{Permission: "edit", Pattern: "*", Action: permission.Allow},
		{Permission: "read", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		SessionID:       "ag1-reviewer-deny",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults(), baseAllowWrite},
		Agents: []engine.Agent{
			{Name: "build", Description: "general"},
			{
				Name:        "reviewer",
				Description: "code review",
				Permissions: reviewerDenyWriteEdit(),
			},
		},
		InitialAgent: "reviewer",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Drain startup AgentSelected.
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AgentSelected)
		return ok && sel.Name == "reviewer"
	})

	eng.Ops() <- protocol.UserInput{Text: "write the file"}
	var end protocol.ToolCallEnd
	var sawEnd bool
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for turn")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				t.Fatalf("unexpected PermissionAsked under agent deny: %#v", ev)
			case protocol.ToolCallEnd:
				if ev.CallID == "w1" {
					end = ev
					sawEnd = true
				}
			case protocol.TurnCompleted:
				goto done
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
done:
	if !sawEnd {
		t.Fatal("missing write ToolCallEnd")
	}
	if !end.IsError {
		t.Errorf("ToolCallEnd IsError=false, want true; output=%q", end.Output)
	}
	if !strings.Contains(strings.ToLower(end.Output), "reject") &&
		!strings.Contains(strings.ToLower(end.Output), "denied") {
		t.Errorf("ToolCallEnd output %q, want rejection/denied wording", end.Output)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original\n" {
		t.Errorf("file content = %q, want unchanged original", data)
	}
}

// TestSwitchBuildToReviewDropsWriteAllow is the AG2 exit criterion at the
// engine boundary: selecting a deny-write agent after build clears prior
// session grants / agent allow, and switching back to build clears the deny.
func TestSwitchBuildToReviewDropsWriteAllow(t *testing.T) {
	dir := t.TempDir()

	// Turn 1 (build): write asks, user Always-grants, file written.
	// Turn 2 (after SelectAgent review): write hard-denied.
	// Turn 3 (after SelectAgent build): write asks again (agent deny cleared).
	call1 := writeToolCall("w-build", "from-build.txt", "build-ok\n")
	call2 := writeToolCall("w-review", "from-review.txt", "should-not-write\n")
	call3 := writeToolCall("w-build2", "from-build2.txt", "build-again\n")
	prov := newScriptedProvider(
		toolCallStep(call1),
		completedStep("build wrote"),
		toolCallStep(call2),
		completedStep("review denied"),
		toolCallStep(call3),
		completedStep("build again"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "ag2-switch-agent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		// Defaults: write=ask. Always grant on turn 1; agent deny must clear it.
		Rules: []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build", Description: "general"},
			{
				Name:        "review",
				Description: "reviewer",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Deny},
				},
			},
		},
		InitialAgent: "build",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AgentSelected)
		return ok && sel.Name == "build"
	})

	// --- Turn 1: build + Always grant ---
	eng.Ops() <- protocol.UserInput{Text: "write as build"}
	var turn1End protocol.ToolCallEnd
	deadline := time.After(10 * time.Second)
loop1:
	for {
		select {
		case <-deadline:
			t.Fatal("timed out turn 1")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				if ev.Permission != "write" {
					t.Fatalf("turn1 ask permission = %q, want write", ev.Permission)
				}
				eng.Ops() <- protocol.PermissionReply{
					RequestID: ev.RequestID,
					Decision:  protocol.DecisionAlways,
				}
			case protocol.ToolCallEnd:
				if ev.CallID == "w-build" {
					turn1End = ev
				}
			case protocol.TurnCompleted:
				break loop1
			case protocol.EngineError:
				t.Fatalf("turn1 engine error: %s", ev.Message)
			}
		}
	}
	if turn1End.CallID == "" || turn1End.IsError {
		t.Fatalf("turn1 write end = %#v, want success", turn1End)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "from-build.txt")); err != nil || string(data) != "build-ok\n" {
		t.Fatalf("from-build.txt = %q err=%v", data, err)
	}

	// --- Switch to review; turn 2 write must hard-deny (grants cleared) ---
	eng.Ops() <- protocol.SelectAgent{Name: "review"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AgentSelected)
		return ok && sel.Name == "review"
	})

	eng.Ops() <- protocol.UserInput{Text: "write as review"}
	var turn2End protocol.ToolCallEnd
	var turn2Asked bool
	deadline = time.After(10 * time.Second)
loop2:
	for {
		select {
		case <-deadline:
			t.Fatal("timed out turn 2")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				turn2Asked = true
				// Should not ask — hard deny. Reply once so we don't hang if buggy.
				eng.Ops() <- protocol.PermissionReply{
					RequestID: ev.RequestID,
					Decision:  protocol.DecisionOnce,
				}
			case protocol.ToolCallEnd:
				if ev.CallID == "w-review" {
					turn2End = ev
				}
			case protocol.TurnCompleted:
				break loop2
			case protocol.EngineError:
				t.Fatalf("turn2 engine error: %s", ev.Message)
			}
		}
	}
	if turn2Asked {
		t.Error("turn2 emitted PermissionAsked; agent deny should hard-reject without ask")
	}
	if turn2End.CallID == "" || !turn2End.IsError {
		t.Fatalf("turn2 write end = %#v, want error", turn2End)
	}
	if !strings.Contains(strings.ToLower(turn2End.Output), "reject") &&
		!strings.Contains(strings.ToLower(turn2End.Output), "denied") {
		t.Errorf("turn2 output %q, want rejection/denied", turn2End.Output)
	}
	if _, err := os.Stat(filepath.Join(dir, "from-review.txt")); !os.IsNotExist(err) {
		t.Errorf("from-review.txt exists (err=%v); write should not have run", err)
	}

	// --- Switch back to build; empty profile clears agent deny ---
	eng.Ops() <- protocol.SelectAgent{Name: "build"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AgentSelected)
		return ok && sel.Name == "build"
	})

	eng.Ops() <- protocol.UserInput{Text: "write as build again"}
	var turn3End protocol.ToolCallEnd
	var turn3Asked bool
	deadline = time.After(10 * time.Second)
loop3:
	for {
		select {
		case <-deadline:
			t.Fatal("timed out turn 3")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				turn3Asked = true
				eng.Ops() <- protocol.PermissionReply{
					RequestID: ev.RequestID,
					Decision:  protocol.DecisionOnce,
				}
			case protocol.ToolCallEnd:
				if ev.CallID == "w-build2" {
					turn3End = ev
				}
			case protocol.TurnCompleted:
				break loop3
			case protocol.EngineError:
				t.Fatalf("turn3 engine error: %s", ev.Message)
			}
		}
	}
	// After clearing agent deny, write is ask again (grants were cleared on
	// review select) OR still allowed if grants somehow survived — either way
	// the write must succeed and not hard-deny.
	if turn3End.CallID == "" || turn3End.IsError {
		t.Fatalf("turn3 write end = %#v, want success after clearing agent deny (asked=%v)", turn3End, turn3Asked)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "from-build2.txt")); err != nil || string(data) != "build-again\n" {
		t.Fatalf("from-build2.txt = %q err=%v", data, err)
	}
}

// TestSelectUnknownAgentDoesNotClearProfile ensures a bad SelectAgent errors
// without replacing the active agent permission layer.
func TestSelectUnknownAgentDoesNotClearProfile(t *testing.T) {
	dir := t.TempDir()
	call := writeToolCall("w-still-denied", "nope.txt", "x\n")
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("still reviewer"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "unknown-agent-keep-profile",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "write", Pattern: "*", Action: permission.Allow}},
		},
		Agents: []engine.Agent{
			{Name: "build"},
			{
				Name:        "reviewer",
				Permissions: permission.Ruleset{{Permission: "write", Pattern: "*", Action: permission.Deny}},
			},
		},
		InitialAgent: "reviewer",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AgentSelected)
		return ok && sel.Name == "reviewer"
	})

	eng.Ops() <- protocol.SelectAgent{Name: "does-not-exist"}
	errEv := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.EngineError)
		return ok
	})
	if msg := errEv.(protocol.EngineError).Message; !strings.Contains(msg, "unknown agent") {
		t.Errorf("EngineError = %q, want unknown agent", msg)
	}

	eng.Ops() <- protocol.UserInput{Text: "try write"}
	var end protocol.ToolCallEnd
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				t.Fatalf("unexpected ask; reviewer profile should still deny: %#v", ev)
			case protocol.ToolCallEnd:
				if ev.CallID == "w-still-denied" {
					end = ev
				}
			case protocol.TurnCompleted:
				goto done
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
done:
	if end.CallID == "" || !end.IsError {
		t.Fatalf("write end = %#v, want denied error (profile retained)", end)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope.txt")); !os.IsNotExist(err) {
		t.Errorf("nope.txt should not exist; err=%v", err)
	}
}

// TestChildCannotWidenViaAgentProfile is the AG3 exit criterion: parent
// read-only agent profile ⇒ child cannot gain write/edit via its own profile.
func TestChildCannotWidenViaAgentProfile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "protected.txt")
	original := "keep-me-safe\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	const childPrompt = "try write"
	taskCall := taskToolCallWithAgent("task-ag3", childPrompt, "writer")
	writeCall := writeToolCall("w-child", "secret.txt", "pwned\n")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(writeCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child after deny")
			s.match = matchToolResult("w-child")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-ag3")
			return s
		}(),
		childCompletedNudgeStep("parent ack ag3 child"),
	)

	// Base allows write so a missing parent ceiling would let the child succeed.
	baseAllow := permission.Ruleset{
		{Permission: "write", Pattern: "*", Action: permission.Allow},
		{Permission: "edit", Pattern: "*", Action: permission.Allow},
		{Permission: "read", Pattern: "*", Action: permission.Allow},
		{Permission: "task", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		SessionID:       "ag3-parent-readonly",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewWrite(), tool.NewEdit()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults(), baseAllow},
		Agents: []engine.Agent{
			{Name: "build", Description: "general"},
			{
				Name:        "reviewer",
				Description: "read-only",
				Permissions: reviewerDenyWriteEdit(),
			},
			{
				Name:        "writer",
				Description: "tries to widen",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Allow},
					{Permission: "edit", Pattern: "*", Action: permission.Allow},
				},
			},
		},
		InitialAgent: "reviewer",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AgentSelected)
		return ok && sel.Name == "reviewer"
	})

	eng.Ops() <- protocol.UserInput{Text: "delegate write to writer child"}
	var (
		askedWrite bool
		taskEnd    protocol.ToolCallEnd
		sawTask    bool
		childDone  bool
		parentDone bool
	)
	deadline := time.After(10 * time.Second)
	for !(parentDone && childDone && sawTask) {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for turn")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				// Hard deny must not ask; if it does, reject so the turn ends.
				if ev.Permission == "write" || ev.Permission == "edit" {
					askedWrite = true
				}
				eng.Ops() <- protocol.PermissionReply{
					RequestID: ev.RequestID,
					Decision:  protocol.DecisionReject,
				}
			case protocol.ChildCompleted:
				childDone = true
			case protocol.ToolCallEnd:
				if ev.CallID == "task-ag3" {
					taskEnd = ev
					sawTask = true
				}
			case protocol.TurnCompleted:
				parentDone = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if askedWrite {
		t.Error("child write emitted PermissionAsked; parent deny must hard-reject")
	}
	if !childDone {
		t.Error("missing ChildCompleted")
	}
	if !sawTask {
		t.Fatal("missing task ToolCallEnd")
	}
	_ = taskEnd
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("file content = %q, want unchanged %q", data, original)
	}
}

// TestChildAgentDenyFurtherRestricts checks a child profile can still tighten
// permissions when the parent allows the same tool.
func TestChildAgentDenyFurtherRestricts(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "open.txt")
	original := "parent-allows\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	const childPrompt = "try write"
	taskCall := taskToolCallWithAgent("task-restrict", childPrompt, "reviewer")
	writeCall := writeToolCall("w-restrict", "open.txt", "child-pwn\n")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(writeCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child after deny")
			s.match = matchToolResult("w-restrict")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-restrict")
			return s
		}(),
		childCompletedNudgeStep("parent ack restrict child"),
	)

	baseAllow := permission.Ruleset{
		{Permission: "write", Pattern: "*", Action: permission.Allow},
		{Permission: "task", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		SessionID:       "ag3-child-restrict",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewWrite()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults(), baseAllow},
		Agents: []engine.Agent{
			{Name: "build", Description: "general"},
			{
				Name:        "reviewer",
				Description: "read-only child",
				Permissions: reviewerDenyWriteEdit(),
			},
		},
		InitialAgent: "build",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AgentSelected)
		return ok && sel.Name == "build"
	})

	eng.Ops() <- protocol.UserInput{Text: "delegate to reviewer"}
	var (
		askedWrite bool
		childDone  bool
		sawTask    bool
		parentDone bool
	)
	deadline := time.After(10 * time.Second)
	for !(parentDone && childDone && sawTask) {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				if ev.Permission == "write" || ev.Permission == "edit" {
					askedWrite = true
				}
				// Approve other asks; write/edit must not ask under child deny.
				eng.Ops() <- protocol.PermissionReply{
					RequestID: ev.RequestID,
					Decision:  protocol.DecisionOnce,
				}
			case protocol.ChildCompleted:
				childDone = true
			case protocol.ToolCallEnd:
				if ev.CallID == "task-restrict" {
					sawTask = true
				}
			case protocol.TurnCompleted:
				parentDone = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if askedWrite {
		t.Error("child write emitted PermissionAsked; child deny must hard-reject")
	}
	if !childDone {
		t.Error("missing ChildCompleted")
	}
	if !sawTask {
		t.Fatal("missing task ToolCallEnd")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("file content = %q, want unchanged %q", data, original)
	}
}
