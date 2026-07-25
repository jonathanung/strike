package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap collects the app-level bindings so routing remains independent of
// Bubble Tea's rendered key strings.
type keyMap struct {
	Quit              key.Binding
	FocusLeft         key.Binding
	FocusRight        key.Binding
	CycleWindowNext   key.Binding
	CycleWindowPrev   key.Binding
	Palette           key.Binding
	KeyHelp           key.Binding
	Interrupt         key.Binding
	TerminalLeave     key.Binding
	CompletionDismiss key.Binding
	CompletionAccept  key.Binding
	CompletionPrev    key.Binding
	CompletionNext    key.Binding
	Send              key.Binding
	Newline           key.Binding
	ExternalEditor    key.Binding
	HistoryPrev       key.Binding
	HistoryNext       key.Binding
	Agent             key.Binding
	SaveDefaults      key.Binding
	ScrollUp          key.Binding
	ScrollDown        key.Binding
	JumpBottom        key.Binding
	ToggleOrientation key.Binding
	// Tool cell selection/expand/copy when the composer is empty (enter still
	// sends when there is text; y still types when the composer has content).
	ToolPrev   key.Binding
	ToolNext   key.Binding
	ToolExpand key.Binding
	ToolCopy   key.Binding

	// Composer readline editing (focusLeft only). ctrl+k must not be stolen by
	// CycleWindowPrev / vertical FocusRight; palette/global chords stay global.
	KillWord      key.Binding
	WordBackward  key.Binding
	WordForward   key.Binding
	KillLineStart key.Binding
	KillLineEnd   key.Binding
	Yank          key.Binding

	// Subagent transcript navigation (opencode-style leader chords).
	Leader            key.Binding
	SessionChildFirst key.Binding
	SessionParent     key.Binding
	SessionChildNext  key.Binding
	SessionChildPrev  key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Quit:              key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		FocusLeft:         key.NewBinding(key.WithKeys("ctrl+h"), key.WithHelp("ctrl+h", "focus left")),
		FocusRight:        key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "focus right")),
		CycleWindowNext:   key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "next window")),
		CycleWindowPrev:   key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "prev window")),
		Palette:           key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette")),
		KeyHelp:           key.NewBinding(key.WithKeys("f1"), key.WithHelp("f1", "keybinds")),
		Interrupt:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "interrupt")),
		TerminalLeave:     key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "leave editor")),
		CompletionDismiss: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "dismiss")),
		CompletionAccept:  key.NewBinding(key.WithKeys("tab", "enter"), key.WithHelp("tab/enter", "accept")),
		CompletionPrev:    key.NewBinding(key.WithKeys("up"), key.WithHelp("up", "previous")),
		CompletionNext:    key.NewBinding(key.WithKeys("down", "ctrl+n"), key.WithHelp("down/ctrl+n", "next")),
		Send:              key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline:           key.NewBinding(key.WithKeys("alt+enter"), key.WithHelp("shift+enter", "newline")),
		ExternalEditor:    key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("ctrl+e", "external editor")),
		HistoryPrev:       key.NewBinding(key.WithKeys("up"), key.WithHelp("up", "history previous")),
		HistoryNext:       key.NewBinding(key.WithKeys("down"), key.WithHelp("down", "history next")),
		Agent:             key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "agent")),
		SaveDefaults:      key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "save defaults")),
		ScrollUp:          key.NewBinding(key.WithKeys("pgup", "ctrl+up"), key.WithHelp("pgup/ctrl+up", "scroll up")),
		ScrollDown:        key.NewBinding(key.WithKeys("pgdown", "ctrl+down"), key.WithHelp("pgdn/ctrl+down", "scroll down")),
		// JumpBottom: ctrl+t is the user chord. Bubble Tea names the legacy
		// control byte KeyCtrlT ("ctrl+t").
		JumpBottom: key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "jump to bottom")),
		// ToggleOrientation: user chord is ctrl+;. Bubble Tea has no KeyType for
		// ctrl+semicolon, so WrapInput rewrites enhanced ctrl+; CSI to alt+;
		// (same pattern as shift+enter → alt+enter for Newline).
		ToggleOrientation: key.NewBinding(key.WithKeys("alt+;"), key.WithHelp("ctrl+;", "toggle split")),
		// Tool cell nav: only when composer is empty (see Model.handleToolCellKeys).
		// alt+[/] avoid stealing printable brackets from the composer.
		ToolPrev:   key.NewBinding(key.WithKeys("alt+["), key.WithHelp("alt+[", "prev tool cell")),
		ToolNext:   key.NewBinding(key.WithKeys("alt+]"), key.WithHelp("alt+]", "next tool cell")),
		ToolExpand: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand tool / open file:line")),
		// ToolCopy: bare y when composer is empty (yank selected/latest cell).
		ToolCopy: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy tool cell")),

		KillWord:      key.NewBinding(key.WithKeys("ctrl+w"), key.WithHelp("ctrl+w", "kill word backward")),
		WordBackward:  key.NewBinding(key.WithKeys("alt+b"), key.WithHelp("alt+b", "word backward")),
		WordForward:   key.NewBinding(key.WithKeys("alt+f"), key.WithHelp("alt+f", "word forward")),
		KillLineStart: key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "kill to line start")),
		KillLineEnd:   key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "kill to line end")),
		Yank:          key.NewBinding(key.WithKeys("ctrl+y"), key.WithHelp("ctrl+y", "yank")),

		Leader:            key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "leader")),
		SessionChildFirst: key.NewBinding(key.WithKeys("down"), key.WithHelp("ctrl+x down", "enter subagent")),
		SessionParent:     key.NewBinding(key.WithKeys("up"), key.WithHelp("ctrl+x up", "parent session")),
		SessionChildNext:  key.NewBinding(key.WithKeys("right"), key.WithHelp("ctrl+x right", "next subagent")),
		SessionChildPrev:  key.NewBinding(key.WithKeys("left"), key.WithHelp("ctrl+x left", "prev subagent")),
	}
}

