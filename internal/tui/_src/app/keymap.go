package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

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
	PermissionMode    key.Binding
	SaveDefaults      key.Binding
	ScrollUp          key.Binding
	ScrollDown        key.Binding
	JumpBottom        key.Binding
	ToggleOrientation key.Binding
	// CopyLastResponse copies the last assistant message (not tool spam) via
	// OSC52. Global so it works while drafting a follow-up (alt+y).
	CopyLastResponse key.Binding
	// Tool cell selection/expand/copy/review/apply when the composer is empty
	// (enter still sends when there is text; y/v/a still type when the composer
	// has content; v/a only act with a selected tool cell).
	ToolPrev   key.Binding
	ToolNext   key.Binding
	ToolExpand key.Binding
	ToolCopy   key.Binding
	ToolReview key.Binding
	ToolApply  key.Binding

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
		Quit:       key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		FocusLeft:  key.NewBinding(key.WithKeys("ctrl+h"), key.WithHelp("ctrl+h", "focus left")),
		FocusRight: key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "focus right")),
		// CycleWindowNext: user chord is ctrl+j. Enhanced CSI rewrites to alt+j;
		// bare LF is also KeyCtrlJ (Ubuntu/legacy terminals). Both cycle panes —
		// never newline (#324). Newline is shift+enter → alt+enter only.
		CycleWindowNext:   key.NewBinding(key.WithKeys("alt+j", "ctrl+j"), key.WithHelp("ctrl+j", "next window")),
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
		// Newline: alt+enter is the post-WrapInput form of enhanced shift+enter
		// CSI. Bare LF (KeyCtrlJ) is ctrl+j pane cycle, not newline (#324).
		Newline:        key.NewBinding(key.WithKeys("alt+enter"), key.WithHelp("shift+enter", "newline")),
		ExternalEditor: key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("ctrl+e", "external editor")),
		HistoryPrev:    key.NewBinding(key.WithKeys("up"), key.WithHelp("up", "history previous")),
		HistoryNext:    key.NewBinding(key.WithKeys("down"), key.WithHelp("down", "history next")),
		// Agent cycles personas (build/plan/…), not concurrent root sessions.
		Agent: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "cycle agent persona")),
		// PermissionMode: shift+tab cycles tool-permission posture (not newline).
		PermissionMode: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "permission mode")),
		SaveDefaults:   key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "save defaults")),
		ScrollUp:       key.NewBinding(key.WithKeys("pgup", "ctrl+up"), key.WithHelp("pgup/ctrl+up", "scroll up")),
		ScrollDown:     key.NewBinding(key.WithKeys("pgdown", "ctrl+down"), key.WithHelp("pgdn/ctrl+down", "scroll down")),
		// JumpBottom: ctrl+t is the user chord. Bubble Tea names the legacy
		// control byte KeyCtrlT ("ctrl+t").
		JumpBottom: key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "jump to bottom")),
		// ToggleOrientation: user chord is ctrl+;. Bubble Tea has no KeyType for
		// ctrl+semicolon, so WrapInput rewrites enhanced ctrl+; CSI to alt+;
		// (same pattern as shift+enter → alt+enter for Newline).
		ToggleOrientation: key.NewBinding(key.WithKeys("alt+;"), key.WithHelp("ctrl+;", "toggle split")),
		// CopyLastResponse: alt+y stays off the printable path so it works with
		// composer text present (unlike bare y cell-copy).
		CopyLastResponse: key.NewBinding(key.WithKeys("alt+y"), key.WithHelp("alt+y", "copy last response")),
		// Tool cell nav: only when composer is empty (see Model.handleToolCellKeys).
		// alt+[/] avoid stealing printable brackets from the composer.
		ToolPrev:   key.NewBinding(key.WithKeys("alt+["), key.WithHelp("alt+[", "prev tool cell")),
		ToolNext:   key.NewBinding(key.WithKeys("alt+]"), key.WithHelp("alt+]", "next tool cell")),
		ToolExpand: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand tool / open file:line")),
		// ToolCopy: bare y when composer is empty (yank selected/latest cell,
		// including assistant/user chat text via OSC52).
		ToolCopy:   key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy cell")),
		ToolReview: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "review edit in editor")),
		ToolApply:  key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apply patch to worktree")),

		KillWord:      key.NewBinding(key.WithKeys("ctrl+w"), key.WithHelp("ctrl+w", "kill word backward")),
		WordBackward:  key.NewBinding(key.WithKeys("alt+b"), key.WithHelp("alt+b", "word backward")),
		WordForward:   key.NewBinding(key.WithKeys("alt+f"), key.WithHelp("alt+f", "word forward")),
		KillLineStart: key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "kill to line start")),
		KillLineEnd:   key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "kill to line end")),
		Yank:          key.NewBinding(key.WithKeys("ctrl+y"), key.WithHelp("ctrl+y", "yank")),

		// Leader chords navigate child/subagent transcripts, not concurrent roots
		// (use the agents pane n/enter/x for multi-root).
		Leader:            key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "subagent leader")),
		SessionChildFirst: key.NewBinding(key.WithKeys("down"), key.WithHelp("ctrl+x down", "enter subagent")),
		SessionParent:     key.NewBinding(key.WithKeys("up"), key.WithHelp("ctrl+x up", "parent session")),
		SessionChildNext:  key.NewBinding(key.WithKeys("right"), key.WithHelp("ctrl+x right", "next subagent")),
		SessionChildPrev:  key.NewBinding(key.WithKeys("left"), key.WithHelp("ctrl+x left", "prev subagent")),
	}
}

