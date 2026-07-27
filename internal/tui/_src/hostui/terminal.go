package tui

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	// authExpiryWarn is how soon before OAuth expiry the header/notice warn.
	authExpiryWarn = 24 * time.Hour
	// notifyAfterTurn is the minimum turn duration before a completion
	// notification is sent (avoids spam on short turns).
	notifyAfterTurn = 30 * time.Second
	// titleTopicMaxRunes caps the window-title topic length (brief labels).
	titleTopicMaxRunes = 32
	// notifyMessageMaxRunes caps OSC 9 desktop notification body length.
	notifyMessageMaxRunes = 120
)

// NotifyMode selects when desktop notifications (OSC 9 + bell) fire.
type NotifyMode string

const (
	// NotifyUnfocusedOnly fires when the terminal is unfocused, or when focus
	// reporting never arrived (heuristic: treat as may-be-unfocused). Default.
	NotifyUnfocusedOnly NotifyMode = "unfocused-only"
	// NotifyOn always fires for attention events and long turn completion.
	NotifyOn NotifyMode = "on"
	// NotifyOff disables desktop notifications.
	NotifyOff NotifyMode = "off"
)

// ParseNotifyMode resolves a config value. Empty yields unfocused-only.
func ParseNotifyMode(value string) (NotifyMode, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(NotifyUnfocusedOnly), "unfocused", "blur":
		return NotifyUnfocusedOnly, true
	case string(NotifyOn), "true", "1", "yes", "always":
		return NotifyOn, true
	case string(NotifyOff), "false", "0", "no", "never":
		return NotifyOff, true
	default:
		return "", false
	}
}

// windowTitle builds the terminal title: "strike", or "strike — {topic}"
// when a topic (first user message, else short session id) is available.
func windowTitle(m Model) string {
	topic := strings.TrimSpace(m.titleTopic)
	if topic == "" {
		topic = shortSessionID(m.sessionID)
	}
	topic = sanitizeTitleTopic(topic)
	if topic == "" {
		return "strike"
	}
	return "strike — " + topic
}

// shortSessionID returns a compact session id fragment for the title bar.
func shortSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	// Prefer the trailing segment when ids look like path-ish or UUID-ish.
	if i := strings.LastIndexAny(id, "/-_"); i >= 0 && i+1 < len(id) {
		tail := id[i+1:]
		if utf8.RuneCountInString(tail) >= 6 {
			id = tail
		}
	}
	return truncateRunes(id, 12)
}

// sanitizeTitleTopic strips controls and collapses whitespace for OSC titles.
func sanitizeTitleTopic(s string) string {
	s = sanitizeDisplayData(s)
	s = strings.Join(strings.Fields(s), " ")
	return truncateRunes(s, titleTopicMaxRunes)
}

// truncateRunes returns the first n runes of s (n < 1 yields empty).
func truncateRunes(s string, n int) string {
	if n < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

// shouldDesktopNotify reports whether a desktop notification should fire.
// attention is true for permission/question (always eligible when mode allows);
// false for turn-complete (also requires a long turn).
func (m Model) shouldDesktopNotify(attention bool) bool {
	mode := m.notifyMode
	if mode == "" {
		mode = NotifyUnfocusedOnly
	}
	switch mode {
	case NotifyOff:
		return false
	case NotifyOn:
		if attention {
			return true
		}
		return m.longTurnElapsed()
	default: // unfocused-only
		if !m.notifyFocusAllows() {
			return false
		}
		if attention {
			return true
		}
		return m.longTurnElapsed()
	}
}

// notifyFocusAllows is true when the terminal is unfocused, or when focus
// reporting has never been observed (heuristic fallback).
func (m Model) notifyFocusAllows() bool {
	if !m.focusKnown {
		return true
	}
	return !m.focused
}

func (m Model) longTurnElapsed() bool {
	return !m.turnStartedAt.IsZero() && time.Since(m.turnStartedAt) >= notifyAfterTurn
}

// desktopNotifyCmd returns a bell+OSC9 cmd when gating allows, else nil.
// Messages must be static/safe — never include secrets or user content.
func (m Model) desktopNotifyCmd(message string, attention bool) tea.Cmd {
	if !m.shouldDesktopNotify(attention) {
		return nil
	}
	return notifyUnfocusedCmd(message)
}

// notifyUnfocusedCmd rings the terminal bell and emits OSC 9 (desktop
// notification) on stderr so altscreen stdout is undisturbed.
func notifyUnfocusedCmd(message string) tea.Cmd {
	message = sanitizeNotifyMessage(message)
	if message == "" {
		return nil
	}
	return func() tea.Msg {
		// BEL + OSC 9 ; message BEL (iTerm2 / compatible terminals).
		_, _ = fmt.Fprintf(os.Stderr, "\a\033]9;%s\a", message)
		return nil
	}
}

// sanitizeNotifyMessage drops control characters that would break OSC payloads
// and caps length so notification text cannot carry large/secret blobs.
func sanitizeNotifyMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\a' || isControlRune(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return truncateRunes(s, notifyMessageMaxRunes)
}
