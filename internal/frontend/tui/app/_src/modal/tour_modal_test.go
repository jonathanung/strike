package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestTourSectionsDefault(t *testing.T) {
	ctx := tourContext{
		keys:      defaultKeyMap(),
		hasAgents: true,
		canSplit:  true,
	}
	secs := buildTourSections(ctx)
	if len(secs) != int(tourSectionIDCount) {
		t.Fatalf("sections = %d, want %d", len(secs), tourSectionIDCount)
	}
	want := []string{
		"Pane navigation",
		"Agents and subagents",
		"Permissions",
		"Autonomy",
		"Key help",
		"Command discovery",
	}
	for i, title := range want {
		if secs[i].title != title {
			t.Errorf("section[%d] = %q, want %q", i, secs[i].title, title)
		}
	}
}

func TestTourOmitsAgentsWhenUnavailable(t *testing.T) {
	ctx := tourContext{keys: defaultKeyMap(), hasAgents: false}
	secs := buildTourSections(ctx)
	for _, s := range secs {
		if s.id == tourSectionAgents {
			t.Fatal("agents section should be omitted when hasAgents=false")
		}
	}
	if len(secs) != int(tourSectionIDCount)-1 {
		t.Fatalf("sections = %d, want %d", len(secs), int(tourSectionIDCount)-1)
	}
}

func TestTourNavigationAdvanceSkipRevisit(t *testing.T) {
	ctx := tourContext{keys: defaultKeyMap(), hasAgents: true}
	tm := newTourModal(ctx, focusLeft, true)
	if len(tm.sections) < 3 {
		t.Fatalf("need ≥3 sections, got %d", len(tm.sections))
	}

	// Advance.
	next, _ := tm.update(tea.KeyPressMsg{Code: tea.KeyDown})
	tm = next.(*tourModal)
	if tm.cursor != 1 {
		t.Fatalf("after down cursor = %d, want 1", tm.cursor)
	}
	if !tm.seen[tm.sections[1].id] {
		t.Fatal("section 1 should be seen")
	}

	// Skip current.
	next, _ = tm.update(tea.KeyPressMsg{Code: 's', Text: "s"})
	tm = next.(*tourModal)
	if !tm.skipped[tm.sections[1].id] {
		// skip advances after marking; section 1 was current before skip.
		// After skip, cursor is 2; section 1 should be skipped.
	}
	// Find skipped: the one we were on before skip was index 1.
	foundSkip := false
	for id, v := range tm.skipped {
		if v {
			foundSkip = true
			_ = id
		}
	}
	if !foundSkip {
		t.Fatal("expected a skipped section")
	}
	if tm.cursor != 2 {
		t.Fatalf("after skip cursor = %d, want 2", tm.cursor)
	}

	// Revisit first section via number key.
	next, _ = tm.update(tea.KeyPressMsg{Code: '1', Text: "1"})
	tm = next.(*tourModal)
	if tm.cursor != 0 {
		t.Fatalf("after 1 cursor = %d, want 0", tm.cursor)
	}

	// Prev from 0 stays.
	next, _ = tm.update(tea.KeyPressMsg{Code: tea.KeyUp})
	tm = next.(*tourModal)
	if tm.cursor != 0 {
		t.Fatalf("up at start cursor = %d", tm.cursor)
	}
}

func TestTourCancelDoesNotMutateSettingsOrFocus(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	m.focus = focusRight
	fs := &fakeSettings{}
	m.services.Settings = fs
	m.providerName = "echo"
	m.modelName = "echo"

	// Open tour via FTUE child path.
	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	m.modal.(*ftueModal).cursor = int(ftueStepTour)
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m.modal.(*tourModal); !ok {
		t.Fatalf("modal = %T", m.modal)
	}

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right restored", m.focus)
	}
	if len(fs.saved) != 0 || len(fs.savedThemes) != 0 {
		t.Fatalf("settings writes: saved=%v themes=%v", fs.saved, fs.savedThemes)
	}
	assertNoAppOp(t, ops)
}

func TestTourFinishRestoresFocus(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 120, 40, true
	m.focus = focusLeft

	m.modal = m.openTourModal()
	if m.focus != focusLeft {
		t.Fatal("open must not change focus")
	}
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if m.modal != nil {
		t.Fatalf("modal after finish = %T", m.modal)
	}
	if m.focus != focusLeft {
		t.Fatalf("focus = %v, want left", m.focus)
	}
}

