package server

import (
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
)

// TestStatusTracksStickyFallback pins #1922: a root sticky ModelFallback
// marks the session as running on its fallback, and the next
// fallback.selected (fallback.return, a new pick) carries the truth.
func TestStatusTracksStickyFallback(t *testing.T) {
	root := protocol.Correlation{SessionID: "s1"}
	child := protocol.Correlation{SessionID: "c1", ParentSessionID: "s1", Depth: 1}
	tests := []struct {
		name   string
		events []protocol.Event
		want   bool
	}{
		{"none", nil, false},
		{"unavailable is one turn", []protocol.Event{protocol.ModelFallback{Correlation: root, Reason: protocol.ModelFallbackReasonUnavailable}}, false},
		{"refused is sticky", []protocol.Event{protocol.ModelFallback{Correlation: root, Reason: protocol.ModelFallbackReasonRefused, Sticky: true}}, true},
		{"child refusal ignored", []protocol.Event{protocol.ModelFallback{Correlation: child, Reason: protocol.ModelFallbackReasonRefused, Sticky: true}}, false},
		{"return clears", []protocol.Event{
			protocol.ModelFallback{Correlation: root, Reason: protocol.ModelFallbackReasonRefused, Sticky: true},
			protocol.FallbackModelSelected{Correlation: root, Provider: "openai", Model: "gpt"},
		}, false},
	}
	for _, tt := range tests {
		l := &Live{}
		for _, ev := range tt.events {
			l.applyStatus(ev)
		}
		if got := l.status.FallbackActive; got != tt.want {
			t.Errorf("%s: FallbackActive = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestStatusIgnoresChildFallbackSelection pins #1963: a delegated child's
// inherited fallback.selected never replaces the root's reported fallback.
func TestStatusIgnoresChildFallbackSelection(t *testing.T) {
	l := &Live{}
	l.applyStatus(protocol.FallbackModelSelected{Correlation: protocol.Correlation{SessionID: "s1"}, Provider: "openai", Model: "gpt"})
	l.applyStatus(protocol.FallbackModelSelected{Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "s1", Depth: 1}, Provider: "other", Model: "x", Active: true})
	if l.status.FallbackProvider != "openai" || l.status.FallbackModel != "gpt" || l.status.FallbackActive {
		t.Errorf("status = %q/%q active=%v, want openai/gpt inactive", l.status.FallbackProvider, l.status.FallbackModel, l.status.FallbackActive)
	}
}
