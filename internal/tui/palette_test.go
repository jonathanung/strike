package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

const paletteCmdTimeout = 2 * time.Second

func TestPaletteContainsOnlySupportedActionsWithStableMetadata(t *testing.T) {
	specs := append([]commandSpec{}, builtinCommandSpecs...)
	specs = append(specs,
		commandSpec{ID: "copy", Name: "/copy", Description: "copy output", Source: commandSourceBuiltin},
		commandSpec{ID: "future", Name: "/future", Description: "future action", Source: commandSourceBuiltin},
		commandSpec{ID: "skill:review", Name: "/review", Description: "review a change", Source: commandSourceSkill},
	)

	got := newPaletteModal(specs, []string{"build"}, paletteAvailability{HasProvider: true}).entries
	want := []paletteEntry{
		{ID: "keybinds", Label: "Keyboard shortcuts", Description: "filterable keybind cheatsheet", Action: paletteAction{Kind: paletteActionKeybinds}},
		{ID: "command:provider", Label: "/provider", Description: "select a provider and model", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}},
		{ID: "command:model", Label: "/model", Description: "select a model for the current provider", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/model"}},
		{ID: "command:effort", Label: "/effort", Description: "set how much reasoning the model spends", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/effort"}},
		{ID: "command:autonomy", Label: "/autonomy", Description: "set exit-gate policy (supervised/agent/checks)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/autonomy"}},
		{ID: "command:auth", Label: "/auth", Description: "manage provider authentication", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/auth"}},
		{ID: "command:settings", Label: "/settings", Description: "manage custom providers and settings", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/settings"}},
		{ID: "agent:build", Label: "/agent build", Description: "select an agent", Action: paletteAction{Kind: paletteActionAgent, Value: "build"}},
		{ID: "command:fast", Label: "/fast", Description: "toggle OpenAI priority tier (faster, ~2× cost)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/fast"}},
		{ID: "command:think", Label: "/think", Description: "show or hide model chain-of-thought", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/think"}},
		{ID: "command:vim", Label: "/vim", Description: "open a file in the embedded editor (pane/overlay) or $EDITOR", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/vim"}},
		{ID: "command:md-read", Label: "/md-read", Description: "open a markdown file in the right pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/md-read"}},
		{ID: "command:theme", Label: "/theme", Description: "select a color theme or set appearance", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/theme"}},
		{ID: "command:layout", Label: "/layout", Description: "toggle horizontal/vertical pane split", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/layout"}},
		{ID: "command:split", Label: "/split", Description: "toggle horizontal/vertical pane split", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/split"}},
		{ID: "command:compact", Label: "/compact", Description: "compact model history (keep recent turns)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/compact"}},
		{ID: "command:fork", Label: "/fork", Description: "duplicate the conversation into a new id", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/fork"}},
		{ID: "command:undo", Label: "/undo", Description: "drop the last completed user/assistant turn", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/undo"}},
		{ID: "command:session", Label: "/session", Description: "browse and resume a past session", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/session"}},
		{ID: "command:help", Label: "/help", Description: "show available commands", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/help"}},
		{ID: "command:keys", Label: "/keys", Description: "show keyboard shortcuts", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/keys"}},
		{ID: "command:memory", Label: "/memory", Description: "list, get, set, delete, export, or import project memory", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/memory"}},
		{ID: "command:issues", Label: "/issues", Description: "list, add, get, close, export, or import project issues", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/issues"}},
		{ID: "command:context", Label: "/context", Description: "inspect effective system-prompt layers", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/context"}},
		{ID: "command:effective-prompt", Label: "/effective-prompt", Description: "inspect effective system-prompt layers", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/effective-prompt"}},
		{ID: "command:upgrade", Label: "/upgrade", Description: "install the latest release and restart", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/upgrade"}},
		{ID: "skill:review", Label: "/review", Description: "review a change", Action: paletteAction{Kind: paletteActionSkill, Value: "review"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("palette entries =\n%#v\nwant\n%#v", got, want)
	}
	for _, entry := range got {
		if entry.Label == "/copy" || entry.Label == "/future" {
			t.Errorf("palette included unsupported builtin %q", entry.Label)
		}
	}
}

func TestPaletteAvailabilityAndDisabledSelection(t *testing.T) {
	t.Run("without provider", func(t *testing.T) {
		for _, command := range []string{"/model", "/review"} {
			m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{})
			assertPaletteDisabled(t, m, command, "select a provider first")
		}
		m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{})
		assertPaletteInvoke(t, m, "/help", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/help"}})
	})

	t.Run("during running turn", func(t *testing.T) {
		for _, command := range []string{"/provider", "/model", "/agent build", "/auth", "/session", "/theme", "/memory", "/issues", "/compact", "/fast", "/layout", "/upgrade", "/review"} {
			m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
			assertPaletteDisabled(t, m, command, "unavailable while a turn is running")
		}
		m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/help", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/help"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/keys", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/keys"}})
		// /vim, /md-read, /think, and prompt inspect stay available mid-turn.
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/think", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/think"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/vim", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/vim"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/md-read", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/md-read"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/context", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/context"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/effective-prompt", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/effective-prompt"}})
	})
}

func TestPaletteEnterEmitsExactInvokeAndInsertMessages(t *testing.T) {
	tests := []struct {
		name    string
		filter  string
		wantMsg paletteInvokeMsg
	}{
		{name: "ordinary action invokes", filter: "/agent build", wantMsg: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionAgent, Value: "build"}}},
		{name: "skill remains insert only", filter: "/review", wantMsg: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionSkill, Value: "review"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true})
			assertPaletteInvoke(t, m, tt.filter, tt.wantMsg)
		})
	}
}

func TestPaletteFilteringRanksExactPrefixAndSubsequenceStablyAndIgnoresCase(t *testing.T) {
	specs := []commandSpec{
		{ID: commandProvider, Name: "/provider", Description: "x", Source: commandSourceBuiltin},
		{ID: commandModel, Name: "/model", Description: "x", Source: commandSourceBuiltin},
		{ID: commandAuth, Name: "/auth", Description: "x", Source: commandSourceBuiltin},
		{ID: commandHelp, Name: "/help", Description: "x", Source: commandSourceBuiltin},
		{ID: "skill:subsequence", Name: "/process-video-editor", Description: "x", Source: commandSourceSkill},
		{ID: "skill:prefix-one", Name: "/provider-tools", Description: "x", Source: commandSourceSkill},
		{ID: "skill:prefix-two", Name: "/provider-check", Description: "x", Source: commandSourceSkill},
	}
	m := newPaletteModal(specs, nil, paletteAvailability{HasProvider: true})
	typePalette(t, m, "/PrOvIdEr")

	want := []paletteInvokeMsg{
		{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}},
		{Action: paletteAction{Kind: paletteActionSkill, Value: "provider-tools"}},
		{Action: paletteAction{Kind: paletteActionSkill, Value: "provider-check"}},
		{Action: paletteAction{Kind: paletteActionSkill, Value: "process-video-editor"}},
	}
	for i, want := range want {
		copy := *m
		for range i {
			updatePalette(t, &copy, tea.KeyMsg{Type: tea.KeyDown})
		}
		assertPaletteEnter(t, &copy, want)
	}
}

func TestPaletteBackspaceRestoresResultsAndZeroResultsDoNotSelect(t *testing.T) {
	m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true})
	// "q" is not an ordered subsequence of any shipped label/description.
	typePalette(t, m, "q")
	if view := m.view(80, theme.Default()); !strings.Contains(view, "no matching actions") {
		t.Errorf("zero-result view did not explain its empty state:\n%s", view)
	}
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != m || cmd != nil {
		t.Fatal("enter with zero results closed the palette or emitted a command")
	}

	updatePalette(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	assertPaletteEnter(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionKeybinds}})
}

