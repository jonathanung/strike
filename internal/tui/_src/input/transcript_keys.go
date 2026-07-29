package tui

import (
	"encoding/binary"
	"hash/fnv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// viewportCacheItem is one displayed cell's cached render outputs.
type viewportCacheItem struct {
	c      cell
	fp     uint64
	raw    string
	linked string
	plain  string
}

// viewportCache avoids re-rendering completed transcript cells on each
// refreshViewport call. Invalidated by width, theme, or workDir changes;
// per-cell fingerprints catch content/selection/expand/flash updates.
type viewportCache struct {
	width   int
	themeID string
	workDir string
	items   []viewportCacheItem
	// cellRenders / cellHits are stats for the most recent refreshViewport.
	cellRenders int
	cellHits    int
}

// lastCell returns the trailing cell if it has type T; a new message part
// starts a new cell otherwise.
func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	width := max(1, m.viewport.Width)
	cells := m.displayCells()
	if len(cells) == 0 {
		m.viewport.SetContent("")
		m.viewport.GotoTop()
		m.transcriptPlainLines = nil
		m.selectedFileRef = -1
		m.vpCache = viewportCache{}
		return
	}
	m.syncToolSelectionFlags()
	// Stick to bottom only when already anchored; otherwise preserve scroll
	// so users reading history are not yanked down on each event.
	atBottom := m.viewport.AtBottom()
	yOff := m.viewport.YOffset

	globalOK := m.vpCache.width == width &&
		m.vpCache.themeID == m.themeID &&
		m.vpCache.workDir == m.workDir
	oldByPtr := map[any]viewportCacheItem{}
	if globalOK {
		for _, it := range m.vpCache.items {
			oldByPtr[it.c] = it
		}
	}

	items := make([]viewportCacheItem, 0, len(cells))
	blocks := make([]string, 0, len(cells)+1)
	plains := make([]string, 0, len(cells)+1)
	renders, hits := 0, 0
	for _, c := range cells {
		if _, ok := c.(*reasoningCell); ok && !m.showThinking {
			continue
		}
		fp := cellRenderFingerprint(c)
		if old, ok := oldByPtr[c]; ok && old.fp == fp {
			items = append(items, old)
			blocks = append(blocks, old.linked)
			plains = append(plains, old.plain)
			hits++
			continue
		}
		raw := m.renderCell(c, width)
		linked := postLinkifyRendered(raw, m.th, m.workDir)
		plain := ansi.Strip(raw)
		items = append(items, viewportCacheItem{c: c, fp: fp, raw: raw, linked: linked, plain: plain})
		blocks = append(blocks, linked)
		plains = append(plains, plain)
		renders++
	}
	// Live working chrome in the transcript when a turn is running and no
	// assistant/tool content has arrived yet (providers with no CoT stream).
	if m.turnRunning && !m.viewingChild() {
		if thinkingPlaceholderVisible(cells, m.showThinking) {
			ph := renderThinkingPlaceholder(width, m.th, m.turnStartedAt)
			blocks = append(blocks, ph)
			plains = append(plains, ansi.Strip(ph))
		}
	}
	content := strings.Join(blocks, "\n\n")
	m.transcriptPlainLines = joinBlockPlainLines(plains)
	if m.selectedFileRef >= len(m.collectFileRefs()) {
		m.selectedFileRef = -1
	}
	m.viewport.SetContent(content)
	if atBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.SetYOffset(yOff)
	}
	m.vpCache = viewportCache{
		width:       width,
		themeID:     m.themeID,
		workDir:     m.workDir,
		items:       items,
		cellRenders: renders,
		cellHits:    hits,
	}
}

// joinBlockPlainLines rebuilds the transcript plain-line map from per-block
// strips, matching strings.Split(ansi.Strip(strings.Join(blocks, "\n\n")), "\n").
func joinBlockPlainLines(plains []string) []string {
	if len(plains) == 0 {
		return nil
	}
	var lines []string
	for i, p := range plains {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Split(p, "\n")...)
	}
	return lines
}

