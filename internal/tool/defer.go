package tool

import "strings"

// Core tools always appear in the provider Tools array when registered, even
// with defer loading enabled. Everything else (orchestration, compatibility
// shims, optional built-ins, MCP) is omitted until toolsearch discovers it,
// the model calls it by name, or deterministic workflow activation promotes it.
//
// Minimal always-visible surface for ordinary repository coding:
// file inspect/edit, bash, progressive task, question, and toolsearch.
var coreToolNames = map[string]struct{}{
	"read": {}, "glob": {}, "grep": {},
	"edit": {}, "write": {}, "apply_patch": {},
	"move": {}, "delete": {},
	"bash":       {},
	"task":       {},
	"toolsearch": {},
	"question":   {},
}

// IsCoreTool reports whether name is always sent in provider Tools under
// defer loading (coding + task + discovery + question).
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
