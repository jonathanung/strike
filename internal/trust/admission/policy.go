// Package admission scans MCP servers, skills, and plugins at register/load
// time and applies a severity→action policy (allow|warn|block|quarantine).
//
// It does not replace OS sandbox or permission rules — it gates whether
// capability surfaces bind into the tool registry / skill catalog at all.
// Finding types live in internal/trust/security for reuse by write-time content
// guards (#890).
package admission

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/trust/security"
)

// Action is the admission verdict for a subject after scanning.
type Action string

const (
	ActionAllow      Action = "allow"
	ActionWarn       Action = "warn"
	ActionBlock      Action = "block"
	ActionQuarantine Action = "quarantine"
)

// Preset IDs (stable for config and docs).
const (
	PresetPermissive = "permissive"
	PresetDefault    = "default"
	PresetStrict     = "strict"
)

// Policy is the resolved admission matrix for one process.
type Policy struct {
	// Preset is the named matrix id (permissive|default|strict).
	Preset string
	// Matrix maps finding severity → action. Missing severities default to allow.
	Matrix map[security.Severity]Action
	// FailClosed: scanner/internal errors become block (strict default true).
	FailClosed bool
	// AllowPaths are validated home-anchored absolute prefixes (see NormalizeAllowPaths).
	AllowPaths []string
	// Home is the operator home directory used for path anchoring (tests set explicitly).
	Home string
}

// Config is the JSON-facing admission object (config.admission).
type Config struct {
	// Preset is permissive|default|strict. Empty means default.
	Preset string `json:"preset,omitempty"`
	// AllowPaths lists home-anchored path prefixes trusted as first-party.
	// Entries must be absolute under $HOME or start with ~/. Bare relative
	// markers (e.g. ".strike/skills") are rejected at load — they are spoofable
	// via subdirectories.
	AllowPaths []string `json:"allowPaths,omitempty"`
	// FailClosed overrides the preset default when non-nil.
	FailClosed *bool `json:"failClosed,omitempty"`
}

// Verdict is the admission outcome for one subject (server, skill, plugin).
type Verdict struct {
	Surface  string             `json:"surface"`
	Target   string             `json:"target"`
	Action   Action             `json:"action"`
	Reason   string             `json:"reason"`
	Findings []security.Finding `json:"findings,omitempty"`
	// ScanError is set when the scanner itself failed; Action reflects fail-open/closed.
	ScanError string `json:"scanError,omitempty"`
}

// BindsTools reports whether tools/skills should enter the active registry.
// quarantine and block both refuse binding; warn and allow bind.
func (v Verdict) BindsTools() bool {
	switch v.Action {
	case ActionBlock, ActionQuarantine:
		return false
	default:
		return true
	}
}

// PresetMatrix returns the shipped severity→action matrix for id.
func PresetMatrix(id string) (map[security.Severity]Action, bool) {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "", PresetDefault:
		return map[security.Severity]Action{
			security.SeverityInfo:     ActionAllow,
			security.SeverityLow:      ActionAllow,
			security.SeverityMedium:   ActionWarn,
			security.SeverityHigh:     ActionQuarantine,
			security.SeverityCritical: ActionBlock,
		}, true
	case PresetPermissive:
		return map[security.Severity]Action{
			security.SeverityInfo:     ActionAllow,
			security.SeverityLow:      ActionAllow,
			security.SeverityMedium:   ActionAllow,
			security.SeverityHigh:     ActionWarn,
			security.SeverityCritical: ActionQuarantine,
		}, true
	case PresetStrict:
		return map[security.Severity]Action{
			security.SeverityInfo:     ActionAllow,
			security.SeverityLow:      ActionWarn,
			security.SeverityMedium:   ActionQuarantine,
			security.SeverityHigh:     ActionBlock,
			security.SeverityCritical: ActionBlock,
		}, true
	default:
		return nil, false
	}
}

