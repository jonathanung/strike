package tui

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/common"
)

// markdownRender is the assistant-cell markdown path; overridable in tests.
var markdownRender = glamourRender

// glamourStyleName is the pinned dark/light style for assistant markdown.
// Glamour v2 removed WithAutoStyle; style must be chosen explicitly via
// WithStylePath so OSC 11 is never queried during NewTermRenderer (#52).
var (
	glamourStyleMu   sync.Mutex
	glamourStyleName string
)

func glamourStyle() string {
	glamourStyleMu.Lock()
	defer glamourStyleMu.Unlock()
	if glamourStyleName == "" {
		if compat.HasDarkBackground {
			glamourStyleName = styles.DarkStyle
		} else {
			glamourStyleName = styles.LightStyle
		}
	}
	return glamourStyleName
}

func setGlamourStyle(dark bool) {
	glamourStyleMu.Lock()
	defer glamourStyleMu.Unlock()
	if dark {
		glamourStyleName = styles.DarkStyle
	} else {
		glamourStyleName = styles.LightStyle
	}
}

func glamourRender(source string, width int) (string, error) {
	// Pad wide-neutral historic scripts before wrap so glamour's width budget
	// matches double-cell terminal paint (#689).
	source = common.PadWideGlyphs(source)
	source = expandMermaidFences(source, width)
	// WithStylePath pins dark|light from E13.3 background detection.
	// Color downsampling is Lip Gloss's job at print time (no WithColorProfile).
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath(glamourStyle()),
		glamour.WithWordWrap(max(1, width)),
	)
	if err != nil {
		return "", err
	}
	out, err := r.Render(source)
	if err != nil {
		return "", err
	}
	return clampRenderWidth(strings.TrimSpace(out), width), nil
}

// clampRenderWidth hard-wraps each line to width with ANSI awareness.
// Glamour does not wrap fenced code lines or long tokens.
func clampRenderWidth(s string, width int) string {
	if s == "" {
		return s
	}
	width = max(1, width)
	// Re-pad in case glamour stripped trailing pad spaces from wide glyphs.
	s = common.PadWideGlyphs(s)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = ansi.Hardwrap(line, width, false)
	}
	return strings.Join(lines, "\n")
}
