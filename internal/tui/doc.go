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
//	go generate ./internal/tui
//
//	make build / make test  # runs generate first
//
// Independent subpackages (real packages, not flattened):
//
//   - theme  — design tokens
//   - ui     — reusable components
//   - term   — PTY + vt10x
//   - common — pure formatting helpers
//
// Import boundary (boundary_test.go): only protocol, host, and tui/….
package tui

//go:generate go run gen_src.go .