// applyOrientationKeys swaps focus vs cycle chords for vertical splits:
// horizontal uses ctrl+h/l focus and ctrl+j/k cycle; vertical swaps those pairs.
func (k *keyMap) applyOrientationKeys(orient splitOrientation) {
	if orient == orientVertical {
		k.FocusLeft = key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "focus top"))
		k.FocusRight = key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "focus bottom"))
		k.CycleWindowNext = key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "next window"))
		k.CycleWindowPrev = key.NewBinding(key.WithKeys("ctrl+h"), key.WithHelp("ctrl+h", "prev window"))
		return
	}
	k.FocusLeft = key.NewBinding(key.WithKeys("ctrl+h"), key.WithHelp("ctrl+h", "focus left"))
	k.FocusRight = key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "focus right"))
	k.CycleWindowNext = key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "next window"))
	k.CycleWindowPrev = key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "prev window"))
}

// keybindEntry is one row in the filterable keybind cheatsheet.
type keybindEntry struct {
	ID       string
	Category string
	Keys     string
	Action   string
}

// keybindCatalog is the single source of truth for cheatsheet rows. App-level
// bindings are taken from keyMap help text; modal/list conventions are listed
// explicitly so the sheet stays complete without live modal state.
func keybindCatalog(keys keyMap) []keybindEntry {
	from := func(id, category string, b key.Binding) keybindEntry {
		help := b.Help()
		return keybindEntry{ID: id, Category: category, Keys: help.Key, Action: help.Desc}
	}
	entries := []keybindEntry{
		from("nav.focus-left", "Navigation", keys.FocusLeft),
		from("nav.focus-right", "Navigation", keys.FocusRight),
		from("nav.window-next", "Navigation", keys.CycleWindowNext),
		from("nav.window-prev", "Navigation", keys.CycleWindowPrev),
		from("nav.scroll-up", "Navigation", keys.ScrollUp),
		from("nav.scroll-down", "Navigation", keys.ScrollDown),
		from("nav.jump-bottom", "Navigation", keys.JumpBottom),
		from("nav.toggle-orient", "Navigation", keys.ToggleOrientation),
		from("nav.tool-prev", "Navigation", keys.ToolPrev),
		from("nav.tool-next", "Navigation", keys.ToolNext),
		from("nav.tool-expand", "Navigation", keys.ToolExpand),
		from("nav.tool-copy", "Navigation", keys.ToolCopy),
		from("nav.leader", "Navigation", keys.Leader),
		from("nav.session-child", "Navigation", keys.SessionChildFirst),
		from("nav.session-parent", "Navigation", keys.SessionParent),
		from("nav.session-next", "Navigation", keys.SessionChildNext),
		from("nav.session-prev", "Navigation", keys.SessionChildPrev),

		from("global.palette", "Global", keys.Palette),
		from("global.keyhelp", "Global", keys.KeyHelp),
		from("global.interrupt", "Global", keys.Interrupt),
		from("global.quit", "Global", keys.Quit),
		from("global.save-defaults", "Global", keys.SaveDefaults),
		from("editor.leave", "Editor", keys.TerminalLeave),

		from("composer.send", "Composer", keys.Send),
		from("composer.newline", "Composer", keys.Newline),
		from("composer.external-editor", "Composer", keys.ExternalEditor),
		from("composer.history-prev", "Composer", keys.HistoryPrev),
		from("composer.history-next", "Composer", keys.HistoryNext),
		from("composer.agent", "Composer", keys.Agent),
		from("composer.kill-word", "Composer", keys.KillWord),
		from("composer.word-back", "Composer", keys.WordBackward),
		from("composer.word-fwd", "Composer", keys.WordForward),
		from("composer.kill-line-start", "Composer", keys.KillLineStart),
		from("composer.kill-line-end", "Composer", keys.KillLineEnd),
		from("composer.yank", "Composer", keys.Yank),

		from("completion.prev", "Completion", keys.CompletionPrev),
		from("completion.next", "Completion", keys.CompletionNext),
		from("completion.accept", "Completion", keys.CompletionAccept),
		from("completion.dismiss", "Completion", keys.CompletionDismiss),

		{ID: "lists.move", Category: "Lists", Keys: "up/down/ctrl+p/ctrl+n", Action: "move selection"},
		{ID: "lists.move-jk", Category: "Lists", Keys: "j/k", Action: "move (pickers without filter)"},
		{ID: "lists.select", Category: "Lists", Keys: "enter", Action: "confirm selection"},
		{ID: "lists.filter", Category: "Lists", Keys: "type", Action: "filter (when available)"},
		{ID: "lists.logout", Category: "Lists", Keys: `\\ \\`, Action: "log out provider (within 3s)"},
		{ID: "lists.close", Category: "Lists", Keys: "esc", Action: "close"},
		{ID: "lists.default", Category: "Lists", Keys: "ctrl+d", Action: "save highlighted default"},

		{ID: "perm.choice", Category: "Permission", Keys: "left/right/h/l/tab", Action: "move choice"},
		{ID: "perm.once", Category: "Permission", Keys: "1/y", Action: "allow once"},
		{ID: "perm.session", Category: "Permission", Keys: "2/s", Action: "allow session"},
		{ID: "perm.project", Category: "Permission", Keys: "3/p", Action: "allow project"},
		{ID: "perm.reject", Category: "Permission", Keys: "4/n/esc", Action: "reject"},
	}
	return entries
}