func TestPaletteNavigationKeysWrapAndSelectExpectedActions(t *testing.T) {
	tests := []struct {
		name string
		keys []tea.KeyMsg
		want paletteInvokeMsg
	}{
		{name: "down", keys: []tea.KeyMsg{{Type: tea.KeyDown}}, want: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}}},
		{name: "up wraps", keys: []tea.KeyMsg{{Type: tea.KeyUp}}, want: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionSkill, Value: "review"}}},
		{name: "ctrl n", keys: []tea.KeyMsg{{Type: tea.KeyCtrlN}}, want: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}}},
		{name: "ctrl p wraps", keys: []tea.KeyMsg{{Type: tea.KeyCtrlP}}, want: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionSkill, Value: "review"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true})
			for _, key := range tt.keys {
				updatePalette(t, m, key)
			}
			assertPaletteEnter(t, m, tt.want)
		})
	}
}

func TestPaletteFilteringResetsSelectionToFirstMatch(t *testing.T) {
	m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true})
	for range 5 {
		updatePalette(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	// Filter to a unique prefix so the first match is deterministic.
	typePalette(t, m, "auth")
	assertPaletteEnter(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/auth"}})
}

func TestPaletteRefreshPreservesFilterAndSelectedAction(t *testing.T) {
	m := newPaletteModal(paletteTestSpecs(), []string{"build", "code reviewer"}, paletteAvailability{HasProvider: true})
	typePalette(t, m, "agent")
	updatePalette(t, m, tea.KeyMsg{Type: tea.KeyDown})

	m.refresh(buildPaletteEntries(paletteTestSpecs(), []string{"build", "code reviewer"}, paletteAvailability{HasProvider: true, TurnRunning: true}))
	if m.filter != "agent" {
		t.Errorf("refresh changed filter to %q, want %q", m.filter, "agent")
	}
	m.refresh(buildPaletteEntries(paletteTestSpecs(), []string{"build", "code reviewer"}, paletteAvailability{HasProvider: true}))
	assertPaletteEnter(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionAgent, Value: "code reviewer"}})
}