// cellRenderFingerprint hashes render-relevant cell state for cache keys.
func cellRenderFingerprint(c cell) uint64 {
	h := fnv.New64a()
	writeStr := func(s string) {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(s)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(s))
	}
	writeByte := func(b byte) { _, _ = h.Write([]byte{b}) }
	writeBool := func(v bool) {
		if v {
			writeByte(1)
		} else {
			writeByte(0)
		}
	}
	writeU64 := func(v uint64) {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], v)
		_, _ = h.Write(n[:])
	}
	switch tc := c.(type) {
	case *userCell:
		writeByte('u')
		writeBool(tc.copiedFlash)
		writeStr(tc.text)
	case *assistantCell:
		writeByte('a')
		writeBool(tc.complete)
		writeBool(tc.copiedFlash)
		writeStr(tc.text)
	case *toolCell:
		writeByte('t')
		writeStr(tc.callID)
		writeStr(tc.name)
		writeStr(string(tc.args))
		writeStr(tc.title)
		writeStr(tc.output)
		writeStr(string(tc.metadata))
		writeBool(tc.done)
		writeBool(tc.isError)
		writeBool(tc.expanded)
		writeBool(tc.selected)
		writeBool(tc.copiedFlash)
	case *exploreCell:
		writeByte('e')
		writeBool(tc.accepting)
		writeBool(tc.expanded)
		writeBool(tc.selected)
		writeBool(tc.copiedFlash)
		for _, call := range tc.calls {
			if call == nil {
				writeByte(0)
				continue
			}
			writeU64(cellRenderFingerprint(call))
		}
	case *reasoningCell:
		writeByte('r')
		writeStr(tc.text)
	case *infoCell:
		writeByte('i')
		writeStr(tc.text)
	case *errorCell:
		writeByte('E')
		writeStr(tc.text)
	case *subagentResultCell:
		writeByte('s')
		writeStr(tc.sessionID)
		writeStr(tc.agent)
		writeStr(tc.status)
		writeStr(tc.summary)
		writeU64(uint64(tc.elapsed))
		writeBool(tc.expanded)
		writeBool(tc.selected)
		writeBool(tc.copiedFlash)
	default:
		// Unknown cell types always miss the cache.
		writeByte('?')
		writeU64(uint64(time.Now().UnixNano()))
	}
	return h.Sum64()
}

// renderCell paints one transcript cell, attaching OSC 8 file links using the
// session work directory as the relative-path base.
func (m *Model) renderCell(c cell, width int) string {
	switch tc := c.(type) {
	case *toolCell:
		return tc.renderLinked(width, m.th, m.workDir)
	case *exploreCell:
		return tc.renderLinked(width, m.th, m.workDir)
	default:
		return c.render(width, m.th)
	}
}

// handleToolCellKeys handles tool selection (alt+[/]), expand/open-at-line
// (alt+enter), copy (y), post-edit review (v), and apply patch (a) when the
// composer is empty. Bare enter is send-only and never expands (#421).
// alt+enter matches both ToolExpand and Newline; with empty composer this
// path wins and expands, otherwise Newline inserts. handled is true when the
// key was consumed; cmd may launch the editor, open a confirm modal, or clear
// a copied flash.
func (m *Model) handleToolCellKeys(msg tea.KeyMsg) (handled bool, cmd tea.Cmd) {
	if m.focus != focusLeft || m.modal != nil || m.completion != nil {
		return false, nil
	}
	if strings.TrimSpace(m.composer.Value()) != "" {
		return false, nil
	}
	switch {
	case key.Matches(msg, m.keyMap.ToolPrev):
		m.moveToolSelection(-1)
		return true, nil
	case key.Matches(msg, m.keyMap.ToolNext):
		m.moveToolSelection(1)
		return true, nil
	case key.Matches(msg, m.keyMap.ToolExpand):
		if m.toggleSelectedTool() {
			return true, nil
		}
		if ref, ok := m.fileRefForEnter(); ok {
			next, c := m.openFileRef(ref)
			*m = next.(Model)
			return true, c
		}
		return false, nil
	case key.Matches(msg, m.keyMap.ToolCopy):
		return m.copySelectedCell()
	case key.Matches(msg, m.keyMap.ToolReview):
		return m.reviewSelectedTool()
	case key.Matches(msg, m.keyMap.ToolApply):
		return m.applySelectedTool()
	}
	return false, nil
}

func (m *Model) selectableCellIndexes() []int {
	var idx []int
	for i, c := range m.displayCells() {
		switch tc := c.(type) {
		case *toolCell:
			if tc.collapsible() || tc.reviewable() || tc.applyable() {
				idx = append(idx, i)
			}
		case *exploreCell:
			if tc.collapsible() {
				idx = append(idx, i)
			}
		case *subagentResultCell:
			if tc.collapsible() {
				idx = append(idx, i)
			}
		}
	}
	return idx
}

