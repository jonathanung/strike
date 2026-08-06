package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type patchCollabTool struct{}

// NewPatchCollab builds the patch_collab tool (inspectable multi-agent patches).
func NewPatchCollab() Tool { return patchCollabTool{} }

func (patchCollabTool) Name() string { return "patch_collab" }

func (patchCollabTool) Contract() Contract {
	// apply mutates the workspace; other actions are coordination-only.
	return staticContract(SideEffectWorkspaceMutative, IdempotencyConditional)
}

func (patchCollabTool) Description() string {
	return `Patch-level collaboration: submit inspectable patches for lead review before apply.

Prefer this over direct edit/write/apply_patch when multiple agents write in parallel
and the orchestrator must preview, conflict-check, combine, or reject changes.

Reuses the apply_patch envelope (*** Begin Patch / *** End Patch).

Actions:
  - submit: register a pending patch (patch text required). Optional title,
    artifact_id/artifact_version (from artifact_write type=patch). Validates
    parse + base plan; does not write files.
  - list: pending/rejected/applied board snapshot (optional status filter).
  - preview: id or inline patch → ops, files, base validity, conflicts vs other
    pending patches. Never writes.
  - conflicts: check id(s) or all pending against base + path overlap.
  - reject: id + reason — mark rejected without applying.
  - apply: id — conflict-check, then apply to the active worktree (session
    worktree when isolation is on). Records files_changed / ownership touches.

Board is team-scoped (lead session) and cleared when the lead session ends.
Children should submit patches and reference them via handoff artifact_refs
(type=patch) instead of only editing the shared tree in place.`
}

func (patchCollabTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["submit", "list", "preview", "conflicts", "reject", "apply"],
				"description": "Collaboration operation"
			},
			"id": {"type": "string", "description": "Pending patch id (p1, p2, …) for preview/reject/apply/conflicts"},
			"ids": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional patch ids for conflicts (default: all pending)"
			},
			"patch": {"type": "string", "description": "apply_patch envelope (required for submit; optional inline preview)"},
			"title": {"type": "string", "description": "Short label for submit"},
			"reason": {"type": "string", "description": "Rejection reason (reject)"},
			"status": {
				"type": "string",
				"enum": ["pending", "rejected", "applied", "all"],
				"description": "Filter for list (default pending; all = every status)"
			},
			"artifact_id": {"type": "string", "description": "Optional artifact store id (type=patch) linked on submit"},
			"artifact_version": {"type": "integer", "description": "Optional CAS version of artifact_id"},
			"expected_version": {
				"type": "integer",
				"description": "CAS token from list/submit for reject/apply; omit or 0 to skip"
			}
		},
		"required": ["action"]
	}`)
}

func (patchCollabTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		Action          string   `json:"action"`
		ID              string   `json:"id"`
		IDs             []string `json:"ids"`
		Patch           string   `json:"patch"`
		Title           string   `json:"title"`
		Reason          string   `json:"reason"`
		Status          string   `json:"status"`
		ArtifactID      string   `json:"artifact_id"`
		ArtifactVersion int      `json:"artifact_version"`
		ExpectedVersion int      `json:"expected_version"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, ErrInvalidArgs(fmt.Sprintf("invalid arguments: %v", err))
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		return Result{}, ErrInvalidArgs("action is required")
	}

	req := PatchCollabRequest{
		Action:          action,
		ID:              strings.TrimSpace(a.ID),
		IDs:             trimNonEmptyStrings(a.IDs),
		Patch:           a.Patch,
		Title:           strings.TrimSpace(a.Title),
		Reason:          strings.TrimSpace(a.Reason),
		Status:          strings.ToLower(strings.TrimSpace(a.Status)),
		ArtifactID:      strings.TrimSpace(a.ArtifactID),
		ArtifactVersion: a.ArtifactVersion,
		ExpectedVersion: a.ExpectedVersion,
	}
	if tc != nil {
		req.WorkDir = tc.WorkDir
	}

	switch action {
	case "submit":
		if strings.TrimSpace(req.Patch) == "" {
			return Result{}, ErrInvalidArgs("patch is required for submit")
		}
	case "list":
		// ok
	case "preview":
		if req.ID == "" && strings.TrimSpace(req.Patch) == "" {
			return Result{}, ErrInvalidArgs("id or patch is required for preview")
		}
	case "conflicts":
		// ok — ids optional
	case "reject":
		if req.ID == "" {
			return Result{}, ErrInvalidArgs("id is required for reject")
		}
		if req.Reason == "" {
			return Result{}, ErrInvalidArgs("reason is required for reject")
		}
	case "apply":
		if req.ID == "" {
			return Result{}, ErrInvalidArgs("id is required for apply")
		}
	default:
		return Result{}, ErrInvalidArgs("action must be submit, list, preview, conflicts, reject, or apply")
	}

	// apply mutates workspace → edit permission (same class as apply_patch).
	perm := "patch_collab"
	patterns := []string{action}
	if action == "apply" {
		perm = "edit"
		patterns = []string{"*"}
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: perm,
		Patterns:   patterns,
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if tc.PatchCollab == nil {
		return Result{}, fmt.Errorf("patch_collab is not available")
	}
	res, err := tc.PatchCollab(ctx, req)
	if err != nil {
		return Result{}, err
	}
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return Result{}, err
	}
	title := "patch_collab " + action
	if res.Conflict {
		title += " conflict"
	} else if res.Patch != nil && res.Patch.ID != "" {
		title += " " + res.Patch.ID
	} else if action == "list" {
		title = fmt.Sprintf("patch_collab list %d", len(res.Patches))
	}
	meta, _ := json.Marshal(res)
	result := Result{Title: title, Output: string(out), Metadata: meta}
	if action == "apply" && !res.Conflict && len(res.Files) > 0 {
		// Surface ownership warnings already folded into Detail by the engine.
		_ = res
	}
	return result, nil
}

func trimNonEmptyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
