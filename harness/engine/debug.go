package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/harness/debugmode"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/pkg/protocol"
)

// activeDebug returns the debug session gating this engine: its own, or the
// nearest ancestor's for a child, so delegation cannot bypass the gate.
func (e *Engine) activeDebug() *debugmode.Session {
	for cur := e; cur != nil; cur = cur.opts.debugFrom {
		if s := cur.debug.Load(); s != nil {
			return s
		}
	}
	return nil
}

// checkDebugGate applies the debug phase gate to one tool call. A nil
// result means the call may proceed.
func (e *Engine) checkDebugGate(call provider.ToolCall) *debugmode.GateError {
	s := e.activeDebug()
	if s == nil {
		return nil
	}
	var ge *debugmode.GateError
	if err := s.CheckToolCall(call.Name, call.Args); errors.As(err, &ge) {
		return ge
	} else if err != nil {
		return &debugmode.GateError{Tool: call.Name, Reason: err.Error()}
	}
	return nil
}

// handleDebugControl applies one debug op and always emits DebugUpdated:
// the new state, or the unchanged state with Error set.
func (e *Engine) handleDebugControl(op protocol.DebugControl) {
	ev := e.applyDebugControl(op)
	ev.Correlation = e.sessionCorr()
	e.emit(ev)
}

func (e *Engine) applyDebugControl(op protocol.DebugControl) protocol.DebugUpdated {
	action := strings.TrimSpace(op.Action)
	s := e.debug.Load()
	fail := func(err error) protocol.DebugUpdated {
		ev := debugUpdated(action, s)
		ev.Error = err.Error()
		return ev
	}
	if s == nil && action != protocol.DebugActionStart {
		return fail(errors.New("debug: no debug session is active; start one first"))
	}
	scan := false
	switch action {
	case protocol.DebugActionStart:
		if s != nil {
			return fail(errors.New("debug: a debug session is already active"))
		}
		s = debugmode.New()
		e.debug.Store(s)
		scan = true
	case protocol.DebugActionHypotheses:
		if err := s.SetHypotheses(hypothesesFromProtocol(op.Hypotheses)); err != nil {
			return fail(err)
		}
	case protocol.DebugActionEvidence:
		if err := s.Attach(op.HypothesisID, debugmode.Evidence{Source: strings.TrimSpace(op.Source), Text: op.Text}); err != nil {
			return fail(err)
		}
	case protocol.DebugActionMark:
		if err := s.Mark(op.HypothesisID, debugmode.Status(strings.TrimSpace(op.Status))); err != nil {
			return fail(err)
		}
	case protocol.DebugActionUnlock:
		if err := s.UnlockFix(op.Text); err != nil {
			return fail(err)
		}
	case protocol.DebugActionFixed:
		if err := s.MarkFixed(); err != nil {
			return fail(err)
		}
		scan = true
	case protocol.DebugActionEnd:
		return e.endDebug(s)
	case protocol.DebugActionScan:
		scan = true
	default:
		return fail(fmt.Errorf("debug: unknown action %q", action))
	}
	ev := debugUpdated(action, s)
	if scan {
		hits, err := e.scanDebug()
		if err != nil {
			ev.Error = err.Error()
		}
		ev.Instrumentation = hits
	}
	return ev
}

// endDebug sweeps tagged instrumentation and clears the session only when
// the sweep verifies zero tagged lines remain.
func (e *Engine) endDebug(s *debugmode.Session) protocol.DebugUpdated {
	root := strings.TrimSpace(e.opts.WorkDir)
	if root == "" {
		ev := debugUpdated(protocol.DebugActionEnd, s)
		ev.Error = "debug: no workspace directory to sweep"
		return ev
	}
	rep, err := s.End(root)
	ev := debugUpdated(protocol.DebugActionEnd, s)
	ev.Removed = hitsToProtocol(rep.Removed)
	ev.Instrumentation = hitsToProtocol(rep.Remaining)
	if err != nil {
		ev.Error = err.Error()
		return ev
	}
	e.debug.CompareAndSwap(s, nil)
	return ev
}

func (e *Engine) scanDebug() ([]protocol.DebugHit, error) {
	root := strings.TrimSpace(e.opts.WorkDir)
	if root == "" {
		return nil, nil
	}
	hits, err := debugmode.Scan(root)
	if err != nil {
		return hitsToProtocol(hits), fmt.Errorf("debug: scan instrumentation: %w", err)
	}
	return hitsToProtocol(hits), nil
}

// restoreDebug resumes the debug session recorded by the last successful
// DebugUpdated in the log. A closed or empty snapshot resumes nothing.
func (e *Engine) restoreDebug(ev *protocol.DebugUpdated) {
	if ev == nil {
		return
	}
	if s := debugmode.Restore(snapshotFromProtocol(*ev)); s != nil {
		e.debug.Store(s)
	}
}

func debugUpdated(action string, s *debugmode.Session) protocol.DebugUpdated {
	ev := protocol.DebugUpdated{Action: action}
	if s == nil {
		return ev
	}
	snap := s.Snapshot()
	ev.Phase = string(snap.Phase)
	ev.Hypotheses = hypothesesToProtocol(snap.Hypotheses)
	ev.Override = snap.Override
	ev.Fixed = snap.Fixed
	return ev
}

func snapshotFromProtocol(ev protocol.DebugUpdated) debugmode.Snapshot {
	snap := debugmode.Snapshot{
		Phase:    debugmode.Phase(ev.Phase),
		Override: ev.Override,
		Fixed:    ev.Fixed,
	}
	for _, h := range ev.Hypotheses {
		dh := debugmode.Hypothesis{
			ID:        h.ID,
			Rank:      h.Rank,
			Statement: h.Statement,
			Confirms:  h.Confirms,
			Refutes:   h.Refutes,
			Status:    debugmode.Status(h.Status),
		}
		for _, x := range h.Evidence {
			dh.Evidence = append(dh.Evidence, debugmode.Evidence{Source: x.Source, Text: x.Text})
		}
		snap.Hypotheses = append(snap.Hypotheses, dh)
	}
	return snap
}

func hypothesesFromProtocol(hs []protocol.DebugHypothesis) []debugmode.Hypothesis {
	out := make([]debugmode.Hypothesis, len(hs))
	for i, h := range hs {
		out[i] = debugmode.Hypothesis{ID: h.ID, Statement: h.Statement, Confirms: h.Confirms, Refutes: h.Refutes}
	}
	return out
}

func hypothesesToProtocol(hs []debugmode.Hypothesis) []protocol.DebugHypothesis {
	if len(hs) == 0 {
		return nil
	}
	out := make([]protocol.DebugHypothesis, len(hs))
	for i, h := range hs {
		ph := protocol.DebugHypothesis{
			ID:        h.ID,
			Rank:      h.Rank,
			Statement: h.Statement,
			Confirms:  h.Confirms,
			Refutes:   h.Refutes,
			Status:    string(h.Status),
		}
		for _, x := range h.Evidence {
			ph.Evidence = append(ph.Evidence, protocol.DebugEvidence{Source: x.Source, Text: x.Text})
		}
		out[i] = ph
	}
	return out
}

func hitsToProtocol(hs []debugmode.Hit) []protocol.DebugHit {
	if len(hs) == 0 {
		return nil
	}
	out := make([]protocol.DebugHit, len(hs))
	for i, h := range hs {
		out[i] = protocol.DebugHit{Path: h.Path, Line: h.Line, Text: h.Text}
	}
	return out
}
