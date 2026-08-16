package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type statusTool struct{}

// NewStatus returns the read-only this-turn harness working-set tool.
// Always visible (core): cheap in-memory snapshot, no git, no disk walk.
func NewStatus() Tool { return statusTool{} }

func (statusTool) Name() string { return "status" }

func (statusTool) Contract() Contract {
	return staticContract(SideEffectNone, IdempotencySafeRetry)
}

func (statusTool) Description() string {
	return `Read-only this-turn harness working set (TurnDiff + recorded FileState hashes).

Lists workspace-relative paths the file tools created, updated, or deleted in the
current turn, plus optional content hashes when a read/write recorded one.
Hashes match edit/apply_patch baseHash when a snapshot was stored. Does not
shell out to git — use the git tool for repository status.

Usage notes:
  - No arguments. Unchanged turns return an empty files list.
  - kind is create, update, or delete (same as TurnDiff.Snapshot).
  - hash is omitted when no content snapshot was recorded (typical for deletes).
  - Resets at the turn boundary with TurnDiff.`
}

func (statusTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

type statusEntry struct {
	Path string     `json:"path"`
	Kind ChangeKind `json:"kind"`
	Hash string     `json:"hash,omitempty"`
}

type statusPayload struct {
	OK    bool          `json:"ok"`
	Files []statusEntry `json:"files"`
	Count int           `json:"count"`
}

func (statusTool) Execute(ctx context.Context, _ json.RawMessage, tc *Context) (Result, error) {
	if err := tc.Ask(ctx, AskRequest{
		Permission: "status",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	payload := statusPayload{OK: true, Files: []statusEntry{}}
	if tc != nil && tc.TurnDiff != nil {
		for _, ch := range tc.TurnDiff.Snapshot() {
			ent := statusEntry{Path: ch.Path, Kind: ch.Kind}
			if hash := statusRecordedHash(tc, ch.Path); hash != "" {
				ent.Hash = hash
			}
			payload.Files = append(payload.Files, ent)
		}
	}
	payload.Count = len(payload.Files)

	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return Result{}, err
	}
	meta, _ := json.Marshal(payload)
	title := "0 files"
	if payload.Count == 1 {
		title = "1 file"
	} else if payload.Count > 1 {
		title = fmt.Sprintf("%d files", payload.Count)
	}
	return Result{Title: title, Output: string(out), Metadata: meta}, nil
}

func statusRecordedHash(tc *Context, rel string) string {
	if tc == nil || tc.Files == nil {
		return ""
	}
	return tc.Files.Hash(statusAbsPath(tc.WorkDir, rel))
}

func statusAbsPath(workDir, rel string) string {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return ""
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	if workDir == "" {
		return filepath.Clean(filepath.FromSlash(rel))
	}
	return filepath.Join(workDir, filepath.FromSlash(rel))
}
