package tui

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

// Headless frame snapshot bounds (#1183). Keep in lockstep with
// internal/tool tui_snapshot caps so tests and the model-facing tool agree.
const (
	maxFrameSnapshotBytes = 32 << 10
	maxFrameSnapshotLines = 80
)

// errNoFrame is returned when no Bubble Tea frame has been painted.
var errNoFrame = errors.New("no TUI frame available (headless/non-TUI session)")

// FrameSnapshot is one headless TUI frame as bounded, redacted text.
type FrameSnapshot struct {
	Text      string
	Width     int
	Height    int
	Truncated bool
	Redacted  bool
}

// SnapshotFrame captures the current (or last painted) Bubble Tea frame as
// ANSI-stripped, redacted, size-bounded text. It fails when the model has
// never been sized or painted — the headless/non-TUI case.
func (m Model) SnapshotFrame() (FrameSnapshot, error) {
	raw := ""
	if m.paint != nil {
		raw = m.paint.lastFrame
	}
	if raw == "" {
		if !m.ready || m.width <= 0 || m.height <= 0 {
			return FrameSnapshot{}, errNoFrame
		}
		raw = m.renderFrame()
		m.noteCachedFrame(raw)
	}
	return normalizeFrameSnapshot(raw, m.width, m.height), nil
}

func normalizeFrameSnapshot(raw string, width, height int) FrameSnapshot {
	plain := ansi.Strip(raw)
	redacted := redact.String(plain)
	text, truncated := boundFrameText(redacted)
	return FrameSnapshot{
		Text:      text,
		Width:     width,
		Height:    height,
		Truncated: truncated,
		Redacted:  redacted != plain,
	}
}

func boundFrameText(s string) (string, bool) {
	truncated := false
	if len(s) > maxFrameSnapshotBytes {
		cut := s[:maxFrameSnapshotBytes]
		if i := strings.LastIndexByte(cut, '\n'); i > maxFrameSnapshotBytes/2 {
			cut = cut[:i]
		}
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		s = cut
		truncated = true
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxFrameSnapshotLines {
		s = strings.Join(lines[:maxFrameSnapshotLines], "\n")
		truncated = true
	}
	if truncated {
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += "... (truncated)"
	}
	return s, truncated
}
