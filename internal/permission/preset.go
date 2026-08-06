package permission

import "strings"

// Preset is a named, documented permission ruleset operators can select via
// config permissionPreset or inspect with /permission presets.
type Preset struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Rules       Ruleset `json:"rules"`
}

// Shipped preset IDs (stable for config and docs).
const (
	PresetIDReadOnly = "read-only"
	PresetIDDev      = "dev"
	// PresetIDYoloSandbox documents allow-all asks while relying on OS sandbox
	// (permissionMode yolo is separate; this preset only widens rule allows).
	PresetIDYoloSandbox = "yolo-with-sandbox"
)

// Presets returns shipped presets in display order.
func Presets() []Preset {
	return []Preset{
		presetReadOnly(),
		presetDev(),
		presetYoloSandbox(),
	}
}

// PresetByID looks up a shipped preset (case-insensitive). ok is false when unknown.
func PresetByID(id string) (Preset, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return Preset{}, false
	}
	for _, p := range Presets() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// ValidPresetID reports whether id is a shipped preset (empty is valid = none).
func ValidPresetID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	_, ok := PresetByID(id)
	return ok
}

func presetReadOnly() Preset {
	// Harden mutators and network; keep search/read free. task stays allow so
	// explore-style children can still spawn under depth limits.
	return Preset{
		ID:   PresetIDReadOnly,
		Name: "Read-only",
		Description: "Allow read/search tools; deny write, edit, bash, webfetch, and mcp. " +
			"Safe for review and exploration without mutating the workspace.",
		Rules: Ruleset{
			{Permission: "read", Pattern: "*", Action: Allow},
			{Permission: "glob", Pattern: "*", Action: Allow},
			{Permission: "grep", Pattern: "*", Action: Allow},
			{Permission: "definition", Pattern: "*", Action: Allow},
			{Permission: "references", Pattern: "*", Action: Allow},
			{Permission: "symbols", Pattern: "*", Action: Allow},
			{Permission: "write", Pattern: "*", Action: Deny},
			{Permission: "edit", Pattern: "*", Action: Deny},
			{Permission: "bash", Pattern: "*", Action: Deny},
			{Permission: "webfetch", Pattern: "*", Action: Deny},
			{Permission: "mcp", Pattern: "*", Action: Deny},
			{Permission: "hook", Pattern: "*", Action: Deny},
			{Permission: "phase_check", Pattern: "*", Action: Deny},
		},
	}
}

func presetDev() Preset {
	// Common local-dev allowlist on top of defaults: go/git/make test flows
	// and workspace edits still ask unless listed; secrets stay denied.
	return Preset{
		ID:   PresetIDDev,
		Name: "Dev",
		Description: "Allow common local-dev shell (go, git status/diff/log, make test) and " +
			"keep write/edit on ask. Denies destructive git push --force and .env writes. " +
			"Broader than read-only; still prompts for unlisted bash and mutations.",
		Rules: Ruleset{
			// doublestar '*' does not cross '/'. Pair bare and path forms so
			// "go test ./..." and "git status" both match.
			{Permission: "bash", Pattern: "go *", Action: Allow},
			{Permission: "bash", Pattern: "go*/**", Action: Allow},
			{Permission: "bash", Pattern: "git status*", Action: Allow},
			{Permission: "bash", Pattern: "git diff*", Action: Allow},
			{Permission: "bash", Pattern: "git log*", Action: Allow},
			{Permission: "bash", Pattern: "git show*", Action: Allow},
			{Permission: "bash", Pattern: "make test*", Action: Allow},
			{Permission: "bash", Pattern: "make *", Action: Ask},
			{Permission: "bash", Pattern: "git push --force*", Action: Deny},
			{Permission: "bash", Pattern: "git push -f*", Action: Deny},
			{Permission: "bash", Pattern: "git push --force*/**", Action: Deny},
			{Permission: "bash", Pattern: "git push -f*/**", Action: Deny},
			{Permission: "write", Pattern: "**/.env", Action: Deny},
			{Permission: "write", Pattern: "**/.env.*", Action: Deny},
			{Permission: "edit", Pattern: "**/.env", Action: Deny},
			{Permission: "edit", Pattern: "**/.env.*", Action: Deny},
		},
	}
}

func presetYoloSandbox() Preset {
	// Rule-level allow-all for tool asks. Operators should pair with sandbox
	// workspace-write or read-only; yolo mode is still a separate dial.
	return Preset{
		ID:   PresetIDYoloSandbox,
		Name: "Yolo with sandbox",
		Description: "Allow all tool permissions at the ruleset layer (no asks from rules). " +
			"Does not disable OS sandbox — keep sandbox at workspace-write or read-only. " +
			"Explicit later deny rules (agent/phase/config after preset) still apply. " +
			"Distinct from permissionMode yolo (which only upgrades remaining asks).",
		Rules: Ruleset{
			{Permission: "*", Pattern: "*", Action: Allow},
		},
	}
}
