package tui

import "strings"

// SurfacePresentation is how a content surface opens: embedded in the right
// pane chrome, or as a large centered modal with background scrim.
// Shared by the markdown reader and (via VimMode aliases) the embedded
// editor path so future surfaces (e.g. first-class nano) can reuse the same
// modes.
type SurfacePresentation string

const (
	// PresentationEmbedded places the surface in the right-pane window stack.
	PresentationEmbedded SurfacePresentation = "embedded"
	// PresentationModal opens a large modal overlay with OverlayCenter scrim.
	PresentationModal SurfacePresentation = "modal"
)

// ParseSurfacePresentation resolves config values for reader/editor surfaces.
// Accepts embedded|modal plus legacy pane|overlay aliases. Empty yields
// embedded (default). Unknown values return ok=false.
func ParseSurfacePresentation(value string) (SurfacePresentation, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(PresentationEmbedded), "pane":
		return PresentationEmbedded, true
	case string(PresentationModal), "overlay":
		return PresentationModal, true
	default:
		return "", false
	}
}

// largeModalOuterWidth is the outer width for near-fullscreen surface modals
// (embedded editor, markdown reader): nearly full terminal with a small gutter.
func largeModalOuterWidth(screenWidth int) int {
	return max(40, screenWidth-4)
}
