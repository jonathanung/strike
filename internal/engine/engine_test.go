package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/echo"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func selectEcho(string) (provider.Provider, string, error) {
	return echo.New(), "echo", nil
}

// TestFullLoop drives user input -> tool call -> permission ask -> approval
// -> tool result -> final message through the real engine with the echo
// provider, asserting the event sequence a frontend would see.
func TestFullLoop(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run echo hello-strike"}

	var sawAsk, sawToolEnd, sawCompleted bool
	var toolOutput string
	deadline := time.After(10 * time.Second)
	for !sawCompleted {
		select {
		case <-deadline:
			t.Fatalf("timed out; ask=%v toolEnd=%v", sawAsk, sawToolEnd)
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				sawAsk = true
				if ev.Permission != "bash" {
					t.Errorf("permission = %q, want bash", ev.Permission)
				}
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.ToolCallEnd:
				sawToolEnd = true
				toolOutput = ev.Output
				if ev.IsError {
					t.Errorf("tool call failed: %s", ev.Output)
				}
			case protocol.TurnCompleted:
				sawCompleted = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if !sawAsk {
		t.Error("no PermissionAsked event")
	}
	if !sawToolEnd {
		t.Error("no ToolCallEnd event")
	}
	if !strings.Contains(toolOutput, "hello-strike") {
		t.Errorf("tool output %q does not contain command output", toolOutput)
	}
}

// TestNoModelSelected verifies input without a selected provider produces
// an EngineError, then works after a SelectModel op.
func TestNoModelSelected(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:   selectEcho,
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
		Rules:    []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for EngineError")
		case ev := <-eng.Events():
			if err, ok := ev.(protocol.EngineError); ok {
				if !strings.Contains(err.Message, "no model selected") {
					t.Errorf("error = %q, want no-model-selected", err.Message)
				}
				goto selected
			}
		}
	}
selected:
	eng.Ops() <- protocol.SelectModel{Provider: "echo"}
	eng.Ops() <- protocol.UserInput{Text: "hello again"}
	sawSelected := false
	for {
		select {
		case <-deadline:
			t.Fatal("timed out after SelectModel")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ModelSelected:
				sawSelected = true
				if ev.Provider != "echo" || ev.Model != "echo" {
					t.Errorf("selected = %s/%s, want echo/echo", ev.Provider, ev.Model)
				}
			case protocol.EngineError:
				t.Fatalf("engine error after select: %s", ev.Message)
			case protocol.TurnCompleted:
				if !sawSelected {
					t.Error("no ModelSelected event")
				}
				return
			}
		}
	}
}

// TestRejectionFeedsBackToModel verifies a rejected permission becomes a
// correctable tool-result error, not a turn abort.
func TestRejectionFeedsBackToModel(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run rm -rf /"}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{
					RequestID: ev.RequestID,
					Decision:  protocol.DecisionReject,
					Message:   "do not delete anything",
				}
			case protocol.ToolCallEnd:
				if !ev.IsError {
					t.Error("rejected call should be an error result")
				}
				if !strings.Contains(ev.Output, "do not delete anything") {
					t.Errorf("rejection feedback missing from output: %q", ev.Output)
				}
			case protocol.TurnCompleted:
				return
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
}

func TestSetFast(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:   selectEcho,
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
		Rules:    []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.SetFast{Enabled: true}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for FastSelected")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.FastSelected:
				if !ev.Enabled {
					t.Fatalf("Enabled = false, want true")
				}
				return
			case protocol.AgentSelected:
				// startup agent selection; ignore
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
}
