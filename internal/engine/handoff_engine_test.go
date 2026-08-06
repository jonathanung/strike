package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// TestChildCompletedStructuredHandoffMerge covers success path: model JSON
// handoff + engine-tracked write path merge on ChildCompleted and notice.
func TestChildCompletedStructuredHandoffMerge(t *testing.T) {
	const (
		taskPrompt   = "implement-handoff-slice"
		parentPrompt = "delegate handoff work"
	)
	handoffJSON := `{
  "summary": "wrote note",
  "files_changed": ["extra.md"],
  "verification": "read back ok",
  "findings": ["path stable"],
  "blockers": [],
  "recommended_next_action": "lead reviews"
}`
	writeCall := writeToolCall("w-note", "note.txt", "hello\n")
	taskCall := taskToolCall("task-handoff", taskPrompt)

	// Allow write without interactive permission prompts.
	allowAll := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.Allow},
	}

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			// Child: write then finish with structured handoff JSON.
			s := toolCallStep(writeCall)
			s.match = matchUserText(taskPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchToolResult("w-note")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-handoff")
			return s
		}(),
		childCompletedNudgeStep("parent ack"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "lead-handoff",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewWrite()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{allowAll},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: parentPrompt}
	events := drainAndReply(t, eng, 15*time.Second)

	var completed []protocol.ChildCompleted
	var notices []string
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		case protocol.UserMessage:
			if strings.Contains(ev.Text, "[child.completed") {
				notices = append(notices, ev.Text)
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if len(completed) != 1 {
		t.Fatalf("ChildCompleted = %d, want 1; events=%v", len(completed), summarizeEvents(events))
	}
	cc := completed[0]
	if cc.Status != protocol.ChildStatusCompleted {
		t.Fatalf("status = %q", cc.Status)
	}
	if cc.Handoff.Incomplete {
		t.Fatalf("want complete handoff, got incomplete: %#v", cc.Handoff)
	}
	if cc.Handoff.Summary != "wrote note" {
		t.Fatalf("summary = %q", cc.Handoff.Summary)
	}
	if cc.Summary != cc.Handoff.Summary {
		t.Fatalf("Summary field %q != handoff.summary %q", cc.Summary, cc.Handoff.Summary)
	}
	// Engine-tracked note.txt + model extra.md.
	files := map[string]bool{}
	for _, f := range cc.Handoff.FilesChanged {
		files[f] = true
	}
	if !files["note.txt"] {
		t.Fatalf("missing engine-tracked note.txt in %#v", cc.Handoff.FilesChanged)
	}
	if !files["extra.md"] {
		t.Fatalf("missing model extra.md in %#v", cc.Handoff.FilesChanged)
	}
	if cc.Handoff.Verification != "read back ok" {
		t.Fatalf("verification = %q", cc.Handoff.Verification)
	}
	if cc.Handoff.RecommendedNextAction != "lead reviews" {
		t.Fatalf("next = %q", cc.Handoff.RecommendedNextAction)
	}

	if len(notices) == 0 {
		t.Fatal("missing [child.completed] notice")
	}
	n := notices[0]
	if !strings.Contains(n, "handoff: ") || !strings.Contains(n, `"files_changed"`) {
		t.Fatalf("notice missing structured handoff: %q", n)
	}
	if !strings.Contains(n, "note.txt") {
		t.Fatalf("notice missing tracked file: %q", n)
	}
}

// TestChildCompletedHandoffFailureIncomplete covers failed child with no model
// JSON → reduced structured payload + incomplete flag.
func TestChildCompletedHandoffFailureIncomplete(t *testing.T) {
	const (
		errMsg     = "child stream boom: handoff-fail-test"
		taskPrompt = "failing-child-handoff"
	)
	taskCall := taskToolCall("task-fail-h", taskPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		streamStep{err: errors.New(errMsg), match: matchUserText(taskPrompt)},
		func() streamStep {
			s := completedStep("parent after fail")
			s.match = matchToolResult("task-fail-h")
			return s
		}(),
		childCompletedNudgeStep("ack fail"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "lead-fail-h",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainAndReply(t, eng, 15*time.Second)

	var completed []protocol.ChildCompleted
	for _, ev := range events {
		if cc, ok := ev.(protocol.ChildCompleted); ok {
			completed = append(completed, cc)
		}
	}
	if len(completed) != 1 {
		t.Fatalf("ChildCompleted = %d; events=%v", len(completed), summarizeEvents(events))
	}
	cc := completed[0]
	if cc.Status != protocol.ChildStatusFailed {
		t.Fatalf("status = %q, want failed", cc.Status)
	}
	if !strings.Contains(cc.Handoff.Summary, errMsg) {
		t.Fatalf("summary = %q, want contain %q", cc.Handoff.Summary, errMsg)
	}
	if cc.Handoff.FilesChanged == nil {
		t.Fatal("want non-nil files_changed")
	}
	if cc.Handoff.Blockers == nil {
		t.Fatal("want non-nil blockers")
	}
	if !cc.Handoff.Incomplete {
		t.Fatalf("want incomplete handoff on prose/error-only failure: %#v", cc.Handoff)
	}
	raw, _ := json.Marshal(cc.Handoff)
	if !strings.Contains(string(raw), `"filesChanged"`) {
		t.Fatalf("wire handoff missing filesChanged: %s", raw)
	}
}
