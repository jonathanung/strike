package tui

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestOptionsNilThemeMatchesDefaultTheme(t *testing.T) {
	defaultModel, _ := newAppTestModelWithOptions(Options{})
	nilModel, _ := newAppTestModelWithOptions(Options{Theme: nil})
	defaultModel = updateApp(t, defaultModel, tea.WindowSizeMsg{Width: 80, Height: 24})
	nilModel = updateApp(t, nilModel, tea.WindowSizeMsg{Width: 80, Height: 24})

	if got, want := nilModel.View(), defaultModel.View(); got != want {
		t.Error("a nil Options.Theme does not render like theme.Default()")
	}
}

func TestOptionsThemeResolvesAndCopiesCallerValueBeforeWidgetSetup(t *testing.T) {
	setTUITrueColor(t)
	input := theme.Theme{
		Accent:     fixedColor("#010203"),
		Text:       fixedColor("#040506"),
		TextMuted:  fixedColor("#070809"),
		Background: theme.NoBackground(),
	}
	m, _ := newAppTestModelWithOptions(Options{Theme: &input})
	input.Accent = fixedColor("#a1a2a3")
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("themed composer text")

	if got := m.View(); !strings.Contains(got, rgbSGR("#010203")) {
		t.Fatalf("resolved custom accent is not observable in the rendered model: %q", got)
	} else if strings.Contains(got, rgbSGR("#a1a2a3")) {
		t.Fatalf("rendered model changed after caller mutated Options.Theme: %q", got)
	}
	if got := m.composer.View(); !strings.Contains(got, rgbSGR("#040506")) {
		t.Fatalf("textarea text did not use the resolved theme text token: %q", got)
	}
}

func TestOptionsThemeCopiesPointedBackgroundColorBeforeRendering(t *testing.T) {
	setTUITrueColor(t)
	background := lipgloss.Color("#112233")
	th := theme.Theme{Background: &background}
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	before := m.View()
	background = "#445566"
	after := m.View()

	if !strings.Contains(before, "48;2;17;34;51") {
		t.Fatalf("initial view omitted configured background: %q", before)
	}
	if strings.Contains(after, "48;2;68;85;102") {
		t.Fatalf("model view changed after caller mutated pointed background: %q", after)
	}
}

func TestCustomThemeSpacingControlsRootTranscriptHeaderAndPermissionLayout(t *testing.T) {
	for _, tt := range []struct {
		name           string
		spacing        theme.Spacing
		indent         string
		headGap        string
		toolDetail     string
		toolOutputLead string
		choiceGap      string
	}{
		{"default", theme.Default().Spacing, "  ", "  ", "bash · build ✓", "  │ ", "  "},
		{"explicit zero", theme.NewSpacing(0, 0, 0, 0), "", "", "bash·build✓", "│", ""},
		{"custom", theme.NewSpacing(2, 4, 3, 5), "    ", "    ", "bash  ·  build  ✓", "    │  ", "    "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			th := theme.Default()
			th.Spacing = tt.spacing

			user := ansi.Strip((&userCell{text: "message"}).render(40, th))
			if got := strings.TrimRight(strings.Split(user, "\n")[1], " "); got != tt.indent+"message" {
				t.Errorf("transcript indentation = %q, want %q", got, tt.indent+"message")
			}
			tool := ansi.Strip((&toolCell{name: "bash", title: "build", output: "output", done: true}).render(40, th))
			toolLines := strings.Split(tool, "\n")
			if toolLines[0] != th.Icons.Tool+strings.Repeat(" ", th.Spacing.XS)+tt.toolDetail {
				t.Errorf("tool detail spacing = %q, want %q", toolLines[0], th.Icons.Tool+strings.Repeat(" ", th.Spacing.XS)+tt.toolDetail)
			}
			if !strings.HasPrefix(toolLines[1], tt.toolOutputLead+"output") {
				t.Errorf("tool output indentation = %q, want prefix %q", toolLines[1], tt.toolOutputLead+"output")
			}

			m, _ := newAppTestModelWithOptions(Options{Theme: &th})
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 60, Height: 18})
			header := ansi.Strip(m.headerView(60))
			badgeSpace := strings.Repeat(" ", th.Spacing.XS)
			if !strings.Contains(header, "strike"+tt.headGap+"["+badgeSpace+"no model"+badgeSpace+"]") {
				t.Errorf("header badge spacing = %q", header)
			}
			for i, row := range strings.Split(m.View(), "\n") {
				if got := ansi.StringWidth(row); got != 60 {
					t.Errorf("model row %d width = %d, want 60", i, got)
				}
			}

			modal, _ := newTestPermissionModal(t.Name())
			permission := ansi.Strip(modal.view(60, th))
			if !strings.Contains(permission, "1) allow once"+tt.choiceGap+"2) allow always"+tt.choiceGap+"3) reject") {
				t.Errorf("permission choice spacing = %q", permission)
			}
			for i, row := range strings.Split(permission, "\n") {
				if got := lipgloss.Width(row); got != 60 {
					t.Errorf("permission row %d width = %d, want 60", i, got)
				}
			}
		})
	}
}

