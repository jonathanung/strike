package tui

import tea "github.com/charmbracelet/bubbletea"

// windowGroup is a named stack of right-pane windows shown together when
// space allows. members are indices into windowRegistry.windows.
type windowGroup struct {
	id      string
	members []int
}

// windowRegistry keeps ordered windows, optional stack groups, and the active
// flat index without exposing registration or lifecycle mechanics before those
// are needed.
type windowRegistry struct {
	windows []window
	groups  []windowGroup
	index   int
}

func newWindowRegistry() windowRegistry {
	windows := []window{
		newContextWindow(),
		newNamedWindow("activity", "activity"),
		newAgentsWindow(),
		newVisualizerWindow(),
		newFilesWindow(),
		newMemoryWindow(),
		newIssuesWindow(),
		newMarkdownWindow(),
		newTerminalWindow(),
	}
	r := windowRegistry{windows: windows}
	r.groups = defaultWindowGroups(windows)
	return r
}

// defaultWindowGroups pairs related panes for simultaneous split display.
// Focus cycle order is the concatenation of group members (same as the former
// flat window list).
func defaultWindowGroups(windows []window) []windowGroup {
	indexOf := map[string]int{}
	for i, w := range windows {
		indexOf[w.id()] = i
	}
	must := func(ids ...string) []int {
		out := make([]int, len(ids))
		for i, id := range ids {
			idx, ok := indexOf[id]
			if !ok {
				return nil
			}
			out[i] = idx
		}
		return out
	}
	groups := []windowGroup{
		{id: "session", members: must("context", "activity")},
		{id: "agents", members: must("agents", "visualizer")},
		{id: "files", members: must("files")},
		{id: "project", members: must("memory", "issues")},
		{id: "markdown", members: must("markdown")},
		{id: "editor", members: must("editor")},
	}
	out := make([]windowGroup, 0, len(groups))
	for _, g := range groups {
		if len(g.members) == 0 {
			continue
		}
		out = append(out, g)
	}
	return out
}

// broadcast delivers msg to every window, collecting their cmds. Used to push
// model-owned context snapshots into the right pane without dual state owners.
func (r windowRegistry) broadcast(msg tea.Msg) (windowRegistry, tea.Cmd) {
	if len(r.windows) == 0 {
		return r, nil
	}
	windows := make([]window, len(r.windows))
	cmds := make([]tea.Cmd, 0, len(r.windows))
	for i, w := range r.windows {
		next, cmd := w.update(msg)
		windows[i] = next
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	r.windows = windows
	return r, tea.Batch(cmds...)
}

func (r windowRegistry) init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(r.windows))
	for _, w := range r.windows {
		cmds = append(cmds, w.init())
	}
	return tea.Batch(cmds...)
}

func (r windowRegistry) active() window {
	if len(r.windows) == 0 {
		return nil
	}
	return r.windows[r.index%len(r.windows)]
}

// activeGroup returns the stack group containing the active window.
// When groups are empty, each window is treated as its own singleton group.
func (r windowRegistry) activeGroup() windowGroup {
	if len(r.windows) == 0 {
		return windowGroup{}
	}
	idx := r.index % len(r.windows)
	if len(r.groups) == 0 {
		return windowGroup{id: r.windows[idx].id(), members: []int{idx}}
	}
	for _, g := range r.groups {
		for _, m := range g.members {
			if m == idx {
				return g
			}
		}
	}
	return windowGroup{id: r.windows[idx].id(), members: []int{idx}}
}

