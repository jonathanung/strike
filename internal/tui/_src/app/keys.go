package tui

import tea "github.com/charmbracelet/bubbletea"

// isEscape reports whether msg is Escape (KeyEsc or the "esc" string form).
// Modals and dismiss paths should use this instead of comparing String() alone
// so CSI-u normalized bare ESC and classic KeyEsc both match.
func isEscape(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyEsc || msg.String() == "esc"
}
