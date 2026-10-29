package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestDelegatedChildOutsideWorkspaceIsRejected(t *testing.T) {
	work := t.TempDir()
	other := t.TempDir()
	taskCall := taskToolCall("task-out", "do the thing")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent saw the rejection")
			s.match = matchToolResult("task-out")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		ChildHandoffRequired: handoffOff(),
		SessionID:            "parent",
		Select:               func(string) (provider.Provider, string, error) { return prov, "scripted-model", nil },
		InitialProvider:      "scripted",
		Registry:             tool.NewRegistry(tool.NewTask()),
		WorkDir:              work,
		WorkspaceRoots:       []string{other},
		Rules:                []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "spawn"}
	events := drainAndReply(t, eng, 10*time.Second)

	var ends []protocol.ToolCallEnd
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			t.Fatal("child should not start outside the workspace")
		case protocol.ToolCallEnd:
			if ev.CallID == "task-out" {
				ends = append(ends, ev)
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if len(ends) != 1 || !ends[0].IsError || !strings.Contains(ends[0].Output, "outside the workspace") {
		t.Fatalf("tool end = %+v", ends)
	}
}