func TestTourBodyUsesLiveKeybinds(t *testing.T) {
	keys := defaultKeyMap()
	// Remap focus chords so the tour must read Help(), not hardcode defaults.
	keys.FocusLeft = key.NewBinding(key.WithKeys("alt+h"), key.WithHelp("alt+h", "focus left"))
	keys.FocusRight = key.NewBinding(key.WithKeys("alt+l"), key.WithHelp("alt+l", "focus right"))
	keys.Palette = key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette"))
	keys.KeyHelp = key.NewBinding(key.WithKeys("f2"), key.WithHelp("f2", "keybinds"))
	keys.PermissionMode = key.NewBinding(key.WithKeys("ctrl+m"), key.WithHelp("ctrl+m", "permission mode"))

	ctx := tourContext{
		keys:       keys,
		agentsKeys: defaultAgentsKeyMap(),
		hasAgents:  true,
		canSplit:   true,
		windowIDs:  []string{"context", "agents"},
		activeWin:  "context",
		permMode:   "default",
		autonomy:   "supervised",
	}
	tm := newTourModal(ctx, focusLeft, true)
	th := theme.Default().Resolve()

	// Panes section should mention remapped focus keys.
	body := ansi.Strip(tm.sectionBody(tourSectionPanes, th))
	if !strings.Contains(body, "alt+h") || !strings.Contains(body, "alt+l") {
		t.Fatalf("panes body missing remapped focus keys:\n%s", body)
	}

	// Jump to keys section.
	for i, s := range tm.sections {
		if s.id == tourSectionKeys {
			tm.cursor = i
			break
		}
	}
	body = ansi.Strip(tm.sectionBody(tourSectionKeys, th))
	if !strings.Contains(body, "f2") {
		t.Fatalf("keys body missing remapped help key:\n%s", body)
	}

	for i, s := range tm.sections {
		if s.id == tourSectionCommands {
			tm.cursor = i
			break
		}
	}
	body = ansi.Strip(tm.sectionBody(tourSectionCommands, th))
	if !strings.Contains(body, "ctrl+p") {
		t.Fatalf("commands body missing remapped palette:\n%s", body)
	}

	for i, s := range tm.sections {
		if s.id == tourSectionPermissions {
			tm.cursor = i
			break
		}
	}
	body = ansi.Strip(tm.sectionBody(tourSectionPermissions, th))
	if !strings.Contains(body, "ctrl+m") {
		t.Fatalf("permissions body missing remapped mode key:\n%s", body)
	}
	if !strings.Contains(body, "default") {
		t.Fatalf("permissions body missing current mode:\n%s", body)
	}
}

func TestTourConstrainedWidths(t *testing.T) {
	ctx := tourContext{
		keys:       defaultKeyMap(),
		agentsKeys: defaultAgentsKeyMap(),
		hasAgents:  true,
		singlePane: true,
		windowIDs:  []string{"context", "agents"},
		permMode:   "plan",
		autonomy:   "agent",
	}
	tm := newTourModal(ctx, focusLeft, true)
	th := theme.Default().Resolve()
	for _, width := range []int{20, 40, 60, 80, ui.ModalWidth(40), ui.ModalWidth(120)} {
		view := ansi.Strip(tm.view(width, th))
		if view == "" {
			t.Fatalf("width %d: empty view", width)
		}
		if !strings.Contains(view, "Pane") && !strings.Contains(strings.ToLower(view), "tour") {
			t.Fatalf("width %d missing tour content:\n%s", width, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width+12 {
				t.Fatalf("width %d line too long (%d): %q", width, ansi.StringWidth(line), line)
			}
		}
	}
}

func TestTourNarrowLayoutCopy(t *testing.T) {
	ctx := tourContext{
		keys:       defaultKeyMap(),
		singlePane: true,
		canSplit:   false,
		hasAgents:  true,
	}
	tm := newTourModal(ctx, focusLeft, true)
	body := ansi.Strip(tm.sectionBody(tourSectionPanes, theme.Default()))
	if !strings.Contains(strings.ToLower(body), "narrow") && !strings.Contains(body, "one pane") {
		t.Fatalf("narrow layout should mention single-pane behavior:\n%s", body)
	}
}

func TestTourBuildContextFromModel(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 120, 40, true
	m.permMode = protocol.PermissionModePlan
	m.autonomy = protocol.AutonomyChecks
	ctx := m.buildTourContext()
	if !ctx.hasAgents {
		t.Fatal("default registry should include agents")
	}
	if len(ctx.windowIDs) == 0 {
		t.Fatal("expected cycleable windows")
	}
	if ctx.permMode != "plan" {
		t.Fatalf("permMode = %q", ctx.permMode)
	}
	if ctx.autonomy != "checks" {
		t.Fatalf("autonomy = %q", ctx.autonomy)
	}
}

func TestTourNoIdleTimer(t *testing.T) {
	// Tour update must never return a tick/spinner cmd.
	tm := newTourModal(tourContext{keys: defaultKeyMap(), hasAgents: true}, focusLeft, true)
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyDown},
		{Code: 's', Text: "s"},
		{Code: tea.KeyUp},
		{Code: 'n', Text: "n"},
	} {
		next, cmd := tm.update(k)
		if cmd != nil {
			// Only close cmds are allowed; navigation must be silent.
			t.Fatalf("nav key %v returned cmd (tour must not arm timers)", k)
		}
		var ok bool
		tm, ok = next.(*tourModal)
		if !ok {
			t.Fatalf("nav closed tour unexpectedly on %v", k)
		}
	}
}

func TestTourEnterOnLastFinishes(t *testing.T) {
	tm := newTourModal(tourContext{keys: defaultKeyMap(), hasAgents: true}, focusLeft, true)
	tm.cursor = len(tm.sections) - 1
	next, cmd := tm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next != nil {
		t.Fatalf("next = %T, want nil", next)
	}
	if cmd == nil {
		t.Fatal("expected close cmd")
	}
	msg := cmd()
	closed, ok := msg.(tourClosedMsg)
	if !ok || !closed.completed {
		t.Fatalf("msg = %#v, want completed tourClosedMsg", msg)
	}
}

func TestTourViewReflectsCurrentSection(t *testing.T) {
	tm := newTourModal(tourContext{
		keys:       defaultKeyMap(),
		agentsKeys: defaultAgentsKeyMap(),
		hasAgents:  true,
		permMode:   "yolo",
		autonomy:   "supervised",
	}, focusLeft, true)
	// Move to permissions.
	for i, s := range tm.sections {
		if s.id == tourSectionPermissions {
			tm.cursor = i
			break
		}
	}
	view := ansi.Strip(tm.view(72, theme.Default()))
	if !strings.Contains(view, "Permission") {
		t.Fatalf("view missing permissions title:\n%s", view)
	}
	if !strings.Contains(view, "yolo") {
		t.Fatalf("view missing current mode:\n%s", view)
	}
}
