package tool

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// PatchOp is one planned file operation from an apply_patch envelope (preview).
type PatchOp struct {
	Type   string `json:"type"` // add | delete | update | move
	Path   string `json:"path"`
	MoveTo string `json:"moveTo,omitempty"`
}

// PatchPreview is the validate-only result of planning a patch against workDir.
// Valid is false when the envelope cannot parse or cannot apply cleanly to the
// current base (exact context match). No files are written.
type PatchPreview struct {
	Ops     []PatchOp `json:"ops"`
	Files   []string  `json:"files"`
	Summary string    `json:"summary,omitempty"`
	Valid   bool      `json:"valid"`
	Error   string    `json:"error,omitempty"`
}

// PreviewPatch parses and plans patch against workDir without writing.
func PreviewPatch(workDir, patch string) PatchPreview {
	planned, _, err := preparePatch(workDir, patch)
	if err != nil {
		return PatchPreview{Valid: false, Error: err.Error(), Ops: []PatchOp{}, Files: []string{}}
	}
	ops := make([]PatchOp, len(planned))
	files := make([]string, 0, len(planned)*2)
	seen := make(map[string]struct{})
	addFile := func(p string) {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		files = append(files, p)
	}
	for i, op := range planned {
		ops[i] = PatchOp{Type: op.Type, Path: op.RelPath, MoveTo: op.RelMove}
		addFile(op.RelPath)
		addFile(op.RelMove)
	}
	sort.Strings(files)
	return PatchPreview{
		Ops:     ops,
		Files:   files,
		Summary: patchSuccessSummary(planned),
		Valid:   true,
	}
}

// PatchFiles returns workspace-relative paths a patch would touch, or an error
// if the envelope is invalid against workDir.
func PatchFiles(workDir, patch string) ([]string, error) {
	prev := PreviewPatch(workDir, patch)
	if !prev.Valid {
		return nil, fmt.Errorf("%s", prev.Error)
	}
	return prev.Files, nil
}

// PathSetOverlap maps each path that appears in two or more named sets to the
// set ids that claim it. Empty when sets are pairwise disjoint.
func PathSetOverlap(sets map[string][]string) map[string][]string {
	pathToIDs := make(map[string]map[string]struct{})
	for id, paths := range sets {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		for _, p := range paths {
			p = filepath.ToSlash(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			if pathToIDs[p] == nil {
				pathToIDs[p] = make(map[string]struct{})
			}
			pathToIDs[p][id] = struct{}{}
		}
	}
	out := make(map[string][]string)
	for p, ids := range pathToIDs {
		if len(ids) < 2 {
			continue
		}
		list := make([]string, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		sort.Strings(list)
		out[p] = list
	}
	return out
}

// NamedPatch is one labeled patch envelope for multi-patch conflict checks.
type NamedPatch struct {
	ID    string
	Patch string
}

// MultiPatchConflict describes path overlap and/or base-apply failures among
// a set of patches (and optionally against the current workDir base).
type MultiPatchConflict struct {
	// PathOverlap maps path → patch ids that all touch it.
	PathOverlap map[string][]string `json:"path_overlap,omitempty"`
	// Invalid maps patch id → plan/validate error against workDir.
	Invalid map[string]string `json:"invalid,omitempty"`
	// HasConflict is true when PathOverlap or Invalid is non-empty.
	HasConflict bool `json:"has_conflict"`
}

// DetectPatchConflicts validates each patch against workDir and reports path
// overlaps between patches. Does not write. Patches that fail to plan still
// contribute parseable paths when possible (best-effort).
func DetectPatchConflicts(workDir string, patches []NamedPatch) MultiPatchConflict {
	sets := make(map[string][]string, len(patches))
	invalid := make(map[string]string)
	for _, np := range patches {
		id := strings.TrimSpace(np.ID)
		if id == "" {
			id = fmt.Sprintf("anon-%d", len(sets)+1)
		}
		prev := PreviewPatch(workDir, np.Patch)
		if !prev.Valid {
			invalid[id] = prev.Error
			if files, err := patchPathsFromParse(np.Patch); err == nil {
				sets[id] = files
			}
			continue
		}
		sets[id] = prev.Files
	}
	overlap := PathSetOverlap(sets)
	out := MultiPatchConflict{
		PathOverlap: overlap,
		Invalid:     invalid,
	}
	if len(overlap) > 0 || len(invalid) > 0 {
		out.HasConflict = true
	}
	if out.PathOverlap == nil {
		out.PathOverlap = map[string][]string{}
	}
	if out.Invalid == nil {
		out.Invalid = map[string]string{}
	}
	return out
}

// ApplyOnePatch validates and commits a single patch; returns summary + files.
// On commit failure the apply_patch stack rolls back that patch.
func ApplyOnePatch(workDir, patch string) (summary string, files []string, err error) {
	prev := PreviewPatch(workDir, patch)
	if !prev.Valid {
		return "", nil, fmt.Errorf("%s", prev.Error)
	}
	summary, err = ApplyPatchToWorkDir(workDir, patch)
	if err != nil {
		return "", nil, err
	}
	return summary, append([]string(nil), prev.Files...), nil
}

// ApplyPatchesSequential applies patches in order against workDir. Each patch
// is re-validated against the updated base. Callers that need atomic multi-apply
// should run DetectPatchConflicts first and refuse path overlaps.
func ApplyPatchesSequential(workDir string, patches []string) (summaries []string, files []string, err error) {
	seen := make(map[string]struct{})
	for i, p := range patches {
		// Capture files before apply (Add would fail preview after write).
		prev := PreviewPatch(workDir, p)
		if !prev.Valid {
			return summaries, files, fmt.Errorf("patch %d: %s", i, prev.Error)
		}
		sum, err := ApplyPatchToWorkDir(workDir, p)
		if err != nil {
			return summaries, files, fmt.Errorf("patch %d: %w", i, err)
		}
		summaries = append(summaries, sum)
		for _, f := range prev.Files {
			if _, ok := seen[f]; ok {
				continue
			}
			seen[f] = struct{}{}
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return summaries, files, nil
}

// patchPathsFromParse extracts paths from the envelope without disk planning.
func patchPathsFromParse(patch string) ([]string, error) {
	hunks, err := parsePatch(patch)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(p string) {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, h := range hunks {
		add(h.Path)
		add(h.MoveTo)
	}
	sort.Strings(out)
	return out, nil
}
