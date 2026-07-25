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
	// notifyAfterTurn is the minimum turn duration before an unfocused
	// completion notification is sent.
	notifyAfterTurn = 30 * time.Second
	// titleTopicMaxRunes caps the window-title topic length.
	titleTopicMaxRunes = 40
)

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

// sanitizeNotifyMessage drops control characters that would break OSC payloads.
func sanitizeNotifyMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\a' || isControlRune(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}
