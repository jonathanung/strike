package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// bangResultMsg is delivered after a composer ! shell run finishes.
type bangResultMsg struct {
	callID   string
	command  string
	output   string
	exitCode int
	err      string
}

// parseBangCommand reports whether text is a bang-escape shell line.
// command is the shell payload (may be empty for bare "!").
func parseBangCommand(text string) (command string, ok bool) {
	if !strings.HasPrefix(text, "!") {
		return "", false
	}
	return strings.TrimSpace(text[1:]), true
}

// handleBang runs a local shell command without starting a model turn.
func (m Model) handleBang(text string) (tea.Model, tea.Cmd) {
	command, ok := parseBangCommand(text)
	if !ok {
		return m, nil
	}
	if command == "" {
		m.setNotice("empty command — use !cmd to run a shell command", true)
		return m, nil
	}
	if m.services.Shell == nil {
		m.setNotice("shell is unavailable", true)
		return m, nil
	}

	display := "!" + command
	m.resetComposer()
	m.clearNotice()
	m.resetHistoryBrowsing()

	callID := fmt.Sprintf("bang-%d", time.Now().UnixNano())
	m.cells = append(m.cells, &userCell{text: display})
	tc := &toolCell{
		callID: callID,
		name:   "bash",
		title:  command,
		done:   false,
	}
	m.cells = append(m.cells, tc)
	if m.toolByID == nil {
		m.toolByID = map[string]*toolCell{}
	}
	m.toolByID[callID] = tc
	m.reflow()

	shell := m.services.Shell
	var histCmd tea.Cmd
	if m.services.History != nil {
		done := m.services.History.Enqueue(display)
		histCmd = func() tea.Msg {
			err := <-done
			return historyAddedMsg{err: err}
		}
	}
	runCmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := shell.Run(ctx, command)
		msg := bangResultMsg{
			callID:   callID,
			command:  command,
			output:   res.Output,
			exitCode: res.ExitCode,
		}
		if err != nil {
			msg.err = err.Error()
			if msg.output == "" {
				msg.output = err.Error()
			}
		}
		return msg
	}
	if histCmd != nil {
		return m, tea.Batch(runCmd, histCmd)
	}
	return m, runCmd
}

func (m Model) applyBangResult(msg bangResultMsg) (tea.Model, tea.Cmd) {
	tc, ok := m.toolByID[msg.callID]
	if !ok || tc == nil {
		if msg.err != "" {
			m.setNotice("!"+msg.command+": "+msg.err, true)
		}
		m.reflow()
		return m, nil
	}
	tc.done = true
	tc.output = msg.output
	if strings.TrimSpace(tc.output) == "" {
		tc.output = "(no output)"
	}
	tc.isError = msg.err != "" || msg.exitCode != 0
	if msg.err != "" && tc.output == "(no output)" {
		tc.output = msg.err
	}
	m.reflow()
	return m, nil
}
