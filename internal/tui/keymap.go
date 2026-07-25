package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap collects the app-level bindings so routing remains independent of
// Bubble Tea's rendered key strings.
type keyMap struct {
	Quit              key.Binding
	FocusPane         key.Binding
	CycleWindow       key.Binding
	Palette           key.Binding
	Interrupt         key.Binding
	CompletionDismiss key.Binding
	CompletionAccept  key.Binding
	CompletionPrev    key.Binding
	CompletionNext    key.Binding
	Send              key.Binding
	Newline           key.Binding
	HistoryPrev       key.Binding
	HistoryNext       key.Binding
	Agent             key.Binding
	SaveDefaults      key.Binding
	ScrollUp          key.Binding
	ScrollDown        key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Quit:              key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		FocusPane:         key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "focus pane")),
		CycleWindow:       key.NewBinding(key.WithKeys("ctrl+l", "ctrl+o"), key.WithHelp("ctrl+l/ctrl+o", "cycle window")),
		Palette:           key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "palette")),
		Interrupt:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "interrupt")),
		CompletionDismiss: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "dismiss")),
		CompletionAccept:  key.NewBinding(key.WithKeys("tab", "enter"), key.WithHelp("tab/enter", "accept")),
		CompletionPrev:    key.NewBinding(key.WithKeys("up", "ctrl+p"), key.WithHelp("up/ctrl+p", "previous")),
		CompletionNext:    key.NewBinding(key.WithKeys("down", "ctrl+n"), key.WithHelp("down/ctrl+n", "next")),
		Send:              key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline:           key.NewBinding(key.WithKeys("alt+enter"), key.WithHelp("alt+enter", "newline")),
		HistoryPrev:       key.NewBinding(key.WithKeys("up"), key.WithHelp("up", "history previous")),
		HistoryNext:       key.NewBinding(key.WithKeys("down"), key.WithHelp("down", "history next")),
		Agent:             key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "agent")),
		SaveDefaults:      key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "save defaults")),
		ScrollUp:          key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "scroll up")),
		ScrollDown:        key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "scroll down")),
	}
}
