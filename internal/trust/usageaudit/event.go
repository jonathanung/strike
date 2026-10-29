package usageaudit

import (
	"strings"

	"github.com/jonathanung/strike/pkg/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

// NoteEvent appends a sensitive protocol event to every open log.
// Unrelated events are ignored. Failures are best-effort: the session path
// must not depend on the chain.
func NoteEvent(ev protocol.Event) {
	in, ok := eventInput(ev)
	if !ok {
		return
	}
	openMu.Lock()
	logs := make([]*Log, 0, len(openLogs))
	for _, l := range openLogs {
		logs = append(logs, l)
	}
	openMu.Unlock()
	for _, l := range logs {
		_, _ = l.Append(in)
	}
}

func eventInput(ev protocol.Event) (Input, bool) {
	switch e := ev.(type) {
	case protocol.PermissionDecided:
		action := ActionPermission
		if e.Decision != "" {
			action = ActionApproval
		}
		summary := strings.TrimSpace(strings.Join([]string{
			e.Permission,
			e.Action,
			string(e.Decision),
		}, " "))
		if summary == "" {
			summary = "permission decision"
		}
		return Input{
			Action:    action,
			SessionID: e.SessionID,
			Summary:   summary,
			Detail: map[string]any{
				"requestId":  e.RequestID,
				"permission": e.Permission,
				"action":     e.Action,
				"decision":   string(e.Decision),
				"layer":      e.Layer,
				"patterns":   redactPatterns(e.Patterns),
			},
		}, true
	case protocol.PermissionModeSelected:
		mode := string(e.Mode)
		if mode == "" {
			mode = "unset"
		}
		return Input{
			Action:    ActionPermission,
			SessionID: e.SessionID,
			Summary:   "permission mode " + mode,
			Detail:    map[string]string{"mode": mode},
		}, true
	case protocol.ToolCallBegin:
		if !shellTool(e.Name) {
			return Input{}, false
		}
		return Input{
			Action:    ActionShell,
			SessionID: e.SessionID,
			Summary:   "shell tool " + e.Name,
			Detail: map[string]any{
				"callId": e.CallID,
				"tool":   e.Name,
				"args":   truncate(redact.String(string(e.Args)), 500),
			},
		}, true
	case protocol.ProcessStarted:
		// Tool-backed processes are already recorded from ToolCallBegin.
		if strings.TrimSpace(e.CallID) != "" {
			return Input{}, false
		}
		argv := make([]string, 0, len(e.Argv))
		for _, a := range e.Argv {
			argv = append(argv, truncate(redact.String(a), 200))
		}
		summary := "process started"
		if len(argv) > 0 {
			summary = "process " + strings.Join(argv, " ")
		}
		return Input{
			Action:    ActionShell,
			SessionID: e.SessionID,
			Summary:   truncate(summary, 500),
			Detail: map[string]any{
				"processId": e.ProcessID,
				"argv":      argv,
				"cwd":       redact.String(e.Cwd),
			},
		}, true
	default:
		return Input{}, false
	}
}

func shellTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "shell", "terminal":
		return true
	default:
		return false
	}
}

func redactPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]string, 0, len(patterns))
	for i, p := range patterns {
		if i >= 8 {
			out = append(out, "…")
			break
		}
		out = append(out, truncate(redact.String(p), 200))
	}
	return out
}
