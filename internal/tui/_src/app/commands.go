package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/host"
)

type commandID string

const (
	commandProvider        commandID = "provider"
	commandModel           commandID = "model"
	commandEffort          commandID = "effort"
	commandAutonomy        commandID = "autonomy"
	commandMode            commandID = "mode"
	commandSandbox         commandID = "sandbox"
	commandPermission      commandID = "permission"
	commandAuth            commandID = "auth"
	commandSettings        commandID = "settings"
	commandAgent           commandID = "agent"
	commandAgents          commandID = "agents"
	commandActivity        commandID = "activity"
	commandFiles           commandID = "files"
	commandVisualizer      commandID = "visualizer"
	commandSystem          commandID = "system"
	commandTelemetry       commandID = "telemetry"
	commandPets            commandID = "pets"
	commandFast            commandID = "fast"
	commandThink           commandID = "think"
	commandVim             commandID = "vim"
	commandNano            commandID = "nano"
	commandHelp            commandID = "help"
	commandKeys            commandID = "keys"
	commandLegend          commandID = "legend"
	commandMDRead          commandID = "md-read"
	commandTheme           commandID = "theme"
	commandLayout          commandID = "layout"
	commandSplit           commandID = "split"
	commandCompact         commandID = "compact"
	commandFork            commandID = "fork"
	commandUndo            commandID = "undo"
	commandRewind          commandID = "rewind"
	commandSession         commandID = "session"
	commandRename          commandID = "rename"
	commandExport          commandID = "export"
	commandTimeline        commandID = "timeline"
	commandDiag            commandID = "diag"
	commandDiagnostic      commandID = "diagnostic"
	commandCopy            commandID = "copy"
	commandMemory          commandID = "memory"
	commandQueue           commandID = "queue"
	commandIssues          commandID = "issues"
	commandPlan            commandID = "plan"
	commandGoal            commandID = "goal"
	commandLoop            commandID = "loop"
	commandWorkflow        commandID = "workflow"
	commandContext         commandID = "context"
	commandEffectivePrompt commandID = "effective-prompt"
	commandCost            commandID = "cost"
	commandUpgrade         commandID = "upgrade"
	commandInit            commandID = "init"
	commandFTUE            commandID = "ftue"
	commandMCP             commandID = "mcp"
	commandLSP             commandID = "lsp"
	commandDiagnostics     commandID = "diagnostics"
	commandExit            commandID = "exit"
	commandQuit            commandID = "quit"

	// Keybind-backed action mirrors (see keybind_slash.go).
	commandFocusLeft     commandID = "focus-left"
	commandFocusRight    commandID = "focus-right"
	commandWindowNext    commandID = "window-next"
	commandWindowPrev    commandID = "window-prev"
	commandGroupNext     commandID = "group-next"
	commandGroupPrev     commandID = "group-prev"
	commandScrollUp      commandID = "scroll-up"
	commandScrollDown    commandID = "scroll-down"
	commandJumpBottom    commandID = "jump-bottom"
	commandPalette       commandID = "palette"
	commandInterrupt     commandID = "interrupt"
	commandSaveDefaults  commandID = "save-defaults"
	commandLeaveEditor   commandID = "leave-editor"
	commandEditPrompt    commandID = "edit-prompt"
	commandAgentNext     commandID = "agent-next"
	commandModeNext      commandID = "mode-next"
	commandToolPrev      commandID = "tool-prev"
	commandToolNext      commandID = "tool-next"
	commandToolExpand    commandID = "tool-expand"
	commandToolCopy      commandID = "tool-copy"
	commandToolReview    commandID = "tool-review"
	commandToolApply     commandID = "tool-apply"
	commandSubagent      commandID = "subagent"
	commandParent        commandID = "parent"
	commandSubagentNext  commandID = "subagent-next"
	commandSubagentPrev  commandID = "subagent-prev"
	commandRootNew       commandID = "root-new"
	commandRootOpen      commandID = "root-open"
	commandRootInterrupt commandID = "root-interrupt"
	commandRootHide      commandID = "root-hide"
	commandRootFilter    commandID = "root-filter"
)

type commandSource string

const (
	commandSourceBuiltin commandSource = "command"
	commandSourceSkill   commandSource = "skill"
)

type commandSpec struct {
	ID          commandID
	Name        string
	Description string
	ArgsHint    string
	Source      commandSource
}

