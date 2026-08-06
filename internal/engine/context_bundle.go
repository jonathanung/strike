package engine

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// protocolContextBundle converts a tool bundle to the wire (camelCase) form.
// Returns nil when empty so ChildStarted omits the field.
func protocolContextBundle(b tool.ContextBundle) *protocol.ContextBundle {
	if b.Empty() {
		return nil
	}
	out := &protocol.ContextBundle{
		Goal:          b.Goal,
		Acceptance:    append([]string(nil), b.Acceptance...),
		AllowedPaths:  append([]string(nil), b.AllowedPaths...),
		RequiredPaths: append([]string(nil), b.RequiredPaths...),
		Constraints:   append([]string(nil), b.Constraints...),
	}
	if len(b.Artifacts) > 0 {
		out.Artifacts = make([]protocol.ArtifactRef, 0, len(b.Artifacts))
		for _, a := range b.Artifacts {
			out.Artifacts = append(out.Artifacts, protocol.ArtifactRef{
				ID:      a.ID,
				Version: a.Version,
				Type:    a.Type,
			})
		}
	}
	if len(b.Items) > 0 {
		out.Items = make([]protocol.ContextBundleItem, 0, len(b.Items))
		for _, it := range b.Items {
			item := protocol.ContextBundleItem{
				ID:    it.ID,
				Kind:  it.Kind,
				Title: it.Title,
				Text:  it.Text,
				Path:  it.Path,
				Hash:  it.Hash,
			}
			if it.Artifact != nil {
				ref := protocol.ArtifactRef{
					ID:      it.Artifact.ID,
					Version: it.Artifact.Version,
					Type:    it.Artifact.Type,
				}
				item.Artifact = &ref
			}
			out.Items = append(out.Items, item)
		}
	}
	if len(b.FilePins) > 0 {
		out.FilePins = make([]protocol.ContextFilePin, 0, len(b.FilePins))
		for _, p := range b.FilePins {
			out.FilePins = append(out.FilePins, protocol.ContextFilePin{
				Path: p.Path,
				Hash: p.Hash,
				Text: p.Text,
			})
		}
	}
	return out
}

// bundlePathScopeRules builds a last-match-wins permission layer that denies
// read/edit/write outside the bundle's allowed_paths globs. Empty allowed
// paths returns nil (no extra scoping).
func bundlePathScopeRules(allowed []string) permission.Ruleset {
	if len(allowed) == 0 {
		return nil
	}
	// Deny-all first, then allow each path (and path/**) so last-match-wins
	// permits only the sealed scope.
	out := permission.Ruleset{
		{Permission: "read", Pattern: "*", Action: permission.Deny},
		{Permission: "edit", Pattern: "*", Action: permission.Deny},
		{Permission: "write", Pattern: "*", Action: permission.Deny},
	}
	for _, p := range allowed {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		patterns := bundlePathPatterns(p)
		for _, pat := range patterns {
			out = append(out,
				permission.Rule{Permission: "read", Pattern: pat, Action: permission.Allow},
				permission.Rule{Permission: "edit", Pattern: pat, Action: permission.Allow},
				permission.Rule{Permission: "write", Pattern: pat, Action: permission.Allow},
			)
		}
	}
	return out
}

// bundlePathPatterns expands a workspace-relative path or glob into permission
// patterns covering the path itself and, for non-glob directory-like entries,
// a recursive /** suffix.
func bundlePathPatterns(p string) []string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return nil
	}
	// Already a glob — use as-is.
	if strings.ContainsAny(p, "*?[") {
		return []string{p}
	}
	// Bare path: allow exact match and everything under it.
	return []string{p, strings.TrimSuffix(p, "/") + "/**"}
}

