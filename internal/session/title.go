package session

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// titleMaxRunes caps auto-derived session titles for pickers and panel chrome.
// Kept brief so agents-pane and session lists stay scannable.
const titleMaxRunes = 32

// TitleFromText derives a display title from free-form user text: trim,
// collapse whitespace, drop controls, truncate. Empty when nothing usable remains.
func TitleFromText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range text {
		switch {
		case r == '\u00a0':
			r = ' '
			fallthrough
		case unicode.IsSpace(r):
			if b.Len() == 0 || prevSpace {
				continue
			}
			b.WriteByte(' ')
			prevSpace = true
		case r < 0x20 || r == 0x7f:
			// drop C0 controls and DEL
			continue
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= titleMaxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:titleMaxRunes])
}

// TitleFromEvents returns the session title recorded in events. The last
// SessionTitled wins; otherwise the first UserMessage is derived via
// TitleFromText. Empty when neither yields a title.
func TitleFromEvents(events []protocol.Event) string {
	var fromMessage string
	var titled string
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.SessionTitled:
			if t := strings.TrimSpace(e.Title); t != "" {
				titled = t
			}
		case protocol.UserMessage:
			if fromMessage == "" {
				fromMessage = TitleFromText(e.Text)
			}
		}
	}
	if titled != "" {
		return titled
	}
	return fromMessage
}
