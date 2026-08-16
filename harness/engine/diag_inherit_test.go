package engine

import (
	"context"
	"testing"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/provider/echo"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestSpawnedChildInheritsBuildDiagnostic(t *testing.T) {
	builder := DiagnosticBuilder(func(DiagnosticBuildInput) protocol.DiagnosticBundle {
		return protocol.DiagnosticBundle{StrikeVersion: "from-builder"}
	})
	e := New(Options{
		SessionID:       "root-diag-inherit",
		WorkDir:         t.TempDir(),
		Registry:        tool.NewRegistry(tool.NewTask()),
		BuildDiagnostic: builder,
		Agents:          []Agent{{Name: "build"}},
		InitialAgent:    "build",
		Select: func(string) (provider.Provider, string, error) {
			return echo.New(), "echo", nil
		},
		InitialProvider: "echo",
	})
	_, err := e.spawnChildInner(context.Background(), tool.TaskRequest{
		Prompt:        "child inspect",
		Agent:         "build",
		ForceDelegate: true,
	}, "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() {
		e.childMu.Lock()
		defer e.childMu.Unlock()
		for _, h := range e.children {
			if h.cancel != nil {
				h.cancel()
			}
		}
	})
	e.childMu.Lock()
	var child *Engine
	for _, h := range e.children {
		child = h.eng
	}
	e.childMu.Unlock()
	if child == nil {
		t.Fatal("no child engine")
	}
	if child.opts.BuildDiagnostic == nil {
		t.Fatal("spawned child dropped BuildDiagnostic")
	}
	ev := child.buildDiagnosticBundleEvent()
	if ev.StrikeVersion != "from-builder" {
		t.Fatalf("child inspect did not use parent builder: %#v", ev)
	}
}
