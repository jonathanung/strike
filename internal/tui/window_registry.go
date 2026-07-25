package tui

import tea "github.com/charmbracelet/bubbletea"

// windowRegistry keeps ordered windows and the active index without exposing
// registration or lifecycle mechanics before those are needed.
type windowRegistry struct {
	windows []window
	index   int
}

func newWindowRegistry() windowRegistry {
	return windowRegistry{windows: []window{
		newPlaceholderWindow("context", "context", "Context window placeholder."),
		newPlaceholderWindow("activity", "activity", "Activity window placeholder."),
	}}
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

func (r windowRegistry) cycle() windowRegistry {
	return r.cycleBy(1)
}

func (r windowRegistry) cycleBy(delta int) windowRegistry {
	if n := len(r.windows); n > 0 {
		r.index = (r.index + delta%n + n) % n
	}
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
