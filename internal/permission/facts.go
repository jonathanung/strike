package permission

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jonathanung/strike-cli/internal/actionfacts"
)

// EvalPath identifies which matching path produced the decisive rule match.
const (
	// EvalPathPattern means only the raw tool pattern (legacy glob) was used.
	EvalPathPattern = "pattern"
	// EvalPathFacts means the decisive match used authoritative action facts.
	EvalPathFacts = "facts"
	// EvalPathNone means no rule matched (default Ask).
	EvalPathNone = "none"
)

// DetailedExplanation extends Explanation with action-fact diagnostics (#888).
type DetailedExplanation struct {
	Explanation
	// EvalPath is pattern|facts|none for the decisive matched rule.
	EvalPath string `json:"evalPath,omitempty"`
	// FactsAuthoritative is true when the parse was complete.
	FactsAuthoritative bool `json:"factsAuthoritative,omitempty"`
	// FactsEnforcement is true when facts may drive deny.
	FactsEnforcement bool `json:"factsEnforcement,omitempty"`
	// FactSummary is a short redaction-friendly projection summary.
	FactSummary string `json:"factSummary,omitempty"`
	// FactKeys are the match keys used when enforcement-eligible (capped).
	FactKeys []string `json:"factKeys,omitempty"`
}

// EvaluateDetailed is last-match-wins evaluation with action-fact projection
// for bash and selected tools (#888).
//
// Per-rule matching (never dual-eval the same rule for deny):
//   - When facts are EnforcementEligible, each rule is tested against fact
//     match keys first; if any key matches, that rule applies via "facts" and
//     the raw pattern is not also consulted for that rule.
//   - Otherwise the rule is tested only against the raw pattern ("pattern").
//   - Non-authoritative / non-eligible facts never contribute to matching
//     (deny cannot rest on partial parses).
func EvaluateDetailed(permission, pattern string, sets ...Ruleset) (Action, DetailedExplanation) {
	return evaluateDetailed(permission, pattern, analyzeForPermission(permission, pattern), unlabeled(sets)...)
}

// ExplainDetailed is ExplainLabeled plus action-fact diagnostics.
func ExplainDetailed(permission, pattern string, layers []LabeledLayer) DetailedExplanation {
	_, det := evaluateDetailed(permission, pattern, analyzeForPermission(permission, pattern), layers...)
	return det
}

func unlabeled(sets []Ruleset) []LabeledLayer {
	layers := make([]LabeledLayer, len(sets))
	for i, set := range sets {
		layers[i] = LabeledLayer{Name: fmt.Sprintf("layer-%d", i), Rules: set}
	}
	return layers
}

func analyzeForPermission(permission, pattern string) actionfacts.Facts {
	permission = strings.TrimSpace(permission)
	switch permission {
	case "bash", "hook", "phase_check":
		return actionfacts.Analyze(actionfacts.Input{
			Tool:    permission,
			Command: pattern,
		})
	case "webfetch", "websearch", "read", "write", "edit", "glob", "grep", "delete", "move":
		return actionfacts.Analyze(actionfacts.Input{
			Tool: permission,
			Argv: []string{pattern},
		})
	default:
		return actionfacts.Facts{
			Tool:  permission,
			Parse: actionfacts.ParseResult{Status: actionfacts.StatusNotApplicable},
		}
	}
}

func evaluateDetailed(permission, pattern string, facts actionfacts.Facts, layers ...LabeledLayer) (Action, DetailedExplanation) {
	if pattern == "" {
		pattern = "*"
	}
	ex := DetailedExplanation{
		Explanation: Explanation{
			Permission: permission,
			Pattern:    pattern,
			Action:     Ask,
		},
		EvalPath:           EvalPathNone,
		FactsAuthoritative: facts.Authoritative(),
		FactsEnforcement:   facts.EnforcementEligible(),
		FactSummary:        actionfacts.Summary(facts),
	}
	if facts.EnforcementEligible() {
		ex.FactKeys = actionfacts.MatchKeys(facts)
	}

	useFacts := facts.EnforcementEligible()
	var trail []Match
	var last *Match
	lastPath := EvalPathNone
	action := Ask

	for li, layer := range layers {
		name := layer.Name
		if name == "" {
			name = fmt.Sprintf("layer-%d", li)
		}
		for ri, rule := range layer.Rules {
			via, ok := rule.matchPath(permission, pattern, useFacts, ex.FactKeys)
			if !ok {
				continue
			}
			m := Match{
				Layer:      name,
				LayerIndex: li,
				RuleIndex:  ri,
				Permission: rule.Permission,
				Pattern:    normalizePattern(rule.Pattern),
				Action:     rule.Action,
			}
			trail = append(trail, m)
			cp := m
			last = &cp
			lastPath = via
			action = rule.Action
		}
	}

	ex.Trail = trail
	ex.Matched = last
	ex.Action = action
	ex.EvalPath = lastPath
	if last == nil {
		ex.EvalPath = EvalPathNone
		ex.Matched = &Match{
			Layer:      LayerDefaultAction,
			LayerIndex: -1,
			RuleIndex:  -1,
			Permission: permission,
			Pattern:    pattern,
			Action:     Ask,
		}
	}
	return action, ex
}

// matchPath returns how the rule matched. When useFacts is set, fact keys are
// tried first; a fact hit skips the raw pattern for this rule (no dual-eval).
func (r Rule) matchPath(permission, pattern string, useFacts bool, factKeys []string) (via string, ok bool) {
	if r.Permission != "*" && r.Permission != permission {
		return "", false
	}
	if useFacts && len(factKeys) > 0 {
		for _, key := range factKeys {
			if r.globMatches(key) {
				return EvalPathFacts, true
			}
		}
		// No fact key matched — fall through to raw pattern only.
	}
	if r.globMatches(pattern) {
		return EvalPathPattern, true
	}
	return "", false
}

func (r Rule) globMatches(pattern string) bool {
	if r.Pattern == "*" || r.Pattern == "" {
		return true
	}
	ok, err := doublestar.Match(r.Pattern, pattern)
	return err == nil && ok
}

// FormatDetailedExplanation renders explain output including fact path.
func FormatDetailedExplanation(ex DetailedExplanation) string {
	base := FormatExplanation(ex.Explanation)
	var extra []string
	if ex.EvalPath != "" && ex.EvalPath != EvalPathNone {
		extra = append(extra, "eval="+ex.EvalPath)
	} else if ex.EvalPath == EvalPathNone && ex.Matched != nil && ex.Matched.Layer == LayerDefaultAction {
		extra = append(extra, "eval=none")
	}
	if ex.FactSummary != "" {
		extra = append(extra, "facts: "+ex.FactSummary)
	}
	if len(extra) == 0 {
		return base
	}
	return base + "\n  " + strings.Join(extra, "\n  ")
}
