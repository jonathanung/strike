package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"
)

// markdownRender is the assistant-cell markdown path; overridable in tests.
var markdownRender = glamourRender

func glamourRender(source string, width int) (string, error) {
	source = expandMermaidFences(source, width)
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
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