// focusOrder is the deterministic cycle sequence of window indices.
func (r windowRegistry) focusOrder() []int {
	if len(r.windows) == 0 {
		return nil
	}
	if len(r.groups) == 0 {
		out := make([]int, len(r.windows))
		for i := range r.windows {
			out[i] = i
		}
		return out
	}
	out := make([]int, 0, len(r.windows))
	seen := make([]bool, len(r.windows))
	for _, g := range r.groups {
		for _, m := range g.members {
			if m < 0 || m >= len(r.windows) || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	for i := range r.windows {
		if !seen[i] {
			out = append(out, i)
		}
	}
	return out
}

func (r windowRegistry) cycle() windowRegistry {
	return r.cycleBy(1)
}

func (r windowRegistry) cycleBy(delta int) windowRegistry {
	order := r.focusOrder()
	n := len(order)
	if n == 0 {
		return r
	}
	pos := 0
	cur := r.index % len(r.windows)
	for i, idx := range order {
		if idx == cur {
			pos = i
			break
		}
	}
	// delta%n in Go keeps the dividend's sign; normalize into [0, n).
	pos = (pos + delta%n + n) % n
	r.index = order[pos]
	return r
}

func (r windowRegistry) update(msg tea.Msg) (windowRegistry, tea.Cmd) {
	if len(r.windows) == 0 {
		return r, nil
	}
	index := r.index % len(r.windows)
	next, cmd := r.windows[index].update(msg)
	windows := append([]window(nil), r.windows...)
	windows[index] = next
	r.windows = windows
	return r, cmd
}

func (r windowRegistry) resize(width, height int) windowRegistry {
	windows := make([]window, len(r.windows))
	for i, w := range r.windows {
		windows[i] = w.resize(width, height)
	}
	r.windows = windows
	return r
}

// memberSlot is the outer width/height allocated to one stacked pane.
type memberSlot struct {
	width  int
	height int
}

// minStackMemberOuter is the smallest useful bordered panel (top, one body, bottom).
const minStackMemberOuter = 3

// computeMemberSlots splits the right-pane outer box among n group members.
// pairHorizontal places members side-by-side (bottom-bar orientation); otherwise
// they stack top/bottom. Returns nil when n < 2 or the box is too small to split.
func computeMemberSlots(width, height, gutter, n int, pairHorizontal bool) []memberSlot {
	width, height, gutter, n = max(0, width), max(0, height), max(0, gutter), max(0, n)
	if n < 2 {
		return nil
	}
	if pairHorizontal {
		if width < minStackMemberOuter+gutter+minStackMemberOuter {
			return nil
		}
		available := max(0, width-gutter*(n-1))
		base := available / n
		if base < minStackMemberOuter {
			return nil
		}
		slots := make([]memberSlot, n)
		used := 0
		for i := 0; i < n; i++ {
			w := base
			if i == n-1 {
				w = available - used
			}
			if w < minStackMemberOuter {
				return nil
			}
			slots[i] = memberSlot{width: w, height: height}
			used += w
		}
		return slots
	}
	if height < minStackMemberOuter+gutter+minStackMemberOuter {
		return nil
	}
	available := max(0, height-gutter*(n-1))
	base := available / n
	if base < minStackMemberOuter {
		return nil
	}
	slots := make([]memberSlot, n)
	used := 0
	for i := 0; i < n; i++ {
		h := base
		if i == n-1 {
			h = available - used
		}
		if h < minStackMemberOuter {
			return nil
		}
		slots[i] = memberSlot{width: width, height: h}
		used += h
	}
	return slots
}

// resizeMembers applies per-window inner dimensions. missing indices keep prior
// size (callers usually pass a full map or use resize for uniform fill).
func (r windowRegistry) resizeMembers(dims map[int]memberSlot) windowRegistry {
	if len(r.windows) == 0 || len(dims) == 0 {
		return r
	}
	windows := make([]window, len(r.windows))
	copy(windows, r.windows)
	for i, slot := range dims {
		if i < 0 || i >= len(windows) {
			continue
		}
		windows[i] = windows[i].resize(max(0, slot.width), max(0, slot.height))
	}
	r.windows = windows
	return r
}

// activate sets the active window to the one with the given id (copy-on-write).
// ok is false when no window matches.
func (r windowRegistry) activate(id string) (windowRegistry, bool) {
	for i, w := range r.windows {
		if w.id() == id {
			r.index = i
			return r, true
		}
	}
	return r, false
}

// replace swaps the window with the given id for w (copy-on-write). When
// activate is true the replaced window becomes active. ok is false when no
// window matches.
func (r windowRegistry) replace(id string, w window, activate bool) (windowRegistry, bool) {
	for i, cur := range r.windows {
		if cur.id() != id {
			continue
		}
		windows := append([]window(nil), r.windows...)
		windows[i] = w
		r.windows = windows
		if activate {
			r.index = i
		}
		return r, true
	}
	return r, false
}
