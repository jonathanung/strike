package tool

import (
	"encoding/json"
	"strings"
)

// SchemaLevel is the progressive disclosure level for a tool's provider schema.
// Basic is the compact default; Advanced exposes the full contract.
type SchemaLevel int

const (
	// SchemaBasic is the compact initial provider schema.
	SchemaBasic SchemaLevel = iota
	// SchemaAdvanced is the full provider schema (all fields/actions).
	SchemaAdvanced
)

// Progressive is optionally implemented by tools that expose a compact basic
// schema and a full advanced schema under a single tool name. Schema() and
// Description() remain the advanced/full forms (used by toolsearch and
// Schemas()). Provider Tools use BasicSchema until PromoteSchema elevates
// the tool for subsequent streams.
type Progressive interface {
	BasicSchema() json.RawMessage
	// BasicDescription is the compact description for the basic schema.
	// Empty falls back to Description().
	BasicDescription() string
}

// progressiveTool reports whether t implements Progressive.
func progressiveTool(t Tool) (Progressive, bool) {
	p, ok := t.(Progressive)
	return p, ok
}

// Task args / actions that require the advanced schema surface.
var taskAdvancedActions = map[string]struct{}{
	"get": {}, "list": {}, "read": {}, "message": {}, "transition": {},
}

// taskAdvancedArgKeys are create/control fields only present on the advanced schema.
var taskAdvancedArgKeys = []string{
	"name", "agent", "model", "effort", "route", "specialty", "capabilities",
	"max_cost_class", "models", "max_concurrent", "criteria", "deps", "subscribe",
	"assignee", "verify", "budget", "force_delegate", "context_bundle",
	"offset", "limit", "last", "include_tools", "include_reasoning",
	"text", "state", "reason", "expected_version",
}

// ArgsNeedAdvancedSchema reports whether raw JSON args for a progressive tool
// require the advanced schema (advanced action or advanced-only fields).
// Unknown tools / empty args return false.
func ArgsNeedAdvancedSchema(toolName string, args json.RawMessage) bool {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return false
	}
	switch name {
	case "task":
		return taskArgsNeedAdvanced(args)
	default:
		return false
	}
}

func taskArgsNeedAdvanced(args json.RawMessage) bool {
	if len(args) == 0 {
		return false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return false
	}
	if actRaw, ok := raw["action"]; ok {
		var act string
		if err := json.Unmarshal(actRaw, &act); err == nil {
			act = strings.ToLower(strings.TrimSpace(act))
			if _, adv := taskAdvancedActions[act]; adv {
				return true
			}
			// aliases that map to advanced-only paths are none for cancel/create
		}
	}
	for _, k := range taskAdvancedArgKeys {
		if v, ok := raw[k]; ok && len(v) > 0 && string(v) != "null" && string(v) != `""` && string(v) != "[]" && string(v) != "{}" {
			// present non-empty advanced field
			// treat false boolean as present (force_delegate:false is still advanced intent? skip false)
			if string(v) == "false" || string(v) == "0" {
				continue
			}
			return true
		}
	}
	return false
}
