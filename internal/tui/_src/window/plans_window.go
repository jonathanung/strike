package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const plansWindowID = "plans"

// planViewMode is the plans right-pane navigation depth.
type planViewMode int

const (
	planModeList planViewMode = iota
	planModeDetail
	planModeSection
	planModeEdit
	planModeConflict
)

// planEditKind selects which field is being edited under CAS.
type planEditKind int

const (
	planEditTitle planEditKind = iota
	planEditSectionTitle
	planEditSectionBody
)

// plansWindow is the right-pane browser/editor for root-owned structured plans.
// State is value-oriented (COW). Mutations use the bound ownerRoot + expected
// Version for CAS; conflicts keep both local draft and remote content.
type plansWindow struct {
	store     host.Plans
	ownerRoot string

	items  []host.PlanMeta
	cursor int

	mode       planViewMode
	plan       host.Plan
	sectionIdx int

	editKind      planEditKind
	editDraft     string
	editVersion   int
	editSectionID string
	editReturn    planViewMode // mode after cancel/save

	conflictLocal  string
	conflictRemote string
	conflictLabel  string

	// activeByRoot remembers the last opened plan id per root so /plan and
	// root switches reopen predictably without bleeding edit state.
	activeByRoot map[string]string

	width  int
	height int
	err    string
}

func newPlansWindow() plansWindow {
	return plansWindow{activeByRoot: map[string]string{}}
}

func (w plansWindow) id() string { return plansWindowID }

func (w plansWindow) title() string { return "plans" }

func (w plansWindow) init() tea.Cmd { return nil }

func (w plansWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case projectDataRefreshMsg:
		return w.reloadPreserve(), nil
	case contextStateMsg:
		if msg.SessionID == "" || msg.SessionID == w.ownerRoot {
			return w, nil
		}
		return w.onRootChange(msg.SessionID), nil
	case tea.KeyPressMsg:
		return w.handleKey(msg)
	}
	return w, nil
}

func (w plansWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w plansWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	if w.store == nil {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("plans unavailable"),
		)
	}
	if w.err != "" && w.mode == planModeList && len(w.items) == 0 {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Error.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis)),
		)
	}
	switch w.mode {
	case planModeConflict:
		return w.viewConflict(th)
	case planModeEdit:
		return w.viewEdit(th)
	case planModeSection:
		return w.viewSection(th)
	case planModeDetail:
		return w.viewDetail(th)
	default:
		return w.viewList(th)
	}
}

func (w plansWindow) viewList(th theme.Theme) string {
	th = th.Resolve()
	visible := w.height
	if visible < 1 {
		visible = 0
	}
	items := make([]ui.ListItem, len(w.items))
	for i, meta := range w.items {
		owner := "other"
		if meta.OwnerRoot != "" && meta.OwnerRoot == w.ownerRoot {
			owner = "mine"
		} else if short := shortSessionID(meta.OwnerRoot); short != "" {
			owner = short
		}
		items[i] = ui.ListItem{
			Label: sanitizeDisplayData(meta.Title),
			Detail: detailJoin(th,
				sanitizeDisplayData(meta.Status),
				detailJoin(th, fmt.Sprintf("%d sec", meta.SectionCount), owner),
			),
			Current: meta.OwnerRoot == w.ownerRoot && meta.Status != "closed",
		}
	}
	return ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  w.cursor,
		Width:   w.width,
		Visible: visible,
		Empty:   "no plans",
	})
}

