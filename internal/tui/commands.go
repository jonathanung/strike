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
	commandAuth            commandID = "auth"
	commandSettings        commandID = "settings"
	commandAgent           commandID = "agent"
	commandFast            commandID = "fast"
	commandVim             commandID = "vim"
	commandHelp            commandID = "help"
	commandKeys            commandID = "keys"
	commandMDRead          commandID = "md-read"
	commandTheme           commandID = "theme"
	commandLayout          commandID = "layout"
	commandSplit           commandID = "split"
	commandCompact         commandID = "compact"
	commandFork            commandID = "fork"
	commandUndo            commandID = "undo"
	commandSession         commandID = "session"
	commandMemory          commandID = "memory"
	commandIssues          commandID = "issues"
	commandContext         commandID = "context"
	commandEffectivePrompt commandID = "effective-prompt"
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
	{ID: commandModel, Name: "/model", Description: "select a model for the current provider", ArgsHint: "[model]", Source: commandSourceBuiltin},
	{ID: commandEffort, Name: "/effort", Description: "set how much reasoning the model spends", ArgsHint: "[level]", Source: commandSourceBuiltin},
	{ID: commandAutonomy, Name: "/autonomy", Description: "set exit-gate policy (supervised/agent/checks)", ArgsHint: "[mode]", Source: commandSourceBuiltin},
	{ID: commandAuth, Name: "/auth", Description: "manage provider authentication", ArgsHint: "[provider]", Source: commandSourceBuiltin},
	{ID: commandSettings, Name: "/settings", Description: "manage custom providers and settings", Source: commandSourceBuiltin},
	{ID: commandAgent, Name: "/agent", Description: "select an agent", ArgsHint: "[name]", Source: commandSourceBuiltin},
	{ID: commandFast, Name: "/fast", Description: "toggle OpenAI priority tier (faster, ~2× cost)", ArgsHint: "[on|off]", Source: commandSourceBuiltin},
	{ID: commandVim, Name: "/vim", Description: "open a file in the embedded editor (pane/overlay) or $EDITOR", ArgsHint: "[path[:line]]", Source: commandSourceBuiltin},
	{ID: commandMDRead, Name: "/md-read", Description: "open a markdown file in the right pane", ArgsHint: "<path>", Source: commandSourceBuiltin},
	{ID: commandTheme, Name: "/theme", Description: "select a color theme or set appearance", ArgsHint: "[name|dark|light|auto]", Source: commandSourceBuiltin},
	{ID: commandLayout, Name: "/layout", Description: "toggle horizontal/vertical pane split", Source: commandSourceBuiltin},
	{ID: commandSplit, Name: "/split", Description: "toggle horizontal/vertical pane split", Source: commandSourceBuiltin},
	{ID: commandCompact, Name: "/compact", Description: "compact model history (keep recent turns)", Source: commandSourceBuiltin},
	{ID: commandFork, Name: "/fork", Description: "duplicate the conversation into a new id", Source: commandSourceBuiltin},
	{ID: commandUndo, Name: "/undo", Description: "drop the last completed user/assistant turn", Source: commandSourceBuiltin},
	{ID: commandSession, Name: "/session", Description: "browse and resume a past session", ArgsHint: "[id]", Source: commandSourceBuiltin},
	{ID: commandHelp, Name: "/help", Description: "show available commands", Source: commandSourceBuiltin},
	{ID: commandKeys, Name: "/keys", Description: "show keyboard shortcuts", Source: commandSourceBuiltin},
	{ID: commandMemory, Name: "/memory", Description: "list, get, set, or delete project memory", ArgsHint: "[list|get|set|rm] ...", Source: commandSourceBuiltin},
	{ID: commandIssues, Name: "/issues", Description: "list, add, get, or close project issues", ArgsHint: "[list|add|get|close] ...", Source: commandSourceBuiltin},
	{ID: commandContext, Name: "/context", Description: "inspect effective system-prompt layers", Source: commandSourceBuiltin},
	{ID: commandEffectivePrompt, Name: "/effective-prompt", Description: "inspect effective system-prompt layers", Source: commandSourceBuiltin},
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
	"auth":             {},
	"settings":         {},
	"agent":            {},
	"fast":             {},
	"vim":              {},
	"md-read":          {},
	"theme":            {},
	"layout":           {},
	"split":            {},
	"compact":          {},
	"fork":             {},
	"undo":             {},
	"rewind":           {},
	"session":          {},
	"help":             {},
	"keys":             {},
	"memory":           {},
	"issues":           {},
	"context":          {},
	"effective-prompt": {},
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
