package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func (m Model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keyMap.Quit) {
		return m, tea.Quit
	}
	if m.modal != nil {
		var cmd tea.Cmd
		m.modal, cmd = m.modal.update(msg)
		if m.modal == nil {
			cmd = tea.Batch(cmd, m.afterModalClosed())
			m.refreshAwaitingPermission()
		}
		m.reflow()
		return m, cmd
	}
	// Leader chords before other routing so ctrl+x down is not eaten.
	if m.leaderArmed {
		if handled, cmd := m.handleLeaderKey(msg); handled {
			m.reflow()
			return m, cmd
		}
	}
	if key.Matches(msg, m.keyMap.Leader) {
		m.completion = nil
		return m, m.armLeader()
	}
	if handled, cmd := m.handleSessionNavKeys(msg); handled {
		m.reflow()
		return m, cmd
	}
	if m.focus == focusLeft && m.completion != nil {
		switch {
		case key.Matches(msg, m.keyMap.CompletionDismiss):
			m.completion = nil
			m.reflow()
			return m, nil
		case key.Matches(msg, m.keyMap.CompletionAccept):
			m.applyCompletion()
			return m, nil
		case key.Matches(msg, m.keyMap.CompletionPrev):
			m.completion.move(-1)
			m.reflow()
			return m, nil
		case key.Matches(msg, m.keyMap.CompletionNext):
			m.completion.move(1)
			m.reflow()
			return m, nil
		case key.Matches(msg, m.keyMap.Newline):
			m.composer.InsertString("\n")
			m.recomputeCompletion()
			m.reflow()
			return m, nil
		}
	}
	// Composer readline before nav chords so ctrl+k kills in the input
	// instead of cycling windows / focusing the right pane.
	if m.focus == focusLeft {
		if next, cmd, ok := m.applyComposerReadline(msg); ok {
			return next, cmd
		}
		// Composer newline before focus/cycle: bare LF (KeyCtrlJ) is how
		// many terminals deliver shift+enter without enhanced keys. It must
		// insert "\n", never cycle. Intentional ctrl+j is rewritten to
		// alt+j and matches CycleWindowNext / Focus* below (#240).
		if key.Matches(msg, m.keyMap.Newline) {
			m.resetHistoryBrowsing()
			m.composer.InsertString("\n")
			m.recomputeCompletion()
			m.reflow()
			return m, nil
		}
	}
	if key.Matches(msg, m.keyMap.FocusLeft) {
		m.completion = nil
		cmd := m.focusPane(focusLeft)
		m.reflow()
		return m, cmd
	}
	if key.Matches(msg, m.keyMap.FocusRight) {
		m.completion = nil
		cmd := m.focusPane(focusRight)
		m.reflow()
		return m, cmd
	}
	if key.Matches(msg, m.keyMap.CycleWindowNext) {
		m.completion = nil
		m.windows = m.windows.cycleBy(1)
		m.windows = refreshProjectDataWindows(m.windows)
		m.reflow()
		return m, nil
	}
	if key.Matches(msg, m.keyMap.CycleWindowPrev) {
		m.completion = nil
		m.windows = m.windows.cycleBy(-1)
		m.windows = refreshProjectDataWindows(m.windows)
		m.reflow()
		return m, nil
	}
	if key.Matches(msg, m.keyMap.Palette) {
		m.completion = nil
		m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())
		m.reflow()
		return m, nil
	}
	if key.Matches(msg, m.keyMap.KeyHelp) {
		m.completion = nil
		m.modal = newKeysModal(m.keyMap)
		m.reflow()
		return m, nil
	}
	if key.Matches(msg, m.keyMap.ToggleOrientation) {
		m.completion = nil
		m.toggleOrientation()
		return m, nil
	}
	// Transcript scroll/jump always target the chat viewport, never the
	// right-pane terminal — handle before focusRight window routing.
	if key.Matches(msg, m.keyMap.ScrollUp) {
		m.viewport.HalfViewUp()
		return m, nil
	}
	if key.Matches(msg, m.keyMap.ScrollDown) {
		m.viewport.HalfViewDown()
		return m, nil
	}
	if key.Matches(msg, m.keyMap.JumpBottom) {
		m.viewport.GotoBottom()
		return m, nil
	}
	if key.Matches(msg, m.keyMap.Interrupt) {
		if m.turnRunning {
			ops := m.ops
			return m, func() tea.Msg {
				ops <- protocol.Interrupt{}
				return nil
			}
		}
		// Idle: esc clears a leftover input queue (rare once auto-drain runs).
		if m.clearInputQueue() {
			m.reflow()
			return m, nil
		}
	}
	if m.focus == focusRight {
		var cmd tea.Cmd
		m.windows, cmd = m.windows.update(msg)
		return m, cmd
	}
	if m.handleHistoryKey(msg) {
		return m, nil
	}
	if handled, cmd := m.handleToolCellKeys(msg); handled {
		m.reflow()
		m.refreshViewport()
		return m, cmd
	}
	// Empty composer + queued prompts: backspace pops last item for edit.
	if m.focus == focusLeft && m.composer.Value() == "" && len(m.inputQueue) > 0 {
		if msg.Type == tea.KeyBackspace || msg.Type == tea.KeyCtrlH {
			if m.popInputQueueToComposer() {
				return m, nil
			}
		}
	}
	// Bracketed paste: images → chip; large multi-line text → chip.
	if msg.Paste {
		m.handleComposerPaste(string(msg.Runes))
		m.recomputeCompletion()
		m.reflow()
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keyMap.Newline):
		// Left-focus only (right pane returned above). Distinct from Send
		// (enter) and from scroll chords (pgup/ctrl+up).
		m.resetHistoryBrowsing()
		m.composer.InsertString("\n")
		m.recomputeCompletion()
		m.reflow()
		return m, nil
	case key.Matches(msg, m.keyMap.ExternalEditor):
		return m.openComposerExternalEditor()
	case key.Matches(msg, m.keyMap.Send):
		// Expand paste chips before send so the model sees full content.
		text := strings.TrimSpace(m.composerTextExpanded())
		images := pendingImageAttachments(m.pendingImages)
		if text == "" && len(images) == 0 {
			// Empty enter is tool expand / open-at-line (handleToolCellKeys).
			return m, nil
		}
		if text != "" && strings.HasPrefix(text, "/") && len(images) == 0 {
			return m.handleCommand(text)
		}
		if m.providerName == "" {
			m.setNeedsModelNotice("No model selected — use /provider <anthropic|openai|xai|echo> [model]", true)
			return m, nil // keep the typed prompt in the composer
		}
		if len(images) > 0 {
			if ok, known := m.modelSupportsImages(); known && !ok {
				m.setNotice(imageUnsupportedMsg, true)
				return m, nil // keep text + chips
			}
		}
		// @file mentions: history/display keep tokens; model text gets contents.
		modelText, notices := expandFileMentions(text, m.services.Files)
		display := displayPromptWithImages(text, m.pendingImages)
		next, cmd := m.submit(protocol.UserInput{Text: modelText, Images: images}, display)
		if len(notices) > 0 {
			mm := next.(Model)
			mm.setNotice(strings.Join(notices, "; "), false)
			mm.reflow()
			return mm, cmd
		}
		return next, cmd
	case key.Matches(msg, m.keyMap.SaveDefaults):
		// Persist the current provider/model/agent as global defaults.
		if m.providerName == "" {
			m.setNeedsModelNotice("nothing to save — select a provider first", true)
			return m, nil
		}
		return m, m.saveDefaultsCmd(m.providerName, m.modelName, m.agentName, string(m.effort), dotJoin(m.th, m.providerName+"/"+m.modelName, m.agentName))
	case key.Matches(msg, m.keyMap.Agent):
		// Tab cycles agents (opencode-style build/plan switching).
		if len(m.agents) > 1 && !m.turnRunning {
			next := m.agents[0]
			for i, name := range m.agents {
				if name == m.agentName {
					next = m.agents[(i+1)%len(m.agents)]
					break
				}
			}
			ops := m.ops
			return m, func() tea.Msg {
				ops <- protocol.SelectAgent{Name: next}
				return nil
			}
		}
		return m, nil
	case key.Matches(msg, m.keyMap.PermissionMode):
		// Shift+Tab cycles tool-permission posture (not a newline).
		if m.turnRunning {
			return m, nil
		}
		next := m.permMode.Next()
		ops := m.ops
		return m, func() tea.Msg {
			ops <- protocol.SetPermissionMode{Mode: next}
			return nil
		}
	}
	return m.updateComposer(msg)
}
