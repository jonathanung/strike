// Package tui is strike's Bubble Tea frontend.
//
// # Source layout
//
// Sources live under _src/<group>/ for traversability. Go requires one directory
// per package, so go generate flattens those files into this directory:
//
//	_src/app/      — model, events, keys, commands
//	_src/layout/   — view, split layout, welcome, chrome
//	_src/modal/    — overlay dialogs and pickers
//	_src/window/   — right-pane windows and registry
//	_src/cell/     — transcript cells and export
//	_src/input/    — composer, completion, mouse, editor launch
//	_src/session/  — session navigation
//	_src/hostui/   — host-adjacent UI (mcp, project data, terminal notify)
//	_src/util/     — shims to common/
//	_src/test/     — cross-cutting tests
//
// Edit files under _src/ only, then run:
//
//	go generate ./internal/frontend/tui/app
//
//	make build / make test  # runs generate first
//
// Flattened internal/frontend/tui/app/*.go copies are gitignored and regenerated on every
// build/test. Editing them is silently discarded — source of truth is _src/.
// TestSrcFlattenInSync fails the suite if the flatten is stale.
//
// Independent subpackages (real packages, not flattened):
//
//   - theme  — design tokens
//   - ui     — reusable components
//   - term   — PTY + vt10x
//   - common — pure formatting helpers
//
// Import boundary (boundary_test.go): only protocol, host, and tui/….
// Charm module paths: charm.land/… (v2) or remaining v1 github.com/charmbracelet/x/…;
// github.com/charmbracelet/…/v2 is rejected (TestCharmImportPaths).
package tui

//go:generate go run gen_src.go .
