package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func runPlan(t *testing.T, m Model, command string) Model {
	t.Helper()
	m.composer.SetValue(command)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	return m
}

func plansWin(t *testing.T, m Model) plansWindow {
	t.Helper()
	for _, w := range m.windows.windows {
		if pw, ok := w.(plansWindow); ok {
			return pw
		}
	}
	t.Fatal("plans window missing")
	return plansWindow{}
}

func TestPlanCommandUnavailable(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Plans = nil
	m.windows = configurePlansWindow(m.windows, nil, m.sessionID)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = runPlan(t, m, "/plan")
	if !strings.Contains(m.notice, "unavailable") {
		t.Fatalf("notice = %q, want unavailable", m.notice)
	}
	// Composer cleared; transcript untouched.
	if m.composer.Value() != "" {
		t.Fatalf("composer = %q, want empty", m.composer.Value())
	}
	if len(m.cells) != 0 {
		t.Fatalf("cells = %d, want 0", len(m.cells))
	}
}

func TestPlanCommandCreateListGetApprove(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	store := newFakePlans()
	m.services.Plans = store
	m.windows = configurePlansWindow(m.windows, store, m.sessionID)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})

	m = runPlan(t, m, "/plan create Ship auth")
	if !strings.Contains(m.notice, "created Ship auth") {
		t.Fatalf("create notice = %q", m.notice)
	}
	pw := plansWin(t, m)
	if m.windows.active().id() != plansWindowID {
		t.Fatalf("active = %q, want plans", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	if pw.mode != planModeDetail || pw.plan.Title != "Ship auth" || pw.plan.OwnerRoot != "root-a" {
		t.Fatalf("after create window = mode=%v plan=%+v", pw.mode, pw.plan)
	}
	// Composer reset; no transcript pollution.
	if m.composer.Value() != "" || len(m.cells) != 0 {
		t.Fatalf("composer/cells polluted: %q cells=%d", m.composer.Value(), len(m.cells))
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = runPlan(t, m, "/plan list")
	pw = plansWin(t, m)
	if pw.mode != planModeList || len(pw.items) != 1 {
		t.Fatalf("list mode=%v items=%+v", pw.mode, pw.items)
	}

	m = runPlan(t, m, "/plan get "+pw.items[0].ID)
	pw = plansWin(t, m)
	if pw.mode != planModeDetail || pw.plan.Title != "Ship auth" {
		t.Fatalf("get detail = mode=%v %+v", pw.mode, pw.plan)
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = runPlan(t, m, "/plan approve")
	pw = plansWin(t, m)
	if pw.plan.Status != "approved" {
		t.Fatalf("approve status = %q notice=%q", pw.plan.Status, m.notice)
	}
	if !strings.Contains(m.notice, "approve") {
		t.Fatalf("approve notice = %q", m.notice)
	}
}

func TestPlansWindowActivePlanPerRootNoBleed(t *testing.T) {
	store := newFakePlans(
		host.Plan{
			ID: "p1", OwnerRoot: "root-a", Title: "Plan A", Status: "draft", Version: 1,
			Sections:  []host.PlanSection{{ID: "s1", Title: "A1", Body: "body-a"}},
			UpdatedAt: time.Now(),
		},
		host.Plan{
			ID: "p2", OwnerRoot: "root-b", Title: "Plan B", Status: "draft", Version: 1,
			Sections:  []host.PlanSection{{ID: "s1", Title: "B1", Body: "body-b"}},
			UpdatedAt: time.Now().Add(time.Second),
		},
	)
	w := newPlansWindow().bind(store, "root-a").resize(40, 16).(plansWindow)
	if w.mode != planModeDetail || w.plan.ID != "p1" || w.plan.Title != "Plan A" {
		t.Fatalf("root-a active = mode=%v %+v", w.mode, w.plan)
	}
	// Start editing so root switch must drop the draft.
	next, _ := w.beginEdit(planEditTitle, w.plan.Title, "", planModeDetail)
	w = next
	w.editDraft = "hijacked title"
	if w.mode != planModeEdit {
		t.Fatal("expected edit mode")
	}

	w = w.onRootChange("root-b")
	if w.ownerRoot != "root-b" {
		t.Fatalf("ownerRoot = %q", w.ownerRoot)
	}
	if w.mode != planModeDetail || w.plan.ID != "p2" {
		t.Fatalf("root-b active = mode=%v %+v", w.mode, w.plan)
	}
	if w.editDraft != "" || w.mode == planModeEdit {
		t.Fatalf("edit state bled across roots: draft=%q mode=%v", w.editDraft, w.mode)
	}

	// Switch back: root-a remembers its plan.
	w = w.onRootChange("root-a")
	if w.plan.ID != "p1" || w.plan.Title != "Plan A" {
		t.Fatalf("root-a restore = %+v", w.plan)
	}
}

func TestPlansWindowSectionNavAndEditCAS(t *testing.T) {
	store := newFakePlans(host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Main", Status: "draft", Version: 1,
		Sections: []host.PlanSection{
			{ID: "s1", Title: "One", Body: "first"},
			{ID: "s2", Title: "Two", Body: "second"},
		},
	})
	w := newPlansWindow().bind(store, "root-a").resize(48, 20).(plansWindow)
	if w.mode != planModeDetail {
		t.Fatalf("mode = %v", w.mode)
	}
	// Open first section.
	w, _ = w.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if w.mode != planModeSection {
		t.Fatalf("section mode = %v", w.mode)
	}
	view := ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(view, "One") || !strings.Contains(view, "first") {
		t.Fatalf("section view = %q", view)
	}

	// Edit body.
	w, _ = w.handleKey(tea.KeyPressMsg{Code: 'e'})
	if w.mode != planModeEdit || w.editKind != planEditSectionBody {
		t.Fatalf("edit = mode=%v kind=%v", w.mode, w.editKind)
	}
	w.editDraft = "revised body"
	var cmd tea.Cmd
	w, cmd = w.handleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected mutation cmd")
	}
	msg := cmd()
	if mut, ok := msg.(projectDataMutatedMsg); !ok || mut.kind != "plans" {
		t.Fatalf("msg = %#v", msg)
	}
	if w.mode != planModeSection || w.plan.Sections[0].Body != "revised body" {
		t.Fatalf("after save = mode=%v sections=%+v", w.mode, w.plan.Sections)
	}
	if w.plan.Version != 2 {
		t.Fatalf("version = %d, want 2", w.plan.Version)
	}
}

func TestPlansWindowConflictSurfacesBothVersions(t *testing.T) {
	store := newFakePlans(host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Main", Status: "draft", Version: 1,
		Sections: []host.PlanSection{{ID: "s1", Title: "One", Body: "original"}},
	})
	w := newPlansWindow().bind(store, "root-a").resize(48, 24).(plansWindow)
	next, _ := w.beginEdit(planEditSectionBody, "original", "s1", planModeSection)
	w = next
	w.editDraft = "local edit"

	// Concurrent agent/user write bumps version.
	body := "remote edit"
	if _, err := store.UpdateSection("p1", "root-a", "s1", nil, &body, 1); err != nil {
		t.Fatal(err)
	}

	next, _ = w.commitEdit()
	w = next
	if w.mode != planModeConflict {
		t.Fatalf("mode = %v, want conflict; err=%q", w.mode, w.err)
	}
	view := ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(view, "local edit") || !strings.Contains(view, "remote edit") {
		t.Fatalf("conflict view missing both versions:\n%s", view)
	}
	if !strings.Contains(view, "version conflict") || !strings.Contains(view, "both edits kept") {
		t.Fatalf("conflict view missing banner:\n%s", view)
	}

	// Keep yours resumes edit against latest version.
	w, _ = w.handleKey(tea.KeyPressMsg{Code: 'e'})
	if w.mode != planModeEdit || w.editDraft != "local edit" || w.editVersion != 2 {
		t.Fatalf("keep yours = mode=%v draft=%q ver=%d", w.mode, w.editDraft, w.editVersion)
	}
	w, _ = w.commitEdit()
	if w.mode == planModeConflict {
		t.Fatalf("second save still conflict: %q", w.err)
	}
	got, ok, err := store.Get("p1")
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if got.Sections[0].Body != "local edit" {
		t.Fatalf("body = %q, want local edit", got.Sections[0].Body)
	}
}

