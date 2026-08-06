package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// patchCollab handles patch_collab tool actions against the team patch board
// and the active worktree (e.opts.WorkDir — session worktree when isolation is on).
func (e *Engine) patchCollab(ctx context.Context, req tool.PatchCollabRequest) (tool.PatchCollabResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.PatchCollabResult{}, err
	}
	if e == nil || e.team == nil || e.team.Len() == 0 {
		return tool.PatchCollabResult{}, fmt.Errorf("patch_collab is not available (no team)")
	}
	actor := strings.TrimSpace(e.opts.SessionID)
	if actor == "" {
		return tool.PatchCollabResult{}, fmt.Errorf("session id is required")
	}
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = strings.TrimSpace(e.opts.WorkDir)
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	out := tool.PatchCollabResult{
		LeadID: e.team.LeadID(),
		Action: action,
	}

	switch action {
	case "list":
		status := req.Status
		if status == "" {
			status = PatchStatusPending
		}
		out.Patches = teamPatchesToTool(e.team.PatchesByStatus(status), false)
		return out, nil

	case "submit":
		prev := tool.PreviewPatch(workDir, req.Patch)
		if !prev.Valid {
			return tool.PatchCollabResult{}, fmt.Errorf("invalid patch: %s", prev.Error)
		}
		item, err := e.team.SubmitPatch(req.Title, req.Patch, actor, prev.Files, req.ArtifactID, req.ArtifactVersion)
		if err != nil {
			return tool.PatchCollabResult{}, err
		}
		out.Patch = teamPatchToTool(item, true)
		out.Preview = &prev
		out.Files = append([]string(nil), prev.Files...)
		out.Patches = teamPatchesToTool(e.team.PatchesByStatus(PatchStatusPending), false)
		return out, nil

	case "preview":
		patchText := req.Patch
		var item *TeamPatch
		if id := strings.TrimSpace(req.ID); id != "" {
			p, ok := e.team.GetPatch(id)
			if !ok {
				return tool.PatchCollabResult{}, fmt.Errorf("patch %q not found", id)
			}
			item = &p
			if strings.TrimSpace(patchText) == "" {
				patchText = p.Patch
			}
			out.Patch = teamPatchToTool(p, true)
		}
		prev := tool.PreviewPatch(workDir, patchText)
		out.Preview = &prev
		if prev.Valid {
			out.Files = append([]string(nil), prev.Files...)
		}
		// Conflicts vs other pending (exclude self when id set).
		conf := e.pendingConflicts(workDir, req.ID, patchText)
		out.Conflicts = &conf
		if conf.HasConflict || !prev.Valid {
			out.Conflict = conf.HasConflict || !prev.Valid
			if !prev.Valid {
				out.Detail = prev.Error
			} else if conf.HasConflict {
				out.Detail = "conflicts with base and/or other pending patches"
			}
		}
		_ = item
		return out, nil

	case "conflicts":
		ids := req.IDs
		if req.ID != "" {
			ids = append([]string{req.ID}, ids...)
		}
		conf := e.conflictsForIDs(workDir, ids)
		out.Conflicts = &conf
		out.Conflict = conf.HasConflict
		if conf.HasConflict {
			out.Detail = "path overlap and/or base validation failures"
		}
		out.Patches = teamPatchesToTool(e.team.PatchesByStatus(PatchStatusPending), false)
		return out, nil

	case "reject":
		item, err := e.team.RejectPatch(req.ID, actor, req.Reason, req.ExpectedVersion)
		if err != nil {
			return patchBoardConflictResult(out, err)
		}
		out.Patch = teamPatchToTool(item, false)
		out.Detail = req.Reason
		return out, nil

	case "apply":
		return e.applyTeamPatch(ctx, out, actor, workDir, req)

	default:
		return tool.PatchCollabResult{}, fmt.Errorf("action must be submit, list, preview, conflicts, reject, or apply")
	}
}

