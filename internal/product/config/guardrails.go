package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// GuardrailDials is a partial update for the session guardrail dials (#1577):
// the turn deadline, cost/token envelopes, default child budget, delegation
// policy, child handoff gate and child isolation. Empty strings leave the
// stored value unchanged. Every field is a string so the Settings API and TUI
// share one vocabulary; values are validated before anything is written.
//
//	TurnTimeout            — "default" (product 1800s) | off|0 | seconds | Go duration (30m, 1h)
//	MaxSessionCostUSD      — off|0|unlimited|default | positive USD
//	MaxTurnTokens          — off|0|unlimited|default | positive int
//	AgentBudget*           — off|0|unlimited|default | positive number (cost in USD)
//	DelegationMode         — off|advise|enforce | default (product enforce)
//	DelegationTinyPromptRunes / DelegationMaxPathsLocal
//	                       — default|0 (engine default) | off (disable rule) | positive int
//	DelegationMaxLiveChildren — off|0|unlimited|default | positive int
//	ChildHandoffRequired   — on|off | default (required)
//	ChildIsolation         — shared|off (children run in the parent workdir) | worktree | default (shared)
type GuardrailDials struct {
	TurnTimeout       string
	MaxSessionCostUSD string
	MaxTurnTokens     string

	AgentBudgetMaxWallClockS     string
	AgentBudgetMaxTokens         string
	AgentBudgetMaxCostUSD        string
	AgentBudgetMaxToolCalls      string
	AgentBudgetMaxDangerousTools string
	AgentBudgetStallAfterS       string
	AgentBudgetLoopDetectN       string

	DelegationMode            string
	DelegationTinyPromptRunes string
	DelegationMaxPathsLocal   string
	DelegationMaxLiveChildren string

	ChildHandoffRequired string
	ChildIsolation       string
}

// ParseTurnTimeoutSeconds maps a turn-timeout token onto session.turnTimeoutS
// seconds with the same semantics as the --turn-timeout flag: "off", "none",
// "disable(d)", "0" and non-positive values disable the deadline (-1); plain
// integers are seconds; Go durations are truncated to whole seconds (min 1).
// Empty input also disables (flag parity); callers that treat empty as
// "unchanged" must check before calling.
func ParseTurnTimeoutSeconds(value string) (int, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "", "off", "none", "disable", "disabled", "0":
		return -1, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			return -1, nil
		}
		return n, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid turn timeout %q (want duration like 30m/1h/1800s, or off)", value)
	}
	if d <= 0 {
		return -1, nil
	}
	secs := int(d / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs, nil
}

// EffectiveChildIsolation reports the effective child filesystem mode for a
// stored session.childIsolation: worktree, or shared (children run directly
// in the parent's workdir — the default, fully supported mode).
func EffectiveChildIsolation(stored string) string {
	if NormalizeChildIsolation(stored) == ChildIsolationWorktree {
		return ChildIsolationWorktree
	}
	return ChildIsolationShared
}

// EffectiveChildHandoffRequired resolves session.childHandoffRequired (nil
// means required).
func EffectiveChildHandoffRequired(stored *bool) bool {
	return stored == nil || *stored
}

// EffectiveDelegationMode resolves session.delegationPolicy.mode the way the
// CLI composition root does: omitted means enforce.
func EffectiveDelegationMode(stored string) string {
	switch m := strings.ToLower(strings.TrimSpace(stored)); m {
	case "":
		return "enforce"
	case "off", "advise", "enforce":
		return m
	case "on", "true", "yes":
		return "enforce"
	default:
		// The engine clamps unknown modes to enforce.
		return "enforce"
	}
}

// SetGlobalGuardrailDials persists non-empty guardrail dials into
// ~/.strike/config. All fields are validated first; an invalid value rejects
// the whole update without writing. Unrelated config is preserved.
func SetGlobalGuardrailDials(d GuardrailDials) error {
	apply, err := parseGuardrailDials(d)
	if err != nil {
		return err
	}
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	for _, fn := range apply {
		fn(&cfg.Session)
	}
	return writeGlobal(cfg, unlock)
}

type sessionEdit func(*SessionConfig)