var builtinCommandSpecs = []commandSpec{
	{ID: commandProvider, Name: "/provider", Description: "select a provider and model", ArgsHint: "[name [model]]", Source: commandSourceBuiltin},
	{ID: commandModel, Name: "/model", Description: "select a model from authenticated providers", ArgsHint: "[model|provider/model]", Source: commandSourceBuiltin},
	{ID: commandEffort, Name: "/effort", Description: "set how much reasoning the model spends", ArgsHint: "[level]", Source: commandSourceBuiltin},
	{ID: commandAutonomy, Name: "/autonomy", Description: "set exit-gate policy (supervised/agent/checks/skip-all)", ArgsHint: "[mode]", Source: commandSourceBuiltin},
	{ID: commandMode, Name: "/mode", Description: "set permission posture (default/plan/accept-edits/yolo)", ArgsHint: "[mode]", Source: commandSourceBuiltin},
	{ID: commandSandbox, Name: "/sandbox", Description: "show OS sandbox policy; /sandbox explain for generated profile", Source: commandSourceBuiltin},
	{ID: commandPermission, Name: "/permission", Description: "explain a tool permission or list presets", ArgsHint: "[explain <tool> [pattern]|presets]", Source: commandSourceBuiltin},
	{ID: commandAuth, Name: "/auth", Description: "manage provider authentication", ArgsHint: "[provider]", Source: commandSourceBuiltin},
	{ID: commandSettings, Name: "/settings", Description: "defaults (theme, sandbox, notify, mode) and custom providers", Source: commandSourceBuiltin},
	{ID: commandAgent, Name: "/agent", Description: "select an agent", ArgsHint: "[name]", Source: commandSourceBuiltin},
	{ID: commandAgents, Name: "/agents", Description: "focus the agents right pane", Source: commandSourceBuiltin},
	{ID: commandActivity, Name: "/activity", Description: "focus the activity right pane", Source: commandSourceBuiltin},
	{ID: commandFiles, Name: "/files", Description: "focus the files right pane", Source: commandSourceBuiltin},
	{ID: commandVisualizer, Name: "/visualizer", Description: "focus the visualizer right pane", Source: commandSourceBuiltin},
	{ID: commandSystem, Name: "/system", Description: "focus the system right pane (requires telemetry on)", Source: commandSourceBuiltin},
	{ID: commandTelemetry, Name: "/telemetry", Description: "show or hide local system metrics (CPU/RAM/disk)", ArgsHint: "[on|off|status]", Source: commandSourceBuiltin},
	{ID: commandPets, Name: "/pets", Description: "focus the pets right pane (ASCII companions)", ArgsHint: "[name]", Source: commandSourceBuiltin},
	{ID: commandFast, Name: "/fast", Description: "toggle OpenAI priority tier (faster, ~2× cost)", ArgsHint: "[on|off]", Source: commandSourceBuiltin},
	{ID: commandThink, Name: "/think", Description: "show or hide model chain-of-thought", ArgsHint: "[on|off]", Source: commandSourceBuiltin},
	{ID: commandVim, Name: "/vim", Description: "open a file in the editor (embedded/modal/takeover; see vimMode)", ArgsHint: "[path|@path[:line]]", Source: commandSourceBuiltin},
	{ID: commandNano, Name: "/nano", Description: "open a file in nano (embedded/modal/takeover; see nanoMode)", ArgsHint: "[path|@path[:line]]", Source: commandSourceBuiltin},
	{ID: commandMDRead, Name: "/md-read", Description: "open a markdown file (embedded right pane or modal; see mdReadMode)", ArgsHint: "<path|@path>", Source: commandSourceBuiltin},
	{ID: commandTheme, Name: "/theme", Description: "select a color theme or set appearance", ArgsHint: "[name|dark|light|auto]", Source: commandSourceBuiltin},
	{ID: commandLayout, Name: "/layout", Description: "toggle horizontal/vertical pane split", Source: commandSourceBuiltin},
	{ID: commandSplit, Name: "/split", Description: "toggle horizontal/vertical pane split", Source: commandSourceBuiltin},
	{ID: commandCompact, Name: "/compact", Description: "compact model history (keep recent turns)", Source: commandSourceBuiltin},
	{ID: commandFork, Name: "/fork", Description: "duplicate the conversation into a new id", Source: commandSourceBuiltin},
	{ID: commandUndo, Name: "/undo", Description: "undo last turn (chat only, or chat + restore files)", ArgsHint: "[chat|files]", Source: commandSourceBuiltin},
	{ID: commandRewind, Name: "/rewind", Description: "fork a new id from a previous turn (keeps original)", ArgsHint: "[turn]", Source: commandSourceBuiltin},
	{ID: commandSession, Name: "/session", Description: "browse and resume a past session", ArgsHint: "[id]", Source: commandSourceBuiltin},
	{ID: commandRename, Name: "/rename", Description: "rename the current session", ArgsHint: "[title]", Source: commandSourceBuiltin},
	{ID: commandExport, Name: "/export", Description: "export the conversation to markdown", ArgsHint: "[path] [--open]", Source: commandSourceBuiltin},
	{ID: commandTimeline, Name: "/timeline", Description: "structured run timeline (collapsed view or JSON export)", ArgsHint: "[export [path]]", Source: commandSourceBuiltin},
	{ID: commandDiag, Name: "/diag", Description: "export prompt/config diagnostic bundle (JSON)", ArgsHint: "[export [path]]", Source: commandSourceBuiltin},
	{ID: commandDiagnostic, Name: "/diagnostic", Description: "alias of /diag", ArgsHint: "[export [path]]", Source: commandSourceBuiltin},
	{ID: commandCopy, Name: "/copy", Description: "copy the last assistant response to the clipboard", Source: commandSourceBuiltin},
	{ID: commandHelp, Name: "/help", Description: "show available commands", Source: commandSourceBuiltin},
	{ID: commandKeys, Name: "/keys", Description: "show keyboard shortcuts", ArgsHint: "[reset]", Source: commandSourceBuiltin},
	{ID: commandLegend, Name: "/legend", Description: "explain UI icons, status glyphs, and chrome", Source: commandSourceBuiltin},
	{ID: commandMemory, Name: "/memory", Description: "list, get, set, delete, export, or import project memory", ArgsHint: "[list|get|set|rm|export|import] ...", Source: commandSourceBuiltin},
	{ID: commandQueue, Name: "/queue", Description: "browse and edit prompts queued while a turn runs", Source: commandSourceBuiltin},
	{ID: commandIssues, Name: "/issues", Description: "list, add, get, close, export, or import project issues", ArgsHint: "[list|add|get|close|export|import] ...", Source: commandSourceBuiltin},
	{ID: commandPlan, Name: "/plan", Description: "browse and edit root-owned structured plans", ArgsHint: "[list|create|get|approve|close|reopen] ...", Source: commandSourceBuiltin},
	{ID: commandGoal, Name: "/goal", Description: "loop harness: set, run, status, pause, resume, abort, log, list", ArgsHint: "[set|run|status|pause|resume|abort|log|list] ...", Source: commandSourceBuiltin},
	{ID: commandLoop, Name: "/loop", Description: "schedule a recurring LLM job (session-only)", ArgsHint: "[interval job|list|stop [id]]", Source: commandSourceBuiltin},
	{ID: commandWorkflow, Name: "/workflow", Description: "list, inspect, start, stop, or edit workflows", ArgsHint: "[list|inspect|start|stop|new|edit] ...", Source: commandSourceBuiltin},
	{ID: commandContext, Name: "/context", Description: "context doctor: system-prompt layer breakdown", Source: commandSourceBuiltin},
	{ID: commandEffectivePrompt, Name: "/effective-prompt", Description: "context doctor: system-prompt layer breakdown", Source: commandSourceBuiltin},
	{ID: commandCost, Name: "/cost", Description: "session token and cost totals", Source: commandSourceBuiltin},
	{ID: commandUpgrade, Name: "/upgrade", Description: "install the latest release and restart", Source: commandSourceBuiltin},
	{ID: commandInit, Name: "/init", Description: "create or update project AGENTS.md", Source: commandSourceBuiltin},
	{ID: commandFTUE, Name: "/ftue", Description: "setup wizard: provider, model, optional init, feature tour, scheduler presets, first prompt", Source: commandSourceBuiltin},
	{ID: commandMCP, Name: "/mcp", Description: "MCP servers: status, retry, disable", ArgsHint: "[retry [name]|disable <name>]", Source: commandSourceBuiltin},
	{ID: commandLSP, Name: "/lsp", Description: "language servers: status, retry, disable", ArgsHint: "[retry [name]|disable <name>]", Source: commandSourceBuiltin},
	{ID: commandDiagnostics, Name: "/diagnostics", Description: "focus the diagnostics right pane", Source: commandSourceBuiltin},
	{ID: commandExit, Name: "/exit", Description: "quit strike", Source: commandSourceBuiltin},
	{ID: commandQuit, Name: "/quit", Description: "quit strike", Source: commandSourceBuiltin},
}

