package engine_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// TestEngineAskCarriesDecisionFacts pins the #1507 wiring: an engine stamps
// its working directory and its project rule store path onto every
// PermissionAsked it emits, so frontends can show the decider where the
// tool call runs and where an "Allow project" grant will be stored.
func TestEngineAskCarriesDecisionFacts(t *testing.T) {
	dir := t.TempDir()
	rulePath := filepath.Join(dir, ".strike", "config")

	args, _ := json.Marshal(map[string]any{"command": "ls -la"})
	prov := newScriptedProvider(
		toolCallStep(provider.ToolCall{ID: "b1", Name: "bash", Args: args}),
		completedStep("done"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "ask-facts",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults()},
		ProjectRulePath: rulePath,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "list files"}
	deadline := time.After(15 * time.Second)
loop:
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the bash ask / turn completion")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				if ev.Permission != "bash" {
					t.Fatalf("ask permission = %q, want bash", ev.Permission)
				}
				if ev.WorkDir != dir {
					t.Errorf("PermissionAsked.WorkDir = %q, want engine WorkDir %q", ev.WorkDir, dir)
				}
				if ev.ProjectRulePath != rulePath {
					t.Errorf("PermissionAsked.ProjectRulePath = %q, want %q", ev.ProjectRulePath, rulePath)
				}
				eng.Ops() <- protocol.PermissionReply{
					RequestID: ev.RequestID,
					Decision:  protocol.DecisionOnce,
				}
			case protocol.TurnCompleted:
				break loop
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
}