func (m *Model) moveToolSelection(delta int) {
	idxs := m.selectableCellIndexes()
	if len(idxs) == 0 {
		m.selectedCell = -1
		return
	}
	cur := -1
	for i, cellIdx := range idxs {
		if cellIdx == m.selectedCell {
			cur = i
			break
		}
	}
	if cur < 0 {
		if delta < 0 {
			m.selectedCell = idxs[len(idxs)-1]
		} else {
			m.selectedCell = idxs[0]
		}
		return
	}
	next := cur + delta
	if next < 0 {
		next = 0
	}
	if next >= len(idxs) {
		next = len(idxs) - 1
	}
	m.selectedCell = idxs[next]
}

func (m *Model) toggleSelectedTool() bool {
	cells := m.displayCells()
	// Expand only applies to collapsible cells; keep selection among those.
	var idxs []int
	for i, c := range cells {
		switch tc := c.(type) {
		case *toolCell:
			if tc.collapsible() {
				idxs = append(idxs, i)
			}
		case *exploreCell:
			if tc.collapsible() {
				idxs = append(idxs, i)
			}
		case *subagentResultCell:
			if tc.collapsible() {
				idxs = append(idxs, i)
			}
		}
	}
	if len(idxs) == 0 {
		return false
	}
	if m.selectedCell < 0 || m.selectedCell >= len(cells) {
		m.selectedCell = idxs[len(idxs)-1]
	} else {
		// Ensure current selection is still collapsible; else jump to last.
		ok := false
		for _, i := range idxs {
			if i == m.selectedCell {
				ok = true
				break
			}
		}
		if !ok {
			m.selectedCell = idxs[len(idxs)-1]
		}
	}
	switch c := cells[m.selectedCell].(type) {
	case *toolCell:
		return c.toggleExpanded()
	case *exploreCell:
		return c.toggleExpanded()
	case *subagentResultCell:
		return c.toggleExpanded()
	}
	return false
}

// reviewSelectedTool opens the selected file-mutating tool's path at the first
// changed hunk. Does not consume "v" when nothing is selected so typing still
// reaches the empty composer.
func (m *Model) reviewSelectedTool() (bool, tea.Cmd) {
	cells := m.displayCells()
	if m.selectedCell < 0 || m.selectedCell >= len(cells) {
		return false, nil
	}
	tc, ok := cells[m.selectedCell].(*toolCell)
	if !ok {
		m.setNotice("select an edit tool cell to review", true)
		return true, nil
	}
	path, line, ok := tc.reviewTarget(m.workDir)
	if !ok {
		m.setNotice("no file to review on this tool", true)
		return true, nil
	}
	if line < 1 {
		line = 1
	}
	updated, cmd := (*m).openFileRef(fileRef{Path: path, Line: line})
	*m = updated.(Model)
	return true, cmd
}

// applySelectedTool opens a confirm modal to write the selected tool's shown
// patch into the active worktree. Does not consume "a" when nothing is
// selected so typing still reaches the empty composer.
func (m *Model) applySelectedTool() (bool, tea.Cmd) {
	cells := m.displayCells()
	if m.selectedCell < 0 || m.selectedCell >= len(cells) {
		return false, nil
	}
	tc, ok := cells[m.selectedCell].(*toolCell)
	if !ok || !tc.applyable() {
		m.setNotice("select an edit/patch tool cell to apply", true)
		return true, nil
	}
	if m.services.Files == nil {
		m.setNotice("file apply unavailable", true)
		return true, nil
	}
	if path, oldS, newS, replaceAll, ok := tc.editApplyRequest(); ok {
		m.modal = newApplyDiffModalEdit(m.services.Files, path, oldS, newS, replaceAll)
		m.reflow()
		return true, nil
	}
	if patch := patchTextFromArgs(tc.args); patch != "" {
		m.modal = newApplyDiffModalPatch(m.services.Files, patch)
		m.reflow()
		return true, nil
	}
	m.setNotice("no patch to apply on this tool", true)
	return true, nil
}