func (w plansWindow) viewDetail(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	if w.plan.ID == "" {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("no plan selected"),
		)
	}
	header := []string{
		st.Accent.Render(sanitizeDisplayData(w.plan.Title)),
		st.Muted.Render(detailJoin(th,
			sanitizeDisplayData(w.plan.Status),
			detailJoin(th, fmt.Sprintf("v%d", w.plan.Version), w.ownerLabel()),
		)),
	}
	if w.err != "" {
		header = append(header, st.Error.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis)))
	}
	headerBlock := wrapToWidth(strings.Join(header, "\n"), w.width)
	headerLines := strings.Count(headerBlock, "\n") + 1
	listH := max(0, w.height-headerLines-1)
	items := make([]ui.ListItem, len(w.plan.Sections))
	for i, sec := range w.plan.Sections {
		detail := sanitizeDisplayData(sec.Body)
		if detail == "" {
			detail = "(empty)"
		}
		if d := sectionDelegateLabel(sec); d != "" {
			detail = detailJoin(th, d, detail)
		}
		items[i] = ui.ListItem{
			Label:  sanitizeDisplayData(sec.Title),
			Detail: detail,
		}
	}
	empty := "no sections"
	if !w.canMutate() {
		empty = "no sections (read-only)"
	}
	listBody := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  w.sectionIdx,
		Width:   w.width,
		Visible: listH,
		Empty:   empty,
	})
	return clampViewHeight(headerBlock+"\n"+listBody, w.height)
}

func (w plansWindow) viewSection(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	sec, ok := w.currentSection()
	if !ok {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("no section selected"),
		)
	}
	lines := []string{
		st.Accent.Render(sanitizeDisplayData(sec.Title)),
		st.Muted.Render(detailJoin(th, sanitizeDisplayData(w.plan.Title), detailJoin(th, fmt.Sprintf("v%d", w.plan.Version), sec.ID))),
	}
	if d := sectionDelegateLabel(sec); d != "" {
		style := st.Muted
		switch sec.DelegateStatus {
		case "in_flight":
			style = st.Accent
		case "failed", "canceled", "malformed", "conflict":
			style = st.Warning
		case "applied":
			style = st.Success
		}
		lines = append(lines, style.Render(d))
		if sec.DelegateDetail != "" {
			lines = append(lines, st.Muted.Render(sanitizeDisplayData(sec.DelegateDetail)))
		}
	}
	if w.err != "" {
		lines = append(lines, st.Error.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis)))
	}
	body := sanitizeDisplayData(sec.Body)
	if body == "" {
		lines = append(lines, st.Muted.Render("(empty body)"))
	} else {
		lines = append(lines, st.Text.Render(body))
	}
	return clampViewHeight(wrapToWidth(strings.Join(lines, "\n"), w.width), w.height)
}

func (w plansWindow) viewEdit(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	label := "title"
	switch w.editKind {
	case planEditSectionTitle:
		label = "section title"
	case planEditSectionBody:
		label = "section body"
	}
	hint := dotJoin(th, "enter save", "esc cancel")
	if w.editKind == planEditSectionBody {
		hint = dotJoin(th, "ctrl+s save", "enter newline", "esc cancel")
	}
	lines := []string{
		st.Muted.Render("edit " + label + " (v" + fmt.Sprint(w.editVersion) + ")"),
		st.Text.Render(sanitizeDisplayData(w.editDraft)) + st.InputCursor.Render(th.Icons.InputCursor),
		st.Muted.Render(hint),
	}
	if w.err != "" {
		lines = append(lines, st.Error.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis)))
	}
	return clampViewHeight(wrapToWidth(strings.Join(lines, "\n"), w.width), w.height)
}

func (w plansWindow) viewConflict(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	lines := []string{
		st.Warning.Render("version conflict - both edits kept"),
		st.Muted.Render(sanitizeDisplayData(w.conflictLabel)),
		st.Accent.Render("yours:"),
		st.Text.Render(sanitizeDisplayData(w.conflictLocal)),
		st.Accent.Render("theirs:"),
		st.Text.Render(sanitizeDisplayData(w.conflictRemote)),
		st.Muted.Render(dotJoin(th, "e keep yours", "t take theirs", "esc back")),
	}
	if w.err != "" {
		lines = append(lines, st.Error.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis)))
	}
	return clampViewHeight(wrapToWidth(strings.Join(lines, "\n"), w.width), w.height)
}

func (w plansWindow) ownerLabel() string {
	if w.plan.OwnerRoot == w.ownerRoot {
		return "mine"
	}
	if short := shortSessionID(w.plan.OwnerRoot); short != "" {
		return short
	}
	return "other"
}