func (e *Engine) applyTeamPatch(ctx context.Context, out tool.PatchCollabResult, actor, workDir string, req tool.PatchCollabRequest) (tool.PatchCollabResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.PatchCollabResult{}, err
	}
	p, ok := e.team.GetPatch(req.ID)
	if !ok {
		return tool.PatchCollabResult{}, fmt.Errorf("patch %q not found", req.ID)
	}
	if p.Status == PatchStatusRejected {
		out.Conflict = true
		out.Detail = "patch is rejected"
		out.Patch = teamPatchToTool(p, false)
		return out, nil
	}
	if p.Status == PatchStatusApplied {
		out.Patch = teamPatchToTool(p, false)
		out.Files = append([]string(nil), p.Files...)
		out.Summary = p.AppliedSummary
		out.Detail = "already applied"
		return out, nil
	}

	// Conflict vs other pending path sets + base validity.
	conf := e.pendingConflicts(workDir, p.ID, p.Patch)
	if conf.HasConflict {
		out.Conflict = true
		out.Conflicts = &conf
		out.Detail = "refusing apply: conflicts with base and/or other pending patches"
		out.Patch = teamPatchToTool(p, false)
		return out, nil
	}

	prev := tool.PreviewPatch(workDir, p.Patch)
	if !prev.Valid {
		out.Conflict = true
		out.Detail = prev.Error
		out.Preview = &prev
		out.Patch = teamPatchToTool(p, false)
		return out, nil
	}

	// Claim ownership before write (block policy can refuse).
	var overlapWarns []string
	for _, rel := range prev.Files {
		abs, display := resolveTeamOwnershipPath(workDir, rel)
		w, err := claimPatchWrite(e, abs, display)
		if err != nil {
			out.Conflict = true
			out.Detail = err.Error()
			out.Patch = teamPatchToTool(p, false)
			return out, nil
		}
		if w != "" {
			overlapWarns = append(overlapWarns, w)
		}
	}

	// Snapshot for turn diff / checkpoints when available via a synthetic tool context path.
	// Apply through shared apply_patch stack (rollback on mid-commit failure).
	summary, files, err := tool.ApplyOnePatch(workDir, p.Patch)
	if err != nil {
		return tool.PatchCollabResult{}, err
	}

	// Record mutations for handoff files_changed + ownership graph.
	for _, rel := range files {
		abs, _ := resolveTeamOwnershipPath(workDir, rel)
		e.noteMutatedPath(abs)
		if e.opts.FileSync != nil {
			// Best-effort: content unknown here; empty content still signals change.
			e.opts.FileSync(abs, "", false)
		}
		if e.turnDiff != nil {
			e.turnDiff.Note(rel, true, false)
		}
		if e.checkpoints != nil {
			// Post-apply snapshot is less useful; skip pre-mutation (already applied).
		}
	}
	// Merge into team ownership as files_changed (handoff accuracy post-apply).
	name := e.ownershipMemberName()
	if name == "" {
		name = actor
	}
	e.RecordChildFilesChanged(actor, name, files)

	item, err := e.team.MarkPatchApplied(req.ID, actor, summary, files, req.ExpectedVersion)
	if err != nil {
		// Disk already written — surface board CAS conflict but keep files list.
		out.Files = files
		out.Summary = summary
		return patchBoardConflictResult(out, err)
	}
	out.Patch = teamPatchToTool(item, false)
	out.Files = files
	out.Summary = summary
	out.Preview = &prev
	if len(overlapWarns) > 0 {
		out.Detail = strings.Join(overlapWarns, "; ")
	}
	return out, nil
}

func claimPatchWrite(e *Engine, abs, display string) (warning string, err error) {
	if e == nil || e.team == nil {
		return "", nil
	}
	own := e.team.Ownership()
	if own == nil {
		return "", nil
	}
	res := own.Touch(e.opts.SessionID, e.ownershipMemberName(), abs, display)
	if res.Overlap {
		e.emitPathOverlap(res)
	}
	if res.Blocked {
		return res.Warning, fmt.Errorf("%s", res.Warning)
	}
	return res.Warning, nil
}

