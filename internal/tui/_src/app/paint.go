package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// paintFPSInterval caps full-frame View rebuilds for soft-coalesced updates
// (streaming TextDelta/ReasoningDelta and working spinner ticks). ~6 FPS sits
// in the 4–8 FPS band targeted for SSH-friendly streaming (#496 / epic #452).
// Immediate-flush messages bypass the cap on the next Update cycle.
const paintFPSInterval = time.Second / 6

// paintFlushMsg drains a pending coalesced paint after paintFPSInterval.
type paintFlushMsg struct{}

// paintCoalesce tracks FPS-capped full-frame rebuilds. Stored as a pointer on
// Model so value-receiver View/Update can share cache and counters.
type paintCoalesce struct {
	lastAt      time.Time
	lastFrame   string
	pending     bool // model changed since last real frame build
	armed       bool // paintFlushMsg tick already scheduled
	suppress    bool // next View returns lastFrame without renderFrame
	frameBuilds int  // full renderFrame invocations (tests / guards)
	nowFn       func() time.Time
}

func (p *paintCoalesce) now() time.Time {
	if p != nil && p.nowFn != nil {
		return p.nowFn()
	}
	return time.Now()
}

func (m *Model) ensurePaint() *paintCoalesce {
	if m.paint == nil {
		m.paint = &paintCoalesce{}
	}
	return m.paint
}

// softCoalesceEvent reports protocol events that may batch under the FPS cap.
func softCoalesceEvent(ev protocol.Event) bool {
	switch ev.(type) {
	case protocol.TextDelta, protocol.ReasoningDelta:
		return true
	default:
		return false
	}
}

// softCoalesceMsg reports Update messages that may batch under the FPS cap.
func softCoalesceMsg(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		return true
	case engineEventMsg:
		return softCoalesceEvent(msg.ev)
	default:
		return false
	}
}

// markImmediatePaint forces the next View to rebuild and anchors the FPS window.
func (m *Model) markImmediatePaint() {
	p := m.ensurePaint()
	p.suppress = false
	p.pending = false
	p.lastAt = p.now()
}

// coalesceSoftPaint allows at most one full frame per paintFPSInterval for
// high-frequency soft updates. Returns a flush tick when a paint is deferred.
func (m *Model) coalesceSoftPaint() tea.Cmd {
	p := m.ensurePaint()
	now := p.now()
	if p.lastAt.IsZero() || now.Sub(p.lastAt) >= paintFPSInterval {
		p.suppress = false
		p.pending = false
		p.lastAt = now
		return nil
	}
	p.suppress = true
	p.pending = true
	if p.armed {
		return nil
	}
	p.armed = true
	wait := paintFPSInterval - now.Sub(p.lastAt)
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return tea.Tick(wait, func(time.Time) tea.Msg { return paintFlushMsg{} })
}

// applyPaintFlush paints once if soft updates piled up during the FPS window.
func (m *Model) applyPaintFlush() {
	p := m.ensurePaint()
	p.armed = false
	if !p.pending {
		// Already flushed by an immediate event; skip a redundant rebuild.
		if p.lastFrame != "" {
			p.suppress = true
		}
		return
	}
	p.pending = false
	p.suppress = false
	p.lastAt = p.now()
}

// noteFrameBuild records a full renderFrame result for the next suppressed View.
func (m Model) noteFrameBuild(frame string) {
	if m.paint == nil {
		return
	}
	m.paint.lastFrame = frame
	m.paint.frameBuilds++
}
