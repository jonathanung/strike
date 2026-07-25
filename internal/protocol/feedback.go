package protocol

import "strings"

// Model-facing tool-result feedback. The engine settles blocked, denied,
// canceled, and other non-success tool calls by appending a RoleTool message
// with these texts so the model can course-correct. Permission denials, user
// rejects, hook blocks, and phase bounces should use these helpers rather than
// inventing ad-hoc strings.

// ToolFeedbackPermissionDenied explains a hard ruleset (or profile) deny.
// reason is optional detail (e.g. "write is not allowed for this agent").
func ToolFeedbackPermissionDenied(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "Permission denied."
	}
	return "Permission denied because " + reason + "."
}

// ToolFeedbackUserRejected explains an interactive permission reject.
// feedback is optional free-text from the user (PermissionReply.Message).
func ToolFeedbackUserRejected(feedback string) string {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return "The user rejected this tool call."
	}
	return "The user rejected this tool call with feedback: " + feedback
}

// ToolFeedbackBlocked explains a non-permission block (hooks, phase gates).
// reason is optional detail from the blocker.
func ToolFeedbackBlocked(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "Tool call blocked."
	}
	return "Tool call blocked: " + reason
}

// ToolFeedbackCanceled is used when a started tool call is interrupted.
func ToolFeedbackCanceled() string {
	return "Tool call canceled because the turn was interrupted."
}

// ToolFeedbackUnstarted is used when a tool call never began execution
// because the turn was interrupted first (history-only; no ToolCallEnd).
func ToolFeedbackUnstarted() string {
	return "Tool call not executed because the turn was interrupted before it started."
}

// ToolFeedbackError wraps a generic tool execution failure for the model.
func ToolFeedbackError(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "Error."
	}
	if strings.HasPrefix(msg, "Error:") {
		return msg
	}
	return "Error: " + msg
}
