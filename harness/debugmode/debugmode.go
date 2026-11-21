// Package debugmode is the engine-side state for a hypothesis-first debug
// session (issue #1345): ranked hypotheses with attached evidence, a phase
// gate that blocks fix edits until a hypothesis is confirmed (or the user
// overrides), and a sweep that removes tagged instrumentation once the user
// marks the bug fixed.
//
// The package is stdlib-only and holds no I/O except Sweep/Scan. Session is
// safe for concurrent use.
package debugmode

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Marker tags every instrumentation line the agent adds during the
// hypothesis phase. The sweep removes whole lines containing it. Built by
// concatenation so this source file does not itself contain the marker.
const Marker = "STRIKE" + "-DEBUG"

// Phase is the debug session lifecycle.
type Phase string

const (
	// PhaseHypothesis allows reads and tagged instrumentation only.
	PhaseHypothesis Phase = "hypothesis"
	// PhaseFix allows normal edits; entered once evidence (or an override) exists.
	PhaseFix Phase = "fix"
	// PhaseClosed is terminal: instrumentation was swept and verified gone.
	PhaseClosed Phase = "closed"
)

// Status is a hypothesis verdict.
type Status string

const (
	StatusOpen      Status = "open"
	StatusConfirmed Status = "confirmed"
	StatusRefuted   Status = "refuted"
)

// Hypothesis limits from the issue: 2–5 ranked hypotheses.
const (
	MinHypotheses = 2
	MaxHypotheses = 5
)

// Evidence is captured runtime output attached to a hypothesis.
type Evidence struct {
	Source string // e.g. "repro", "log", "command"
	Text   string
}

// Hypothesis is one ranked candidate cause.
type Hypothesis struct {
	ID        string
	Rank      int // 1 = most likely; assigned from list order
	Statement string
	// Confirms / Refutes describe the evidence that would settle it.
	Confirms string
	Refutes  string
	Status   Status
	Evidence []Evidence
}

// Session is one debug session's state.
type Session struct {
	mu         sync.Mutex
	phase      Phase
	hypotheses []Hypothesis
	override   string
	fixed      bool
}

// New starts a session in the hypothesis phase.
func New() *Session {
	return &Session{phase: PhaseHypothesis}
}

// Restore rebuilds a session from a snapshot (session resume). Only the
// hypothesis and fix phases are resumable; any other phase returns nil
// because no debug session is active.
func Restore(snap Snapshot) *Session {
	if snap.Phase != PhaseHypothesis && snap.Phase != PhaseFix {
		return nil
	}
	return &Session{
		phase:      snap.Phase,
		hypotheses: cloneHypotheses(snap.Hypotheses),
		override:   snap.Override,
		fixed:      snap.Fixed && snap.Phase == PhaseFix,
	}
}

// Snapshot is an immutable copy of session state for rendering/persistence.
type Snapshot struct {
	Phase      Phase
	Hypotheses []Hypothesis
	// Override is the user's recorded reason for unlocking fix without a
	// confirmed hypothesis; empty when the gate was satisfied by evidence.
	Override string
	Fixed    bool
}

// Snapshot returns a deep copy of the current state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Phase:      s.phase,
		Hypotheses: cloneHypotheses(s.hypotheses),
		Override:   s.override,
		Fixed:      s.fixed,
	}
}