func (w plansWindow) canMutate() bool {
	if w.store == nil || w.ownerRoot == "" || w.plan.ID == "" {
		return false
	}
	if w.plan.OwnerRoot != w.ownerRoot {
		return false
	}
	return w.plan.Status != "closed"
}

func (w plansWindow) currentSection() (host.PlanSection, bool) {
	if w.sectionIdx < 0 || w.sectionIdx >= len(w.plan.Sections) {
		return host.PlanSection{}, false
	}
	return w.plan.Sections[w.sectionIdx], true
}

func (w plansWindow) bind(store host.Plans, ownerRoot string) plansWindow {
	w.store = store
	w.ownerRoot = strings.TrimSpace(ownerRoot)
	if w.activeByRoot == nil {
		w.activeByRoot = map[string]string{}
	}
	w = w.clearEditState()
	w.mode = planModeList
	w.plan = host.Plan{}
	w.sectionIdx = 0
	w = w.reload()
	if id := w.pickActivePlanID(); id != "" {
		w = w.openPlan(id)
	}
	return w
}

// bindList forces list mode (e.g. /plan list) after binding.
func (w plansWindow) bindList(store host.Plans, ownerRoot string) plansWindow {
	w = w.bind(store, ownerRoot)
	w.mode = planModeList
	w.plan = host.Plan{}
	w.sectionIdx = 0
	return w
}

func (w plansWindow) onRootChange(ownerRoot string) plansWindow {
	// Remember current plan for the previous root before switching.
	if w.ownerRoot != "" && w.plan.ID != "" {
		if w.activeByRoot == nil {
			w.activeByRoot = map[string]string{}
		}
		w.activeByRoot[w.ownerRoot] = w.plan.ID
	}
	w.ownerRoot = strings.TrimSpace(ownerRoot)
	w = w.clearEditState()
	w.err = ""
	w = w.reload()
	if id := w.pickActivePlanID(); id != "" {
		return w.openPlan(id)
	}
	w.mode = planModeList
	w.plan = host.Plan{}
	w.sectionIdx = 0
	return w
}

func (w plansWindow) pickActivePlanID() string {
	if w.activeByRoot != nil {
		if id := w.activeByRoot[w.ownerRoot]; id != "" {
			for _, meta := range w.items {
				if meta.ID == id {
					return id
				}
			}
		}
	}
	// Newest-first index: first non-closed plan owned by this root.
	for _, meta := range w.items {
		if meta.OwnerRoot == w.ownerRoot && meta.Status != "closed" {
			return meta.ID
		}
	}
	return ""
}

func (w plansWindow) rememberActive() plansWindow {
	if w.ownerRoot == "" || w.plan.ID == "" {
		return w
	}
	if w.activeByRoot == nil {
		w.activeByRoot = map[string]string{}
	}
	w.activeByRoot[w.ownerRoot] = w.plan.ID
	return w
}

func (w plansWindow) reload() plansWindow {
	if w.store == nil {
		w.items = nil
		w.err = ""
		w.cursor = 0
		return w
	}
	items, err := w.store.List()
	if err != nil {
		w.err = err.Error()
		w.items = nil
		w.cursor = 0
		return w
	}
	w.err = ""
	w.items = append([]host.PlanMeta(nil), items...)
	if len(w.items) == 0 {
		w.cursor = 0
	} else if w.cursor >= len(w.items) {
		w.cursor = len(w.items) - 1
	} else if w.cursor < 0 {
		w.cursor = 0
	}
	return w
}

// reloadPreserve refreshes the index and, when a plan is open, reloads it
// without dropping navigation (used after agent tool writes).
func (w plansWindow) reloadPreserve() plansWindow {
	if w.mode == planModeEdit || w.mode == planModeConflict {
		// Do not clobber an in-progress local edit; list still refreshes.
		w = w.reload()
		return w
	}
	id := w.plan.ID
	secID := ""
	if sec, ok := w.currentSection(); ok {
		secID = sec.ID
	}
	mode := w.mode
	w = w.reload()
	if id == "" || mode == planModeList {
		return w
	}
	w = w.openPlan(id)
	if !w.planLoaded(id) {
		w.mode = planModeList
		return w
	}
	if mode == planModeSection || mode == planModeDetail {
		if secID != "" {
			for i, s := range w.plan.Sections {
				if s.ID == secID {
					w.sectionIdx = i
					break
				}
			}
		}
		w.mode = mode
		if mode == planModeSection && (w.sectionIdx < 0 || w.sectionIdx >= len(w.plan.Sections)) {
			w.mode = planModeDetail
		}
	}
	return w
}

