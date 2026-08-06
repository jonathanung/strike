package tool

import "strings"

// Core tools always appear in the provider Tools array when registered, even
// with defer loading enabled. Everything else (optional built-ins, MCP) is
// omitted until toolsearch discovers it (or the model calls it by name).
var coreToolNames = map[string]struct{}{
	"read": {}, "glob": {}, "grep": {},
	"edit": {}, "write": {}, "apply_patch": {},
	"move": {}, "delete": {},
	"bash": {},
	"task": {}, "task_status": {}, "task_read": {}, "task_message": {}, "task_interrupt": {},
	"delegate":        {},
	"wait":            {},
	"agent_roster":    {},
	"agent_ownership": {},
	"agent_message":   {},
	"agent_broadcast": {},
	"agent_thread":    {},
	"team_task":       {},
	"plan_write":      {},
	"plan_read":       {},
	"plan_delegate":   {},
	"toolsearch":      {},
	"question":        {},
	"enter_plan_mode": {}, "exit_plan_mode": {}, "phase_done": {},
}

// IsCoreTool reports whether name is always sent in provider Tools under
// defer loading (coding + task + discovery + plan/question workflow).
func IsCoreTool(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	_, ok := coreToolNames[name]
	return ok
}

// IsDeferredTool reports tools that may be omitted from provider Tools when
// defer loading is on (non-core built-ins and all MCP tools).
func IsDeferredTool(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || IsCoreTool(name) {
		return false
	}
	return true
}
