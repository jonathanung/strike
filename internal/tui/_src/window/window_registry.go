package tui

import tea "charm.land/bubbletea/v2"

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
		newTelemetryWindow(),
		newAgentsWindow(),
		newVisualizerWindow(),
		newFilesWindow(),
		newDiagnosticsWindow(),
		newMemoryWindow(),
		newIssuesWindow(),
		newPlansWindow(),
		newMarkdownWindow(),
		newTerminalWindow(),
		newPetsWindow(),
	}
	r := windowRegistry{windows: windows}
	r.groups = defaultWindowGroups(windows)
	return r
}

// windowCycleable reports whether w participates in the right-pane cycle and
// stack groups. Disabled system telemetry stays registered but is hidden.
func windowCycleable(w window) bool {
	if tw, ok := w.(telemetryWindow); ok {
		return tw.enabled
	}
	return true
}

// defaultWindowGroups pairs related panes for simultaneous split display.
// Focus cycle order is the concatenation of group members (same as the former
// flat window list). Telemetry is on by default; /telemetry off omits it.
func defaultWindowGroups(windows []window) []windowGroup {
	indexOf := map[string]int{}
	for i, w := range windows {
		if !windowCycleable(w) {
			continue
		}
		indexOf[w.id()] = i
	}
	// required fails the whole group if any id is missing; optional skips
	// absent ids (e.g. telemetry when opted out) but keeps the rest.
	required := func(ids ...string) []int {
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
	optional := func(ids ...string) []int {
		out := make([]int, 0, len(ids))
		for _, id := range ids {
			idx, ok := indexOf[id]
			if !ok {
				continue
			}
			out = append(out, idx)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	groups := []windowGroup{
		{id: "session", members: optional("context", "activity", "telemetry")},
		{id: "agents", members: required("agents", "visualizer")},
		{id: "files", members: required("files", "diagnostics")},
		{id: "project", members: required("memory", "issues", "plans")},
		{id: "markdown", members: required("markdown")},
		{id: "editor", members: required("editor")},
		{id: "pets", members: required("pets")},
	}
	// Plugin panes (§9.3): shared "plugin" stack group; never inject into built-ins.
	var pluginIDs []string
	for _, w := range windows {
		if _, ok := w.(pluginPaneWindow); ok {
			pluginIDs = append(pluginIDs, w.id())
		}
	}
	if len(pluginIDs) > 0 {
		if members := optional(pluginIDs...); len(members) > 0 {
			groups = append(groups, windowGroup{id: pluginWindowGroupID, members: members})
		}
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
			if !windowCycleable(r.windows[i]) {
				continue
			}
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

// cycleGroupBy moves focus to the first member of the group delta steps away
// from the active window's group (wrapping). When groups are empty, each
// cycleable window is its own group (same as cycleBy).
func (r windowRegistry) cycleGroupBy(delta int) windowRegistry {
	groups := r.cycleGroups()
	n := len(groups)
	if n == 0 {
		return r
	}
	if n == 1 {
		// Single group: land on its first member (no-op when already there).
		if len(groups[0].members) > 0 {
			r.index = groups[0].members[0]
		}
		return r
	}
	cur := r.index % len(r.windows)
	pos := 0
	for i, g := range groups {
		for _, m := range g.members {
			if m == cur {
				pos = i
				goto found
			}
		}
	}
found:
	pos = (pos + delta%n + n) % n
	if len(groups[pos].members) == 0 {
		return r
	}
	r.index = groups[pos].members[0]
	return r
}

// cycleGroups returns stack groups used for group-level focus cycling.
// Empty registry groups fall back to one singleton group per cycleable window
// in focusOrder, matching activeGroup's singleton behavior.
func (r windowRegistry) cycleGroups() []windowGroup {
	if len(r.windows) == 0 {
		return nil
	}
	if len(r.groups) > 0 {
		out := make([]windowGroup, 0, len(r.groups))
		for _, g := range r.groups {
			if len(g.members) == 0 {
				continue
			}
			out = append(out, g)
		}
		if len(out) > 0 {
			return out
		}
	}
	order := r.focusOrder()
	out := make([]windowGroup, 0, len(order))
	for _, idx := range order {
		out = append(out, windowGroup{id: r.windows[idx].id(), members: []int{idx}})
	}
	return out
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

// minStackMemberOuter is the smallest useful chrome panel (top, one body, bottom).
// Matches ui.ChromeMinOuter for default soft chrome so stacked tiles keep rounded frames.
const minStackMemberOuter = 6

// computeMemberSlots splits the right-pane outer box among n group members.
// pairHorizontal places members side-by-side (bottom-bar orientation); otherwise
// they stack top/bottom. preferred, when non-nil and len==n, is a content-sized
// outer height (or width when pairHorizontal) hint per member: values <=0 mean
// "flex" (absorb remainder). Sparse panes keep tight preferred sizes so empty
// bordered voids shrink (#680). Returns nil when n < 2 or the box is too small.
func computeMemberSlots(width, height, gutter, n int, pairHorizontal bool, preferred ...[]int) []memberSlot {
	width, height, gutter, n = max(0, width), max(0, height), max(0, gutter), max(0, n)
	if n < 2 {
		return nil
	}
	var pref []int
	if len(preferred) > 0 && len(preferred[0]) == n {
		pref = preferred[0]
	}
	if pairHorizontal {
		if width < minStackMemberOuter+gutter+minStackMemberOuter {
			return nil
		}
		available := max(0, width-gutter*(n-1))
		sizes := distributeFlexSizes(available, n, minStackMemberOuter, pref)
		if sizes == nil {
			return nil
		}
		slots := make([]memberSlot, n)
		for i := 0; i < n; i++ {
			slots[i] = memberSlot{width: sizes[i], height: height}
		}
		return slots
	}
	if height < minStackMemberOuter+gutter+minStackMemberOuter {
		return nil
	}
	available := max(0, height-gutter*(n-1))
	sizes := distributeFlexSizes(available, n, minStackMemberOuter, pref)
	if sizes == nil {
		return nil
	}
	slots := make([]memberSlot, n)
	for i := 0; i < n; i++ {
		slots[i] = memberSlot{width: width, height: sizes[i]}
	}
	return slots
}

// distributeFlexSizes allocates available cells across n members.
// preferred[i] > 0 is a content affordance (clamped to [minSize, available]);
// preferred[i] <= 0 marks a flex member that absorbs remainder after preferred
// panes take their share. With no preferred hints, sizes are equal (legacy).
// Returns nil when even minSize*n cannot fit.
func distributeFlexSizes(available, n, minSize int, preferred []int) []int {
	if n < 1 || available < minSize*n {
		return nil
	}
	sizes := make([]int, n)
	if len(preferred) != n {
		// Equal split (last absorbs rounding).
		base := available / n
		if base < minSize {
			return nil
		}
		used := 0
		for i := 0; i < n; i++ {
			sizes[i] = base
			if i == n-1 {
				sizes[i] = available - used
			}
			if sizes[i] < minSize {
				return nil
			}
			used += sizes[i]
		}
		return sizes
	}
	// First pass: clamp preferred, count flex members.
	flexIdx := make([]int, 0, n)
	used := 0
	for i := 0; i < n; i++ {
		p := preferred[i]
		if p <= 0 {
			sizes[i] = minSize // provisional floor; flex grows later
			flexIdx = append(flexIdx, i)
			used += minSize
			continue
		}
		// Content-sized: prefer p but never below min or above what remains
		// if every other member also needs minSize.
		maxForMe := available - minSize*(n-1)
		h := min(max(p, minSize), maxForMe)
		sizes[i] = h
		used += h
	}
	if used > available {
		// Shrink preferred panes (largest first) until we fit; keep minSize.
		overflow := used - available
		for overflow > 0 {
			// Find largest preferred (non-flex) above minSize.
			best := -1
			for i := 0; i < n; i++ {
				if preferred[i] <= 0 {
					continue
				}
				if sizes[i] <= minSize {
					continue
				}
				if best < 0 || sizes[i] > sizes[best] {
					best = i
				}
			}
			if best < 0 {
				return nil
			}
			sizes[best]--
			overflow--
		}
		used = available
	}
	// Remainder goes to flex members (equal share; last absorbs rounding).
	remain := available - used
	if len(flexIdx) == 0 {
		// No flex member: give remainder to the last pane so the stack fills.
		sizes[n-1] += remain
		return sizes
	}
	// used already counted minSize per flex; add remain on top.
	extraBase := remain / len(flexIdx)
	extraUsed := 0
	for j, i := range flexIdx {
		extra := extraBase
		if j == len(flexIdx)-1 {
			extra = remain - extraUsed
		}
		sizes[i] += extra
		extraUsed += extra
	}
	return sizes
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
// ok is false when no window matches or the window is hidden (e.g. telemetry off).
func (r windowRegistry) activate(id string) (windowRegistry, bool) {
	for i, w := range r.windows {
		if w.id() != id {
			continue
		}
		if !windowCycleable(w) {
			return r, false
		}
		r.index = i
		return r, true
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
