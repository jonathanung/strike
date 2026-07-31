package tool

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxMCPGuidanceListed is how many MCP tools may appear before the guidance
// layer switches from silence (schemas already list them) to a compact
// per-server summary. Below this count, MCP tools are not restated in the
// system prompt.
const MaxMCPGuidanceListed = 16

// shortPurposes is the single source of truth for built-in tool one-liners on
// the provider Tools wire (CompactSchemaDescription). Tests fail if a built-in
// name drifts from this map.
var shortPurposes = map[string]string{
	"read":            "read file contents",
	"glob":            "find files by name pattern",
	"grep":            "search file contents by regex",
	"edit":            "exact string replacement in a file",
	"write":           "create or overwrite a file",
	"apply_patch":     "coordinated multi-file patch",
	"bash":            "run a shell command",
	"task":            "delegate a bounded subtask to a child agent (optional agent/model)",
	"task_status":     "check status of a delegated child task",
	"task_read":       "read a bounded child transcript slice",
	"task_message":    "send guidance to a running child task",
	"task_interrupt":  "cancel a running child task",
	"agent_roster":    "list lead and teammate agents on the session team",
	"agent_message":   "send a peer message to one teammate",
	"agent_broadcast": "broadcast a peer message to all other teammates",
	"team_task":       "shared team task board (create/list/claim/complete)",
	"webfetch":        "fetch a URL",
	"todowrite":       "write the multi-step todo list",
	"todoread":        "read the current todo list",
	"memory_write":    "store durable project memory",
	"memory_read":     "read durable project memory",
	"issue_write":     "create or update a project issue",
	"issue_read":      "read project issues",
	"notebook_edit":   "edit a Jupyter notebook cell",
	"sleep":           "pause for a number of seconds",
	"skill":           "load a named skill into context",
	"question":        "ask the user a clarifying question",
	"enter_plan_mode": "start the plan→implement workflow",
	"exit_plan_mode":  "leave plan mode for build or orchestrator",
	"phase_done":      "advance the active workflow phase gate",
	"toolsearch":      "search registered tool names/descriptions",
}

// BuiltinShortPurposes returns a copy of the built-in name→purpose map.
func BuiltinShortPurposes() map[string]string {
	out := make(map[string]string, len(shortPurposes))
	for k, v := range shortPurposes {
		out[k] = v
	}
	return out
}

// PermissionName maps a tool name to the permission.Ruleset key used at Ask.
func PermissionName(toolName string) string {
	switch toolName {
	case "apply_patch", "notebook_edit":
		return "edit"
	default:
		if strings.HasPrefix(toolName, "mcp_") {
			return "mcp"
		}
		return toolName
	}
}

// ShortPurpose returns a compact one-line purpose for a tool name.
// Built-ins use shortPurposes; others fall back to a truncated description.
func ShortPurpose(name, description string) string {
	if p, ok := shortPurposes[name]; ok {
		return p
	}
	return truncatePurpose(description, 72)
}

// CompactSchemaDescription returns the model-facing tool description for
// provider Tools arrays (every Stream). Built-ins use short purposes so the
// always-on schema payload stays small; full prose remains on Registry.Schemas
// for toolsearch. The skill tool keeps its available-skills catalog. Other
// tools (MCP, plugins) get a truncated first sentence (120 runes).
//
// This is a size budget on descriptions, not deferred loading (#438): every
// non-denied registered tool still appears in Tools with full InputSchema.
func CompactSchemaDescription(name, description string) string {
	name = strings.TrimSpace(name)
	if name == "skill" {
		return compactSkillSchemaDescription(description)
	}
	if p, ok := shortPurposes[name]; ok {
		return p
	}
	return truncatePurpose(description, 120)
}

// compactSkillSchemaDescription keeps the available-skills list (required for
// the model to pick a name) while dropping the multi-paragraph usage prose.
func compactSkillSchemaDescription(description string) string {
	base := shortPurposes["skill"]
	if base == "" {
		base = "load a named skill into context"
	}
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Available skills:") {
			list := strings.TrimSpace(strings.TrimPrefix(line, "Available skills:"))
			if list == "" {
				return base
			}
			return base + ". Available skills: " + list
		}
	}
	return base
}

// GuidanceEntry is one effective tool name for additive prompt composition.
// Names/descriptions/schemas live in the provider Tools array; guidance only
// needs the effective name set to select when-to-use tips.
type GuidanceEntry struct {
	Name string
}