func TestThemeStylesComposerTextInputsAndSpinnerWithoutWidgetBackgrounds(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Text = fixedColor("#102030")
	th.Accent = fixedColor("#405060")
	th.AccentAlt = fixedColor("#708090")
	th.Background = theme.NoBackground()
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m.composer.SetValue("composer")
	m.turnRunning = true

	apiKey := newAPIKeyModal("anthropic", m.services.Auth, m.th, false)
	apiKey.input.SetValue("key")
	permission := newPermissionModal(protocol.PermissionAsked{RequestID: "theme"}, m.ops, m.th)
	enterPermissionFeedback(t, permission)
	permission.feedback.SetValue("feedback")

	for name, out := range map[string]string{
		"composer":   m.composer.View(),
		"spinner":    m.spin.View(),
		"API key":    apiKey.input.View(),
		"permission": permission.feedback.View(),
		"final view": m.View(),
	} {
		if hasTUIBackgroundSGR(out) {
			t.Errorf("%s emitted a background-setting SGR sequence in NoBackground mode: %q", name, out)
		}
	}
	if got := m.composer.View(); !strings.Contains(got, rgbSGR("#102030")) {
		t.Errorf("composer text did not use the custom Text token: %q", got)
	}
	if got := m.spin.View(); !strings.Contains(got, rgbSGR("#708090")) {
		t.Errorf("spinner did not use the custom AccentAlt (working) token: %q", got)
	}
	for name, out := range map[string]string{
		"API key textinput":    apiKey.input.View(),
		"permission textinput": permission.feedback.View(),
	} {
		if !strings.Contains(out, rgbSGR("#102030")) {
			t.Errorf("%s did not use the custom Text token: %q", name, out)
		}
	}
}

func TestModelViewCanvasCoversModalGuttersAndFooter(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.modal = newProviderModal(m.services, "", m.ops, m.th)
	out := m.View()

	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("modal view has %d rows, want 24", len(lines))
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != 80 {
			t.Errorf("modal view row %d width = %d, want 80", i, got)
		}
	}
	for i, background := range tuiBackgroundCells(out) {
		if background != "48;2;17;34;51" {
			t.Fatalf("modal view cell %d background = %q, want canvas background", i, background)
		}
	}
}

func fixedColor(hex string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: hex, Dark: hex}
}

func setTUITrueColor(t *testing.T) {
	t.Helper()
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })
}

func rgbSGR(hex string) string {
	r, _ := strconv.ParseInt(hex[1:3], 16, 0)
	g, _ := strconv.ParseInt(hex[3:5], 16, 0)
	b, _ := strconv.ParseInt(hex[5:7], 16, 0)
	return "38;2;" + strconv.FormatInt(r, 10) + ";" + strconv.FormatInt(g, 10) + ";" + strconv.FormatInt(b, 10)
}

func hasTUIBackgroundSGR(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		end := i + 2
		for end < len(s) && (s[end] < '@' || s[end] > '~') {
			end++
		}
		if end == len(s) || s[end] != 'm' {
			continue
		}
		params := strings.Split(s[i+2:end], ";")
		for j := 0; j < len(params); j++ {
			code, err := strconv.Atoi(params[j])
			if err != nil {
				continue
			}
			if (code == 38 || code == 48) && j+4 < len(params) && params[j+1] == "2" {
				if code == 48 {
					return true
				}
				j += 4
				continue
			}
			if (code >= 40 && code <= 47) || (code >= 100 && code <= 107) {
				return true
			}
		}
		i = end
	}
	return false
}

func tuiBackgroundCells(s string) []string {
	var cells []string
	background := ""
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			end := i + 2
			for end < len(s) && (s[end] < '@' || s[end] > '~') {
				end++
			}
			if end < len(s) && s[end] == 'm' {
				background = tuiNextBackground(s[i+2:end], background)
			}
			i = end + 1
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		for range ansi.StringWidth(string(r)) {
			cells = append(cells, background)
		}
		i += n
	}
	return cells
}

func tuiNextBackground(params, background string) string {
	for i, parts := 0, strings.Split(params, ";"); i < len(parts); i++ {
		code, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		switch {
		case code == 0 || code == 49:
			background = ""
		case code == 48 && i+4 < len(parts) && parts[i+1] == "2":
			background = "48;2;" + parts[i+2] + ";" + parts[i+3] + ";" + parts[i+4]
			i += 4
		case code == 38 && i+4 < len(parts) && parts[i+1] == "2":
			i += 4
		case code == 48 || (code >= 40 && code <= 47) || (code >= 100 && code <= 107):
			background = "other"
		}
	}
	return background
}