func (w plansWindow) planLoaded(id string) bool {
	return w.plan.ID != "" && w.plan.ID == id
}

func (w plansWindow) openPlan(id string) plansWindow {
	id = strings.TrimSpace(id)
	if w.store == nil || id == "" {
		return w
	}
	p, ok, err := w.store.Get(id)
	if err != nil {
		w.err = err.Error()
		return w
	}
	if !ok {
		w.err = "plan not found"
		w.mode = planModeList
		w.plan = host.Plan{}
		return w
	}
	w.err = ""
	w.plan = p
	w.sectionIdx = 0
	w.mode = planModeDetail
	w = w.rememberActive()
	// Sync list cursor to this plan when present.
	for i, meta := range w.items {
		if meta.ID == id {
			w.cursor = i
			break
		}
	}
	return w
}

func (w plansWindow) openSelected() plansWindow {
	if w.cursor < 0 || w.cursor >= len(w.items) {
		return w
	}
	return w.openPlan(w.items[w.cursor].ID)
}

func (w plansWindow) clearEditState() plansWindow {
	w.editDraft = ""
	w.editSectionID = ""
	w.editVersion = 0
	w.conflictLocal = ""
	w.conflictRemote = ""
	w.conflictLabel = ""
	if w.mode == planModeEdit || w.mode == planModeConflict {
		w.mode = planModeDetail
	}
	return w
}

func (w plansWindow) handleKey(msg tea.KeyPressMsg) (plansWindow, tea.Cmd) {
	if w.store == nil {
		return w, nil
	}
	switch w.mode {
	case planModeConflict:
		return w.handleConflictKey(msg)
	case planModeEdit:
		return w.handleEditKey(msg)
	case planModeSection:
		return w.handleSectionKey(msg)
	case planModeDetail:
		return w.handleDetailKey(msg)
	default:
		return w.handleListKey(msg)
	}
}

func (w plansWindow) handleListKey(msg tea.KeyPressMsg) (plansWindow, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if w.cursor > 0 {
			w.cursor--
		}
	case "down", "j":
		if w.cursor < len(w.items)-1 {
			w.cursor++
		}
	case "enter", "right", "l":
		w = w.openSelected()
	case "r":
		w = w.reload()
	}
	return w, nil
}

func (w plansWindow) handleDetailKey(msg tea.KeyPressMsg) (plansWindow, tea.Cmd) {
	switch msg.String() {
	case "esc", "left", "h", "backspace":
		w.mode = planModeList
		w.err = ""
		return w, nil
	case "up", "k":
		if w.sectionIdx > 0 {
			w.sectionIdx--
		}
	case "down", "j":
		if w.sectionIdx < len(w.plan.Sections)-1 {
			w.sectionIdx++
		}
	case "enter", "right", "l":
		if len(w.plan.Sections) > 0 && w.sectionIdx >= 0 && w.sectionIdx < len(w.plan.Sections) {
			w.mode = planModeSection
			w.err = ""
		}
	case "r":
		if w.plan.ID != "" {
			w = w.openPlan(w.plan.ID)
		} else {
			w = w.reload()
		}
	case "t":
		if w.canMutate() {
			return w.beginEdit(planEditTitle, w.plan.Title, "", planModeDetail)
		}
		w.err = "read-only: not the owning root or plan is closed"
	case "e":
		if sec, ok := w.currentSection(); ok && w.canMutate() {
			return w.beginEdit(planEditSectionTitle, sec.Title, sec.ID, planModeDetail)
		}
		if !w.canMutate() {
			w.err = "read-only: not the owning root or plan is closed"
		}
	case "a":
		return w.setStatus("approved")
	case "c":
		return w.setStatus("closed")
	case "o":
		return w.reopenPlan()
	case "n":
		return w.addSection()
	}
	return w, nil
}

