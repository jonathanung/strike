package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Large multi-line pastes collapse to a chip in the composer so they do not
// flood the textarea. Full text is retained and expanded on send.
const (
	largePasteMinLines = 3
	largePasteMinRunes = 1000
)

// pasteChip is one collapsed paste: Placeholder appears in the composer Value,
// Content is the full text restored before submit.
type pasteChip struct {
	Placeholder string
	Content     string
}

func normalizePaste(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func pasteLineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n") + 1
	if strings.HasSuffix(s, "\n") {
		n--
	}
	return n
}

func isLargePaste(s string) bool {
	if s == "" {
		return false
	}
	if pasteLineCount(s) >= largePasteMinLines {
		return true
	}
	return utf8.RuneCountInString(s) >= largePasteMinRunes
}

func pastePlaceholderLabel(lines int, suffix int) string {
	if suffix <= 1 {
		return fmt.Sprintf("[pasted %d lines]", lines)
	}
	return fmt.Sprintf("[pasted %d lines #%d]", lines, suffix)
}

func expandPendingPastes(text string, pastes []pasteChip) string {
	if len(pastes) == 0 || text == "" {
		return text
	}
	// Longer placeholders first so "#2" variants are not partially matched by
	// a shorter same-prefix label (we use distinct full strings, but keep
	// replacement order stable and longest-first for safety).
	ordered := make([]pasteChip, len(pastes))
	copy(ordered, pastes)
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if len(ordered[j].Placeholder) > len(ordered[i].Placeholder) {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}
	for _, p := range ordered {
		if p.Placeholder == "" {
			continue
		}
		text = strings.ReplaceAll(text, p.Placeholder, p.Content)
	}
	return text
}

func prunePendingPastes(value string, pastes []pasteChip) []pasteChip {
	if len(pastes) == 0 {
		return nil
	}
	kept := make([]pasteChip, 0, len(pastes))
	for _, p := range pastes {
		if p.Placeholder != "" && strings.Contains(value, p.Placeholder) {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func (m *Model) pastePlaceholderInUse(label string) bool {
	if strings.Contains(m.composer.Value(), label) {
		return true
	}
	for _, p := range m.pendingPastes {
		if p.Placeholder == label {
			return true
		}
	}
	return false
}

func (m *Model) nextPastePlaceholder(lines int) string {
	for suffix := 1; ; suffix++ {
		label := pastePlaceholderLabel(lines, suffix)
		if !m.pastePlaceholderInUse(label) {
			return label
		}
	}
}

// handleComposerPaste inserts a bracketed paste. Images become an [image N]
// chip; large text pastes become a line chip; small pastes insert verbatim.
func (m *Model) handleComposerPaste(raw string) {
	if m.tryAttachImagePaste(raw) {
		m.pendingPastes = prunePendingPastes(m.composer.Value(), m.pendingPastes)
		m.pendingImages = prunePendingImages(m.composer.Value(), m.pendingImages)
		return
	}
	text := normalizePaste(raw)
	if text == "" {
		return
	}
	m.resetHistoryBrowsing()
	if !isLargePaste(text) {
		m.composer.InsertString(text)
		m.pendingPastes = prunePendingPastes(m.composer.Value(), m.pendingPastes)
		m.pendingImages = prunePendingImages(m.composer.Value(), m.pendingImages)
		return
	}
	lines := pasteLineCount(text)
	if lines < 1 {
		lines = 1
	}
	placeholder := m.nextPastePlaceholder(lines)
	m.pendingPastes = append(m.pendingPastes, pasteChip{
		Placeholder: placeholder,
		Content:     text,
	})
	m.composer.InsertString(placeholder)
}

// composerTextExpanded returns the composer value with paste chips restored.
func (m *Model) composerTextExpanded() string {
	return expandPendingPastes(m.composer.Value(), m.pendingPastes)
}
