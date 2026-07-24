// Package ui is strike's reusable Bubble Tea component library. Every
// component renders purely from theme tokens — colors via theme.Theme, glyphs
// via theme.Icons — and never hardcodes a color or a glyph, so the whole TUI
// restyles from one place (see internal/tui/theme).
//
// Components are stateless string renderers with three guarantees:
//
//   - Width-safe: given a width they return a block whose every line fits
//     within it (verify with lipgloss.Width). They never overflow.
//   - Graceful: at tiny widths they degrade (drop borders, truncate, single
//     column) instead of panicking.
//   - Zero-value tolerant: a bare theme.Theme{} still renders (icons fall
//     back to theme.DefaultIcons()).
//
// Imports are limited to the standard library, lipgloss, bubbles,
// charmbracelet/x/ansi, and internal/tui/theme. Views compose these
// components rather than building raw lipgloss boxes or lists.
package ui