func TestPlansWindowConflictTakeTheirs(t *testing.T) {
	store := newFakePlans(host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Main", Status: "draft", Version: 1,
		Sections: []host.PlanSection{{ID: "s1", Title: "One", Body: "original"}},
	})
	w := newPlansWindow().bind(store, "root-a").resize(40, 20).(plansWindow)
	next, _ := w.beginEdit(planEditTitle, "Main", "", planModeDetail)
	w = next
	w.editDraft = "stale title"
	if _, err := store.UpdateTitle("p1", "root-a", "remote title", 1); err != nil {
		t.Fatal(err)
	}
	w, _ = w.commitEdit()
	if w.mode != planModeConflict {
		t.Fatalf("mode = %v", w.mode)
	}
	w, _ = w.handleKey(tea.KeyPressMsg{Code: 't'})
	if w.mode != planModeDetail {
		t.Fatalf("after take theirs mode = %v", w.mode)
	}
	if w.plan.Title != "remote title" {
		t.Fatalf("title = %q", w.plan.Title)
	}
	if w.editDraft != "" {
		t.Fatalf("draft still set: %q", w.editDraft)
	}
}

func TestPlansWindowReadOnlyOtherRoot(t *testing.T) {
	store := newFakePlans(host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Owned", Status: "draft", Version: 1,
		Sections: []host.PlanSection{{ID: "s1", Title: "S", Body: "b"}},
	})
	w := newPlansWindow().bindList(store, "root-b").resize(40, 12).(plansWindow)
	w = w.openPlan("p1")
	if w.canMutate() {
		t.Fatal("non-owner should not mutate")
	}
	w, _ = w.handleKey(tea.KeyPressMsg{Code: 't'})
	if w.mode == planModeEdit {
		t.Fatal("edit should be refused")
	}
	if !strings.Contains(w.err, "read-only") {
		t.Fatalf("err = %q", w.err)
	}
}

