package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

const paletteCmdTimeout = 2 * time.Second

func TestPaletteContainsOnlySupportedActionsWithStableMetadata(t *testing.T) {
	specs := append([]commandSpec{}, builtinCommandSpecs...)
	specs = append(specs,
		commandSpec{ID: "nonesuch", Name: "/nonesuch", Description: "unsupported builtin", Source: commandSourceBuiltin},
		commandSpec{ID: "future", Name: "/future", Description: "future action", Source: commandSourceBuiltin},
		commandSpec{ID: "skill:review", Name: "/review", Description: "review a change", Source: commandSourceSkill},
	)

	got := newPaletteModal(specs, []string{"build"}, paletteAvailability{HasProvider: true}).entries
	want := []paletteEntry{
		{ID: "keybinds-rebind", Label: "Keyboard shortcuts", Description: "interactively rebind keyboard shortcuts", Action: paletteAction{Kind: paletteActionKeybindEditor}},
		{ID: "command:provider", Label: "/provider", Description: "select a provider and model", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}},
		{ID: "command:model", Label: "/model", Description: "select a model from authenticated providers", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/model"}},
		{ID: "command:effort", Label: "/effort", Description: "set how much reasoning the model spends", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/effort"}},
		{ID: "command:autonomy", Label: "/autonomy", Description: "set exit-gate policy (supervised/agent/checks)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/autonomy"}},
		{ID: "command:mode", Label: "/mode", Description: "set permission posture (default/plan/accept-edits/yolo)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/mode"}},
		{ID: "command:auth", Label: "/auth", Description: "manage provider authentication", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/auth"}},
		{ID: "command:settings", Label: "/settings", Description: "defaults (theme, editor, mode) and custom providers", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/settings"}},
		{ID: "agent:build", Label: "/agent build", Description: "select an agent", Action: paletteAction{Kind: paletteActionAgent, Value: "build"}},
		{ID: "command:agents", Label: "/agents", Description: "focus the agents right pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/agents"}},
		{ID: "command:activity", Label: "/activity", Description: "focus the activity right pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/activity"}},
		{ID: "command:files", Label: "/files", Description: "focus the files right pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/files"}},
		{ID: "command:visualizer", Label: "/visualizer", Description: "focus the visualizer right pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/visualizer"}},
		{ID: "command:system", Label: "/system", Description: "focus the system right pane (requires telemetry on)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/system"}},
		{ID: "command:telemetry", Label: "/telemetry", Description: "show or hide local system metrics (CPU/RAM/disk)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/telemetry"}},
		{ID: "command:fast", Label: "/fast", Description: "toggle OpenAI priority tier (faster, ~2× cost)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/fast"}},
		{ID: "command:think", Label: "/think", Description: "show or hide model chain-of-thought", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/think"}},
		{ID: "command:vim", Label: "/vim", Description: "open a file in the editor (embedded/modal/takeover; see vimMode)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/vim"}},
		{ID: "command:nano", Label: "/nano", Description: "open a file in nano (embedded/modal/takeover; see nanoMode)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/nano"}},
		{ID: "command:md-read", Label: "/md-read", Description: "open a markdown file (embedded right pane or modal; see mdReadMode)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/md-read"}},
		{ID: "command:theme", Label: "/theme", Description: "select a color theme or set appearance", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/theme"}},
		{ID: "command:layout", Label: "/layout", Description: "toggle horizontal/vertical pane split", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/layout"}},
		{ID: "command:split", Label: "/split", Description: "toggle horizontal/vertical pane split", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/split"}},
		{ID: "command:compact", Label: "/compact", Description: "compact model history (keep recent turns)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/compact"}},
		{ID: "command:fork", Label: "/fork", Description: "duplicate the conversation into a new id", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/fork"}},
		{ID: "command:undo", Label: "/undo", Description: "undo last turn (chat only, or chat + restore files)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/undo"}},
		{ID: "command:rewind", Label: "/rewind", Description: "fork a new id from a previous turn (keeps original)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/rewind"}},
		{ID: "command:session", Label: "/session", Description: "browse and resume a past session", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/session"}},
		{ID: "command:rename", Label: "/rename", Description: "rename the current session", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/rename"}},
		{ID: "command:export", Label: "/export", Description: "export the conversation to markdown", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/export"}},
		{ID: "command:copy", Label: "/copy", Description: "copy the last assistant response to the clipboard", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/copy"}},
		{ID: "command:help", Label: "/help", Description: "show available commands", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/help"}},
		{ID: "command:keys", Label: "/keys", Description: "show keyboard shortcuts", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/keys"}},
		{ID: "command:legend", Label: "/legend", Description: "explain UI icons, status glyphs, and chrome", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/legend"}},
		{ID: "command:memory", Label: "/memory", Description: "list, get, set, delete, export, or import project memory", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/memory"}},
		{ID: "command:issues", Label: "/issues", Description: "list, add, get, close, export, or import project issues", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/issues"}},
		{ID: "command:goal", Label: "/goal", Description: "loop harness: set, run, status, pause, resume, abort, log, list", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/goal"}},
		{ID: "command:loop", Label: "/loop", Description: "schedule a recurring LLM job (session-only)", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/loop"}},
		{ID: "command:context", Label: "/context", Description: "context doctor: system-prompt layer breakdown", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/context"}},
		{ID: "command:effective-prompt", Label: "/effective-prompt", Description: "context doctor: system-prompt layer breakdown", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/effective-prompt"}},
		{ID: "command:cost", Label: "/cost", Description: "session token and cost totals", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/cost"}},
		{ID: "command:upgrade", Label: "/upgrade", Description: "install the latest release and restart", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/upgrade"}},
		{ID: "command:init", Label: "/init", Description: "create or update project AGENTS.md", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/init"}},
		{ID: "command:mcp", Label: "/mcp", Description: "MCP servers: status, retry, disable", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/mcp"}},
		{ID: "command:exit", Label: "/exit", Description: "quit strike", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/exit"}},
		{ID: "command:quit", Label: "/quit", Description: "quit strike", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/quit"}},
		{ID: "command:focus-left", Label: "/focus-left", Description: "focus left pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/focus-left"}},
		{ID: "command:focus-right", Label: "/focus-right", Description: "focus right pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/focus-right"}},
		{ID: "command:window-next", Label: "/window-next", Description: "cycle to next right-pane window", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/window-next"}},
		{ID: "command:window-prev", Label: "/window-prev", Description: "cycle to previous right-pane window", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/window-prev"}},
		{ID: "command:group-next", Label: "/group-next", Description: "cycle to next right-pane stack group", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/group-next"}},
		{ID: "command:group-prev", Label: "/group-prev", Description: "cycle to previous right-pane stack group", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/group-prev"}},
		{ID: "command:scroll-up", Label: "/scroll-up", Description: "scroll transcript up", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/scroll-up"}},
		{ID: "command:scroll-down", Label: "/scroll-down", Description: "scroll transcript down", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/scroll-down"}},
		{ID: "command:jump-bottom", Label: "/jump-bottom", Description: "jump transcript to latest output", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/jump-bottom"}},
		{ID: "command:palette", Label: "/palette", Description: "open command palette", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/palette"}},
		{ID: "command:interrupt", Label: "/interrupt", Description: "interrupt the running turn", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/interrupt"}},
		{ID: "command:save-defaults", Label: "/save-defaults", Description: "save provider/model/agent/effort/mode defaults", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/save-defaults"}},
		{ID: "command:leave-editor", Label: "/leave-editor", Description: "leave embedded editor pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/leave-editor"}},
		{ID: "command:edit-prompt", Label: "/edit-prompt", Description: "edit prompt in external editor", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/edit-prompt"}},
		{ID: "command:agent-next", Label: "/agent-next", Description: "cycle agent persona", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/agent-next"}},
		{ID: "command:mode-next", Label: "/mode-next", Description: "cycle permission mode", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/mode-next"}},
		{ID: "command:tool-prev", Label: "/tool-prev", Description: "select previous tool cell", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/tool-prev"}},
		{ID: "command:tool-next", Label: "/tool-next", Description: "select next tool cell", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/tool-next"}},
		{ID: "command:tool-expand", Label: "/tool-expand", Description: "expand tool cell or open file:line", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/tool-expand"}},
		{ID: "command:tool-copy", Label: "/tool-copy", Description: "copy selected transcript cell", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/tool-copy"}},
		{ID: "command:tool-review", Label: "/tool-review", Description: "review selected edit in editor", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/tool-review"}},
		{ID: "command:tool-apply", Label: "/tool-apply", Description: "apply selected patch to worktree", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/tool-apply"}},
		{ID: "command:subagent", Label: "/subagent", Description: "enter first subagent transcript", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/subagent"}},
		{ID: "command:parent", Label: "/parent", Description: "return to parent session transcript", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/parent"}},
		{ID: "command:subagent-next", Label: "/subagent-next", Description: "next sibling subagent", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/subagent-next"}},
		{ID: "command:subagent-prev", Label: "/subagent-prev", Description: "previous sibling subagent", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/subagent-prev"}},
		{ID: "command:root-new", Label: "/root-new", Description: "spawn a new concurrent root session", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/root-new"}},
		{ID: "command:root-open", Label: "/root-open", Description: "activate selected agents-pane root", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/root-open"}},
		{ID: "command:root-interrupt", Label: "/root-interrupt", Description: "interrupt selected agents-pane root", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/root-interrupt"}},
		{ID: "command:root-hide", Label: "/root-hide", Description: "hide selected root from agents pane", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/root-hide"}},
		{ID: "command:root-filter", Label: "/root-filter", Description: "cycle agents pane view filter", Action: paletteAction{Kind: paletteActionBuiltin, Value: "/root-filter"}},
		{ID: "skill:review", Label: "/review", Description: "review a change", Action: paletteAction{Kind: paletteActionSkill, Value: "review"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("palette entries =\n%#v\nwant\n%#v", got, want)
	}
	for _, entry := range got {
		if entry.Label == "/nonesuch" || entry.Label == "/future" {
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
		// /vim, /nano, /md-read, /think, /export, /copy, /cost, /mcp, pane jumps,
		// permission mode dial, and prompt inspect stay available mid-turn.
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/mode", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/mode"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/mode-next", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/mode-next"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/think", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/think"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/vim", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/vim"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/nano", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/nano"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/md-read", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/md-read"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/export", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/export"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/copy", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/copy"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/context", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/context"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/effective-prompt", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/effective-prompt"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/cost", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/cost"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/mcp", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/mcp"}})
		for _, pane := range []string{"/agents", "/activity", "/files", "/visualizer", "/system"} {
			m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
			assertPaletteInvoke(t, m, pane, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: pane}})
		}
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/exit", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/exit"}})
		m = newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true, TurnRunning: true})
		assertPaletteInvoke(t, m, "/quit", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/quit"}})
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
			updatePalette(t, &copy, tea.KeyPressMsg{Code: tea.KeyDown})
		}
		assertPaletteEnter(t, &copy, want)
	}
}

func TestPaletteBackspaceRestoresResultsAndZeroResultsDoNotSelect(t *testing.T) {
	m := newPaletteModal(paletteTestSpecs(), []string{"build"}, paletteAvailability{HasProvider: true})
	// "zzzz" is not an ordered subsequence of any shipped label/description.
	typePalette(t, m, "zzzz")
	if view := m.view(80, theme.Default()); !strings.Contains(view, "no matching actions") {
		t.Errorf("zero-result view did not explain its empty state:\n%s", view)
	}
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next != m || cmd != nil {
		t.Fatal("enter with zero results closed the palette or emitted a command")
	}

	for range 4 {
		updatePalette(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	assertPaletteEnter(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionKeybindEditor}})
}

func TestPaletteNavigationKeysWrapAndSelectExpectedActions(t *testing.T) {
	tests := []struct {
		name string
		keys []tea.KeyPressMsg
		want paletteInvokeMsg
	}{
		{name: "down", keys: []tea.KeyPressMsg{{Code: tea.KeyDown}}, want: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}}},
		{name: "up wraps", keys: []tea.KeyPressMsg{{Code: tea.KeyUp}}, want: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionSkill, Value: "review"}}},
		{name: "ctrl n", keys: []tea.KeyPressMsg{{Code: 'n', Mod: tea.ModCtrl}}, want: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}}},
		{name: "ctrl p wraps", keys: []tea.KeyPressMsg{{Code: 'p', Mod: tea.ModCtrl}}, want: paletteInvokeMsg{Action: paletteAction{Kind: paletteActionSkill, Value: "review"}}},
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
		updatePalette(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	// Filter to a unique prefix so the first match is deterministic.
	typePalette(t, m, "auth")
	assertPaletteEnter(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/auth"}})
}

func TestPaletteRefreshPreservesFilterAndSelectedAction(t *testing.T) {
	m := newPaletteModal(paletteTestSpecs(), []string{"build", "code reviewer"}, paletteAvailability{HasProvider: true})
	typePalette(t, m, "agent")
	updatePalette(t, m, tea.KeyPressMsg{Code: tea.KeyDown})

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
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	updatePalette(t, m, tea.KeyPressMsg{Text: text})
}

func updatePalette(t *testing.T, m *paletteModal, key tea.KeyPressMsg) {
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
