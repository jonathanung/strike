package protocol

import (
	"strings"
	"unicode/utf8"
)

// RewindPoint is a fork-at-turn candidate: keep the first KeepEvents of the
// source log (0-based exclusive end index = keep count).
type RewindPoint struct {
	// KeepEvents is how many leading events the forked session should contain.
	KeepEvents int
	// Turn is the 1-based completed user turn index ending at this point.
	Turn int
	// Preview is a short label from the user message that started the turn.
	Preview string
}

// RewindPoints lists inclusive keep-counts at each completed root user turn.
// Child-lineage events are ignored for turn boundaries. SessionRewound drops
// the prior completed-turn point so the list matches restore/transcript view.
func RewindPoints(events []Event) []RewindPoint {
	var out []RewindPoint
	turn := 0
	var preview string
	inTurn := false
	for i, ev := range events {
		if !rewindRootEvent(ev) {
			continue
		}
		switch e := ev.(type) {
		case UserMessage:
			turn++
			preview = rewindPreview(e.Text)
			inTurn = true
		case TurnCompleted:
			if !inTurn {
				continue
			}
			out = append(out, RewindPoint{
				KeepEvents: i + 1,
				Turn:       turn,
				Preview:    preview,
			})
			inTurn = false
			preview = ""
		case SessionRewound:
			if len(out) > 0 {
				out = out[:len(out)-1]
				if turn > 0 {
					turn--
				}
			}
			inTurn = false
			preview = ""
		}
	}
	return out
}

func rewindRootEvent(ev Event) bool {
	switch e := ev.(type) {
	case UserMessage:
		return e.ParentSessionID == "" && e.Depth == 0
	case TurnCompleted:
		return e.ParentSessionID == "" && e.Depth == 0
	case SessionRewound:
		return e.ParentSessionID == "" && e.Depth == 0
	default:
		return true
	}
}

func rewindPreview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	const maxRunes = 48
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes-1]) + "…"
}