func TestPlansWindowNilAndEmpty(t *testing.T) {
	w := newPlansWindow().resize(32, 8).(plansWindow)
	view := ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(view, "unavailable") {
		t.Fatalf("nil store view = %q", view)
	}
	store := newFakePlans()
	w = newPlansWindow().bind(store, "root-a").resize(32, 8).(plansWindow)
	if w.mode != planModeList || len(w.items) != 0 {
		t.Fatalf("empty = mode=%v items=%v", w.mode, w.items)
	}
	view = ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(view, "no plans") {
		t.Fatalf("empty view = %q", view)
	}
}

func TestPlansWindowWidthSafeAtCommonSizes(t *testing.T) {
	store := newFakePlans(host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Wide plan title that should truncate safely",
		Status: "draft", Version: 1,
		Sections: []host.PlanSection{
			{ID: "s1", Title: "Section with a very long title for wrap checks", Body: strings.Repeat("word ", 40)},
		},
	})
	// Pane inner widths approximating 80x24 / 93x40 / 120x40 right columns.
	for _, width := range []int{28, 36, 48, 60} {
		w := newPlansWindow().bind(store, "root-a").resize(width, 16).(plansWindow)
		for _, mode := range []planViewMode{planModeList, planModeDetail, planModeSection} {
			w.mode = mode
			if mode == planModeList {
				w = w.bindList(store, "root-a").resize(width, 16).(plansWindow)
			}
			view := w.view(theme.Default().Resolve())
			for i, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Errorf("width=%d mode=%v line %d width=%d: %q", width, mode, i, got, ansi.Strip(line))
				}
			}
		}
	}
}

func TestSectionDelegateLabel(t *testing.T) {
	if got := sectionDelegateLabel(host.PlanSection{}); got != "" {
		t.Fatalf("empty = %q", got)
	}
	got := sectionDelegateLabel(host.PlanSection{
		DelegateStatus:    "in_flight",
		DelegateChildName: "refiner",
	})
	if got != "delegating → refiner" {
		t.Fatalf("in_flight = %q", got)
	}
	if got := sectionDelegateLabel(host.PlanSection{DelegateStatus: "conflict"}); got != "delegate conflict" {
		t.Fatalf("conflict = %q", got)
	}
}

