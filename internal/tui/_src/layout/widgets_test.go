package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestStaticWorkingChromeDetection(t *testing.T) {
	for _, tt := range []struct {
		name   string
		chrome string
		ssh    string
		tty    string
		want   bool
	}{
		{name: "local default", want: false},
		{name: "ssh connection", ssh: "1.2.3.4 22 5.6.7.8 9", want: true},
		{name: "ssh tty", tty: "/dev/pts/1", want: true},
		{name: "env static", chrome: "static", want: true},
		{name: "env off", chrome: "off", want: true},
		{name: "env animate overrides ssh", chrome: "animate", ssh: "x", tty: "y", want: false},
		{name: "env on overrides ssh", chrome: "on", ssh: "x", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STRIKE_WORKING_CHROME", tt.chrome)
			t.Setenv("SSH_CONNECTION", tt.ssh)
			t.Setenv("SSH_TTY", tt.tty)
			if got := staticWorkingChrome(); got != tt.want {
				t.Fatalf("staticWorkingChrome() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStyleSpinnerFPSAndFrames(t *testing.T) {
	th := theme.Default()
	th.Icons.Dot = "·"
	th.Icons.Cursor = ">"

	t.Run("animated local", func(t *testing.T) {
		t.Setenv("STRIKE_WORKING_CHROME", "animate")
		t.Setenv("SSH_CONNECTION", "")
		t.Setenv("SSH_TTY", "")
		sp := newSpinner(th)
		if got := sp.Spinner.FPS; got != localWorkingSpinnerFPS {
			t.Errorf("FPS = %v, want %v", got, localWorkingSpinnerFPS)
		}
		if got, want := sp.Spinner.Frames, []string{"·", ">"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("Frames = %v, want %v", got, want)
		}
		// Longer interval than MiniDot ⇒ lower frame rate for SSH-friendly wire use.
		if localWorkingSpinnerFPS <= spinner.MiniDot.FPS {
			t.Errorf("local interval %v must be slower than MiniDot %v", localWorkingSpinnerFPS, spinner.MiniDot.FPS)
		}
		if localWorkingSpinnerFPS < time.Second/4 {
			t.Errorf("local interval %v exceeds ~4 FPS budget", localWorkingSpinnerFPS)
		}
	})

	t.Run("static ssh", func(t *testing.T) {
		t.Setenv("STRIKE_WORKING_CHROME", "")
		t.Setenv("SSH_CONNECTION", "10.0.0.1 1 10.0.0.2 22")
		t.Setenv("SSH_TTY", "")
		sp := newSpinner(th)
		if got, want := sp.Spinner.Frames, []string{"·"}; len(got) != 1 || got[0] != want[0] {
			t.Errorf("Frames = %v, want %v", got, want)
		}
		// Static mode still renders a themed glyph for Working header chrome.
		if view := sp.View(); !strings.Contains(view, "·") {
			t.Errorf("static spinner view missing Dot: %q", view)
		}
	})
}

func TestFocusedInputsRenderStaticThemedReverseCursorAfterMovingLeft(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Accent = fixedColor("#405060")
	th.Background = theme.NoBackground()

	composer := newComposer(th)
	composer.SetWidth(20)
	composer.SetValue("abc")
	composer, _ = composer.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !composer.VirtualCursor() || composer.Styles().Cursor.Blink {
		t.Fatalf("composer want virtual static cursor, virtual=%v blink=%v", composer.VirtualCursor(), composer.Styles().Cursor.Blink)
	}
	assertStaticCursorOnMovedCharacter(t, "composer", composer.View())

	input := newTextInput(th, "")
	input.Focus()
	input.SetWidth(20)
	input.SetValue("abc")
	input, _ = input.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !input.VirtualCursor() || input.Styles().Cursor.Blink {
		t.Fatalf("text input want virtual static cursor, virtual=%v blink=%v", input.VirtualCursor(), input.Styles().Cursor.Blink)
	}
	assertStaticCursorOnMovedCharacter(t, "text input", input.View())
}

func assertStaticCursorOnMovedCharacter(t *testing.T, name string, out string) {
	t.Helper()
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
	if !strings.Contains(ta.Placeholder, "!") {
		t.Errorf("composer placeholder = %q, want it to mention ! for shell", ta.Placeholder)
	}
}

func TestComposerFooterAdvertisesEnterAndShiftEnter(t *testing.T) {
	// Bordered composer at a non-compact size with empty transcript should
	// surface send/newline help in the panel footer.
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 40})
	sendKey := keyHint(m.keyMap.Send).Key
	nlKey := keyHint(m.keyMap.Newline).Key
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, sendKey) {
		t.Errorf("bordered view missing send key %q:\n%s", sendKey, plain)
	}
	if !strings.Contains(plain, nlKey) && !strings.Contains(plain, keyHint(m.keyMap.Newline).Label) {
		t.Errorf("bordered view missing newline key/label %q/%q:\n%s", nlKey, keyHint(m.keyMap.Newline).Label, plain)
	}
	// Direct footer helper also advertises both from keyMap.
	footer := ansi.Strip(composerFooter(m.th, m.keyMap, 60, false))
	if !strings.Contains(footer, sendKey) || !strings.Contains(footer, nlKey) {
		t.Errorf("composerFooter = %q, want %q and %q", footer, sendKey, nlKey)
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