func TestPalettePreservesExactMultiWordAgentAction(t *testing.T) {
	m := newPaletteModal(paletteTestSpecs(), []string{"code reviewer"}, paletteAvailability{HasProvider: true})
	assertPaletteInvoke(t, m, "/agent code reviewer", paletteInvokeMsg{
		Action: paletteAction{Kind: paletteActionAgent, Value: "code reviewer"},
	})
}

func TestInvalidAndCollidingConstructedSkillsAreOmittedFromCatalogAndPalette(t *testing.T) {
	skills := []host.Skill{
		{Name: "valid-skill", Description: "kept"},
		{Name: "", Description: "empty"},
		{Name: "two words", Description: "whitespace"},
		{Name: "bad/name", Description: "slash"},
		{Name: "help", Description: "builtin collision"},
		{Name: "bad\x1b]52;c;payload\x07", Description: "OSC52"},
		{Name: "bad\nname", Description: "newline"},
		{Name: "bad\tname", Description: "tab"},
		{Name: "bad\u009bname", Description: "C1"},
		{Name: "bad" + string([]byte{0xff}), Description: "invalid UTF-8"},
	}
	catalog := commandCatalog(skills)
	entries := newPaletteModal(catalog, nil, paletteAvailability{HasProvider: true}).entries

	var skillSpecs, skillEntries []string
	for _, spec := range catalog {
		if spec.Source == commandSourceSkill {
			skillSpecs = append(skillSpecs, spec.Name)
		}
	}
	for _, entry := range entries {
		if entry.Action.Kind == paletteActionSkill {
			skillEntries = append(skillEntries, entry.Label)
		}
	}
	if !reflect.DeepEqual(skillSpecs, []string{"/valid-skill"}) {
		t.Errorf("skill command specs = %q, want only valid skill", skillSpecs)
	}
	if !reflect.DeepEqual(skillEntries, []string{"/valid-skill"}) {
		t.Errorf("palette skill entries = %q, want only valid skill", skillEntries)
	}
}

func TestInvalidConstructedAgentNamesAreOmittedFromPalette(t *testing.T) {
	agents := []string{
		"代码 reviewer",
		"bad\x1b]52;c;payload\x07",
		"bad\nname",
		"bad\tname",
		"bad\u009bname",
		"bad" + string([]byte{0xff}),
		" leading",
		"trailing ",
	}
	entries := newPaletteModal(commandCatalog(nil), agents, paletteAvailability{HasProvider: true}).entries

	var got []string
	for _, entry := range entries {
		if entry.Action.Kind == paletteActionAgent {
			got = append(got, entry.Action.Value)
		}
	}
	if !reflect.DeepEqual(got, []string{"代码 reviewer"}) {
		t.Errorf("palette agent actions = %q, want only valid multi-word Unicode agent", got)
	}
}

