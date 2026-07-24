package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestDialogEmbedsTitleAndPlacesHintAtFoot(t *testing.T) {
	out := Dialog(theme.Default(), DialogOpts{
		Title: "Select provider",
		Hint:  "enter select · esc close",
		Width: 50,
	}, "choose a provider")

	if top := firstLine(out); !strings.Contains(top, "─ Select provider ") {
		t.Errorf("title not embedded in top border: %q", top)
	}
	if !strings.Contains(out, "choose a provider") {
		t.Error("dialog body missing")
	}
	if !strings.Contains(out, "enter select") || !strings.Contains(out, "esc close") {
		t.Error("dialog hint missing")
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w != 50 {
			t.Errorf("line %d width = %d, want 50", i, w)
		}
	}
}

func TestDialogToneOverridesBorderColor(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	th := theme.Default()
	warn := Dialog(th, DialogOpts{Title: "Permission required", Width: 40, Tone: ToneWarning}, "rm -rf")
	normal := Dialog(th, DialogOpts{Title: "Permission required", Width: 40}, "rm -rf")
	if warn == normal {
		t.Error("ToneWarning did not change the dialog border color")
	}
}