// BuildGuidance renders the model-visible tools guidance layer from the
// effective tool list (already filtered for hard denies / depth).
//
// Split of responsibility:
//   - Provider Tools schemas carry name, compact description, and parameter
//     JSON for every callable tool on this turn (#436).
//   - This system-prompt layer is additive only: a short policy preamble,
//     optional MCP server summary when the MCP surface is large, and
//     recommended-use tips conditioned on which tools are present (#437).
//
// It does not restate the full name/purpose catalog (that duplicated schemas).
// Output is compact and deterministic.
func BuildGuidance(entries []GuidanceEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var mcp []GuidanceEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name, "mcp_") {
			mcp = append(mcp, e)
		}
	}

	var b strings.Builder
	b.WriteString("# Available tools\n\n")
	b.WriteString("Names, descriptions, and parameter schemas are in the provider Tools array for this turn (hard-denied tools omitted). This section is additive only: usage policy and when-to-use tips. Prefer purpose-built tools over improvising with bash. There is no websearch tool.\n")

	if len(mcp) > MaxMCPGuidanceListed {
		b.WriteString("\n")
		b.WriteString(formatMCPGuidanceSummary(mcp))
	}

	if g := recommendedGuidance(entries); g != "" {
		b.WriteString("\n")
		b.WriteString(g)
	}
	return strings.TrimSpace(b.String()) + "\n"
}

// formatMCPGuidanceSummary groups a large MCP surface by server so guidance
// stays small while schemas still list every tool.
func formatMCPGuidanceSummary(mcp []GuidanceEntry) string {
	byServer := map[string]int{}
	order := make([]string, 0)
	for _, e := range mcp {
		srv := mcpServerOf(e.Name)
		if _, ok := byServer[srv]; !ok {
			order = append(order, srv)
		}
		byServer[srv]++
	}
	sort.Strings(order)
	var b strings.Builder
	fmt.Fprintf(&b, "MCP tools (%d from %d servers) — call as `mcp_<server>_<tool>`; use `toolsearch` when unsure:\n",
		len(mcp), len(order))
	for _, srv := range order {
		fmt.Fprintf(&b, "- `%s` (%d tools)\n", srv, byServer[srv])
	}
	return b.String()
}

func mcpServerOf(name string) string {
	// mcp_<server>_<tool…>
	rest := strings.TrimPrefix(name, "mcp_")
	if rest == name || rest == "" {
		return "mcp"
	}
	server, _, ok := strings.Cut(rest, "_")
	if !ok || server == "" {
		return "mcp"
	}
	return server
}

