package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// TestUnsetInitialEffortEmitsNoConfirmation keeps startup quiet for the
// default case. Announcing an unset dial would put "effort: — provider
// default" in the notice line on every launch, which says nothing.
func TestUnsetInitialEffortEmitsNoConfirmation(t *testing.T) {
	rec := &recordingProvider{}
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return rec, "m", nil },
		InitialProvider: "recording",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// AgentSelected is emitted after the effort step, so seeing it means the
	// startup sequence got past the point where an effort event would appear.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for startup to settle")
		case ev := <-eng.Events():
			if sel, ok := ev.(protocol.EffortSelected); ok {
				t.Fatalf("startup emitted EffortSelected{%q} with no configured effort", sel.Level)
			}
			if _, ok := ev.(protocol.AgentSelected); ok {
				return
			}
		}
	}
}