// childContextBundleSystemPrompt teaches children about the sealed bundle and
// missing_context / provenance handoff fields.
const childContextBundleSystemPrompt = `## Context bundle (sealed at spawn)

A structured context package was attached when you were spawned. Read it with
the context_bundle tool (action get|item|list_items) before assuming lead
intent. It may include goal, acceptance criteria, allowed/required paths,
artifact refs, constraints, addressable items, and file pins.

When you cannot proceed for lack of context, do NOT invent files or decisions.
End with a structured handoff that includes missing_context (and blockers).
The engine marks the task blocked so the lead can resupply context:

{
  "summary": "blocked: missing context",
  "files_changed": [],
  "findings": [],
  "blockers": ["need X to continue"],
  "missing_context": [
    {"kind": "path", "path": "docs/spec.md", "detail": "not in bundle"},
    {"kind": "question", "question": "Which API version?"},
    {"kind": "artifact", "artifact_id": "ab12cd34"},
    {"kind": "item", "item_id": "contract-1"}
  ],
  "recommended_next_action": "lead: attach missing paths/answers and re-delegate"
}

When conclusions rest on bundle items, cite them in provenance (item ids):
"provenance": ["goal", "artifact-1", "constraint-2"]
`

// formatContextBundlePromptLayer renders a compact JSON summary for the system
// prompt (full body remains available via context_bundle tool).
func formatContextBundlePromptLayer(b tool.ContextBundle) string {
	if b.Empty() {
		return ""
	}
	// Prefer a slim view for the prompt layer (omit large file pin bodies).
	type pinView struct {
		Path    string `json:"path"`
		Hash    string `json:"hash,omitempty"`
		HasText bool   `json:"has_text,omitempty"`
	}
	type itemView struct {
		ID    string `json:"id"`
		Kind  string `json:"kind,omitempty"`
		Title string `json:"title,omitempty"`
		Path  string `json:"path,omitempty"`
	}
	pins := make([]pinView, 0, len(b.FilePins))
	for _, p := range b.FilePins {
		pins = append(pins, pinView{
			Path:    p.Path,
			Hash:    p.Hash,
			HasText: strings.TrimSpace(p.Text) != "",
		})
	}
	items := make([]itemView, 0, len(b.Items))
	for _, it := range b.Items {
		items = append(items, itemView{ID: it.ID, Kind: it.Kind, Title: it.Title, Path: it.Path})
	}
	view := map[string]any{
		"goal":           b.Goal,
		"acceptance":     b.Acceptance,
		"allowed_paths":  b.AllowedPaths,
		"required_paths": b.RequiredPaths,
		"artifacts":      b.Artifacts,
		"constraints":    b.Constraints,
		"items":          items,
		"file_pins":      pins,
		"note":           "Full item text and file pin bodies: use context_bundle tool.",
	}
	raw, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return ""
	}
	return "## Attached context bundle\n\n```json\n" + string(raw) + "\n```\n"
}

// applyMissingContextStatus promotes completed → blocked when the handoff
// reports missing_context (child cannot proceed honestly).
func applyMissingContextStatus(status protocol.ChildStatus, h *protocol.CompletionHandoff) protocol.ChildStatus {
	if h == nil || len(h.MissingContext) == 0 {
		return status
	}
	switch status {
	case protocol.ChildStatusFailed, protocol.ChildStatusCanceled:
		return status
	default:
		// Ensure blockers mention missing context for lead visibility.
		if len(h.Blockers) == 0 {
			h.Blockers = []string{"missing context"}
		}
		if strings.TrimSpace(h.Summary) == "" || h.Summary == defaultHandoffSummary(protocol.ChildStatusCompleted) {
			h.Summary = "blocked: missing context"
		}
		return protocol.ChildStatusBlocked
	}
}

// engineContextBundlePtr returns a pointer to a clone of the engine's sealed
// bundle for tool.Context, or nil when none is attached.
func engineContextBundlePtr(e *Engine) *tool.ContextBundle {
	if e == nil || e.opts.ContextBundle.Empty() {
		return nil
	}
	b := e.opts.ContextBundle.Clone()
	return &b
}
