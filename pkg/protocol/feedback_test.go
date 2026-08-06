package protocol

import "testing"

func TestToolFeedbackHelpers(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"denied empty", ToolFeedbackPermissionDenied(""), "Permission denied."},
		{"denied reason", ToolFeedbackPermissionDenied(" write is not allowed "), "Permission denied because write is not allowed."},
		{"reject empty", ToolFeedbackUserRejected(""), "The user rejected this tool call."},
		{"reject feedback", ToolFeedbackUserRejected(" do not delete "), "The user rejected this tool call with feedback: do not delete"},
		{"blocked empty", ToolFeedbackBlocked(""), "Tool call blocked."},
		{"blocked reason", ToolFeedbackBlocked("hook policy"), "Tool call blocked: hook policy"},
		{"canceled", ToolFeedbackCanceled(), "Tool call canceled because the turn was interrupted."},
		{"unstarted", ToolFeedbackUnstarted(), "Tool call not executed because the turn was interrupted before it started."},
		{"error empty", ToolFeedbackError(""), "Error."},
		{"error plain", ToolFeedbackError("boom"), "Error: boom"},
		{"error prefixed", ToolFeedbackError("Error: already"), "Error: already"},
		{"error trim", ToolFeedbackError("  x  "), "Error: x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}