// agentsKeyMap is the agents-pane local bindings (not remappable). Help text is
// the source for /keys, the pane footer, and empty-state copy.
type agentsKeyMap struct {
	Move      key.Binding
	Open      key.Binding
	Spawn     key.Binding
	Interrupt key.Binding
	Hide      key.Binding
	Filter    key.Binding
}

func defaultAgentsKeyMap() agentsKeyMap {
	return agentsKeyMap{
		Move:      key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("j/k", "move")),
		Open:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "activate root")),
		Spawn:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new root")),
		Interrupt: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "interrupt root")),
		// Hide removes the row from the agents pane only; session JSONL stays.
		Hide:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "hide from pane")),
		Filter: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "cycle filter")),
	}
}

// applyOrientationKeys swaps focus vs cycle chords for vertical splits.
// Caller must start from a horizontal baseline (defaultKeyMap + overrides);
// horizontal is a no-op. Vertical swaps the pairs and updates help text.
func (k *keyMap) applyOrientationKeys(orient splitOrientation) {
	if orient != orientVertical {
		return
	}
	fl, fr := k.FocusLeft, k.FocusRight
	cn, cp := k.CycleWindowNext, k.CycleWindowPrev
	k.FocusLeft = rebindFrom(cn, "focus top")
	k.FocusRight = rebindFrom(cp, "focus bottom")
	k.CycleWindowNext = rebindFrom(fr, "next window")
	k.CycleWindowPrev = rebindFrom(fl, "prev window")
}

func rebindFrom(src key.Binding, desc string) key.Binding {
	keys := src.Keys()
	if len(keys) == 0 {
		return key.NewBinding(key.WithHelp("", desc))
	}
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(joinChordHelp(keys), desc))
}

func joinChordHelp(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return strings.Join(keys, "/")
}

