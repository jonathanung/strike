package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) updateComposer(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := m.composer.Value()
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	if cleaned := stripComposerOSCLeak(m.composer.Value()); cleaned != m.composer.Value() {
		m.composer.SetValue(cleaned)
	}
	if m.historyPos >= 0 && m.composer.Value() != before {
		m.resetHistoryBrowsing()
	}
	if m.composer.Value() != before {
		m.pendingPastes = prunePendingPastes(m.composer.Value(), m.pendingPastes)
		m.pendingImages = prunePendingImages(m.composer.Value(), m.pendingImages)
	}
	m.recomputeCompletion()
	m.reflow()
	return m, cmd
}

// applyComposerReadline handles focusLeft readline chords so they are not
// stolen by window-cycle / focus bindings (notably ctrl+k). Palette and other
// global chords are matched earlier and remain global.
//
// ctrl+k only claims the event when it deletes text; at EOL / empty composer it
// falls through so vertical FocusRight and horizontal CycleWindowPrev still work.
func (m Model) applyComposerReadline(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keyMap.Yank):
		if m.killBuf == "" {
			return m, nil, true
		}
		m.resetHistoryBrowsing()
		m.composer.InsertString(m.killBuf)
		m.recomputeCompletion()
		m.reflow()
		return m, nil, true
	case key.Matches(msg, m.keyMap.WordBackward), key.Matches(msg, m.keyMap.WordForward):
		next, cmd := m.updateComposer(msg)
		return next.(Model), cmd, true
	case key.Matches(msg, m.keyMap.KillWord), key.Matches(msg, m.keyMap.KillLineStart):
		before := m.composer.Value()
		next, cmd := m.updateComposer(msg)
		nm := next.(Model)
		if killed, ok := contiguousDeletion(before, nm.composer.Value()); ok {
			nm.killBuf = killed
		}
		return nm, cmd, true
	case key.Matches(msg, m.keyMap.KillLineEnd):
		before := m.composer.Value()
		next, cmd := m.updateComposer(msg)
		nm := next.(Model)
		killed, ok := contiguousDeletion(before, nm.composer.Value())
		if !ok {
			// No deletion — leave the key for nav (cycle prev / focus bottom).
			return m, nil, false
		}
		nm.killBuf = killed
		return nm, cmd, true
	default:
		return m, nil, false
	}
}

// contiguousDeletion returns the single deleted span when after is before with
// one contiguous rune range removed (kill-word / kill-line style edits).
func contiguousDeletion(before, after string) (string, bool) {
	br, ar := []rune(before), []rune(after)
	if len(ar) >= len(br) {
		return "", false
	}
	i := 0
	for i < len(ar) && br[i] == ar[i] {
		i++
	}
	deleted := len(br) - len(ar)
	if i+deleted > len(br) {
		return "", false
	}
	if string(br[i+deleted:]) != string(ar[i:]) {
		return "", false
	}
	killed := string(br[i : i+deleted])
	if killed == "" {
		return "", false
	}
	return killed, true
}

func (m *Model) recomputeCompletion() {
	if m.historyPos >= 0 {
		m.completion = nil
		return
	}
	line := m.composer.Line()
	info := m.composer.LineInfo()
	col := info.StartColumn + info.ColumnOffset
	value := m.composer.Value()
	m.completion = leadingSlashCompletion(value, line, col, m.commands)
	if m.completion != nil {
		return
	}
	m.completion = m.atFileCompletionAt(value, line, col)
}

// atFileCompletionAt runs @file fuzzy search when Files is available.
func (m *Model) atFileCompletionAt(value string, line, col int) *completionState {
	if m.services.Files == nil {
		return nil
	}
	query, ok := activeAtQueryParts(value, line, col)
	if !ok {
		return nil
	}
	paths, err := m.services.Files.SearchFiles(query, 30)
	if err != nil || len(paths) == 0 {
		return nil
	}
	return atFileCompletion(value, line, col, paths)
}