// applyApplyDiffResult handles the confirm-modal outcome for worktree apply.
func (m *Model) applyApplyDiffResult(msg applyDiffResultMsg) tea.Cmd {
	m.modal = nil
	promote := m.afterModalClosed()
	switch {
	case msg.canceled:
		m.setNotice("apply canceled", false)
	case msg.err != "":
		m.setNotice("apply failed: "+msg.err, true)
	case msg.already:
		m.setNotice("already applied: "+msg.path, false)
	case msg.multi:
		if msg.summary != "" {
			m.setNotice("applied: "+msg.summary, false)
		} else {
			m.setNotice("applied patch", false)
		}
	default:
		label := msg.path
		if msg.count > 1 {
			label += " (" + itoa(msg.count) + " replacements)"
		}
		m.setNotice("applied "+label, false)
	}
	m.reflow()
	return promote
}

func (m *Model) syncToolSelectionFlags() {
	for i, c := range m.displayCells() {
		sel := i == m.selectedCell
		switch tc := c.(type) {
		case *toolCell:
			tc.selected = sel
		case *exploreCell:
			tc.selected = sel
		case *subagentResultCell:
			tc.selected = sel
		}
	}
}

// collectFileRefs returns path:line citations in transcript order.
func (m Model) collectFileRefs() []fileRef {
	var refs []fileRef
	for _, line := range m.transcriptPlainLines {
		for _, sp := range findFileRefSpans(line) {
			refs = append(refs, sp.fileRef)
		}
	}
	return refs
}

// fileRefForEnter picks the click-selected citation, else the most recent one.
func (m Model) fileRefForEnter() (fileRef, bool) {
	refs := m.collectFileRefs()
	if len(refs) == 0 {
		return fileRef{}, false
	}
	if m.selectedFileRef >= 0 && m.selectedFileRef < len(refs) {
		return refs[m.selectedFileRef], true
	}
	return refs[len(refs)-1], true
}

// openFileRef launches the configured editor at path:line via /vim plumbing.
func (m Model) openFileRef(ref fileRef) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(ref.Path) == "" {
		return m, nil
	}
	args := []string{ref.Path}
	if ref.Line > 0 {
		args = []string{ref.Path + ":" + itoa(ref.Line)}
	}
	// Remember selection so a subsequent empty enter re-opens the same ref.
	refs := m.collectFileRefs()
	m.selectedFileRef = -1
	for i, r := range refs {
		if r.Path == ref.Path && r.Line == ref.Line {
			m.selectedFileRef = i
			break
		}
	}
	return m.handleVimCommand(args)
}

// fileRefAtMouse maps a left-click in the transcript viewport to a path:line.
func (m Model) fileRefAtMouse(msg tea.MouseMsg) (fileRef, bool) {
	if m.modal != nil || len(m.transcriptPlainLines) == 0 {
		return fileRef{}, false
	}
	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		return fileRef{}, false
	}
	relY := msg.Y - oy
	relX := msg.X - ox
	if relY < 0 || relX < 0 || relY >= m.viewport.Height {
		return fileRef{}, false
	}
	lineIdx := m.viewport.YOffset + relY
	if lineIdx < 0 || lineIdx >= len(m.transcriptPlainLines) {
		return fileRef{}, false
	}
	return fileRefAtColumn(m.transcriptPlainLines[lineIdx], relX)
}

// transcriptContentOrigin is the top-left cell of the transcript viewport body
// in screen coordinates (after header and panel chrome).
func (m Model) transcriptContentOrigin() (x, y int, ok bool) {
	if len(m.displayCells()) == 0 {
		return 0, 0, false
	}
	r, ok := m.transcriptContentRect()
	if !ok {
		return 0, 0, false
	}
	return r.X, r.Y, true
}

// copySelectedCell stages OSC52 for the selected (or latest copyable)
// transcript cell and starts a brief "copied" flash. Returns false when
// nothing was copyable so bare y can fall through to the composer.
func (m *Model) copySelectedCell() (bool, tea.Cmd) {
	cells := m.displayCells()
	idx := m.resolveCopyCellIndex()
	if idx < 0 {
		return false, nil
	}
	text := cellCopyText(cells[idx])
	if text == "" {
		return false, nil
	}
	// Only collapsible/reviewable tool rows keep a sticky selection; chat
	// cells are copy targets without changing tool-nav selection.
	switch cells[idx].(type) {
	case *toolCell, *exploreCell, *subagentResultCell:
		m.selectedCell = idx
	}
	m.cellClip.stage(text)
	m.copyFlashGen++
	gen := m.copyFlashGen
	setCellCopiedFlash(cells[idx], true)
	// Clear any other cell flashes so only the copied row shows feedback.
	for i, c := range cells {
		if i == idx {
			continue
		}
		clearCellCopiedFlash(c)
	}
	return true, tea.Tick(cellCopiedFlash, func(time.Time) tea.Msg {
		return clearCellCopiedFlashMsg{idx: idx, gen: gen}
	})
}

