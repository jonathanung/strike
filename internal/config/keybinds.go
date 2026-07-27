package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// KeybindChords is one or more key sequences for a remappable binding id.
// JSON accepts a string ("ctrl+t") or a string array (["pgup","ctrl+up"]).
type KeybindChords []string

// UnmarshalJSON accepts a JSON string or array of strings.
func (k *KeybindChords) UnmarshalJSON(data []byte) error {
	data = bytesTrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*k = nil
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*k = KeybindChords{s}
		return nil
	}
	var ss []string
	if err := json.Unmarshal(data, &ss); err != nil {
		return fmt.Errorf("keybind value must be a string or array of strings: %w", err)
	}
	*k = KeybindChords(ss)
	return nil
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

// KnownKeybindIDs lists remappable binding ids (app-level keyMap / cheatsheet rows).
// Modal list and permission conventions are not remappable.
var KnownKeybindIDs = map[string]struct{}{
	"nav.focus-left":           {},
	"nav.focus-right":          {},
	"nav.window-next":          {},
	"nav.window-prev":          {},
	"nav.scroll-up":            {},
	"nav.scroll-down":          {},
	"nav.jump-bottom":          {},
	"nav.toggle-orient":        {},
	"nav.tool-prev":            {},
	"nav.tool-next":            {},
	"nav.tool-expand":          {},
	"nav.tool-copy":            {},
	"nav.tool-review":          {},
	"nav.tool-apply":           {},
	"nav.leader":               {},
	"nav.session-child":        {},
	"nav.session-parent":       {},
	"nav.session-next":         {},
	"nav.session-prev":         {},
	"global.palette":           {},
	"global.keyhelp":           {},
	"global.interrupt":         {},
	"global.quit":              {},
	"global.save-defaults":     {},
	"editor.leave":             {},
	"composer.send":            {},
	"composer.newline":         {},
	"composer.external-editor": {},
	"composer.history-prev":    {},
	"composer.history-next":    {},
	"composer.agent":           {},
	"composer.permission-mode": {},
	"composer.kill-word":       {},
	"composer.word-back":       {},
	"composer.word-fwd":        {},
	"composer.kill-line-start": {},
	"composer.kill-line-end":   {},
	"composer.yank":            {},
	"completion.prev":          {},
	"completion.next":          {},
	"completion.accept":        {},
	"completion.dismiss":       {},
}

// criticalKeybindIDs cannot be cleared or disabled via config.
var criticalKeybindIDs = map[string]struct{}{
	"global.quit":      {},
	"global.interrupt": {},
}

// ValidateKeybinds checks binding ids and chord sequences. Unknown ids and
// invalid/empty chords return a clear error. Critical quit/interrupt bindings
// cannot be emptied (no disable path without an escape hatch).
//
// Chord conflicts across actions are allowed: many defaults share keys in
// different UI contexts (e.g. alt+enter for newline vs tool expand when the
// composer is empty). Routing order in the TUI decides the winner; project
// layer overrides global per id (last-wins).
func ValidateKeybinds(binds map[string]KeybindChords) error {
	if len(binds) == 0 {
		return nil
	}
	for id, chords := range binds {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("keybinds: empty binding id")
		}
		if _, ok := KnownKeybindIDs[id]; !ok {
			return fmt.Errorf("keybinds: unknown binding id %q", id)
		}
		normalized, err := normalizeChords(chords)
		if err != nil {
			return fmt.Errorf("keybinds %q: %w", id, err)
		}
		if len(normalized) == 0 {
			if _, crit := criticalKeybindIDs[id]; crit {
				return fmt.Errorf("keybinds %q: cannot disable critical binding (quit/interrupt require at least one chord)", id)
			}
			return fmt.Errorf("keybinds %q: at least one key sequence is required", id)
		}
		binds[id] = normalized
	}
	return nil
}

// MergeKeybinds returns a copy of base with layer ids overwriting (last-wins).
func MergeKeybinds(base, layer map[string]KeybindChords) map[string]KeybindChords {
	if len(base) == 0 && len(layer) == 0 {
		return nil
	}
	out := make(map[string]KeybindChords, len(base)+len(layer))
	for id, chords := range base {
		out[id] = append(KeybindChords(nil), chords...)
	}
	for id, chords := range layer {
		out[id] = append(KeybindChords(nil), chords...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// KeybindsMap copies validated chords into a plain map for host/TUI options.
func KeybindsMap(binds map[string]KeybindChords) map[string][]string {
	if len(binds) == 0 {
		return nil
	}
	out := make(map[string][]string, len(binds))
	for id, chords := range binds {
		out[id] = append([]string(nil), chords...)
	}
	return out
}

func normalizeChords(chords KeybindChords) (KeybindChords, error) {
	if len(chords) == 0 {
		return nil, nil
	}
	out := make(KeybindChords, 0, len(chords))
	seen := map[string]struct{}{}
	for _, raw := range chords {
		s := strings.TrimSpace(raw)
		if s == "" {
			return nil, fmt.Errorf("empty key sequence")
		}
		s = strings.ToLower(s)
		if err := validateChord(s); err != nil {
			return nil, err
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}

// validateChord accepts Bubble Tea key.String forms used by strike bindings
// (ctrl+c, alt+enter, f1, pgup, alt+[, y, …).
func validateChord(s string) error {
	if len(s) > 40 {
		return fmt.Errorf("key sequence %q is too long", s)
	}
	for _, r := range s {
		if r > unicode.MaxASCII || unicode.IsControl(r) {
			return fmt.Errorf("key sequence %q contains invalid characters", s)
		}
		if unicode.IsSpace(r) {
			return fmt.Errorf("key sequence %q must not contain whitespace", s)
		}
	}
	// Single printable key (letter, digit, or common punctuation used in binds).
	if !strings.Contains(s, "+") {
		if isPlainKey(s) {
			return nil
		}
		return fmt.Errorf("invalid key sequence %q", s)
	}
	parts := strings.Split(s, "+")
	if len(parts) < 2 {
		return fmt.Errorf("invalid key sequence %q", s)
	}
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("invalid key sequence %q", s)
		}
		if i < len(parts)-1 {
			switch p {
			case "ctrl", "alt", "shift", "meta", "cmd", "super":
				// ok
			default:
				return fmt.Errorf("invalid modifier %q in %q", p, s)
			}
			continue
		}
		if !isPlainKey(p) {
			return fmt.Errorf("invalid key %q in %q", p, s)
		}
	}
	return nil
}

func isPlainKey(s string) bool {
	switch s {
	case "enter", "return", "esc", "escape", "tab", "space", "backspace",
		"delete", "del", "up", "down", "left", "right", "home", "end",
		"pgup", "pgdown", "pgdn", "insert":
		return true
	}
	if len(s) >= 2 && s[0] == 'f' {
		n := s[1:]
		if n == "10" || n == "11" || n == "12" {
			return true
		}
		if len(n) == 1 && n[0] >= '1' && n[0] <= '9' {
			return true
		}
	}
	if len(s) == 1 {
		r := rune(s[0])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
		switch r {
		case '[', ']', ';', ',', '.', '/', '\\', '\'', '`', '-', '=',
			'!', '@', '#', '$', '%', '^', '&', '*', '(', ')', '_', '+',
			'{', '}', '|', ':', '"', '<', '>', '?':
			return true
		}
	}
	return false
}
