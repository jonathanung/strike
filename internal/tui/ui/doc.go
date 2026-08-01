// Package ui is strike's reusable Bubble Tea component library. Every
// component renders purely from resolved theme tokens — colors, background,
// borders, and spacing via theme.Theme, glyphs via theme.Icons — so the whole
// TUI restyles from one place (see internal/tui/theme).
//
// Components are stateless string renderers with three guarantees:
//
//   - Width-safe: given a width they return a block whose every line fits
//     within it (verify with lipgloss.Width). They never overflow.
//   - Graceful: at tiny widths they degrade (drop borders, truncate, single
//     column) instead of panicking.
//   - Structural panels: Panel's Borderless option omits all chrome while
//     preserving its exact Width and optional Height contract.
//   - Zero-value tolerant: a bare theme.Theme{} resolves to the default theme.
//
// Imports are limited to the standard library, lipgloss, bubbles,
// charmbracelet/x/ansi, internal/tui/theme, and internal/tui/common (pure
// display helpers). Views compose these components rather than building raw
// lipgloss boxes or lists.
package ui