func parseGuardrailDials(d GuardrailDials) ([]sessionEdit, error) {
	var edits []sessionEdit
	intDial := func(raw, field string, set func(*SessionConfig, int)) error {
		if raw == "" {
			return nil
		}
		n, err := parseLimitInt(raw, field)
		if err != nil {
			return err
		}
		edits = append(edits, func(s *SessionConfig) { set(s, n) })
		return nil
	}
	costDial := func(raw, field string, set func(*SessionConfig, float64)) error {
		if raw == "" {
			return nil
		}
		v, err := parseLimitCost(raw, field)
		if err != nil {
			return err
		}
		edits = append(edits, func(s *SessionConfig) { set(s, v) })
		return nil
	}
	thresholdDial := func(raw, field string, set func(*SessionConfig, int)) error {
		if raw == "" {
			return nil
		}
		n, err := parseDelegationThreshold(raw, field)
		if err != nil {
			return err
		}
		edits = append(edits, func(s *SessionConfig) { set(s, n) })
		return nil
	}

	if d.TurnTimeout != "" {
		secs := 0
		if !isDefaultToken(d.TurnTimeout) {
			n, err := ParseTurnTimeoutSeconds(d.TurnTimeout)
			if err != nil {
				return nil, fmt.Errorf("invalid turnTimeoutS %q (want default|off|seconds|duration like 30m)", d.TurnTimeout)
			}
			secs = n
		}
		edits = append(edits, func(s *SessionConfig) { s.TurnTimeoutS = secs })
	}
	steps := []error{
		costDial(d.MaxSessionCostUSD, "maxSessionCostUSD", func(s *SessionConfig, v float64) { s.MaxSessionCostUSD = v }),
		intDial(d.MaxTurnTokens, "maxTurnTokens", func(s *SessionConfig, n int) { s.MaxTurnTokens = n }),
		intDial(d.AgentBudgetMaxWallClockS, "agentBudget.maxWallClockS", func(s *SessionConfig, n int) { s.AgentBudget.MaxWallClockS = n }),
		intDial(d.AgentBudgetMaxTokens, "agentBudget.maxTokens", func(s *SessionConfig, n int) { s.AgentBudget.MaxTokens = n }),
		costDial(d.AgentBudgetMaxCostUSD, "agentBudget.maxCostUsd", func(s *SessionConfig, v float64) { s.AgentBudget.MaxCostUSD = v }),
		intDial(d.AgentBudgetMaxToolCalls, "agentBudget.maxToolCalls", func(s *SessionConfig, n int) { s.AgentBudget.MaxToolCalls = n }),
		intDial(d.AgentBudgetMaxDangerousTools, "agentBudget.maxDangerousTools", func(s *SessionConfig, n int) { s.AgentBudget.MaxDangerousTools = n }),
		intDial(d.AgentBudgetStallAfterS, "agentBudget.stallAfterS", func(s *SessionConfig, n int) { s.AgentBudget.StallAfterS = n }),
		intDial(d.AgentBudgetLoopDetectN, "agentBudget.loopDetectN", func(s *SessionConfig, n int) { s.AgentBudget.LoopDetectN = n }),
		thresholdDial(d.DelegationTinyPromptRunes, "delegationPolicy.tinyPromptRunes", func(s *SessionConfig, n int) { s.DelegationPolicy.TinyPromptRunes = n }),
		thresholdDial(d.DelegationMaxPathsLocal, "delegationPolicy.maxPathsLocal", func(s *SessionConfig, n int) { s.DelegationPolicy.MaxPathsLocal = n }),
		intDial(d.DelegationMaxLiveChildren, "delegationPolicy.maxLiveChildren", func(s *SessionConfig, n int) { s.DelegationPolicy.MaxLiveChildren = n }),
	}
	for _, err := range steps {
		if err != nil {
			return nil, err
		}
	}
	if d.DelegationMode != "" {
		mode, ok := parseDelegationMode(d.DelegationMode)
		if !ok {
			return nil, fmt.Errorf("unknown delegationPolicy.mode %q (want off|advise|enforce|default)", d.DelegationMode)
		}
		edits = append(edits, func(s *SessionConfig) { s.DelegationPolicy.Mode = mode })
	}
	if d.ChildHandoffRequired != "" {
		v, ok := parseHandoffRequired(d.ChildHandoffRequired)
		if !ok {
			return nil, fmt.Errorf("unknown childHandoffRequired %q (want on|off|default)", d.ChildHandoffRequired)
		}
		edits = append(edits, func(s *SessionConfig) { s.ChildHandoffRequired = v })
	}
	if d.ChildIsolation != "" {
		mode, ok := parseChildIsolation(d.ChildIsolation)
		if !ok {
			return nil, fmt.Errorf("unknown childIsolation %q (want shared|worktree|default)", d.ChildIsolation)
		}
		edits = append(edits, func(s *SessionConfig) { s.ChildIsolation = mode })
	}
	return edits, nil
}

func isDefaultToken(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "default", "unset":
		return true
	}
	return false
}

func isUnlimitedToken(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "0", "none", "unlimited", "default", "unset":
		return true
	}
	return false
}

// parseLimitInt parses a non-negative cap where zero means unlimited.
func parseLimitInt(raw, field string) (int, error) {
	if isUnlimitedToken(raw) {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid %s %q (want off or a positive integer)", field, raw)
	}
	return n, nil
}

// parseLimitCost parses a non-negative finite USD cap where zero means unlimited.
func parseLimitCost(raw, field string) (float64, error) {
	if isUnlimitedToken(raw) {
		return 0, nil
	}
	v, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(raw), "$"), 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("invalid %s %q (want off or a positive USD amount)", field, raw)
	}
	return v, nil
}

// parseDelegationThreshold: default|0 → 0 (engine default); off → -1 (rule
// disabled); positive int.
func parseDelegationThreshold(raw, field string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "default", "0", "unset":
		return 0, nil
	case "off", "disabled", "none":
		return -1, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s %q (want default|off or a positive integer)", field, raw)
	}
	return n, nil
}

func parseDelegationMode(raw string) (string, bool) {
	switch m := strings.ToLower(strings.TrimSpace(raw)); m {
	case "off", "advise", "enforce":
		return m, true
	case "default", "unset":
		return "", true
	}
	return "", false
}

func parseHandoffRequired(raw string) (*bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "yes", "required":
		v := true
		return &v, true
	case "off", "false", "no", "optional":
		v := false
		return &v, true
	case "default", "unset":
		return nil, true
	}
	return nil, false
}

func parseChildIsolation(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ChildIsolationShared, ChildIsolationOff:
		return ChildIsolationShared, true
	case ChildIsolationWorktree:
		return ChildIsolationWorktree, true
	case "default", "unset":
		return "", true
	}
	return "", false
}