func TestPlansWindowShowsDelegateProgress(t *testing.T) {
	store := newFakePlans(host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Ship", Status: "draft", Version: 2,
		Sections: []host.PlanSection{
			{ID: "s1", Title: "Research", Body: "look", DelegateStatus: "in_flight", DelegateChildName: "ra"},
			{ID: "s2", Title: "Build", Body: "code", DelegateStatus: "applied"},
		},
	})
	w := newPlansWindow().bind(store, "root-a").resize(48, 16).(plansWindow)
	w.mode = planModeDetail
	view := ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(view, "delegating") {
		t.Fatalf("detail missing delegate progress:\n%s", view)
	}
	w.mode = planModeSection
	w.sectionIdx = 0
	view = ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(view, "delegating → ra") {
		t.Fatalf("section missing delegate badge:\n%s", view)
	}
}

func TestPlansWindowRefreshAfterToolWrite(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	store := newFakePlans(host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Before", Status: "draft", Version: 1,
	})
	m.services.Plans = store
	m.windows = configurePlansWindow(m.windows, store, m.sessionID)
	m = runPlan(t, m, "/plan list")
	if len(plansWin(t, m).items) != 1 {
		t.Fatal("expected one plan")
	}
	if _, err := store.Create("root-a", "After tool", nil); err != nil {
		t.Fatal(err)
	}
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "plan_write", Args: []byte(`{}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Output: "ok"})
	pw := plansWin(t, m)
	if len(pw.items) != 2 {
		t.Fatalf("after tool refresh items = %+v", pw.items)
	}
}

func TestPlansWindowOpenDoesNotCorruptComposerOrTranscript(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.cells = []cell{&userCell{text: "hello"}, &assistantCell{text: "world", complete: true}}
	store := newFakePlans()
	m.services.Plans = store
	m.windows = configurePlansWindow(m.windows, store, m.sessionID)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.composer.SetValue("draft prompt")
	// Focus right via /plan should reset composer (command path) but keep cells.
	m = runPlan(t, m, "/plan")
	if len(m.cells) != 2 {
		t.Fatalf("cells = %d, want 2", len(m.cells))
	}
	if m.composer.Value() != "" {
		t.Fatalf("composer after /plan = %q", m.composer.Value())
	}
	// Mouse/focus back left should not drop transcript.
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	if len(m.cells) != 2 {
		t.Fatalf("cells after focus left = %d", len(m.cells))
	}
}

func TestPlansWindowAddSectionAndStatusKeys(t *testing.T) {
	store := newFakePlans(host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Main", Status: "draft", Version: 1,
	})
	w := newPlansWindow().bind(store, "root-a").resize(40, 12).(plansWindow)
	var cmd tea.Cmd
	w, cmd = w.handleKey(tea.KeyPressMsg{Code: 'n'})
	if cmd == nil {
		t.Fatal("expected add section cmd")
	}
	if len(w.plan.Sections) != 1 {
		t.Fatalf("sections = %+v", w.plan.Sections)
	}
	w, cmd = w.handleKey(tea.KeyPressMsg{Code: 'a'})
	if cmd == nil || w.plan.Status != "approved" {
		t.Fatalf("approve = status=%q cmd=%v", w.plan.Status, cmd != nil)
	}
	w, cmd = w.handleKey(tea.KeyPressMsg{Code: 'c'})
	if cmd == nil || w.plan.Status != "closed" {
		t.Fatalf("close = status=%q", w.plan.Status)
	}
	w, cmd = w.handleKey(tea.KeyPressMsg{Code: 'o'})
	if cmd == nil || w.plan.Status != "draft" {
		t.Fatalf("reopen = status=%q", w.plan.Status)
	}
}

func TestContextStateMsgRebindsPlanOwner(t *testing.T) {
	store := newFakePlans(
		host.Plan{ID: "p1", OwnerRoot: "root-a", Title: "A", Status: "draft", Version: 1, UpdatedAt: time.Now()},
		host.Plan{ID: "p2", OwnerRoot: "root-b", Title: "B", Status: "draft", Version: 1, UpdatedAt: time.Now().Add(time.Second)},
	)
	w := newPlansWindow().bind(store, "root-a").resize(40, 10).(plansWindow)
	if w.plan.ID != "p1" {
		t.Fatalf("start = %s", w.plan.ID)
	}
	updated, _ := w.update(contextStateMsg{SessionID: "root-b"})
	w = updated.(plansWindow)
	if w.ownerRoot != "root-b" || w.plan.ID != "p2" {
		t.Fatalf("after context = owner=%s plan=%s", w.ownerRoot, w.plan.ID)
	}
}