func init() {
	// Append keybind-backed mirrors so builtinCommandSpecs stays the single
	// ordered catalog used by completion, palette, and /help.
	builtinCommandSpecs = append(builtinCommandSpecs, keybindBackedCommandSpecs...)
}

// commandCatalog builds the slash-command catalog from the builtins and the
// host-supplied skills. Skills arrive pre-filtered by the host, but their
// display names are still guarded here (validSkillName) because they become
// terminal-rendered slash commands, and their descriptions are sanitized
// against control-sequence injection.
func commandCatalog(skills []host.Skill) []commandSpec {
	catalog := make([]commandSpec, len(builtinCommandSpecs), len(builtinCommandSpecs)+len(skills))
	copy(catalog, builtinCommandSpecs)
	for _, skill := range skills {
		if !validSkillName(skill.Name) {
			continue
		}
		argsHint := ""
		if skill.HasArgs {
			argsHint = "$ARGUMENTS"
		}
		catalog = append(catalog, commandSpec{
			ID:          commandID("skill:" + skill.Name),
			Name:        "/" + sanitizeDisplayData(skill.Name),
			Description: sanitizeDisplayData(skill.Description),
			ArgsHint:    argsHint,
			Source:      commandSourceSkill,
		})
	}
	return catalog
}

// isControlRune reports whether r is a C0 or C1 control character (including
// the escape that begins a terminal control sequence).
func isControlRune(r rune) bool {
	return r <= 0x1f || (r >= 0x7f && r <= 0x9f)
}

