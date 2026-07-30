package tui

import (
	"strings"
	"sync"

	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"
)

// markdownRender is the assistant-cell markdown path; overridable in tests.
var markdownRender = glamourRender

// glamourStyleName is the pinned dark/light style for assistant markdown.
// Never "auto": glamour.WithAutoStyle queries OSC 11 on every NewTermRenderer,
// and that reply races Bubble Tea's stdin into the composer (#52).
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
	source = expandMermaidFences(source, width)
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle()),
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
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = ansi.Hardwrap(line, width, false)
	}
	return strings.Join(lines, "\n")
}