// Phase returns the current phase.
func (s *Session) Phase() Phase {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

// SetHypotheses replaces the ranked hypothesis list. Only valid in the
// hypothesis phase. Ranks follow slice order; statuses reset to open.
func (s *Session) SetHypotheses(hs []Hypothesis) error {
	if len(hs) < MinHypotheses || len(hs) > MaxHypotheses {
		return fmt.Errorf("debug: need %d–%d hypotheses, got %d", MinHypotheses, MaxHypotheses, len(hs))
	}
	seen := make(map[string]struct{}, len(hs))
	out := make([]Hypothesis, len(hs))
	for i, h := range hs {
		id := strings.TrimSpace(h.ID)
		if id == "" {
			return fmt.Errorf("debug: hypothesis %d has empty id", i+1)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("debug: duplicate hypothesis id %q", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(h.Statement) == "" {
			return fmt.Errorf("debug: hypothesis %q has empty statement", id)
		}
		out[i] = Hypothesis{
			ID:        id,
			Rank:      i + 1,
			Statement: strings.TrimSpace(h.Statement),
			Confirms:  strings.TrimSpace(h.Confirms),
			Refutes:   strings.TrimSpace(h.Refutes),
			Status:    StatusOpen,
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != PhaseHypothesis {
		return fmt.Errorf("debug: hypotheses can only be set in the %s phase (now %s)", PhaseHypothesis, s.phase)
	}
	s.hypotheses = out
	return nil
}

// Attach records evidence against a hypothesis.
func (s *Session) Attach(id string, ev Evidence) error {
	if strings.TrimSpace(ev.Text) == "" {
		return errors.New("debug: evidence text is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == PhaseClosed {
		return errors.New("debug: session is closed")
	}
	h := s.find(id)
	if h == nil {
		return fmt.Errorf("debug: unknown hypothesis %q", id)
	}
	h.Evidence = append(h.Evidence, ev)
	return nil
}

// Mark sets a hypothesis verdict. Confirming requires attached evidence so
// the fix phase never unlocks on an unsupported claim.
func (s *Session) Mark(id string, st Status) error {
	switch st {
	case StatusOpen, StatusConfirmed, StatusRefuted:
	default:
		return fmt.Errorf("debug: invalid status %q", st)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == PhaseClosed {
		return errors.New("debug: session is closed")
	}
	h := s.find(id)
	if h == nil {
		return fmt.Errorf("debug: unknown hypothesis %q", id)
	}
	if st == StatusConfirmed && len(h.Evidence) == 0 {
		return fmt.Errorf("debug: hypothesis %q has no evidence attached; attach evidence before confirming", id)
	}
	h.Status = st
	return nil
}

// UnlockFix moves to the fix phase. It succeeds when at least one
// hypothesis is confirmed, or when override (the user's stated reason) is
// non-empty; the override is kept so it can be recorded in the transcript.
func (s *Session) UnlockFix(override string) error {
	override = strings.TrimSpace(override)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != PhaseHypothesis {
		return fmt.Errorf("debug: fix can only be unlocked from the %s phase (now %s)", PhaseHypothesis, s.phase)
	}
	if override == "" && !s.hasConfirmedLocked() {
		return errors.New("debug: no confirmed hypothesis; confirm one with evidence or override explicitly")
	}
	if !s.hasConfirmedLocked() {
		s.override = override
	}
	s.phase = PhaseFix
	return nil
}

// MarkFixed records the user's statement that the bug is fixed. Cleanup is
// gated on this, not on a fix merely being proposed.
func (s *Session) MarkFixed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != PhaseFix {
		return fmt.Errorf("debug: bug can only be marked fixed in the %s phase (now %s)", PhaseFix, s.phase)
	}
	s.fixed = true
	return nil
}

// End sweeps instrumentation under root and closes the session only when
// the sweep verifies zero tagged lines remain. Requires MarkFixed.
func (s *Session) End(root string) (SweepReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != PhaseFix || !s.fixed {
		return SweepReport{}, errors.New("debug: mark the bug fixed before ending the debug session")
	}
	rep, err := Sweep(root)
	if err != nil {
		return rep, err
	}
	if len(rep.Remaining) > 0 {
		return rep, fmt.Errorf("debug: %d tagged line(s) remain after sweep", len(rep.Remaining))
	}
	s.phase = PhaseClosed
	return rep, nil
}

func (s *Session) find(id string) *Hypothesis {
	id = strings.TrimSpace(id)
	for i := range s.hypotheses {
		if s.hypotheses[i].ID == id {
			return &s.hypotheses[i]
		}
	}
	return nil
}

func (s *Session) hasConfirmedLocked() bool {
	for _, h := range s.hypotheses {
		if h.Status == StatusConfirmed {
			return true
		}
	}
	return false
}

func cloneHypotheses(hs []Hypothesis) []Hypothesis {
	if hs == nil {
		return nil
	}
	out := make([]Hypothesis, len(hs))
	for i, h := range hs {
		out[i] = h
		out[i].Evidence = append([]Evidence(nil), h.Evidence...)
	}
	return out
}