// applyKeybindOverrides replaces binding keys (and help key text) for known ids.
// Unknown ids are ignored (config validation rejects them at load).
// Curated help labels (shift+enter, ctrl+;, leader chords) stay only when the
// override matches the stock key list for that binding.
func applyKeybindOverrides(k *keyMap, overrides map[string][]string) {
	if len(overrides) == 0 || k == nil {
		return
	}
	set := func(b *key.Binding, id string, curatedHelp string) {
		chords, ok := overrides[id]
		if !ok || len(chords) == 0 {
			return
		}
		desc := b.Help().Desc
		helpKey := joinChordHelp(chords)
		if curatedHelp != "" && sameKeys(chords, b.Keys()) {
			helpKey = curatedHelp
		}
		*b = key.NewBinding(key.WithKeys(chords...), key.WithHelp(helpKey, desc))
	}
	set(&k.FocusLeft, "nav.focus-left", "")
	set(&k.FocusRight, "nav.focus-right", "")
	set(&k.CycleWindowNext, "nav.window-next", "ctrl+j")
	set(&k.CycleWindowPrev, "nav.window-prev", "")
	set(&k.ScrollUp, "nav.scroll-up", "")
	set(&k.ScrollDown, "nav.scroll-down", "")
	set(&k.JumpBottom, "nav.jump-bottom", "")
	set(&k.ToggleOrientation, "nav.toggle-orient", "ctrl+;")
	set(&k.ToolPrev, "nav.tool-prev", "")
	set(&k.ToolNext, "nav.tool-next", "")
	set(&k.ToolExpand, "nav.tool-expand", "")
	set(&k.ToolCopy, "nav.tool-copy", "")
	set(&k.ToolReview, "nav.tool-review", "")
	set(&k.ToolApply, "nav.tool-apply", "")
	set(&k.Leader, "nav.leader", "")
	set(&k.SessionChildFirst, "nav.session-child", "ctrl+x down")
	set(&k.SessionParent, "nav.session-parent", "ctrl+x up")
	set(&k.SessionChildNext, "nav.session-next", "ctrl+x right")
	set(&k.SessionChildPrev, "nav.session-prev", "ctrl+x left")
	set(&k.Palette, "global.palette", "")
	set(&k.KeyHelp, "global.keyhelp", "")
	set(&k.Interrupt, "global.interrupt", "")
	set(&k.Quit, "global.quit", "")
	set(&k.SaveDefaults, "global.save-defaults", "")
	set(&k.CopyLastResponse, "global.copy-last", "")
	set(&k.TerminalLeave, "editor.leave", "")
	set(&k.Send, "composer.send", "")
	set(&k.Newline, "composer.newline", "shift+enter")
	set(&k.ExternalEditor, "composer.external-editor", "")
	set(&k.HistoryPrev, "composer.history-prev", "")
	set(&k.HistoryNext, "composer.history-next", "")
	set(&k.Agent, "composer.agent", "")
	set(&k.PermissionMode, "composer.permission-mode", "shift+tab")
	set(&k.KillWord, "composer.kill-word", "")
	set(&k.WordBackward, "composer.word-back", "")
	set(&k.WordForward, "composer.word-fwd", "")
	set(&k.KillLineStart, "composer.kill-line-start", "")
	set(&k.KillLineEnd, "composer.kill-line-end", "")
	set(&k.Yank, "composer.yank", "")
	set(&k.CompletionPrev, "completion.prev", "")
	set(&k.CompletionNext, "completion.next", "")
	set(&k.CompletionAccept, "completion.accept", "")
	set(&k.CompletionDismiss, "completion.dismiss", "")
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// buildKeyMap returns defaults with config overrides applied, then orientation.
func buildKeyMap(overrides map[string][]string, orient splitOrientation) keyMap {
	km := defaultKeyMap()
	applyKeybindOverrides(&km, overrides)
	km.applyOrientationKeys(orient)
	return km
}

// keybindEntry is one row in the filterable keybind cheatsheet.
type keybindEntry struct {
	ID       string
	Category string
	Keys     string
	Action   string
	// Context marks rows promoted into the "Current focus" section for the
	// pane/window that was focused when the cheatsheet opened.
	Context bool
}

// keysModalContext selects which catalog rows lead the /keys cheatsheet.
// Zero value leaves catalog order unchanged (no Current focus section).
type keysModalContext struct {
	Label      string   // short title suffix: "composer", "agents", …
	Categories []string // catalog Category values to promote
	IDPrefixes []string // catalog ID prefixes to promote (e.g. "nav.tool-")
}

// keysModalContextFor maps pane focus + active right-pane window to cheatsheet
// priority. Left focus → composer/transcript; right focus → that window's binds.
func keysModalContextFor(focus paneFocus, windowID string) keysModalContext {
	if focus == focusRight {
		switch windowID {
		case agentsWindowID:
			return keysModalContext{Label: "agents", Categories: []string{"Agents"}}
		case terminalWindowID:
			return keysModalContext{Label: "editor", Categories: []string{"Editor"}}
		case filesWindowID:
			return keysModalContext{Label: "files", Categories: []string{"Lists"}}
		case memoryWindowID:
			return keysModalContext{Label: "memory", Categories: []string{"Lists"}}
		case issuesWindowID:
			return keysModalContext{Label: "issues", Categories: []string{"Lists"}}
		case "":
			return keysModalContext{Label: "right pane", Categories: []string{"Navigation"}}
		default:
			return keysModalContext{Label: windowID, Categories: []string{"Navigation"}}
		}
	}
	// Left pane: composer editing plus transcript scroll/tool-cell chords.
	return keysModalContext{
		Label:      "composer",
		Categories: []string{"Composer"},
		IDPrefixes: []string{"nav.scroll-", "nav.jump-", "nav.tool-"},
	}
}

// orderKeybindEntries puts current-focus rows first (catalog-relative order
// preserved within each group). Category matches lead, then ID-prefix matches,
// then the remaining catalog. Does not drop any binds.
func orderKeybindEntries(entries []keybindEntry, ctx keysModalContext) []keybindEntry {
	if len(entries) == 0 {
		return entries
	}
	if ctx.Label == "" && len(ctx.Categories) == 0 && len(ctx.IDPrefixes) == 0 {
		return entries
	}
	cats := make(map[string]struct{}, len(ctx.Categories))
	for _, c := range ctx.Categories {
		if c != "" {
			cats[c] = struct{}{}
		}
	}
	byCat := make([]keybindEntry, 0, len(entries))
	byPref := make([]keybindEntry, 0, len(entries))
	rest := make([]keybindEntry, 0, len(entries))
	for _, e := range entries {
		if _, ok := cats[e.Category]; ok {
			e.Context = true
			byCat = append(byCat, e)
			continue
		}
		pref := false
		for _, p := range ctx.IDPrefixes {
			if p != "" && strings.HasPrefix(e.ID, p) {
				pref = true
				break
			}
		}
		if pref {
			e.Context = true
			byPref = append(byPref, e)
			continue
		}
		rest = append(rest, e)
	}
	if len(byCat)+len(byPref) == 0 {
		return entries
	}
	out := make([]keybindEntry, 0, len(entries))
	out = append(out, byCat...)
	out = append(out, byPref...)
	out = append(out, rest...)
	return out
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
		from("nav.tool-review", "Navigation", keys.ToolReview),
		from("nav.tool-apply", "Navigation", keys.ToolApply),
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
		from("global.copy-last", "Global", keys.CopyLastResponse),
		from("editor.leave", "Editor", keys.TerminalLeave),

		from("composer.send", "Composer", keys.Send),
		from("composer.newline", "Composer", keys.Newline),
		from("composer.external-editor", "Composer", keys.ExternalEditor),
		from("composer.history-prev", "Composer", keys.HistoryPrev),
		from("composer.history-next", "Composer", keys.HistoryNext),
		from("composer.agent", "Composer", keys.Agent),
		from("composer.permission-mode", "Composer", keys.PermissionMode),
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
	}
	// Agents pane (concurrent roots). Not remappable; help from agentsKeyMap.
	ak := defaultAgentsKeyMap()
	entries = append(entries,
		from("agents.move", "Agents", ak.Move),
		from("agents.open", "Agents", ak.Open),
		from("agents.spawn", "Agents", ak.Spawn),
		from("agents.interrupt", "Agents", ak.Interrupt),
		from("agents.hide", "Agents", ak.Hide),
		from("agents.filter", "Agents", ak.Filter),
		keybindEntry{ID: "lists.move", Category: "Lists", Keys: "up/down/ctrl+p/ctrl+n", Action: "move selection"},
		keybindEntry{ID: "lists.move-jk", Category: "Lists", Keys: "j/k", Action: "move (pickers without filter)"},
		keybindEntry{ID: "lists.select", Category: "Lists", Keys: "enter", Action: "confirm selection"},
		keybindEntry{ID: "lists.filter", Category: "Lists", Keys: "type", Action: "filter (when available)"},
		keybindEntry{ID: "lists.logout", Category: "Lists", Keys: "ctrl+x", Action: "log out provider (confirm y/n)"},
		keybindEntry{ID: "lists.close", Category: "Lists", Keys: "esc", Action: "close"},
		keybindEntry{ID: "lists.default", Category: "Lists", Keys: "ctrl+d", Action: "save highlighted default"},
		keybindEntry{ID: "perm.choice", Category: "Permission", Keys: "left/right/h/l/tab", Action: "move choice"},
		keybindEntry{ID: "perm.once", Category: "Permission", Keys: "1/y", Action: "allow once"},
		keybindEntry{ID: "perm.session", Category: "Permission", Keys: "2/s", Action: "allow session"},
		keybindEntry{ID: "perm.project", Category: "Permission", Keys: "3/p", Action: "allow project"},
		keybindEntry{ID: "perm.reject", Category: "Permission", Keys: "4/n/esc", Action: "reject"},
	)
	return entries
}
