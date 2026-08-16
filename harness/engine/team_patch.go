package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Pending patch lifecycle statuses.
const (
	PatchStatusPending  = "pending"
	PatchStatusRejected = "rejected"
	PatchStatusApplied  = "applied"
)

// MaxPatchTitleRunes caps patch board titles.
const MaxPatchTitleRunes = 200

// MaxPatchBodyBytes caps stored apply_patch envelope size on the board.
const MaxPatchBodyBytes = 256 * 1024

// MaxTeamPatches bounds items on one team patch board.
const MaxTeamPatches = 128

// TeamPatch is one inspectable patch awaiting lead review/apply.
type TeamPatch struct {
	ID              string
	Title           string
	Patch           string // apply_patch envelope
	Status          string // pending|rejected|applied
	Submitter       string // session id
	Files           []string
	ArtifactID      string
	ArtifactVersion int
	RejectReason    string
	AppliedSummary  string
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PatchConflictError is returned when CAS version or status gate fails.
type PatchConflictError struct {
	Reason string
	Patch  TeamPatch
}

func (e *PatchConflictError) Error() string {
	if e == nil {
		return "patch conflict"
	}
	if e.Reason == "" {
		return "patch conflict"
	}
	return e.Reason
}

// Patches returns a stable snapshot of team patches (id ascending).
func (t *Team) Patches() []TeamPatch {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.patchesSnapshotLocked("")
}

// PatchesByStatus filters by status (empty or "all" = every status).
func (t *Team) PatchesByStatus(status string) []TeamPatch {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.patchesSnapshotLocked(status)
}

func (t *Team) patchesSnapshotLocked(status string) []TeamPatch {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "all" {
		status = ""
	}
	if len(t.patches) == 0 {
		return []TeamPatch{}
	}
	out := make([]TeamPatch, 0, len(t.patches))
	for _, item := range t.patches {
		if status != "" && item.Status != status {
			continue
		}
		out = append(out, cloneTeamPatch(item))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// GetPatch returns a copy of the patch by id.
func (t *Team) GetPatch(id string) (TeamPatch, bool) {
	if t == nil {
		return TeamPatch{}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return TeamPatch{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	item, ok := t.patches[id]
	if !ok {
		return TeamPatch{}, false
	}
	return cloneTeamPatch(item), true
}

// SubmitPatch registers a pending patch. files should be workspace-relative paths.
func (t *Team) SubmitPatch(title, patch, submitter string, files []string, artifactID string, artifactVersion int) (TeamPatch, error) {
	if t == nil {
		return TeamPatch{}, fmt.Errorf("no team")
	}
	title = strings.TrimSpace(title)
	patch = strings.TrimSpace(patch)
	submitter = strings.TrimSpace(submitter)
	if patch == "" {
		return TeamPatch{}, fmt.Errorf("patch is required")
	}
	if len(patch) > MaxPatchBodyBytes {
		return TeamPatch{}, fmt.Errorf("patch exceeds %d bytes (%d)", MaxPatchBodyBytes, len(patch))
	}
	if title != "" && utf8.RuneCountInString(title) > MaxPatchTitleRunes {
		return TeamPatch{}, fmt.Errorf("title exceeds %d runes", MaxPatchTitleRunes)
	}
	if submitter == "" {
		return TeamPatch{}, fmt.Errorf("submitter is required")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return TeamPatch{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[submitter]; !ok {
		return TeamPatch{}, fmt.Errorf("actor is not on this team")
	}
	if len(t.patches) >= MaxTeamPatches {
		return TeamPatch{}, fmt.Errorf("patch board is full (%d)", MaxTeamPatches)
	}
	t.patchSeq++
	id := fmt.Sprintf("p%d", t.patchSeq)
	now := time.Now().UTC()
	item := TeamPatch{
		ID:              id,
		Title:           title,
		Patch:           patch,
		Status:          PatchStatusPending,
		Submitter:       submitter,
		Files:           normalizePatchFiles(files),
		ArtifactID:      strings.TrimSpace(artifactID),
		ArtifactVersion: artifactVersion,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if t.patches == nil {
		t.patches = make(map[string]TeamPatch, 8)
	}
	t.patches[id] = item
	return cloneTeamPatch(item), nil
}

// RejectPatch marks a pending patch rejected with reason. expectedVersion > 0 enables CAS.
func (t *Team) RejectPatch(id, actor, reason string, expectedVersion int) (TeamPatch, error) {
	if t == nil {
		return TeamPatch{}, fmt.Errorf("no team")
	}
	id = strings.TrimSpace(id)
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if id == "" {
		return TeamPatch{}, fmt.Errorf("id is required")
	}
	if actor == "" {
		return TeamPatch{}, fmt.Errorf("actor is required")
	}
	if reason == "" {
		return TeamPatch{}, fmt.Errorf("reason is required")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return TeamPatch{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[actor]; !ok {
		return TeamPatch{}, fmt.Errorf("actor is not on this team")
	}
	item, ok := t.patches[id]
	if !ok {
		return TeamPatch{}, fmt.Errorf("patch %q not found", id)
	}
	if expectedVersion > 0 && item.Version != expectedVersion {
		return TeamPatch{}, &PatchConflictError{
			Reason: fmt.Sprintf("version conflict: have %d, expected %d", item.Version, expectedVersion),
			Patch:  cloneTeamPatch(item),
		}
	}
	if item.Status == PatchStatusRejected {
		return cloneTeamPatch(item), nil // idempotent
	}
	if item.Status == PatchStatusApplied {
		return TeamPatch{}, &PatchConflictError{
			Reason: fmt.Sprintf("patch %q is already applied", id),
			Patch:  cloneTeamPatch(item),
		}
	}
	item.Status = PatchStatusRejected
	item.RejectReason = reason
	item.Version++
	item.UpdatedAt = time.Now().UTC()
	t.patches[id] = item
	return cloneTeamPatch(item), nil
}

// MarkPatchApplied marks a pending patch applied after a successful worktree write.
func (t *Team) MarkPatchApplied(id, actor, summary string, files []string, expectedVersion int) (TeamPatch, error) {
	if t == nil {
		return TeamPatch{}, fmt.Errorf("no team")
	}
	id = strings.TrimSpace(id)
	actor = strings.TrimSpace(actor)
	if id == "" {
		return TeamPatch{}, fmt.Errorf("id is required")
	}
	if actor == "" {
		return TeamPatch{}, fmt.Errorf("actor is required")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return TeamPatch{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[actor]; !ok {
		return TeamPatch{}, fmt.Errorf("actor is not on this team")
	}
	item, ok := t.patches[id]
	if !ok {
		return TeamPatch{}, fmt.Errorf("patch %q not found", id)
	}
	if expectedVersion > 0 && item.Version != expectedVersion {
		return TeamPatch{}, &PatchConflictError{
			Reason: fmt.Sprintf("version conflict: have %d, expected %d", item.Version, expectedVersion),
			Patch:  cloneTeamPatch(item),
		}
	}
	if item.Status == PatchStatusApplied {
		return cloneTeamPatch(item), nil // idempotent
	}
	if item.Status == PatchStatusRejected {
		return TeamPatch{}, &PatchConflictError{
			Reason: fmt.Sprintf("patch %q is rejected", id),
			Patch:  cloneTeamPatch(item),
		}
	}
	item.Status = PatchStatusApplied
	item.AppliedSummary = strings.TrimSpace(summary)
	if len(files) > 0 {
		item.Files = normalizePatchFiles(files)
	}
	item.Version++
	item.UpdatedAt = time.Now().UTC()
	t.patches[id] = item
	return cloneTeamPatch(item), nil
}

// clearPatchesLocked drops all patch board items. Caller holds t.mu.
func (t *Team) clearPatchesLocked() {
	t.patches = make(map[string]TeamPatch)
	t.patchSeq = 0
}

func cloneTeamPatch(p TeamPatch) TeamPatch {
	out := p
	if p.Files != nil {
		out.Files = append([]string(nil), p.Files...)
	}
	return out
}

func normalizePatchFiles(files []string) []string {
	if len(files) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(files))
	out := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimSpace(strings.ReplaceAll(f, "\\", "/"))
		if f == "" {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
