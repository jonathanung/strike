package engine

import (
	"errors"
	"strings"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// maxSteerAckRunes caps the one-line summary and conflict reason.
const maxSteerAckRunes = 200

// errNoUnackedSteer is returned when steer_ack has nothing to acknowledge:
// no steer was applied in this turn, or every applied steer was already
// acknowledged. One acknowledgement per steer, never invented.
var errNoUnackedSteer = errors.New("steer_ack: no unacknowledged steer in this turn; call it only right after a user steer lands, once per steer")

// recordAppliedSteer queues an applied (boundary or cancel_restart) steer for
// the agent's acknowledgement. Queued-fallback steers never reach here: they
// become an ordinary next message.
func (e *Engine) recordAppliedSteer(targetTurnID string) {
	e.pendingSteerMu.Lock()
	defer e.pendingSteerMu.Unlock()
	e.unackedSteers = append(e.unackedSteers, targetTurnID)
}

// hasUnackedSteer reports whether a steer applied this turn awaits steer_ack.
func (e *Engine) hasUnackedSteer() bool {
	if e == nil {
		return false
	}
	e.pendingSteerMu.Lock()
	defer e.pendingSteerMu.Unlock()
	return len(e.unackedSteers) > 0
}

// clearUnackedSteers drops unanswered steers when the turn ends.
func (e *Engine) clearUnackedSteers() {
	e.pendingSteerMu.Lock()
	defer e.pendingSteerMu.Unlock()
	e.unackedSteers = nil
}

// ackSteer answers the oldest unacknowledged steer with a durable
// TurnSteerAck. It never blocks the turn: a conflict is reported, not asked.
func (e *Engine) ackSteer(corr protocol.Correlation, ack tool.SteerAck) error {
	summary := oneLineCapped(ack.Summary, maxSteerAckRunes)
	reason := oneLineCapped(ack.Reason, maxSteerAckRunes)
	if summary == "" {
		return errors.New("steer_ack: summary is required (one line: what you are doing now)")
	}
	if ack.Conflict && reason == "" {
		return errors.New("steer_ack: reason is required when conflict is true")
	}
	if !ack.Conflict {
		reason = ""
	}
	e.pendingSteerMu.Lock()
	if len(e.unackedSteers) == 0 {
		e.pendingSteerMu.Unlock()
		return errNoUnackedSteer
	}
	target := e.unackedSteers[0]
	e.unackedSteers = e.unackedSteers[1:]
	e.pendingSteerMu.Unlock()
	e.emit(protocol.TurnSteerAck{
		Correlation:  corr,
		TargetTurnID: target,
		Summary:      summary,
		Conflict:     ack.Conflict,
		Reason:       reason,
	})
	return nil
}

// oneLineCapped collapses whitespace to single spaces and caps the rune count.
func oneLineCapped(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return strings.TrimSpace(string(r[:limit-1])) + "…"
}
