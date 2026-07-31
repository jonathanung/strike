package tui

import (
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const filesWindowID = "files"

// filesRefreshInterval caps idle directory rescans (~1 Hz).
const filesRefreshInterval = time.Second

// filesOpenMsg requests opening a workspace-relative path from the files explorer.
// .md paths route to /md-read; everything else uses /vim plumbing.
type filesOpenMsg struct {
	path string
}

// filesRefreshMsg triggers a cheap re-list of expanded directories.
type filesRefreshMsg struct{}

func filesRefreshCmd() tea.Cmd {
	return tea.Tick(filesRefreshInterval, func(time.Time) tea.Msg {
		return filesRefreshMsg{}
	})
}

// isWorkspaceFSTool reports tools that may create/delete/rename workspace files.
func isWorkspaceFSTool(name string) bool {
	switch name {
	case "write", "edit", "bash", "apply_patch", "notebook_edit":
		return true
	default:
		return false
	}
}

// filesWindow is the right-pane file explorer: a lazy tree of the workspace
// rooted at workDir, listed through host.Files.
type filesWindow struct {
	root   string
	files  host.Files
	nodes  []ui.TreeNode
	cursor int
	width  int
	height int
	err    string
}

func newFilesWindow() filesWindow {
	return filesWindow{}
}

func (w filesWindow) id() string { return filesWindowID }

func (w filesWindow) title() string {
	if w.root == "" {
		return "files"
	}
	base := filepath.Base(w.root)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "files"
	}
	return "files"
}

// init does not arm the idle poll. Polling runs only while the files pane is
// active (see filesWindowActive + Model.Update filesRefreshMsg) so session
// init with context/activity visible is event-driven (#481).
func (w filesWindow) init() tea.Cmd { return nil }

// filesWindowActive reports whether the files explorer is in the active
// right-pane group (and therefore visible when the right pane is shown).
func filesWindowActive(r windowRegistry) bool {
	for _, wi := range r.activeGroup().members {
		if wi < 0 || wi >= len(r.windows) {
			continue
		}
		if r.windows[wi].id() == filesWindowID {
			return true
		}
	}
	return false
}

// filesPollCmd arms the ~1 Hz directory rescan when the files pane is active.
func filesPollCmd(r windowRegistry) tea.Cmd {
	if !filesWindowActive(r) {
		return nil
	}
	return filesRefreshCmd()
}

func (w filesWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return w.handleKey(msg)
	}
	return w, nil
}

func (w filesWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w filesWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	if w.files == nil {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("file listing unavailable"),
		)
	}
	if w.root == "" {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("no workspace directory"),
		)
	}
	if w.err != "" && len(w.nodes) == 0 {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Error.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis)),
		)
	}
	visible := w.height
	if visible < 1 {
		visible = 0
	}
	body := ui.Tree(th, ui.TreeOpts{
		Nodes:   w.nodes,
		Cursor:  w.cursor,
		Width:   w.width,
		Visible: visible,
		Empty:   "empty directory",
	})
	if w.err == "" {
		return body
	}
	// Keep a one-line error under a partially loaded tree when height allows.
	errLine := st.Error.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis))
	if w.height <= 1 {
		return errLine
	}
	if body == "" {
		return errLine
	}
	lines := strings.Split(body, "\n")
	if len(lines) >= w.height {
		lines = lines[:max(0, w.height-1)]
	}
	lines = append(lines, errLine)
	return strings.Join(lines, "\n")
}

// bind sets the workspace root and listing backend, then loads the top level.
func (w filesWindow) bind(root string, files host.Files) filesWindow {
	w.root = strings.TrimSpace(root)
	w.files = files
	w.nodes = nil
	w.cursor = 0
	w.err = ""
	if w.root == "" || w.files == nil {
		return w
	}
	return w.loadRoot()
}

func (w filesWindow) loadRoot() filesWindow {
	nodes, err := w.listNodes("")
	if err != nil {
		w.err = err.Error()
		w.nodes = nil
		return w
	}
	w.err = ""
	w.nodes = nodes
	w.cursor = 0
	return w
}

// refresh re-lists the root and currently expanded directories, preserving
// expansion and cursor when those paths still exist.
func (w filesWindow) refresh() filesWindow {
	if w.files == nil || w.root == "" {
		return w
	}
	expanded := map[string]struct{}{}
	filesCollectExpanded(w.nodes, expanded)
	var cursorID string
	rows := ui.FlattenTree(w.nodes)
	if w.cursor >= 0 && w.cursor < len(rows) {
		cursorID = rows[w.cursor].ID
	}
	prevCursor := w.cursor

	nodes, err := w.listNodes("")
	if err != nil {
		w.err = err.Error()
		// Keep the prior tree so a transient list error does not blank the pane.
		return w
	}
	w.err = ""
	w.nodes = w.restoreExpanded(nodes, expanded)

	rows = ui.FlattenTree(w.nodes)
	w.cursor = 0
	if cursorID != "" {
		for i, row := range rows {
			if row.ID == cursorID {
				w.cursor = i
				return w
			}
		}
	}
	if len(rows) == 0 {
		w.cursor = 0
	} else if prevCursor >= len(rows) {
		w.cursor = len(rows) - 1
	} else if prevCursor > 0 {
		w.cursor = prevCursor
	}
	return w
}

func filesCollectExpanded(nodes []ui.TreeNode, out map[string]struct{}) {
	for _, n := range nodes {
		if n.Expanded && !n.Leaf && n.ID != "" {
			out[n.ID] = struct{}{}
		}
		if len(n.Children) > 0 {
			filesCollectExpanded(n.Children, out)
		}
	}
}

