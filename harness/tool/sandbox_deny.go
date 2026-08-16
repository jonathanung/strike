package tool

import (
	"fmt"
	"strings"
)

// sandboxDenialPattern maps OS/sandbox stderr/stdout fragments to a stable
// human reason. Matching is case-insensitive substring search.
// Longer / more specific substrings first so seatbelt "deny file-write-create"
// is not classified only as generic file-write.
var sandboxDenialPatterns = []struct {
	sub    string
	reason string
}{
	{"read-only file system", "write blocked: filesystem is read-only under OS sandbox"},
	{"readonly file system", "write blocked: filesystem is read-only under OS sandbox"},
	{"operation not permitted", "operation not permitted by OS sandbox"},
	{"permission denied", "permission denied by OS sandbox"},
	// macOS seatbelt / sandboxd style messages (specific before generic deny file-write)
	{"deny file-write-create", "file create denied by OS sandbox (seatbelt)"},
	{"deny file-write-data", "file write denied by OS sandbox (seatbelt)"},
	{"deny file-write", "file write denied by OS sandbox (seatbelt)"},
	{"deny network", "network denied by OS sandbox"},
	{"sandbox: ", "blocked by OS sandbox profile"},
}

// detectSandboxDenial reports whether a finished process looks like an OS
// sandbox capability block. Requires the sandbox launcher to have been applied
// (not degraded/off) and a non-zero exit. Returns a short human reason.
func detectSandboxDenial(proc ProcessResult) (reason string, ok bool) {
	if !proc.SandboxApplied || proc.ExitCode == 0 {
		return "", false
	}
	// Timeout/cancel are their own codes — do not collapse into sandbox_denied.
	switch proc.Status {
	case ProcessStatusTimeout, ProcessStatusCanceled:
		return "", false
	}
	text := proc.Output
	if text == "" {
		text = proc.Stdout + proc.Stderr
	}
	lower := strings.ToLower(text)
	for _, p := range sandboxDenialPatterns {
		if strings.Contains(lower, p.sub) {
			return p.reason, true
		}
	}
	return "", false
}

// formatSandboxDenial builds model-facing output with a stable code prefix and
// human reason, preserving any partial command output.
func formatSandboxDenial(reason, output string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "blocked by OS sandbox"
	}
	head := fmt.Sprintf("%s: %s", CodeSandboxDenied, reason)
	body := strings.TrimSpace(output)
	if body == "" || body == "(no output)" {
		return head
	}
	return head + "\n" + body
}