func TestPaletteViewDoesNotRenderDescriptionControlPayloadAsTerminalMetadataOrRows(t *testing.T) {
	description := "ordinary 世界\x1b]52;c;copied\x07\x1b[31m\u009b31m\ninjected-row\tcell"
	catalog := commandCatalog([]host.Skill{{Name: "inspect", Description: description}})
	assertNoUntrustedTerminalControls(t, catalog[len(catalog)-1].Description)
	m := newPaletteModal(catalog, nil, paletteAvailability{HasProvider: true})
	typePalette(t, m, "/inspect")

	rendered := m.view(120, theme.Default())
	if strings.Contains(rendered, "\x1b]52;c;copied\x07") {
		t.Fatalf("palette rendered attacker OSC52 sequence: %q", rendered)
	}
	plain := ansi.Strip(rendered)
	assertNoUntrustedTerminalControls(t, plain)
	assertPayloadRemainsOnDescriptionRow(t, plain, "ordinary 世界", "injected-row")
}

func TestPaletteEscapeClosesWithoutCommand(t *testing.T) {
	m := newPaletteModal(paletteTestSpecs(), nil, paletteAvailability{HasProvider: true})
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if next != nil || cmd != nil {
		t.Fatal("escape did not close the palette without emitting a command")
	}
}

func TestPaletteViewHandlesTinyWidthsAndExplainsDisabledAndEmptyStates(t *testing.T) {
	m := newPaletteModal(paletteTestSpecs(), nil, paletteAvailability{})
	for _, width := range []int{0, 1, 2, 3, 4} {
		if got := m.view(width, theme.Default()); got == "" {
			t.Errorf("view at width %d was empty", width)
		}
	}
	typePalette(t, m, "/model")
	if view := m.view(80, theme.Default()); !strings.Contains(view, "select a provider first") {
		t.Errorf("disabled view did not explain why the action is unavailable:\n%s", view)
	}
	typePalette(t, m, "-missing")
	if view := m.view(80, theme.Default()); !strings.Contains(view, "no matching actions") {
		t.Errorf("empty view did not explain that no actions match:\n%s", view)
	}
}

func paletteTestSpecs() []commandSpec {
	return append(append([]commandSpec{}, builtinCommandSpecs...), commandSpec{
		ID: "skill:review", Name: "/review", Description: "review a change", Source: commandSourceSkill,
	})
}

func assertPaletteDisabled(t *testing.T, m *paletteModal, filter, reason string) {
	t.Helper()
	typePalette(t, m, filter)
	if view := m.view(80, theme.Default()); !strings.Contains(view, reason) {
		t.Errorf("disabled action %q did not show reason %q:\n%s", filter, reason, view)
	}
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != m {
		t.Errorf("enter on disabled action %q closed the palette", filter)
	}
	if cmd != nil {
		t.Errorf("enter on disabled action %q emitted a command", filter)
	}
}

func assertPaletteInvoke(t *testing.T, m *paletteModal, filter string, want paletteInvokeMsg) {
	t.Helper()
	typePalette(t, m, filter)
	assertPaletteEnter(t, m, want)
}

func assertPaletteEnter(t *testing.T, m *paletteModal, want paletteInvokeMsg) {
	t.Helper()
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatal("enter on enabled action did not close the palette")
	}
	msg := runPaletteCmd(t, cmd)
	got, ok := msg.(paletteInvokeMsg)
	if !ok {
		t.Fatalf("enter emitted %T, want paletteInvokeMsg", msg)
	}
	if got != want {
		t.Errorf("invoke message = %#v, want %#v", got, want)
	}
}

func typePalette(t *testing.T, m *paletteModal, text string) {
	t.Helper()
	updatePalette(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

func updatePalette(t *testing.T, m *paletteModal, key tea.KeyMsg) {
	t.Helper()
	next, cmd := m.update(key)
	if next == nil {
		t.Fatalf("key %q unexpectedly closed the palette", key.String())
	}
	if cmd != nil {
		t.Fatalf("key %q unexpectedly emitted a command", key.String())
	}
}

func runPaletteCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a tea command, got nil")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(paletteCmdTimeout):
		t.Fatalf("tea command did not complete within %s", paletteCmdTimeout)
		return nil
	}
}
