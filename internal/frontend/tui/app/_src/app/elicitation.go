package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike/internal/protocol"
)

// elicitationCancelNotice names the MCP server whose request was cancelled.
// The server text itself is not shown: the TUI cannot render the form.
func elicitationCancelNotice(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		server = "An MCP server"
	}
	return server + " asked for input mid-call; the terminal UI cannot show that form yet, so the request was cancelled. Use the web cockpit to answer it."
}

// elicitationCancelCmd answers a pending elicitation with cancel.
func elicitationCancelCmd(ops chan<- protocol.Op, requestID string) tea.Cmd {
	if ops == nil || requestID == "" {
		return nil
	}
	return func() tea.Msg {
		ops <- protocol.ElicitationReply{RequestID: requestID, Action: protocol.ElicitationCancel}
		return nil
	}
}