// cellCopyText returns the y-to-copy payload for a transcript cell, or empty.
func cellCopyText(c cell) string {
	switch tc := c.(type) {
	case *toolCell:
		return tc.copyText()
	case *exploreCell:
		return tc.copyText()
	case *subagentResultCell:
		return tc.copyText()
	case *assistantCell:
		return tc.copyText()
	case *reasoningCell:
		return tc.copyText()
	case *userCell:
		return tc.copyText()
	default:
		return ""
	}
}

func setCellCopiedFlash(c cell, on bool) {
	switch tc := c.(type) {
	case *toolCell:
		tc.copiedFlash = on
	case *exploreCell:
		tc.copiedFlash = on
	case *subagentResultCell:
		tc.copiedFlash = on
	case *assistantCell:
		tc.copiedFlash = on
	case *userCell:
		tc.copiedFlash = on
	}
}

func clearCellCopiedFlash(c cell) {
	setCellCopiedFlash(c, false)
}

// copyLastAssistantResponse stages OSC52 for the last assistant message plain
// text (skips tool/explore spam). Uses the newest non-empty assistant cell —
// complete when the turn finished, or the live stream mid-turn. Notice on
// success/failure.
func (m *Model) copyLastAssistantResponse() tea.Cmd {
	cells := m.displayCells()
	idx := resolveLastAssistantCopyIndex(cells)
	if idx < 0 {
		m.setNotice("no assistant response to copy", true)
		return nil
	}
	text := cellCopyText(cells[idx])
	if text == "" {
		m.setNotice("no assistant response to copy", true)
		return nil
	}
	m.cellClip.stage(text)
	m.copyFlashGen++
	gen := m.copyFlashGen
	setCellCopiedFlash(cells[idx], true)
	for i, c := range cells {
		if i == idx {
			continue
		}
		clearCellCopiedFlash(c)
	}
	m.setNotice("copied last response", false)
	return tea.Tick(cellCopiedFlash, func(time.Time) tea.Msg {
		return clearCellCopiedFlashMsg{idx: idx, gen: gen}
	})
}

// resolveLastAssistantCopyIndex returns the index of the newest non-empty
// assistant cell for /copy and alt+y, or -1 when none.
func resolveLastAssistantCopyIndex(cells []cell) int {
	for i := len(cells) - 1; i >= 0; i-- {
		a, ok := cells[i].(*assistantCell)
		if !ok {
			continue
		}
		if a.copyText() == "" {
			continue
		}
		return i
	}
	return -1
}

// resolveCopyCellIndex prefers the current tool/explore selection when it has
// copyable content; otherwise the latest tool/explore, then assistant, then
// user cell with a non-empty payload.
func (m *Model) resolveCopyCellIndex() int {
	cells := m.displayCells()
	if m.selectedCell >= 0 && m.selectedCell < len(cells) {
		if cellCopyText(cells[m.selectedCell]) != "" {
			switch cells[m.selectedCell].(type) {
			case *toolCell, *exploreCell, *subagentResultCell:
				return m.selectedCell
			}
		}
	}
	latestTool, latestAsst, latestUser := -1, -1, -1
	for i := len(cells) - 1; i >= 0; i-- {
		if cellCopyText(cells[i]) == "" {
			continue
		}
		switch cells[i].(type) {
		case *toolCell, *exploreCell, *subagentResultCell:
			if latestTool < 0 {
				latestTool = i
			}
		case *assistantCell:
			if latestAsst < 0 {
				latestAsst = i
			}
		case *userCell:
			if latestUser < 0 {
				latestUser = i
			}
		}
		if latestTool >= 0 && latestAsst >= 0 && latestUser >= 0 {
			break
		}
	}
	if latestTool >= 0 {
		return latestTool
	}
	if latestAsst >= 0 {
		return latestAsst
	}
	return latestUser
}
