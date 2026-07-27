package tool

import (
	"fmt"
	"sort"
	"strings"
)

// MaxMCPGuidanceListed is how many MCP tools may appear before the guidance
// layer switches from silence (schemas already list them) to a compact
// per-server summary. Below this count, MCP tools are not restated in the
// system prompt.
const MaxMCPGuidanceListed = 16

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
//   - Provider Tools schemas carry name, description, and parameter JSON for
//     every callable tool on this turn.
//   - This system-prompt layer is additive only: a short policy preamble,
//     optional MCP server summary when the MCP surface is large, and
//     recommended-use tips conditioned on which tools are present.
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
		"Use `task` for bounded non-blocking delegation (optional `agent` persona and `model` catalog id); use `task_status`/`task_read` only when an intermediate check is needed (not every second). Prefer `task_message` to steer and `task_interrupt` to cancel. Completion still arrives once as `[child.completed]` — never sleep-poll.")
	add(has("task") && !has("task_status", "task_read"),
		"Use `task` for bounded non-blocking delegation (self-contained prompt). A later `[child.completed]` delivers the summary — never sleep-poll for task completion.")
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
	add(has("todowrite", "todoread"),
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