// sanitizeDisplayData replaces control characters with the Unicode replacement
// rune, so untrusted skill/agent text cannot inject terminal metadata or extra
// rows when rendered.
func sanitizeDisplayData(value string) string {
	return strings.Map(func(r rune) rune {
		if isControlRune(r) {
			return utf8.RuneError
		}
		return r
	}, value)
}

// reservedCommandNames are slash-command names owned by builtins; a skill may
// not shadow one.
var reservedCommandNames = map[string]struct{}{
	"provider":         {},
	"model":            {},
	"effort":           {},
	"autonomy":         {},
	"mode":             {},
	"sandbox":          {},
	"permission":       {},
	"auth":             {},
	"settings":         {},
	"agent":            {},
	"agents":           {},
	"activity":         {},
	"files":            {},
	"visualizer":       {},
	"system":           {},
	"telemetry":        {},
	"pets":             {},
	"fast":             {},
	"think":            {},
	"vim":              {},
	"nano":             {},
	"md-read":          {},
	"theme":            {},
	"layout":           {},
	"split":            {},
	"compact":          {},
	"fork":             {},
	"undo":             {},
	"rewind":           {},
	"session":          {},
	"rename":           {},
	"export":           {},
	"timeline":         {},
	"diag":             {},
	"diagnostic":       {},
	"copy":             {},
	"help":             {},
	"keys":             {},
	"legend":           {},
	"memory":           {},
	"queue":            {},
	"issues":           {},
	"plan":             {},
	"goal":             {},
	"loop":             {},
	"workflow":         {},
	"context":          {},
	"effective-prompt": {},
	"cost":             {},
	"upgrade":          {},
	"init":             {},
	"ftue":             {},
	"mcp":              {},
	"lsp":              {},
	"diagnostics":      {},
	"exit":             {},
	"quit":             {},
	// Keybind-backed action mirrors.
	"focus-left":     {},
	"focus-right":    {},
	"window-next":    {},
	"window-prev":    {},
	"group-next":     {},
	"group-prev":     {},
	"scroll-up":      {},
	"scroll-down":    {},
	"jump-bottom":    {},
	"palette":        {},
	"interrupt":      {},
	"save-defaults":  {},
	"leave-editor":   {},
	"edit-prompt":    {},
	"agent-next":     {},
	"mode-next":      {},
	"tool-prev":      {},
	"tool-next":      {},
	"tool-expand":    {},
	"tool-copy":      {},
	"tool-review":    {},
	"tool-apply":     {},
	"subagent":       {},
	"parent":         {},
	"subagent-next":  {},
	"subagent-prev":  {},
	"root-new":       {},
	"root-open":      {},
	"root-interrupt": {},
	"root-hide":      {},
	"root-filter":    {},
}

// validSkillName reports whether a skill name is safe to render and select as a
// slash command: valid UTF-8, no control characters, no whitespace or slash,
// and not a reserved builtin name. It mirrors the host's authoritative
// ValidateSkillName so the frontend never renders an unsafe name, without
// importing the config package across the frontend boundary.
func validSkillName(name string) bool {
	if !displaySafeName(name) {
		return false
	}
	if _, reserved := reservedCommandNames[name]; reserved {
		return false
	}
	for _, r := range name {
		if r == '/' || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// validAgentName reports whether an agent name is safe to render and select:
// valid UTF-8, no control characters, and no leading/trailing whitespace
// (internal spaces are allowed for multi-word names). Agents are not filtered
// host-side, so the frontend guards their display safety here.
func validAgentName(name string) bool {
	return displaySafeName(name) && strings.TrimSpace(name) == name
}

// displaySafeName is the shared floor for identifier safety: non-empty, valid
// UTF-8, and free of C0/C1 control characters.
func displaySafeName(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if isControlRune(r) {
			return false
		}
	}
	return true
}
