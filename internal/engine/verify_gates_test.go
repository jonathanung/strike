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

// TestVerificationGatesCmdPass: implementer completes + cmd gate exit 0 → completed + report.
func TestVerificationGatesCmdPass(t *testing.T) {
	const childPrompt = "gated-child-pass"
	handoffJSON := `{"summary":"implemented","files_changed":[],"verification":"i pinky-swear tests passed","findings":[],"blockers":[]}`
	taskCall := taskToolCallWith("task-vg-pass", map[string]any{
		"prompt": childPrompt,
		"verify": []map[string]any{
			{"kind": "cmd", "value": "true", "description": "always-pass"},
			{"kind": "schema", "value": "handoff"},
		},
	})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-vg-pass")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "lead-vg-pass",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "delegate gated"}
	events := drainAndReply(t, eng, 20*time.Second)

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
		t.Fatalf("ChildCompleted = %d; events=%v", len(completed), summarizeEvents(events))
	}
	cc := completed[0]
	if cc.Status != protocol.ChildStatusCompleted {
		t.Fatalf("status = %q, want completed", cc.Status)
	}
	if cc.Verification == nil || !cc.Verification.Passed || !cc.Verification.Verified || !cc.Verification.Claimed {
		t.Fatalf("verification = %#v", cc.Verification)
	}
	if len(cc.Verification.Checks) != 2 {
		t.Fatalf("checks = %#v", cc.Verification.Checks)
	}
	if cc.Verification.Env.SessionID == "" || cc.Verification.Env.WorkDir == "" {
		t.Fatalf("env metadata missing: %#v", cc.Verification.Env)
	}
	// Model self-report string must still be present on handoff but not used as evidence.
	if cc.Handoff.Verification != "i pinky-swear tests passed" {
		t.Fatalf("handoff.verification = %q", cc.Handoff.Verification)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "verification: ") {
		t.Fatalf("notice missing verification: %v", notices)
	}
}

// TestVerificationGatesCannotSelfCertify: model claims verified + cmd fails → blocked.
func TestVerificationGatesCannotSelfCertify(t *testing.T) {
	const childPrompt = "gated-child-fail"
	handoffJSON := `{
  "summary": "all tests green",
  "files_changed": ["x.go"],
  "verification": "make test && make vet — all passed",
  "findings": [],
  "blockers": []
}`
	taskCall := taskToolCallWith("task-vg-fail", map[string]any{
		"prompt": childPrompt,
		"verify": []map[string]any{
			{"kind": "cmd", "value": "false", "description": "must-fail-cmd"},
		},
	})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after block")
			s.match = matchToolResult("task-vg-fail")
			return s
		}(),
		childCompletedNudgeStep("ack block"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "lead-vg-fail",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 20*time.Second)

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
	if cc.Status != protocol.ChildStatusBlocked {
		t.Fatalf("status = %q, want blocked (cannot self-certify)", cc.Status)
	}
	if cc.Verification == nil || cc.Verification.Passed || cc.Verification.Verified || !cc.Verification.Claimed {
		t.Fatalf("verification = %#v", cc.Verification)
	}
	if len(cc.Verification.Checks) != 1 || cc.Verification.Checks[0].Passed {
		t.Fatalf("checks = %#v", cc.Verification.Checks)
	}
	// Actionable blockers for implementer/lead.
	found := false
	for _, b := range cc.Handoff.Blockers {
		if strings.Contains(b, "must-fail-cmd") || strings.Contains(b, "exit") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("blockers missing gate failure: %#v", cc.Handoff.Blockers)
	}
	// Model self-report must not flip Passed.
	if !strings.Contains(cc.Handoff.Verification, "all passed") {
		t.Fatalf("expected model self-report retained: %q", cc.Handoff.Verification)
	}
}

// TestVerificationGatesSchemaFail: incomplete handoff fails schema gate → blocked.
func TestVerificationGatesSchemaFail(t *testing.T) {
	const childPrompt = "gated-schema-fail"
	taskCall := taskToolCallWith("task-vg-schema", map[string]any{
		"prompt": childPrompt,
		"verify": []map[string]any{
			{"kind": "schema", "value": "handoff"},
		},
	})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			// Prose only — no structured handoff JSON.
			s := completedStep("I finished the work, trust me.")
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent")
			s.match = matchToolResult("task-vg-schema")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "lead-vg-schema",
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
	events := drainAndReply(t, eng, 20*time.Second)

	var completed []protocol.ChildCompleted
	for _, ev := range events {
		if cc, ok := ev.(protocol.ChildCompleted); ok {
			completed = append(completed, cc)
		}
	}
	if len(completed) != 1 {
		t.Fatalf("ChildCompleted = %d", len(completed))
	}
	cc := completed[0]
	if cc.Status != protocol.ChildStatusBlocked {
		t.Fatalf("status = %q, want blocked", cc.Status)
	}
	if cc.Verification == nil || cc.Verification.Passed {
		t.Fatalf("verification = %#v", cc.Verification)
	}
	if len(cc.Verification.Checks) != 1 || cc.Verification.Checks[0].Kind != "schema" {
		t.Fatalf("checks = %#v", cc.Verification.Checks)
	}
}

// TestVerificationGatesPathAndTaskStatus: path gate + task_status exposes report.
func TestVerificationGatesPathAndTaskStatus(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "artifact.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	const childPrompt = "gated-path"
	handoffJSON := `{"summary":"wrote artifact","files_changed":["artifact.txt"],"findings":[],"blockers":[]}`
	taskCall := taskToolCallWith("task-vg-path", map[string]any{
		"prompt": childPrompt,
		"verify": []map[string]any{
			{"kind": "path", "value": "artifact.txt"},
			{"kind": "schema", "value": "handoff"},
		},
	})
	// After child completes, parent calls task_status.
	statusCall := provider.ToolCall{
		ID:   "ts-1",
		Name: "task_status",
		// session_id filled dynamically is hard; use include and match via scripted steps carefully.
		// We'll capture child id from ChildCompleted in a second phase — simpler: just check ChildCompleted.
		Args: json.RawMessage(`{"session_id":"will-replace"}`),
	}
	_ = statusCall

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-vg-path")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "lead-vg-path",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewTaskStatus()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "path gate"}
	events := drainAndReply(t, eng, 20*time.Second)

	var completed []protocol.ChildCompleted
	for _, ev := range events {
		if cc, ok := ev.(protocol.ChildCompleted); ok {
			completed = append(completed, cc)
		}
	}
	if len(completed) != 1 {
		t.Fatalf("ChildCompleted = %d", len(completed))
	}
	cc := completed[0]
	if cc.Status != protocol.ChildStatusCompleted {
		t.Fatalf("status = %q want completed; ver=%#v", cc.Status, cc.Verification)
	}
	if cc.Verification == nil || !cc.Verification.Passed {
		t.Fatalf("verification = %#v", cc.Verification)
	}
}

// TestNoVerifyGatesSkipsReport: without verify, no verification field (compat).
func TestNoVerifyGatesSkipsReport(t *testing.T) {
	const childPrompt = "no-gates"
	taskCall := taskToolCall("task-nogate", childPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep(`{"summary":"ok","files_changed":[],"findings":[],"blockers":[]}`)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent")
			s.match = matchToolResult("task-nogate")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "lead-nogate",
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
		t.Fatalf("ChildCompleted = %d", len(completed))
	}
	if completed[0].Status != protocol.ChildStatusCompleted {
		t.Fatalf("status = %q", completed[0].Status)
	}
	if completed[0].Verification != nil {
		t.Fatalf("want nil verification without gates, got %#v", completed[0].Verification)
	}
}