// restoreExpanded loads children for IDs in expanded and marks those nodes open.
func (w filesWindow) restoreExpanded(nodes []ui.TreeNode, expanded map[string]struct{}) []ui.TreeNode {
	if len(expanded) == 0 {
		return nodes
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Leaf || n.ID == "" {
			continue
		}
		if _, ok := expanded[n.ID]; !ok {
			continue
		}
		children, err := w.listNodes(n.ID)
		if err != nil {
			// Leave collapsed on transient/child list errors.
			continue
		}
		n.Children = w.restoreExpanded(children, expanded)
		n.Lazy = true
		n.Expanded = true
	}
	return nodes
}

// refreshFilesWindows reloads the files explorer tree from host.Files.
func refreshFilesWindows(r windowRegistry) windowRegistry {
	if len(r.windows) == 0 {
		return r
	}
	windows := append([]window(nil), r.windows...)
	changed := false
	for i, w := range windows {
		fw, ok := w.(filesWindow)
		if !ok {
			continue
		}
		windows[i] = fw.refresh()
		changed = true
	}
	if !changed {
		return r
	}
	r.windows = windows
	return r
}

func (w filesWindow) handleKey(msg tea.KeyPressMsg) (filesWindow, tea.Cmd) {
	if w.files == nil || w.root == "" {
		return w, nil
	}
	var cmd tea.Cmd
	rows := ui.FlattenTree(w.nodes)
	switch msg.String() {
	case "up", "k":
		if w.cursor > 0 {
			w.cursor--
		}
	case "down", "j":
		if w.cursor < len(rows)-1 {
			w.cursor++
		}
	case "enter", "right", "l":
		w, cmd = w.expandOrToggle(true)
	case "left", "h":
		w, cmd = w.expandOrToggle(false)
	}
	// Clamp after structural changes.
	rows = ui.FlattenTree(w.nodes)
	if len(rows) == 0 {
		w.cursor = 0
	} else if w.cursor >= len(rows) {
		w.cursor = len(rows) - 1
	} else if w.cursor < 0 {
		w.cursor = 0
	}
	return w, cmd
}

// expandOrToggle expands (open=true) or collapses (open=false) the cursor row.
// Enter/right toggles expandable nodes and opens leaf files; left only collapses.
func (w filesWindow) expandOrToggle(open bool) (filesWindow, tea.Cmd) {
	rows := ui.FlattenTree(w.nodes)
	if w.cursor < 0 || w.cursor >= len(rows) {
		return w, nil
	}
	row := rows[w.cursor]
	if !row.Expandable {
		if open && strings.TrimSpace(row.ID) != "" {
			path := row.ID
			return w, func() tea.Msg { return filesOpenMsg{path: path} }
		}
		return w, nil
	}
	if !open {
		if !row.Expanded {
			return w, nil
		}
		ui.TreeToggleExpanded(w.nodes, row.Path)
		return w, nil
	}
	// open / toggle
	if row.Expanded {
		ui.TreeToggleExpanded(w.nodes, row.Path)
		return w, nil
	}
	n, ok := ui.TreeNodeAt(w.nodes, row.Path)
	if !ok {
		return w, nil
	}
	if n.Lazy && len(n.Children) == 0 {
		children, err := w.listNodes(n.ID)
		if err != nil {
			w.err = err.Error()
			return w, nil
		}
		if !filesSetChildren(w.nodes, row.Path, children) {
			return w, nil
		}
		w.err = ""
	}
	ui.TreeToggleExpanded(w.nodes, row.Path)
	return w, nil
}

func (w filesWindow) listNodes(rel string) ([]ui.TreeNode, error) {
	entries, err := w.files.ListDir(rel)
	if err != nil {
		return nil, err
	}
	nodes := make([]ui.TreeNode, 0, len(entries))
	for _, e := range entries {
		id := e.Name
		if rel != "" {
			id = filepath.Join(rel, e.Name)
		}
		// Prefer forward slashes in IDs for stable display across platforms.
		id = filepath.ToSlash(id)
		n := ui.TreeNode{
			ID:    id,
			Label: e.Name,
		}
		if e.IsDir {
			n.Lazy = true
			n.Tone = ui.ToneAccent
		} else {
			n.Leaf = true
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// filesSetChildren replaces Children on the node at path. Mutates nodes.
func filesSetChildren(nodes []ui.TreeNode, path []int, children []ui.TreeNode) bool {
	p := filesNodePtr(nodes, path)
	if p == nil {
		return false
	}
	p.Children = children
	p.Lazy = true // keep expandable even when empty after load
	return true
}

func filesNodePtr(nodes []ui.TreeNode, path []int) *ui.TreeNode {
	if len(path) == 0 {
		return nil
	}
	if path[0] < 0 || path[0] >= len(nodes) {
		return nil
	}
	if len(path) == 1 {
		return &nodes[path[0]]
	}
	return filesNodePtr(nodes[path[0]].Children, path[1:])
}

// configureFilesWindow binds host.Files + workDir onto the files window slot.
func configureFilesWindow(r windowRegistry, root string, files host.Files) windowRegistry {
	for i, w := range r.windows {
		fw, ok := w.(filesWindow)
		if !ok {
			continue
		}
		next := fw.bind(root, files)
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r
	}
	return r
}

// openFilesExplorerPath opens a tree leaf: markdown via /md-read, else /vim.
func (m Model) openFilesExplorerPath(path string) (tea.Model, tea.Cmd) {
	path = strings.TrimSpace(path)
	if path == "" {
		return m, nil
	}
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return m.handleMDRead("/md-read "+path, []string{"/md-read"})
	}
	return m.openFileRef(fileRef{Path: path})
}