// ValidPresetID reports whether id is a shipped admission preset (empty = default).
func ValidPresetID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	_, ok := PresetMatrix(id)
	return ok
}

// Resolve builds a Policy from config. home may be empty (path checks skip home expand).
// Invalid preset returns an error. AllowPaths are normalized (invalid entries error).
func Resolve(cfg Config, home string) (Policy, error) {
	preset := strings.ToLower(strings.TrimSpace(cfg.Preset))
	if preset == "" {
		preset = PresetDefault
	}
	matrix, ok := PresetMatrix(preset)
	if !ok {
		return Policy{}, fmt.Errorf("unknown admission.preset %q (want permissive|default|strict)", cfg.Preset)
	}
	paths, err := NormalizeAllowPaths(cfg.AllowPaths, home)
	if err != nil {
		return Policy{}, err
	}
	failClosed := preset == PresetStrict
	if cfg.FailClosed != nil {
		failClosed = *cfg.FailClosed
	}
	// Copy matrix so callers cannot mutate shipped tables.
	cp := make(map[security.Severity]Action, len(matrix))
	for k, v := range matrix {
		cp[k] = v
	}
	return Policy{
		Preset:     preset,
		Matrix:     cp,
		FailClosed: failClosed,
		AllowPaths: paths,
		Home:       home,
	}, nil
}

// ActionFor maps a severity through the policy matrix (missing → allow).
func (p Policy) ActionFor(sev security.Severity) Action {
	if p.Matrix == nil {
		return ActionAllow
	}
	if a, ok := p.Matrix[sev]; ok {
		return a
	}
	return ActionAllow
}

// Decide collapses findings into one Verdict. Highest severity drives the action;
// when multiple findings share that severity, the strictest action wins
// (block > quarantine > warn > allow).
func (p Policy) Decide(surface, target string, findings []security.Finding) Verdict {
	v := Verdict{
		Surface:  surface,
		Target:   target,
		Action:   ActionAllow,
		Findings: append([]security.Finding(nil), findings...),
	}
	if len(findings) == 0 {
		v.Reason = "no findings"
		return v
	}
	maxSev := security.MaxSeverity(findings)
	// Among findings at max severity, pick strictest action.
	action := ActionAllow
	var reasons []string
	for _, f := range findings {
		if f.Severity != maxSev {
			continue
		}
		a := p.ActionFor(f.Severity)
		if stricter(a, action) {
			action = a
		}
		if f.Message != "" {
			reasons = append(reasons, f.Rule+": "+f.Message)
		} else {
			reasons = append(reasons, f.Rule)
		}
	}
	v.Action = action
	v.Reason = strings.Join(reasons, "; ")
	if v.Reason == "" {
		v.Reason = string(action) + " (" + string(maxSev) + ")"
	}
	return v
}

// OnScanError returns a verdict when the scanner itself failed.
func (p Policy) OnScanError(surface, target string, err error) Verdict {
	msg := "scan error"
	if err != nil {
		msg = err.Error()
	}
	if p.FailClosed {
		return Verdict{
			Surface:   surface,
			Target:    target,
			Action:    ActionBlock,
			Reason:    "admission fail-closed: " + msg,
			ScanError: msg,
		}
	}
	return Verdict{
		Surface:   surface,
		Target:    target,
		Action:    ActionWarn,
		Reason:    "admission fail-open: " + msg,
		ScanError: msg,
	}
}

func stricter(a, b Action) bool {
	return actionRank(a) > actionRank(b)
}

func actionRank(a Action) int {
	switch a {
	case ActionAllow:
		return 1
	case ActionWarn:
		return 2
	case ActionQuarantine:
		return 3
	case ActionBlock:
		return 4
	default:
		return 0
	}
}

// FormatVerdict is a one-line operator-visible summary.
func FormatVerdict(v Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "admission %s %s → %s", v.Surface, v.Target, v.Action)
	if v.Reason != "" {
		fmt.Fprintf(&b, " (%s)", v.Reason)
	}
	return b.String()
}
