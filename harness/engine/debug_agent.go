package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/harness/debugmode"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// debugSessionTool is the agent's hypothesis/evidence tool (internal/tools).
const debugSessionTool = "debug_session"

// activationFamilyDebug surfaces debug_session while a debug session is active.
const activationFamilyDebug = "debug"

// agentDebugActions are the debug ops the agent may issue. Everything else
// (start, unlock/override, fixed, end, scan) is user-only (#1961).
var agentDebugActions = map[string]struct{}{
	protocol.DebugActionHypotheses: {},
	protocol.DebugActionEvidence:   {},
	protocol.DebugActionMark:       {},
}

// errDebugUserOnly explains why the agent cannot move the session forward.
const errDebugUserOnly = "starting the session, unlocking fix, overriding the gate, marking the bug fixed and ending the session are the user's decisions (Debug panel); ask the user when you think one is due"

// agentDebugControl applies one agent debug op through the same
// debugmode.Session the Debug panel drives and emits debug.updated on
// success. Refusals and validation errors go back to the model only; the
// panel's state is unchanged so nothing is emitted.
func (e *Engine) agentDebugControl(corr protocol.Correlation, op tool.DebugOp) (string, error) {
	action := strings.TrimSpace(op.Action)
	if _, ok := agentDebugActions[action]; !ok {
		return "", fmt.Errorf("debug_session: action %q is not allowed; you may set hypotheses, attach evidence and mark verdicts. %s", action, errDebugUserOnly)
	}
	if e.debug.Load() == nil {
		if e.activeDebug() != nil {
			return "", errors.New("debug_session: the debug session belongs to the parent agent; report your findings to it instead")
		}
		return "", errors.New("debug_session: no debug session is active; the user starts one from the Debug panel")
	}
	ctl := protocol.DebugControl{
		Action:       action,
		HypothesisID: op.HypothesisID,
		Status:       op.Status,
		Source:       op.Source,
		Text:         op.Text,
	}
	for _, h := range op.Hypotheses {
		ctl.Hypotheses = append(ctl.Hypotheses, protocol.DebugHypothesis{
			ID: h.ID, Statement: h.Statement, Confirms: h.Confirms, Refutes: h.Refutes,
		})
	}
	ev := e.applyDebugControl(ctl)
	if ev.Error != "" {
		return "", errors.New(ev.Error)
	}
	ev.Correlation = corr
	e.emit(ev)
	return debugSummary(ev), nil
}

// debugSummary is the one-line state the tool returns to the model.
func debugSummary(ev protocol.DebugUpdated) string {
	parts := make([]string, 0, len(ev.Hypotheses))
	for _, h := range ev.Hypotheses {
		parts = append(parts, fmt.Sprintf("%s=%s(%d evidence)", h.ID, h.Status, len(h.Evidence)))
	}
	s := "Debug phase " + ev.Phase
	if len(parts) > 0 {
		s += "; hypotheses: " + strings.Join(parts, ", ")
	}
	return s
}

// hasOwnDebugSession reports whether this engine owns an active debug
// session (children are gated by the parent's but do not drive it).
func (e *Engine) hasOwnDebugSession() bool {
	return e != nil && e.debug.Load() != nil
}

// debugPromptLayer is the system note shown while a debug session is
// active: the phase, the marker convention and the gate.
func (e *Engine) debugPromptLayer() (promptLayer, bool) {
	s := e.debug.Load()
	if s == nil {
		return promptLayer{}, false
	}
	phase := s.Phase()
	text := debugPromptText(phase)
	if text == "" {
		return promptLayer{}, false
	}
	return promptLayer{
		Kind:   protocol.PromptLayerPhase,
		Source: "debug:" + string(phase),
		Mode:   protocol.PromptLayerAppend,
		Text:   text,
	}, true
}

func debugPromptText(phase debugmode.Phase) string {
	m := debugmode.Marker
	switch phase {
	case debugmode.PhaseHypothesis:
		return fmt.Sprintf(`# Debug session (phase: hypothesis)

The user started a hypothesis-first debug session. Do not fix anything yet.
1. Your first output is the ranked hypothesis list: call debug_session with action "hypotheses" and %d-%d hypotheses, most likely first, each with what evidence would confirm or refute it. They appear as cards in the user's Debug panel.
2. Instrument to gather evidence. Every line you add, change or remove must contain the marker %s (for example in a trailing comment on the same line). edit and apply_patch with any untagged change are denied; write, delete and move are denied in this phase.
3. Run the repro, then attach the captured output with action "evidence" and set verdicts with action "mark" (confirmed, refuted or open). Confirming requires attached evidence.
Gate: fix edits unlock only when the user presses Unlock (after a confirmed hypothesis) or overrides with a reason. You cannot unlock, override, mark the bug fixed or end the session; ask the user when you think one is due.`, debugmode.MinHypotheses, debugmode.MaxHypotheses, m)
	case debugmode.PhaseFix:
		return fmt.Sprintf(`# Debug session (phase: fix)

The user unlocked fix edits; normal edits are allowed. Keep any new instrumentation tagged %s. When the user marks the bug fixed and ends the session, every line containing %s is swept, and the session closes only once none remain. You cannot mark the bug fixed or end the session.`, m, m)
	}
	return ""
}
