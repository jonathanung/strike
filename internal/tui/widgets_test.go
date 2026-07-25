package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestFocusedInputsRenderStaticThemedReverseCursorAfterMovingLeft(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Accent = fixedColor("#405060")
	th.Background = theme.NoBackground()

	composer := newComposer(th)
	composer.SetWidth(20)
	composer.SetValue("abc")
	composer, _ = composer.Update(tea.KeyMsg{Type: tea.KeyLeft})
	assertStaticCursorOnMovedCharacter(t, "composer", composer.Cursor.Mode(), composer.View())

	input := newTextInput(th, "")
	input.Focus()
	input.Width = 20
	input.SetValue("abc")
	input, _ = input.Update(tea.KeyMsg{Type: tea.KeyLeft})
	assertStaticCursorOnMovedCharacter(t, "text input", input.Cursor.Mode(), input.View())
}

func assertStaticCursorOnMovedCharacter(t *testing.T, name string, mode cursor.Mode, out string) {
	t.Helper()
	if mode != cursor.CursorStatic {
		t.Errorf("%s cursor mode = %v, want CursorStatic", name, mode)
	}
	if plain := ansi.Strip(out); !strings.Contains(plain, "abc") {
		t.Errorf("%s did not render typed text: %q", name, out)
	}
	if !hasSGRParameter(out, "7") {
		t.Errorf("%s cursor did not render reverse SGR 7 after moving left: %q", name, out)
	}
	if !strings.Contains(out, rgbSGR("#405060")) {
		t.Errorf("%s cursor did not use the theme Accent/InputCursor color: %q", name, out)
	}
	if hasTUIBackgroundSGR(out) {
		t.Errorf("%s emitted a background-setting SGR sequence in NoBackground mode: %q", name, out)
	}
}

func hasSGRParameter(s, want string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		end := i + 2
		for end < len(s) && (s[end] < '@' || s[end] > '~') {
			end++
		}
		if end < len(s) && s[end] == 'm' {
			for _, parameter := range strings.Split(s[i+2:end], ";") {
				if parameter == want {
					return true
				}
			}
		}
		i = end
	}
	return false
}

func TestTextInputUsesThemeInputCursorAsItsPrompt(t *testing.T) {
	th := theme.Default()
	th.Icons.InputCursor = "@"
	in := newTextInput(th, "placeholder")
	in.Focus()
	in.SetValue("text")
	if !strings.HasPrefix(in.Prompt, "@") {
		t.Errorf("text input prompt = %q, want custom InputCursor prefix", in.Prompt)
	}
	if out := in.View(); !strings.Contains(out, "@") {
		t.Errorf("text input output did not render custom InputCursor: %q", out)
	}
}

func TestTextInputDefaultPromptIsTwoDisplayCells(t *testing.T) {
	if got := lipgloss.Width(newTextInput(theme.Default(), "").Prompt); got != 2 {
		t.Errorf("default text input prompt width = %d, want 2", got)
	}
}

func TestComposerPlaceholderMentionsAskAnythingAndSlash(t *testing.T) {
	ta := newComposer(theme.Default())
	if !strings.Contains(ta.Placeholder, "Ask anything") {
		t.Errorf("composer placeholder = %q, want it to contain %q", ta.Placeholder, "Ask anything")
	}
	if !strings.Contains(ta.Placeholder, "/") {
		t.Errorf("composer placeholder = %q, want it to mention / for commands", ta.Placeholder)
	}
}

func TestComposerFooterAdvertisesEnterAndShiftEnter(t *testing.T) {
	// Bordered composer at a non-compact size with empty transcript should
	// surface send/newline help in the panel footer.
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 40})
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "enter") {
		t.Errorf("bordered view missing enter send help:\n%s", plain)
	}
	if !strings.Contains(plain, "shift+enter") && !strings.Contains(plain, "newline") {
		t.Errorf("bordered view missing shift+enter/newline help:\n%s", plain)
	}
	// Direct footer helper also advertises both.
	footer := ansi.Strip(composerFooter(m.th, 60))
	if !strings.Contains(footer, "enter") || !strings.Contains(footer, "shift+enter") {
		t.Errorf("composerFooter = %q, want enter and shift+enter", footer)
	}
}

func TestComposerAndTextInputHonorThemeXSPromptGap(t *testing.T) {
	for _, tt := range []struct {
		name string
		th   theme.Theme
		want int
	}{
		{"default", theme.Default(), theme.Default().Spacing.XS},
		{"explicit zero", theme.Theme{Spacing: theme.NewSpacing(0, 2, 3, 4)}, 0},
		{"custom", theme.Theme{Spacing: theme.NewSpacing(4, 2, 3, 4)}, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolved := tt.th.Resolve()
			composer := newComposer(tt.th)
			input := newTextInput(tt.th, "")
			if got := lipgloss.Width(composer.Prompt) - lipgloss.Width(resolved.Icons.Prompt); got != tt.want {
				t.Errorf("composer prompt gap = %d, want %d", got, tt.want)
			}
			if got := lipgloss.Width(input.Prompt) - lipgloss.Width(resolved.Icons.InputCursor); got != tt.want {
				t.Errorf("text input prompt gap = %d, want %d", got, tt.want)
			}
		})
	}
}