func (w plansWindow) handleSectionKey(msg tea.KeyPressMsg) (plansWindow, tea.Cmd) {
	switch msg.String() {
	case "esc", "left", "h", "backspace":
		w.mode = planModeDetail
		w.err = ""
		return w, nil
	case "up", "k":
		if w.sectionIdx > 0 {
			w.sectionIdx--
		}
	case "down", "j":
		if w.sectionIdx < len(w.plan.Sections)-1 {
			w.sectionIdx++
		}
	case "r":
		id := w.plan.ID
		secID := ""
		if sec, ok := w.currentSection(); ok {
			secID = sec.ID
		}
		w = w.openPlan(id)
		if secID != "" {
			for i, s := range w.plan.Sections {
				if s.ID == secID {
					w.sectionIdx = i
					w.mode = planModeSection
					break
				}
			}
		}
	case "t", "e":
		if sec, ok := w.currentSection(); ok && w.canMutate() {
			kind := planEditSectionBody
			initial := sec.Body
			if msg.String() == "t" {
				kind = planEditSectionTitle
				initial = sec.Title
			}
			return w.beginEdit(kind, initial, sec.ID, planModeSection)
		}
		if !w.canMutate() {
			w.err = "read-only: not the owning root or plan is closed"
		}
	case "a":
		return w.setStatus("approved")
	case "c":
		return w.setStatus("closed")
	case "o":
		return w.reopenPlan()
	}
	return w, nil
}

func (w plansWindow) handleEditKey(msg tea.KeyPressMsg) (plansWindow, tea.Cmd) {
	switch msg.String() {
	case "esc":
		ret := w.editReturn
		w = w.clearEditState()
		w.mode = ret
		w.err = ""
		return w, nil
	case "ctrl+s":
		return w.commitEdit()
	case "enter":
		if w.editKind == planEditSectionBody {
			w.editDraft += "\n"
			w.err = ""
			return w, nil
		}
		return w.commitEdit()
	case "backspace", "ctrl+h":
		if w.editDraft != "" {
			_, size := utf8.DecodeLastRuneInString(w.editDraft)
			w.editDraft = w.editDraft[:len(w.editDraft)-size]
		}
		w.err = ""
		return w, nil
	}
	if msg.Text != "" {
		w.editDraft += msg.Text
		w.err = ""
	}
	return w, nil
}

func (w plansWindow) handleConflictKey(msg tea.KeyPressMsg) (plansWindow, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		ret := w.editReturn
		if ret == 0 && w.editSectionID != "" {
			ret = planModeSection
		} else if ret == 0 {
			ret = planModeDetail
		}
		local := w.conflictLocal
		kind := w.editKind
		secID := w.editSectionID
		w = w.clearEditState()
		w.mode = ret
		// Preserve draft only if user re-enters edit; just leave conflict.
		_ = local
		_ = kind
		_ = secID
		return w, nil
	case "e":
		// Keep yours: resume edit with local draft against latest remote version.
		w.editDraft = w.conflictLocal
		w.editVersion = w.plan.Version
		w.mode = planModeEdit
		w.err = ""
		w.conflictLocal = ""
		w.conflictRemote = ""
		w.conflictLabel = ""
		return w, nil
	case "t":
		// Take theirs: discard local, stay on refreshed remote content.
		ret := w.editReturn
		if ret == 0 {
			ret = planModeDetail
		}
		w = w.clearEditState()
		w.mode = ret
		w.err = ""
		return w, nil
	}
	return w, nil
}

func (w plansWindow) beginEdit(kind planEditKind, initial, sectionID string, ret planViewMode) (plansWindow, tea.Cmd) {
	w.editKind = kind
	w.editDraft = initial
	w.editVersion = w.plan.Version
	w.editSectionID = sectionID
	w.editReturn = ret
	w.mode = planModeEdit
	w.err = ""
	return w, nil
}

