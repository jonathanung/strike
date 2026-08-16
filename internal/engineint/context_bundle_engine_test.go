package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/internal/tools"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// TestContextBundleAttachAndChildRead: lead attaches a sealed bundle; child
// reads it via context_bundle (asserted via provider seeing a successful tool
// result); ChildStarted carries the bundle for snapshots.
func TestContextBundleAttachAndChildRead(t *testing.T) {
	const (
		taskPrompt   = "use-context-bundle"
		parentPrompt = "spawn with bundle"
	)
	bundleCall := provider.ToolCall{
		ID:   "cb-get",
		Name: "context_bundle",
		Args: json.RawMessage(`{"action":"get"}`),
	}
	taskCall := taskToolCallWith("task-bundle", map[string]any{
		"prompt": taskPrompt,
		"context_bundle": map[string]any{
			"goal":           "implement feature X",
			"acceptance":     []string{"tests pass"},
			"allowed_paths":  []string{"internal/feature"},
			"required_paths": []string{"docs/spec.md"},
			"constraints":    []string{"no network"},
			"artifacts":      []map[string]any{{"id": "art1", "type": "contract"}},
			"items": []map[string]any{
				{"id": "contract-1", "kind": "note", "text": "use v2 API"},
			},
		},
	})
	handoffJSON := `{
  "summary": "done with provenance",
  "files_changed": [],
  "findings": ["used sealed contract"],
  "blockers": [],
  "provenance": ["goal", "contract-1"],
  "recommended_next_action": "lead merges"
}`

	allowAll := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.Allow},
	}
	var sawBundleToolResult bool
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(bundleCall)
			s.match = matchUserText(taskPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = func(req provider.Request) bool {
				if !matchToolResult("cb-get")(req) {
					return false
				}
				// Child tool events stay on the child stream; assert the tool
				// result body here (stable interface for sealed context).
				for _, m := range req.Messages {
					if m.Role != provider.RoleTool || m.ToolResult == nil || m.ToolResult.CallID != "cb-get" {
						continue
					}
					if m.ToolResult.IsError {
						return false
					}
					out := m.ToolResult.Output
					if strings.Contains(out, "implement feature X") && strings.Contains(out, "contract-1") {
						sawBundleToolResult = true
						return true
					}
				}
				return false
			}
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-bundle")
			return s
		}(),
		childCompletedNudgeStep("parent ack"),
	)

	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "lead-bundle",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tools.NewContextBundle()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{allowAll},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: parentPrompt}
	events := drainAndReply(t, eng, 15*time.Second)

	var started []protocol.ChildStarted
	var completed []protocol.ChildCompleted
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started = append(started, ev)
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if len(started) != 1 {
		t.Fatalf("ChildStarted = %d; events=%v", len(started), summarizeEvents(events))
	}
	if started[0].ContextBundle == nil {
		t.Fatal("ChildStarted missing contextBundle")
	}
	if started[0].ContextBundle.Goal != "implement feature X" {
		t.Fatalf("goal = %q", started[0].ContextBundle.Goal)
	}
	if len(started[0].ContextBundle.AllowedPaths) != 1 || started[0].ContextBundle.AllowedPaths[0] != "internal/feature" {
		t.Fatalf("allowed = %#v", started[0].ContextBundle.AllowedPaths)
	}
	// Synthetic + custom items present on wire.
	itemIDs := map[string]bool{}
	for _, it := range started[0].ContextBundle.Items {
		itemIDs[it.ID] = true
	}
	if !itemIDs["goal"] || !itemIDs["contract-1"] {
		t.Fatalf("items = %#v", started[0].ContextBundle.Items)
	}

	if !sawBundleToolResult {
		t.Fatal("context_bundle tool did not return sealed goal/items to the child model")
	}

	if len(completed) != 1 {
		t.Fatalf("ChildCompleted = %d", len(completed))
	}
	if completed[0].Status != protocol.ChildStatusCompleted {
		t.Fatalf("status = %q", completed[0].Status)
	}
	if len(completed[0].Handoff.Provenance) != 2 {
		t.Fatalf("provenance = %#v", completed[0].Handoff.Provenance)
	}
}