// pendingConflicts checks patchText (+ optional selfID) against other pending patches.
func (e *Engine) pendingConflicts(workDir, selfID, patchText string) tool.MultiPatchConflict {
	pending := e.team.PatchesByStatus(PatchStatusPending)
	named := make([]tool.NamedPatch, 0, len(pending)+1)
	selfID = strings.TrimSpace(selfID)
	// Include the candidate under a stable id.
	candID := selfID
	if candID == "" {
		candID = "_candidate"
	}
	if strings.TrimSpace(patchText) != "" {
		named = append(named, tool.NamedPatch{ID: candID, Patch: patchText})
	}
	for _, p := range pending {
		if selfID != "" && p.ID == selfID {
			continue // already included as candidate with maybe fresher text
		}
		named = append(named, tool.NamedPatch{ID: p.ID, Patch: p.Patch})
	}
	return tool.DetectPatchConflicts(workDir, named)
}

func (e *Engine) conflictsForIDs(workDir string, ids []string) tool.MultiPatchConflict {
	pending := e.team.PatchesByStatus(PatchStatusPending)
	want := make(map[string]struct{})
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			want[id] = struct{}{}
		}
	}
	named := make([]tool.NamedPatch, 0, len(pending))
	for _, p := range pending {
		if len(want) > 0 {
			if _, ok := want[p.ID]; !ok {
				continue
			}
		}
		named = append(named, tool.NamedPatch{ID: p.ID, Patch: p.Patch})
	}
	// Also allow explicit ids that are not pending (rejected/applied) for base check.
	for id := range want {
		found := false
		for _, n := range named {
			if n.ID == id {
				found = true
				break
			}
		}
		if found {
			continue
		}
		if p, ok := e.team.GetPatch(id); ok {
			named = append(named, tool.NamedPatch{ID: p.ID, Patch: p.Patch})
		}
	}
	return tool.DetectPatchConflicts(workDir, named)
}

func patchBoardConflictResult(out tool.PatchCollabResult, err error) (tool.PatchCollabResult, error) {
	var conf *PatchConflictError
	if errors.As(err, &conf) {
		out.Conflict = true
		out.Detail = conf.Reason
		out.Patch = teamPatchToTool(conf.Patch, false)
		return out, nil
	}
	return tool.PatchCollabResult{}, err
}

func teamPatchToTool(item TeamPatch, includeBody bool) *tool.PatchCollabItem {
	out := &tool.PatchCollabItem{
		ID:              item.ID,
		Title:           item.Title,
		Status:          item.Status,
		Submitter:       item.Submitter,
		Files:           append([]string(nil), item.Files...),
		ArtifactID:      item.ArtifactID,
		ArtifactVersion: item.ArtifactVersion,
		RejectReason:    item.RejectReason,
		AppliedSummary:  item.AppliedSummary,
		Version:         item.Version,
	}
	if out.Files == nil {
		out.Files = []string{}
	}
	if !item.CreatedAt.IsZero() {
		out.CreatedAt = item.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !item.UpdatedAt.IsZero() {
		out.UpdatedAt = item.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if includeBody {
		out.Patch = item.Patch
	}
	return out
}

func teamPatchesToTool(items []TeamPatch, includeBody bool) []tool.PatchCollabItem {
	if items == nil {
		return []tool.PatchCollabItem{}
	}
	out := make([]tool.PatchCollabItem, 0, len(items))
	for _, item := range items {
		out = append(out, *teamPatchToTool(item, includeBody))
	}
	return out
}

// absInWorkDir joins workDir and rel when rel is not absolute.
func absInWorkDir(workDir, rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	if workDir == "" {
		return filepath.Clean(rel)
	}
	return filepath.Clean(filepath.Join(workDir, rel))
}