func (w plansWindow) commitEdit() (plansWindow, tea.Cmd) {
	if w.store == nil || w.ownerRoot == "" || w.plan.ID == "" {
		w.err = "cannot save"
		return w, nil
	}
	if w.plan.OwnerRoot != w.ownerRoot {
		w.err = "only the owning root may mutate this plan"
		return w, nil
	}
	if w.plan.Status == "closed" {
		w.err = "plan is closed; reopen before editing"
		return w, nil
	}
	var (
		updated host.Plan
		err     error
	)
	switch w.editKind {
	case planEditTitle:
		title := strings.TrimSpace(w.editDraft)
		if title == "" {
			w.err = "title is required"
			return w, nil
		}
		updated, err = w.store.UpdateTitle(w.plan.ID, w.ownerRoot, title, w.editVersion)
	case planEditSectionTitle:
		title := strings.TrimSpace(w.editDraft)
		if title == "" {
			w.err = "section title is required"
			return w, nil
		}
		updated, err = w.store.UpdateSection(w.plan.ID, w.ownerRoot, w.editSectionID, &title, nil, w.editVersion)
	case planEditSectionBody:
		body := w.editDraft
		updated, err = w.store.UpdateSection(w.plan.ID, w.ownerRoot, w.editSectionID, nil, &body, w.editVersion)
	default:
		w.err = "unknown edit"
		return w, nil
	}
	if err != nil {
		if isPlanConflict(err) {
			return w.enterConflict(err)
		}
		w.err = err.Error()
		return w, nil
	}
	ret := w.editReturn
	secID := w.editSectionID
	w = w.clearEditState()
	w.plan = updated
	w.mode = ret
	w.err = ""
	w = w.rememberActive()
	w = w.reload()
	// Restore section cursor after reload of plan fields.
	if secID != "" {
		for i, s := range w.plan.Sections {
			if s.ID == secID {
				w.sectionIdx = i
				break
			}
		}
	}
	notice := "plans: saved " + updated.Title
	return w, func() tea.Msg {
		return projectDataMutatedMsg{kind: "plans", notice: notice}
	}
}

func (w plansWindow) enterConflict(err error) (plansWindow, tea.Cmd) {
	// Reload remote and surface both versions.
	remote, ok, getErr := w.store.Get(w.plan.ID)
	if getErr != nil {
		w.err = err.Error() + "; reload failed: " + getErr.Error()
		return w, nil
	}
	if !ok {
		w.err = err.Error() + "; plan disappeared"
		return w, nil
	}
	w.plan = remote
	w.conflictLocal = w.editDraft
	w.conflictLabel = "title"
	remoteVal := remote.Title
	switch w.editKind {
	case planEditSectionTitle:
		w.conflictLabel = "section title"
		remoteVal = ""
		for _, s := range remote.Sections {
			if s.ID == w.editSectionID {
				remoteVal = s.Title
				break
			}
		}
	case planEditSectionBody:
		w.conflictLabel = "section body"
		remoteVal = ""
		for _, s := range remote.Sections {
			if s.ID == w.editSectionID {
				remoteVal = s.Body
				break
			}
		}
	}
	if remoteVal == "" {
		remoteVal = "(empty)"
	}
	if w.conflictLocal == "" {
		w.conflictLocal = "(empty)"
	}
	w.conflictRemote = remoteVal
	w.mode = planModeConflict
	w.err = ""
	return w, nil
}

func (w plansWindow) setStatus(status string) (plansWindow, tea.Cmd) {
	if w.plan.ID == "" || w.store == nil {
		return w, nil
	}
	if w.plan.OwnerRoot != w.ownerRoot {
		w.err = "only the owning root may mutate this plan"
		return w, nil
	}
	updated, err := w.store.SetStatus(w.plan.ID, w.ownerRoot, status, w.plan.Version)
	if err != nil {
		if isPlanConflict(err) {
			w = w.openPlan(w.plan.ID)
			w.err = "version conflict; reloaded, retry status change"
			return w, nil
		}
		w.err = err.Error()
		return w, nil
	}
	w.plan = updated
	w.err = ""
	w = w.rememberActive()
	w = w.reload()
	return w, func() tea.Msg {
		return projectDataMutatedMsg{
			kind:   "plans",
			notice: fmt.Sprintf("plans: %s %s", status, updated.Title),
		}
	}
}