func activeAtQueryParts(value string, row, col int) (string, bool) {
	lines := strings.Split(value, "\n")
	if row < 0 || row >= len(lines) {
		return "", false
	}
	line := []rune(lines[row])
	if col < 0 || col > len(line) {
		return "", false
	}
	end := col
	start := end
	for start > 0 {
		r := line[start-1]
		if isFileMentionPathRune(r) {
			start--
			continue
		}
		if r == '@' {
			start--
			break
		}
		return "", false
	}
	if start >= end || start >= len(line) || line[start] != '@' {
		return "", false
	}
	if start > 0 && !unicode.IsSpace(line[start-1]) {
		return "", false
	}
	if col <= start {
		return "", false
	}
	return string(line[start+1 : end]), true
}

func (m *Model) applyCompletion() {
	if m.completion == nil || m.completion.Selected >= len(m.completion.Candidates) {
		return
	}
	candidate := m.completion.Candidates[m.completion.Selected]
	replacement := m.completion.Replace
	value := []rune(m.composer.Value())
	if replacement.Start < 0 || replacement.End < replacement.Start || replacement.End > len(value) {
		m.completion = nil
		m.reflow()
		return
	}
	var name []rune
	delimiter := []rune(nil)
	if candidate.Path != "" {
		name = []rune("@" + candidate.Path)
		if replacement.End == len(value) || !unicode.IsSpace(value[replacement.End]) {
			delimiter = []rune(" ")
		}
	} else {
		name = []rune(candidate.Spec.Name)
		if candidate.Source == commandSourceSkill && (replacement.End == len(value) || !unicode.IsSpace(value[replacement.End])) {
			delimiter = []rune(" ")
		}
	}
	next := make([]rune, 0, len(value)-(replacement.End-replacement.Start)+len(name)+len(delimiter))
	next = append(next, value[:replacement.Start]...)
	next = append(next, name...)
	next = append(next, delimiter...)
	next = append(next, value[replacement.End:]...)
	m.setComposerValueAt(string(next), replacement.Start+len(name)+len(delimiter))
	m.completion = nil
	m.reflow()
}

func (m *Model) setComposerValueAt(value string, offset int) {
	runes := []rune(value)
	offset = max(0, min(offset, len(runes)))
	targetRow, targetCol := 0, 0
	for _, r := range runes[:offset] {
		if r == '\n' {
			targetRow++
			targetCol = 0
		} else {
			targetCol++
		}
	}
	m.composer.SetValue(value)
	// History/completion replacements are plain text; drop chips that no
	// longer appear (or clear all when the value is wholly replaced).
	m.pendingPastes = prunePendingPastes(value, m.pendingPastes)
	m.pendingImages = prunePendingImages(value, m.pendingImages)
	for steps := 0; m.composer.Line() > targetRow && steps <= len(runes)+1; steps++ {
		m.composer.CursorUp()
	}
	m.composer.SetCursor(targetCol)
}

func (m *Model) resetComposer() {
	m.composer.Reset()
	m.pendingPastes = nil
	m.pendingImages = nil
	m.completion = nil
	m.resetHistoryBrowsing()
	m.reflow()
}

func (m *Model) handleHistoryKey(msg tea.KeyMsg) bool {
	if m.historyPos >= 0 {
		switch {
		case key.Matches(msg, m.keyMap.HistoryPrev):
			if m.historyPos > 0 {
				m.historyPos--
			}
			m.recallHistory(m.entries[m.historyPos])
			return true
		case key.Matches(msg, m.keyMap.HistoryNext):
			if m.historyPos < len(m.entries)-1 {
				m.historyPos++
				m.recallHistory(m.entries[m.historyPos])
			} else {
				draft := m.historyDraft
				m.resetHistoryBrowsing()
				m.setComposerValueAt(draft, len([]rune(draft)))
				m.recomputeCompletion()
				m.reflow()
			}
			return true
		}
	}
	if !key.Matches(msg, m.keyMap.HistoryPrev) || m.composer.Value() != "" || len(m.entries) == 0 {
		return false
	}
	m.historyDraft = m.composer.Value()
	m.historyPos = len(m.entries) - 1
	m.recallHistory(m.entries[m.historyPos])
	return true
}

func (m *Model) recallHistory(prompt string) {
	m.setComposerValueAt(prompt, len([]rune(prompt)))
	m.recomputeCompletion()
	m.reflow()
}

func (m *Model) resetHistoryBrowsing() {
	m.historyPos = -1
	m.historyDraft = ""
}