func recommendedGuidance(entries []GuidanceEntry) string {
	have := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		have[e.Name] = struct{}{}
	}
	has := func(names ...string) bool {
		for _, n := range names {
			if _, ok := have[n]; ok {
				return true
			}
		}
		return false
	}
	hasAll := func(names ...string) bool {
		for _, n := range names {
			if _, ok := have[n]; !ok {
				return false
			}
		}
		return true
	}

	var lines []string
	add := func(cond bool, line string) {
		if cond {
			lines = append(lines, "- "+line)
		}
	}

	add(has("read", "glob", "grep") && has("bash"),
		"Prefer `read`/`glob`/`grep` over shelling out (`cat`/`find`/`grep`) for ordinary code exploration.")
	add(has("read", "glob", "grep") && !has("bash"),
		"Use `read`/`glob`/`grep` for ordinary code exploration.")
	add(has("apply_patch") && has("edit") && has("write"),
		"Prefer `apply_patch` for multi-file coordinated edits; `edit` for one exact replacement; `write` only for new files.")
	add(has("apply_patch") && has("edit") && !has("write"),
		"Prefer `apply_patch` for multi-file coordinated edits; `edit` for one exact replacement.")
	add(has("edit") && has("write") && !has("apply_patch"),
		"Prefer `edit` for one exact replacement; `write` only for new files.")
	add(has("edit") && !has("write") && !has("apply_patch"),
		"Use `edit` for exact in-place replacements.")
	add(has("write") && !has("edit") && !has("apply_patch"),
		"Use `write` only when creating or fully replacing a file.")
	add(has("webfetch") && has("bash"),
		"Prefer `webfetch` over curl/wget in bash for ordinary page fetches.")
	add(has("webfetch") && !has("bash"),
		"Use `webfetch` for ordinary page fetches.")
	add(has("question"),
		"Use `question` when a decision genuinely belongs to the user.")
	add(has("task") && has("task_status", "task_read"),
		"Use `task` for bounded non-blocking delegation (optional `agent`/`model`/`name`). Do not busy-poll `task_status` — prefer `[child.completed]` and the peer inbox. One-off `task_status`/`task_read` only when needed. `task_message` steers owned children; `task_interrupt` cancels. Bound fan-out (MaxChildDepth).")
	add(has("task") && !has("task_status", "task_read"),
		"Use `task` for bounded non-blocking delegation (self-contained prompt). A later `[child.completed]` delivers the finished summary — never sleep-poll for task completion.")
	add(has("agent_roster"),
		"Use `agent_roster` to list the lead and teammates (session ids, personas, states) on the implicit session team — prefer over status polling when you only need who is live.")
	add(has("agent_message") || has("agent_broadcast"),
		"Prefer `agent_message` / `agent_broadcast` for mid-flight coordination (blockers, handoffs, child→lead early). Prefer `[child.completed]` for finished work products. Avoid chatty loops. `task_message` remains parent→owned-child steer only — not a parent-only team control plane.")
	add(has("team_task"),
		"Use `team_task` for a shared claim/assign board across teammates (create/list/update/claim/complete; CAS via expected_version). Prefer `todowrite`/`todoread` for solo lead planning only — not for multi-agent claim coordination.")

	add(has("sleep") && has("bash") && has("task"),
		"Prefer `sleep` over bash sleep for external readiness (services, rate limits). Never sleep-poll for `task`/subagent completion.")
	add(has("sleep") && has("bash") && !has("task"),
		"Prefer `sleep` over bash sleep when waiting for external readiness.")
	add(hasAll("memory_write", "memory_read"),
		"Use `memory_write`/`memory_read` for durable project guidance. Tags `instruction`/`preference`/`project-convention` auto-load (capped); other tags stay on-demand.")
	add(has("memory_read") && !has("memory_write"),
		"Use `memory_read` for durable project memory. Auto-loaded tags are already in context when present.")
	add(hasAll("issue_write", "issue_read"),
		"Use `issue_write`/`issue_read` for project-local tracked issues on demand (never auto-injected).")
	add(has("issue_read") && !has("issue_write"),
		"Use `issue_read` for project-local tracked issues on demand.")
	add(has("todowrite", "todoread") && has("team_task"),
		"Use `todowrite`/`todoread` for solo multi-step tracking (full list on each write). Use `team_task` when teammates must claim shared work items.")
	add(has("todowrite", "todoread") && !has("team_task"),
		"Use `todowrite`/`todoread` for multi-step task tracking (full list on each write).")
	add(has("skill"),
		"Use `skill` to load named skill content when a task matches.")
	switch {
	case has("enter_plan_mode", "exit_plan_mode") && has("phase_done"):
		add(true, "Use `enter_plan_mode`/`exit_plan_mode` and `phase_done` only in applicable workflows; plan phase hard-denies write/edit.")
	case has("enter_plan_mode", "exit_plan_mode"):
		add(true, "Use `enter_plan_mode`/`exit_plan_mode` only in applicable workflows; plan phase hard-denies write/edit.")
	case has("phase_done"):
		add(true, "Use `phase_done` to advance the active workflow phase gate.")
	}
	add(has("toolsearch"),
		"Use `toolsearch` to discover tools by name or description when the list is large.")
	add(hasMCP(entries),
		"Call MCP tools by their registered `mcp_*` names when present.")

	// Always-on operational notes when any tools exist.
	lines = append(lines,
		"- Never create docs/README unless asked.",
		"- Batch independent tool calls in one response. Chain dependent bash with `&&` when using bash.",
		"- Permission denied → change approach from feedback; do not retry the same call.",
		"- Tab/`/agent` only switches persona on the same session history — not a subagent.",
	)

	if len(lines) == 0 {
		return ""
	}
	return "## Recommended use\n\n" + strings.Join(lines, "\n") + "\n"
}

func hasMCP(entries []GuidanceEntry) bool {
	for _, e := range entries {
		if strings.HasPrefix(e.Name, "mcp_") {
			return true
		}
	}
	return false
}

func truncatePurpose(desc string, maxRunes int) string {
	s := strings.TrimSpace(desc)
	if s == "" {
		return ""
	}
	// Strip leading [mcp:server] prefix from bridge descriptions.
	if strings.HasPrefix(s, "[mcp:") {
		if i := strings.Index(s, "]"); i >= 0 && i+1 < len(s) {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	// First sentence / line only.
	if i := strings.IndexAny(s, ".\n"); i >= 0 {
		part := strings.TrimSpace(s[:i])
		if part != "" {
			s = part
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	// Lowercase first rune for list consistency when it looks like a sentence.
	r, size := utf8.DecodeRuneInString(s)
	if unicode.IsUpper(r) && !strings.HasPrefix(s, "MCP") {
		s = string(unicode.ToLower(r)) + s[size:]
	}
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes-1]) + "…"
}
