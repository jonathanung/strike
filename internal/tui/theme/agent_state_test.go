package theme_test

import (
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestAgentStateLabel(t *testing.T) {
	cases := []struct {
		state theme.AgentState
		want  string
	}{
		{theme.AgentStateReady, "ready"},
		{theme.AgentStateWorking, "working"},
		{theme.AgentStateAttention, "needs you"},
		{theme.AgentStateError, "error"},
		{theme.AgentStateDead, "dead"},
	}
	for _, tc := range cases {
		if got := tc.state.Label(); got != tc.want {
			t.Errorf("%v.Label() = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestAgentStateColorTokens(t *testing.T) {
	th := theme.Default()
	cases := []struct {
		state theme.AgentState
		want  lipgloss.AdaptiveColor
	}{
		{theme.AgentStateReady, th.Success},
		{theme.AgentStateWorking, th.AccentAlt},
		{theme.AgentStateAttention, th.Warning},
		{theme.AgentStateError, th.Error},
		{theme.AgentStateDead, th.TextMuted},
	}
	for _, tc := range cases {
		got := th.AgentStateColor(tc.state)
		if got != tc.want {
			t.Errorf("AgentStateColor(%v) = %#v, want %#v", tc.state, got, tc.want)
		}
	}
}

func TestAgentStateColorUsesCustomThemeTokens(t *testing.T) {
	th := theme.Theme{
		Success:   lipgloss.AdaptiveColor{Light: "#010101", Dark: "#010101"},
		AccentAlt: lipgloss.AdaptiveColor{Light: "#020202", Dark: "#020202"},
		Warning:   lipgloss.AdaptiveColor{Light: "#030303", Dark: "#030303"},
		Error:     lipgloss.AdaptiveColor{Light: "#040404", Dark: "#040404"},
		TextMuted: lipgloss.AdaptiveColor{Light: "#050505", Dark: "#050505"},
	}.Resolve()

	if got := th.AgentStateColor(theme.AgentStateReady); got != th.Success {
		t.Errorf("ready color = %#v, want Success %#v", got, th.Success)
	}
	if got := th.AgentStateColor(theme.AgentStateWorking); got != th.AccentAlt {
		t.Errorf("working color = %#v, want AccentAlt %#v", got, th.AccentAlt)
	}
	if got := th.AgentStateColor(theme.AgentStateAttention); got != th.Warning {
		t.Errorf("attention color = %#v, want Warning %#v", got, th.Warning)
	}
	if got := th.AgentStateColor(theme.AgentStateError); got != th.Error {
		t.Errorf("error color = %#v, want Error %#v", got, th.Error)
	}
	if got := th.AgentStateColor(theme.AgentStateDead); got != th.TextMuted {
		t.Errorf("dead color = %#v, want TextMuted %#v", got, th.TextMuted)
	}
}

func TestAgentStateStylesUseTokenForeground(t *testing.T) {
	th := theme.Default()
	for _, state := range []theme.AgentState{
		theme.AgentStateReady,
		theme.AgentStateWorking,
		theme.AgentStateAttention,
		theme.AgentStateError,
		theme.AgentStateDead,
	} {
		want := th.AgentStateColor(state)
		if got := th.AgentStateStyle(state).GetForeground(); got != want {
			t.Errorf("AgentStateStyle(%v) fg = %v, want %v", state, got, want)
		}
		strong := th.AgentStateStrongStyle(state)
		if got := strong.GetForeground(); got != want {
			t.Errorf("AgentStateStrongStyle(%v) fg = %v, want %v", state, got, want)
		}
		if !strong.GetBold() {
			t.Errorf("AgentStateStrongStyle(%v) is not bold", state)
		}
	}
}

func TestSpinnerStyleUsesWorkingToken(t *testing.T) {
	th := theme.Default()
	if got, want := th.S().Spinner.GetForeground(), th.AccentAlt; got != want {
		t.Errorf("Spinner fg = %v, want AccentAlt %v", got, want)
	}
}

func TestDefaultWarningIsClearYellow(t *testing.T) {
	// Needs-you / attention chrome uses Warning; both adaptive sides must be a
	// clear yellow with enough weight for light and dark terminals.
	th := theme.Default()
	want := lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#ffcc33"}
	if th.Warning != want {
		t.Fatalf("Default Warning = %#v, want clear yellow %#v", th.Warning, want)
	}
	if got := th.AgentStateColor(theme.AgentStateAttention); got != want {
		t.Fatalf("Attention color = %#v, want Warning yellow %#v", got, want)
	}
}

func TestPackagedThemesWarningYellowReadable(t *testing.T) {
	// Stock packs must keep needs-you on a yellow warning token. Light side is
	// deeper than dark so pale yellows do not wash out on light backgrounds.
	cases := []struct {
		id          string
		light, dark string
	}{
		{"nord", "#9e7a2f", "#ebcb8b"},
		{"dracula", "#8a7f12", "#f1fa8c"},
		{"monokai", "#8a8018", "#e6db74"},
		{"catppuccin", "#df8e1d", "#f9e2af"},
		{"gruvbox", "#b57614", "#fabd2f"},
	}
	cat := theme.Builtin()
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			e, ok := theme.Lookup(cat, tc.id)
			if !ok {
				t.Fatalf("theme %q missing from Builtin", tc.id)
			}
			got := e.Theme.AgentStateColor(theme.AgentStateAttention)
			want := lipgloss.AdaptiveColor{Light: tc.light, Dark: tc.dark}
			if got != want {
				t.Fatalf("Attention/Warning = %#v, want yellow %#v", got, want)
			}
		})
	}
}
