package tui

// Frame-layer dirty mask (#494). renderFrame recomposes every layer by default.
// Known cheap Update paths set allowSkip so unchanged layers reuse the last
// string. Correctness over cleverness: unknown paths leave allowSkip=0.
//
// Invalidation matrix (layer ← causes that require recompose):
//
//	header  ← spinner tick, agent/working chrome, badges, usage meter, theme,
//	          resize, focus (status bar width sharing), workdir, provider/model
//	left    ← transcript/viewport, composer, notice, completion popup, theme,
//	          resize, layout/reflow, thinking placeholder refresh
//	right   ← window registry/cycle, activity/context/agents broadcasts, focus
//	          chrome (dim/focus edge), theme, resize, split orientation, modal
//	          open/close (dims right), project data mutations
//	footer  ← hints set (focus side, keymap, orientation), danger banner, theme,
//	          resize
//
// Partial skip paths (allowSkip bits = layers that may reuse cache):
//   - spinner.TickMsg while Working, cache warm → skip left|right|footer
//   - all other messages → allowSkip=0 (full compose)
//
// Geometry fingerprint mismatch forces full compose even when allowSkip is set.

// frameDirty is a bitset of compose layers.
type frameDirty uint8

const (
	dirtyHeader frameDirty = 1 << iota
	dirtyLeft
	dirtyRight
	dirtyFooter
	dirtyAll = dirtyHeader | dirtyLeft | dirtyRight | dirtyFooter
)

// frameCache holds last composed layer strings. Pointer-backed so value-receiver
// View/Update can mutate it across Model copies (same pattern as cellClip).
type frameCache struct {
	// allowSkip lists layers that may reuse cache on the next renderFrame.
	// Cleared after each paint. Zero means full compose (safe default).
	allowSkip frameDirty

	// Layout fingerprint; mismatch ⇒ ignore allowSkip.
	width, height                int
	leftW, bodyH, rightW, rightH int
	gutter, vGutter              int
	focus                        paneFocus
	split                        splitOrientation
	showLeft, showRight          bool
	splitVertical, leftCompact   bool
	rightCompact                 bool
	hasModal                     bool
	headerOn, hintsOn, dangerOn  bool
	bandHeight                   int

	header, leftBody, right, footer string

	// Compose counters for tests/spy (#494 acceptance).
	rightComposeN  int
	headerComposeN int
}

func newFrameCache() *frameCache {
	return &frameCache{}
}

// markFrameSkip allows the next renderFrame to reuse the listed layers when
// the geometry fingerprint still matches and the cached string is non-empty
// (or the layer is intentionally blank for the current show* flags).
func (m *Model) markFrameSkip(bits frameDirty) {
	if m.frames == nil {
		m.frames = newFrameCache()
	}
	m.frames.allowSkip = bits
}

func (m *Model) clearFrameSkip() {
	if m.frames != nil {
		m.frames.allowSkip = 0
	}
}

func (c *frameCache) matchGeo(
	width, height, leftW, bodyH, rightW, rightH, gutter, vGutter int,
	focus paneFocus, split splitOrientation,
	showLeft, showRight, splitVertical, leftCompact, rightCompact, hasModal bool,
	headerOn, hintsOn, dangerOn bool, bandHeight int,
) bool {
	return c.width == width && c.height == height &&
		c.leftW == leftW && c.bodyH == bodyH &&
		c.rightW == rightW && c.rightH == rightH &&
		c.gutter == gutter && c.vGutter == vGutter &&
		c.focus == focus && c.split == split &&
		c.showLeft == showLeft && c.showRight == showRight &&
		c.splitVertical == splitVertical &&
		c.leftCompact == leftCompact && c.rightCompact == rightCompact &&
		c.hasModal == hasModal &&
		c.headerOn == headerOn && c.hintsOn == hintsOn && c.dangerOn == dangerOn &&
		c.bandHeight == bandHeight
}

func (c *frameCache) storeGeo(
	width, height, leftW, bodyH, rightW, rightH, gutter, vGutter int,
	focus paneFocus, split splitOrientation,
	showLeft, showRight, splitVertical, leftCompact, rightCompact, hasModal bool,
	headerOn, hintsOn, dangerOn bool, bandHeight int,
) {
	c.width, c.height = width, height
	c.leftW, c.bodyH = leftW, bodyH
	c.rightW, c.rightH = rightW, rightH
	c.gutter, c.vGutter = gutter, vGutter
	c.focus, c.split = focus, split
	c.showLeft, c.showRight = showLeft, showRight
	c.splitVertical = splitVertical
	c.leftCompact, c.rightCompact = leftCompact, rightCompact
	c.hasModal = hasModal
	c.headerOn, c.hintsOn, c.dangerOn = headerOn, hintsOn, dangerOn
	c.bandHeight = bandHeight
}
