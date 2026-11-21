package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

func newSteerAckEngine(t *testing.T, reg *tool.Registry) *Engine {
	t.Helper()
	return New(Options{
		SessionID: "s1",
		WorkDir:   t.TempDir(),
		Registry:  reg,
		Agents:    []Agent{{Name: "build"}},
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
}

// drainSteerAcks returns the TurnSteerAck events buffered on the engine.
func drainSteerAcks(e *Engine) []protocol.TurnSteerAck {
	var out []protocol.TurnSteerAck
	for {
		select {
		case ev := <-e.events:
			if a, ok := ev.(protocol.TurnSteerAck); ok {
				out = append(out, a)
			}
		default:
			return out
		}
	}
}

func TestAckSteerEmitsOncePerAppliedSteer(t *testing.T) {
	e := newSteerAckEngine(t, nil)
	corr := protocol.Correlation{SessionID: "s1", TurnID: "t1"}

	if err := e.ackSteer(corr, tool.SteerAck{Summary: "now v2"}); err == nil {
		t.Fatal("ack with no applied steer must fail")
	}

	e.recordAppliedSteer("t1")
	e.recordAppliedSteer("t1")
	if err := e.ackSteer(corr, tool.SteerAck{Summary: "  now using\nthe v2 api "}); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	if err := e.ackSteer(corr, tool.SteerAck{Summary: "dropping the refactor", Conflict: true, Reason: "plan was mid-refactor"}); err != nil {
		t.Fatalf("second ack: %v", err)
	}
	if err := e.ackSteer(corr, tool.SteerAck{Summary: "again"}); err == nil {
		t.Fatal("a third ack for two steers must fail")
	}

	got := drainSteerAcks(e)
	if len(got) != 2 {
		t.Fatalf("acks = %d, want 2: %+v", len(got), got)
	}
	if got[0].Summary != "now using the v2 api" || got[0].Conflict || got[0].Reason != "" || got[0].TargetTurnID != "t1" || got[0].TurnID != "t1" {
		t.Errorf("first ack = %+v", got[0])
	}
	if !got[1].Conflict || got[1].Reason != "plan was mid-refactor" {
		t.Errorf("second ack = %+v", got[1])
	}
}

func TestAckSteerValidation(t *testing.T) {
	tests := []struct {
		name string
		ack  tool.SteerAck
		want string
	}{
		{"empty summary", tool.SteerAck{Summary: "   "}, "summary is required"},
		{"conflict without reason", tool.SteerAck{Summary: "x", Conflict: true}, "reason is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSteerAckEngine(t, nil)
			e.recordAppliedSteer("t1")
			err := e.ackSteer(protocol.Correlation{}, tt.ack)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if !e.hasUnackedSteer() {
				t.Fatal("a rejected ack must not consume the steer")
			}
		})
	}
}

func TestAckSteerDropsReasonWithoutConflictAndCaps(t *testing.T) {
	e := newSteerAckEngine(t, nil)
	e.recordAppliedSteer("t1")
	long := strings.Repeat("a", maxSteerAckRunes+50)
	if err := e.ackSteer(protocol.Correlation{}, tool.SteerAck{Summary: long, Reason: "ignored"}); err != nil {
		t.Fatal(err)
	}
	got := drainSteerAcks(e)
	if len(got) != 1 {
		t.Fatalf("acks = %d", len(got))
	}
	if got[0].Reason != "" {
		t.Errorf("reason without conflict = %q, want empty", got[0].Reason)
	}
	if n := len([]rune(got[0].Summary)); n != maxSteerAckRunes {
		t.Errorf("summary runes = %d, want %d", n, maxSteerAckRunes)
	}
}

func TestClearUnackedSteersAtTurnEnd(t *testing.T) {
	e := newSteerAckEngine(t, nil)
	e.recordAppliedSteer("t1")
	e.clearUnackedSteers()
	if e.hasUnackedSteer() {
		t.Fatal("unacked steers must clear at turn end")
	}
	if err := e.ackSteer(protocol.Correlation{}, tool.SteerAck{Summary: "late"}); err == nil {
		t.Fatal("a later turn must not answer an earlier steer")
	}
}

func TestSteerAckActivationFollowsUnackedSteer(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead(), namedStub{name: steerAckTool})
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)
	e := newSteerAckEngine(t, reg)
	for _, f := range e.applyWorkflowToolActivation() {
		if f == activationFamilySteer {
			t.Fatal("steer family active with no steer")
		}
	}
	if reg.Discovered(steerAckTool) {
		t.Fatal("steer_ack discovered with no steer")
	}
	e.recordAppliedSteer("t1")
	found := false
	for _, f := range e.applyWorkflowToolActivation() {
		found = found || f == activationFamilySteer
	}
	if !found || !reg.Discovered(steerAckTool) {
		t.Fatal("steer_ack not activated after an applied steer")
	}
}
