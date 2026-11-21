package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// childOnlyFailProvider fails the delegated child's requests (those whose
// history carries the child prompt) and scripts the parent's.
type childOnlyFailProvider struct {
	parent      *scriptedProvider
	childPrompt string
	failures    failingProvider
}

func (p *childOnlyFailProvider) Name() string { return "child-only-fail" }

func (p *childOnlyFailProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	if matchUserText(p.childPrompt)(req) {
		return p.failures.Stream(ctx, req)
	}
	return p.parent.Stream(ctx, req)
}

// TestChildInheritsFallbackAndParentSeesItsNotice pins #1963: a child
// spawned while a fallback is set inherits the primary/fallback pair, and
// a child turn the fallback answered is marked on the parent's stream with
// the child's correlation.
func TestChildInheritsFallbackAndParentSeesItsNotice(t *testing.T) {
	const childPrompt = "child work on fallback"
	primary := &childOnlyFailProvider{
		parent: newScriptedProvider(
			toolCallStep(taskToolCall("task-fb", childPrompt)),
			func() streamStep {
				s := completedStep("parent done")
				s.match = matchToolResult("task-fb")
				return s
			}(),
			childCompletedNudgeStep("parent ack fallback child"),
		),
		childPrompt: childPrompt,
		failures:    failingProvider{err: errors.New("primary unavailable for child")},
	}
	backup := newScriptedProvider(completedStep("child answered on backup"))
	providers := map[string]provider.Provider{"primary": primary, "backup": backup}
	eng := engine.New(engine.Options{
		ChildHandoffRequired: handoffOff(),
		SessionID:            "parent-fallback-child",
		InitialProvider:      "primary",
		Select: func(name string) (provider.Provider, string, error) {
			p, ok := providers[name]
			if !ok {
				return nil, "", errors.New("unknown provider " + name)
			}
			return p, name + "-default", nil
		},
		Registry: tool.NewRegistry(tool.NewTask()),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	setFallback(t, eng, "backup", "b1")
	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 10*time.Second)

	var child []protocol.ModelFallback
	for _, fb := range fallbackEvents(events) {
		if fb.SessionID == "parent-fallback-child" {
			t.Errorf("parent turn fell back: %+v", fb)
			continue
		}
		child = append(child, fb)
	}
	if len(child) != 1 {
		t.Fatalf("child ModelFallback on parent stream = %d, want 1; events=%v", len(child), summarizeEvents(events))
	}
	fb := child[0]
	if fb.Depth != 1 || fb.ParentSessionID != "parent-fallback-child" {
		t.Errorf("child notice correlation = %+v, want depth 1 under the parent", fb.Correlation)
	}
	if fb.PrimaryProvider != "primary" || fb.FallbackProvider != "backup" || fb.FallbackModel != "b1" {
		t.Errorf("child notice = %+v, want primary -> backup/b1", fb)
	}
	if fb.Reason != protocol.ModelFallbackReasonUnavailable || fb.Sticky {
		t.Errorf("child notice reason=%q sticky=%v, want unavailable, not sticky", fb.Reason, fb.Sticky)
	}
	for _, ev := range events {
		if end, ok := ev.(protocol.ChildCompleted); ok && end.Status != protocol.ChildStatusCompleted {
			t.Errorf("ChildCompleted status = %q, want completed", end.Status)
		}
	}
}
