package tui

// paintBudget holds redraw counters for CI budget guards (#452 epic, #495).
// Shared via pointer so value-receiver View/renderFrame can still increment.
// Production paths always allocate one in New; zero value is a no-op observer.
type paintBudget struct {
	viewCalls            int
	renderFrameCalls     int
	refreshViewportCalls int
	renderCellCalls      int
	lastViewBytes        int
}

func (p *paintBudget) reset() {
	if p == nil {
		return
	}
	*p = paintBudget{}
}
