package engine

import (
	"context"
	"testing"

	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// diagBlockingProvider never emits: its Stream blocks until the stream
// context is cancelled. The test below uses it so the spawned child cannot
// finish its first turn — and retire from Engine.children into
// childHistory — before the test inspects the live child map. With the
// instant echo provider the child could complete and retire first under
// -race scheduling, leaving "no child engine" even though the spawn
// succeeded (~1/20 under -race, 0/20 without, on main e9cd5a63).
type diagBlockingProvider struct{}

func (diagBlockingProvider) Name() string { return "diag-blocking" }

func (diagBlockingProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.StreamEvent, error) {
	stream := make(chan provider.StreamEvent)
	go func() {
		defer close(stream)
		<-ctx.Done()
	}()
	return stream, nil
}

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
			return diagBlockingProvider{}, "diag-blocking", nil
		},
		InitialProvider: "diag-blocking",
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
	// Do not call buildDiagnosticBundleEvent here: the child Run loop is live
	// and composeSystemLayers is not safe from the test goroutine.
}