// TestContextBundleMissingContextBlocks: child reports missing_context → blocked.
func TestContextBundleMissingContextBlocks(t *testing.T) {
	const (
		taskPrompt   = "need-more-context"
		parentPrompt = "spawn missing-context child"
	)
	handoffJSON := `{
  "summary": "cannot proceed",
  "files_changed": [],
  "findings": [],
  "blockers": ["spec missing"],
  "missing_context": [
    {"kind": "path", "path": "docs/spec.md", "detail": "required but absent"},
    {"kind": "question", "question": "Target Go version?"}
  ],
  "recommended_next_action": "lead attaches docs/spec.md"
}`
	taskCall := taskToolCallWith("task-mc", map[string]any{
		"prompt": taskPrompt,
		"context_bundle": map[string]any{
			"goal": "work that needs a spec",
		},
	})
	allowAll := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.Allow},
	}
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchUserText(taskPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-mc")
			return s
		}(),
		childCompletedNudgeStep("parent ack"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "lead-mc",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tools.NewContextBundle()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{allowAll},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: parentPrompt}
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
	if cc.Status != protocol.ChildStatusBlocked {
		t.Fatalf("status = %q want blocked; handoff=%#v", cc.Status, cc.Handoff)
	}
	if len(cc.Handoff.MissingContext) != 2 {
		t.Fatalf("missing_context = %#v", cc.Handoff.MissingContext)
	}
	if cc.Handoff.MissingContext[0].Path != "docs/spec.md" {
		t.Fatalf("path = %#v", cc.Handoff.MissingContext[0])
	}
}

// TestContextBundlePathScopeDeniesOutside: allowed_paths scopes child write.
// Child tool ends are not re-emitted on the parent stream; assert via the
// child's next provider turn (tool result is error) and that the file is absent.
func TestContextBundlePathScopeDeniesOutside(t *testing.T) {
	const (
		taskPrompt   = "write-outside-scope"
		parentPrompt = "spawn scoped child"
	)
	// Child tries to write outside allowed_paths.
	writeCall := writeToolCall("w-out", "secrets/out.txt", "nope\n")
	taskCall := taskToolCallWith("task-scope", map[string]any{
		"prompt": taskPrompt,
		"context_bundle": map[string]any{
			"goal":          "only touch internal/ok",
			"allowed_paths": []string{"internal/ok"},
		},
	})
	// Parent allow-all, but bundle scope layer is appended after and should deny.
	allowAll := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.Allow},
	}
	var sawDeniedWrite bool
	workDir := t.TempDir()
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(writeCall)
			s.match = matchUserText(taskPrompt)
			return s
		}(),
		func() streamStep {
			// After denied write, finish.
			s := completedStep(`{"summary":"could not write","files_changed":[],"blockers":["denied"],"findings":[]}`)
			s.match = func(req provider.Request) bool {
				if !matchToolResult("w-out")(req) {
					return false
				}
				for _, m := range req.Messages {
					if m.Role != provider.RoleTool || m.ToolResult == nil || m.ToolResult.CallID != "w-out" {
						continue
					}
					if m.ToolResult.IsError {
						sawDeniedWrite = true
						return true
					}
				}
				return false
			}
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-scope")
			return s
		}(),
		childCompletedNudgeStep("parent ack"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "lead-scope",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewWrite(), tools.NewContextBundle()),
		WorkDir:         workDir,
		Rules:           []permission.Ruleset{allowAll},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: parentPrompt}
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
	if !sawDeniedWrite {
		t.Fatal("expected child write outside allowed_paths to be denied")
	}
	// File must not exist on disk.
	if _, err := os.Stat(filepath.Join(workDir, "secrets", "out.txt")); err == nil {
		t.Fatal("write outside scope created file")
	}
}
