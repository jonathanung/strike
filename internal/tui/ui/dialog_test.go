package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestDialogEmbedsTitleAndPlacesHintAtFoot(t *testing.T) {
	out := Dialog(theme.Default(), DialogOpts{
		Title: "Select provider",
		Hint:  "enter select · esc close",
		Width: 50,
	}, "choose a provider")

	if top := ansi.Strip(firstLine(out)); !strings.Contains(top, "Select provider") {
		t.Errorf("title not embedded in top chrome: %q", top)
	}
	if strings.ContainsAny(ansi.Strip(firstLine(out)), "╭╮│") {
		t.Errorf("default dialog used box-drawing chrome: %q", firstLine(out))
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

func TestDialogToneOverridesChromeEmphasis(t *testing.T) {
	th := theme.Default()
	warn := Dialog(th, DialogOpts{Title: "Permission required", Width: 40, Tone: ToneWarning}, "rm -rf")
	normal := Dialog(th, DialogOpts{Title: "Permission required", Width: 40}, "rm -rf")
	if warn == normal {
		t.Error("ToneWarning did not change the dialog chrome emphasis")
	}
}

func TestDialogHintWrapsWhenLongerThanWidth(t *testing.T) {
	// Long hint word-wraps, but is capped at two lines with an ellipsis on the
	// last visible line so short terminals do not clip the overlay.
	hint := "enter select · esc close · type to filter · ctrl+d save default · tab cycles · more keys · even more"
	out := Dialog(theme.Default(), DialogOpts{
		Title: "Select model",
		Hint:  hint,
		Width: 28,
	}, "body line")
	plain := out
	if !strings.Contains(plain, "enter select") {
		t.Errorf("wrapped dialog dropped hint start:\n%s", plain)
	}
	if !strings.Contains(plain, "…") && !strings.Contains(plain, "...") {
		t.Errorf("expected ellipsis on capped hint, got:\n%s", plain)
	}
	// Multi-line: more than title + body + single hint row.
	if strings.Count(plain, "\n") < 4 {
		t.Errorf("expected multi-line dialog with wrapped hint, got %d newlines:\n%s", strings.Count(plain, "\n"), plain)
	}
	// At most two muted hint body lines (plus title/body/spacing/chrome).
	hintBody := 0
	for _, line := range strings.Split(ansi.Strip(plain), "\n") {
		if strings.Contains(line, "enter select") || strings.Contains(line, "filter") || strings.Contains(line, "…") || strings.Contains(line, "...") {
			hintBody++
		}
	}
	if hintBody > 2 {
		t.Errorf("hint exceeded 2 lines (%d):\n%s", hintBody, plain)
	}
	for i, line := range strings.Split(plain, "\n") {
		if w := lipgloss.Width(line); w > 28 {
			t.Errorf("line %d width = %d, want <= 28: %q", i, w, line)
		}
	}
}

func TestDialogWrapsLongBodyText(t *testing.T) {
	const width = 32
	body := "Which of the following is the best description of the primary purpose of a unit test?"
	out := Dialog(theme.Default(), DialogOpts{
		Title: "question",
		Hint:  "enter select",
		Width: width,
	}, body)
	// Solid focus chrome puts Icons.FocusBar in the pad column; drop it before
	// joining so wrapped words reassemble across lines.
	plain := strings.ReplaceAll(ansi.Strip(out), theme.Default().Resolve().Icons.FocusBar, "")
	compact := strings.Join(strings.Fields(plain), " ")
	if !strings.Contains(compact, "primary purpose of a unit test") {
		t.Fatalf("dialog dropped wrapped body:\n%s\ncompact=%q", ansi.Strip(out), compact)
	}
	if strings.Contains(plain, "unit test…") || strings.Contains(plain, "unit test...") {
		t.Fatalf("body should wrap, not ellipsis mid-sentence:\n%s", plain)
	}
	// More than title + single body + hint.
	if strings.Count(out, "\n") < 4 {
		t.Fatalf("expected multi-line wrapped body, got:\n%s", plain)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d width %d > %d: %q", i, w, width, line)
		}
	}
}

func TestDialogPlacesHintUsingThemeSpacing(t *testing.T) {
	for _, tt := range []struct {
		name         string
		spacing      theme.Spacing
		wantNewlines int
	}{
		{"explicit zero", theme.NewSpacing(0, 0, 0, 0), 1},
		{"custom small", theme.NewSpacing(0, 3, 0, 0), 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			th := theme.Default()
			th.Spacing = tt.spacing
			out := Dialog(th, DialogOpts{Width: 30, Hint: "hint"}, "body")
			lines := strings.Split(out, "\n")
			bodyLine, hintLine := -1, -1
			for i, line := range lines {
				if strings.Contains(line, "body") {
					bodyLine = i
				}
				if strings.Contains(line, "hint") {
					hintLine = i
				}
			}
			if bodyLine < 0 || hintLine < 0 {
				t.Fatalf("dialog omitted body or hint: %q", out)
			}
			if got := hintLine - bodyLine; got != tt.wantNewlines {
				t.Errorf("body-to-hint line distance = %d, want %d\n%s", got, tt.wantNewlines, out)
			}
		})
	}
}
