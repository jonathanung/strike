package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func (e *Engine) emitPathOverlap(res tool.TouchResult) {
	if e == nil || !res.Overlap {
		return
	}
	holders := make([]protocol.PathOverlapHolder, 0, len(res.Holders))
	for _, h := range res.Holders {
		holders = append(holders, protocol.PathOverlapHolder{
			SessionID: h.SessionID,
			Name:      h.Name,
			Source:    h.Source,
			Mode:      h.Mode,
		})
	}
	display := res.Display
	if display == "" {
		display = res.Path
	}
	e.emit(protocol.PathOverlap{
		Correlation: protocol.Correlation{
			SessionID:       e.opts.SessionID,
			ParentSessionID: e.opts.ParentSessionID,
			Depth:           e.opts.Depth,
		},
		Path:    display,
		Policy:  res.Policy,
		Blocked: res.Blocked,
		Holders: holders,
		Warning: res.Warning,
	})
}

func (e *Engine) ownershipMemberName() string {
	if e == nil || e.team == nil {
		return ""
	}
	if m, ok := e.team.Member(e.opts.SessionID); ok {
		return m.Name
	}
	return ""
}

func (e *Engine) ownershipQuery(ctx context.Context) (tool.OwnershipSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tool.OwnershipSnapshot{}, err
	}
	if e == nil || e.team == nil {
		return tool.OwnershipSnapshot{}, fmt.Errorf("no team")
	}
	own := e.team.Ownership()
	if own == nil {
		return tool.OwnershipSnapshot{Policy: tool.OverlapWarn, Claims: []tool.PathClaim{}}, nil
	}
	snap := own.Snapshot()
	// Prefer workspace-relative paths in the snapshot for model readability.
	if wd := strings.TrimSpace(e.opts.WorkDir); wd != "" {
		for i := range snap.Claims {
			if rel, err := filepath.Rel(wd, snap.Claims[i].Path); err == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				snap.Claims[i].Path = rel
			}
		}
		for i := range snap.Overlaps {
			if rel, err := filepath.Rel(wd, snap.Overlaps[i]); err == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				snap.Overlaps[i] = rel
			}
		}
	}
	return snap, nil
}

func (e *Engine) ownershipLease(ctx context.Context, path string, exclusive bool) (tool.TouchResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.TouchResult{}, err
	}
	if e == nil || e.team == nil {
		return tool.TouchResult{}, fmt.Errorf("no team")
	}
	own := e.team.Ownership()
	if own == nil {
		return tool.TouchResult{}, fmt.Errorf("ownership tracker unavailable")
	}
	abs, display := resolveTeamOwnershipPath(e.opts.WorkDir, path)
	res := own.AcquireLease(e.opts.SessionID, e.ownershipMemberName(), abs, display, exclusive)
	if res.Overlap {
		e.emitPathOverlap(res)
	}
	if res.Blocked {
		return res, fmt.Errorf("%s", res.Warning)
	}
	return res, nil
}

func (e *Engine) ownershipReleaseLease(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || e.team == nil {
		return fmt.Errorf("no team")
	}
	own := e.team.Ownership()
	if own == nil {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		own.ReleaseAllLeases(e.opts.SessionID)
		return nil
	}
	abs, _ := resolveTeamOwnershipPath(e.opts.WorkDir, path)
	own.ReleaseLease(e.opts.SessionID, abs)
	return nil
}

// RecordChildFilesChanged merges structured handoff files_changed into the
// team ownership graph. Safe when own/team is nil or paths empty. Used when
// #771 structured handoffs supply files_changed; write-path touches already
// cover edit/write/apply_patch during the run.
func (e *Engine) RecordChildFilesChanged(sessionID, name string, paths []string) {
	if e == nil || e.team == nil || len(paths) == 0 {
		return
	}
	own := e.team.Ownership()
	if own == nil {
		return
	}
	own.RecordFilesChanged(sessionID, name, e.opts.WorkDir, paths)
}

func resolveTeamOwnershipPath(workDir, p string) (abs, display string) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", ""
	}
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
		display = abs
		if workDir != "" {
			if rel, err := filepath.Rel(workDir, abs); err == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				display = rel
			}
		}
		return abs, display
	}
	display = filepath.Clean(p)
	if workDir == "" {
		return display, display
	}
	return filepath.Clean(filepath.Join(workDir, p)), display
}
