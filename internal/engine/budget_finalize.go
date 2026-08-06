package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// budgetFinalizationReserve is the wall-clock ceiling for one soft-budget
// finalization turn. Finalization cannot exceed this reserve (#879).
const budgetFinalizationReserve = 45 * time.Second

// budgetFinalizationPrompt asks the child for a structured handoff before hard stop.
func budgetFinalizationPrompt(kind, reason string) string {
	kind = strings.TrimSpace(kind)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "agent budget exhausted"
	}
	var b strings.Builder
	b.WriteString("## Budget finalization (required)\n\n")
	b.WriteString("Your run hit a resource budget and will stop after this message. ")
	b.WriteString("Do **not** call any tools. Immediately return your current findings as a ")
	b.WriteString("single machine-parseable JSON handoff object (whole message, trailing object, or ")
	b.WriteString("```json")
	b.WriteString(" fence).\n\n")
	if kind != "" {
		fmt.Fprintf(&b, "Exceeded budget: %s\n", kind)
	}
	fmt.Fprintf(&b, "Reason: %s\n\n", reason)
	b.WriteString("Required schema (empty arrays/strings allowed when honest):\n")
	b.WriteString(`{
  "summary": "short outcome so far",
  "files_changed": ["path/relative.go"],
  "verification": "what you ran and results",
  "findings": ["notable discovery or risk"],
  "blockers": ["what stopped you"],
  "recommended_next_action": "concrete next step for the lead",
  "artifact_refs": [{"id": "artifactId", "version": 1, "type": "findings"}],
  "provenance": [],
  "missing_context": [],
  "incomplete": true
}
`)
	b.WriteString("\nPrefer artifact_refs for large findings already written. Include partial ")
	b.WriteString("files_changed and findings even if work is incomplete. This is your only ")
	b.WriteString("chance to hand off before hard termination.")
	return b.String()
}

// softBudgetAllowsFinalization reports whether a budget kind should attempt one
// reserved model handoff turn before hard stop. Hard cancel / trust-boundary /
// session ceilings never reach this path.
func softBudgetAllowsFinalization(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "wall_clock", "tokens", "cost_usd", "tool_calls", "dangerous_tools", "stall", "loop":
		return true
	default:
		return false
	}
}

// classifyHandoffQuality distinguishes complete / partial / unavailable (#879).
// parsed is true when model-supplied structured JSON was decoded.
func classifyHandoffQuality(h protocol.CompletionHandoff, parsed bool) string {
	if parsed && !h.Incomplete {
		// Structured schema present — complete even if some fields are empty.
		return protocol.HandoffQualityComplete
	}
	if handoffHasSubstance(h) {
		return protocol.HandoffQualityPartial
	}
	return protocol.HandoffQualityUnavailable
}

// handoffHasSubstance reports recoverable work product beyond generic defaults.
func handoffHasSubstance(h protocol.CompletionHandoff) bool {
	if len(h.Findings) > 0 || len(h.FilesChanged) > 0 || len(h.ArtifactRefs) > 0 {
		return true
	}
	if len(h.MissingContext) > 0 || len(h.Provenance) > 0 {
		return true
	}
	if strings.TrimSpace(h.Verification) != "" {
		return true
	}
	if strings.TrimSpace(h.RecommendedNextAction) != "" {
		return true
	}
	// Non-generic blockers (more than a single engine default).
	if len(h.Blockers) > 1 {
		return true
	}
	if len(h.Blockers) == 1 {
		b := strings.TrimSpace(h.Blockers[0])
		if b != "" && b != "task failed" && b != "task canceled" &&
			!strings.HasPrefix(b, "wall-clock budget") &&
			!strings.HasPrefix(b, "token budget") &&
			!strings.HasPrefix(b, "cost budget") &&
			!strings.HasPrefix(b, "tool-call budget") &&
			!strings.HasPrefix(b, "dangerous-tool budget") &&
			!strings.HasPrefix(b, "stale/stall") &&
			!strings.HasPrefix(b, "loop detected") {
			return true
		}
	}
	sum := strings.TrimSpace(h.Summary)
	if sum == "" {
		return false
	}
	switch sum {
	case "task completed", "task failed", "task canceled", "task blocked (verification failed)":
		return false
	}
	// Budget reason alone is not usable findings.
	if strings.Contains(sum, "budget exhausted") || strings.Contains(sum, "loop detected") ||
		strings.Contains(sum, "stale/stall") {
		// Still partial if summary has more than the reason line (model prose).
		if strings.Count(sum, "\n") >= 1 && len(sum) > 80 {
			return true
		}
		return false
	}
	// Any other non-empty summary counts as partial recovery.
	return true
}

// applyHandoffQuality sets Quality from parse result + substance.
func applyHandoffQuality(h *protocol.CompletionHandoff, parsed bool) {
	if h == nil {
		return
	}
	h.Quality = classifyHandoffQuality(*h, parsed)
}

// mergeArtifactRefsIntoHandoff unions engine-tracked artifact refs into handoff.
func mergeArtifactRefsIntoHandoff(h *protocol.CompletionHandoff, tracked []protocol.ArtifactRef) {
	if h == nil || len(tracked) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(h.ArtifactRefs)+len(tracked))
	for _, r := range h.ArtifactRefs {
		id := strings.TrimSpace(r.ID)
		if id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, r := range tracked {
		id := strings.TrimSpace(r.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		h.ArtifactRefs = append(h.ArtifactRefs, protocol.ArtifactRef{
			ID:      id,
			Version: r.Version,
			Type:    strings.TrimSpace(r.Type),
		})
	}
}