func (w plansWindow) reopenPlan() (plansWindow, tea.Cmd) {
	if w.plan.ID == "" || w.store == nil {
		return w, nil
	}
	if w.plan.OwnerRoot != w.ownerRoot {
		w.err = "only the owning root may mutate this plan"
		return w, nil
	}
	updated, err := w.store.Reopen(w.plan.ID, w.ownerRoot, w.plan.Version)
	if err != nil {
		if isPlanConflict(err) {
			w = w.openPlan(w.plan.ID)
			w.err = "version conflict; reloaded, retry reopen"
			return w, nil
		}
		w.err = err.Error()
		return w, nil
	}
	w.plan = updated
	w.err = ""
	w = w.rememberActive()
	w = w.reload()
	return w, func() tea.Msg {
		return projectDataMutatedMsg{
			kind:   "plans",
			notice: "plans: reopened " + updated.Title,
		}
	}
}

func (w plansWindow) addSection() (plansWindow, tea.Cmd) {
	if !w.canMutate() {
		w.err = "read-only: not the owning root or plan is closed"
		return w, nil
	}
	title := fmt.Sprintf("Section %d", len(w.plan.Sections)+1)
	updated, err := w.store.AddSection(w.plan.ID, w.ownerRoot, title, "", w.plan.Version)
	if err != nil {
		if isPlanConflict(err) {
			w = w.openPlan(w.plan.ID)
			w.err = "version conflict; reloaded, retry add section"
			return w, nil
		}
		w.err = err.Error()
		return w, nil
	}
	w.plan = updated
	w.sectionIdx = len(w.plan.Sections) - 1
	if w.sectionIdx < 0 {
		w.sectionIdx = 0
	}
	w.err = ""
	w = w.rememberActive()
	w = w.reload()
	return w, func() tea.Msg {
		return projectDataMutatedMsg{
			kind:   "plans",
			notice: "plans: added section to " + updated.Title,
		}
	}
}

func isPlanConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "version conflict")
}

// sectionDelegateLabel is a short plan-progress badge for section list/detail.
// Generic agent/activity panes remain the live subagent focus surface.
func sectionDelegateLabel(sec host.PlanSection) string {
	st := strings.TrimSpace(sec.DelegateStatus)
	if st == "" {
		return ""
	}
	who := strings.TrimSpace(sec.DelegateChildName)
	if who == "" {
		who = shortSessionID(sec.DelegateChildID)
	}
	switch st {
	case "in_flight":
		if who != "" {
			return "delegating → " + who
		}
		return "delegating"
	case "applied":
		return "delegate applied"
	case "failed":
		return "delegate failed"
	case "canceled":
		return "delegate canceled"
	case "conflict":
		return "delegate conflict"
	case "malformed":
		return "delegate malformed"
	default:
		return "delegate " + st
	}
}

func clampViewHeight(body string, height int) string {
	if height <= 0 {
		return body
	}
	parts := strings.Split(body, "\n")
	if len(parts) > height {
		parts = parts[:height]
		return strings.Join(parts, "\n")
	}
	return body
}

// configurePlansWindow binds host.Plans and the active root onto the plans slot.
func configurePlansWindow(r windowRegistry, store host.Plans, ownerRoot string) windowRegistry {
	for i, w := range r.windows {
		pw, ok := w.(plansWindow)
		if !ok {
			continue
		}
		next := pw.bind(store, ownerRoot)
		// Initial configure should not auto-open detail until /plan — keep list
		// empty-state friendly on startup. bind() opens active plan; for cold
		// start with no plans that is a no-op. With existing plans, prefer list
		// until the user opens the browser so the right pane stays quiet.
		next.mode = planModeList
		next.plan = host.Plan{}
		next.sectionIdx = 0
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r
	}
	return r
}
