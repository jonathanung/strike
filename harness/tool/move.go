package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type moveTool struct{}

func NewMove() Tool { return moveTool{} }

func (moveTool) Name() string { return "move" }

func (moveTool) Contract() Contract {
	return staticContract(SideEffectWorkspaceMutative, IdempotencyConditional)
}

func (moveTool) Description() string {
	return `Moves or renames a file within allowed roots (workspace or session temp).

Usage:
- Prefer this tool over bash mv/cp for ordinary renames so path validation, permissions, ownership, freshness, and TurnDiff apply.
- source and destination must stay inside the workspace root, or inside the session temporary directory (absolute paths only for temp).
- By default refuses to overwrite an existing destination; set overwrite=true to replace a regular file destination.
- Optional baseHash (sha256 hex of expected source content) fails closed with precondition_failed on mismatch.
- Same-filesystem moves use atomic rename. Cross-filesystem moves copy then remove the source (documented non-atomic window); destination is still written via temp+rename when possible.
- Does not move directories (use bash only when a directory tree move is required). Refuses symlink escapes via existing path rules.`
}

func (moveTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"source": {"type": "string", "description": "Path of the file to move"},
			"destination": {"type": "string", "description": "Destination path (rename or new location)"},
			"overwrite": {"type": "boolean", "description": "Replace an existing regular-file destination (default false)"},
			"baseHash": {"type": "string", "description": "Optional sha256 hex of expected source content; fails with precondition_failed on mismatch"}
		},
		"required": ["source", "destination"]
	}`)
}

type moveArgs struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Overwrite   bool   `json:"overwrite"`
	BaseHash    string `json:"baseHash"`
}

func (moveTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a moveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, ErrInvalidArgs(fmt.Sprintf("invalid arguments: %v", err))
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	tempDir := ""
	if tc != nil {
		tempDir = tc.SessionTempDir
	}
	srcPath, srcRel, err := resolveAllowedPath(tc.WorkDir, tempDir, a.Source)
	if err != nil {
		return Result{}, err
	}
	dstPath, dstRel, err := resolveAllowedPath(tc.WorkDir, tempDir, a.Destination)
	if err != nil {
		return Result{}, err
	}
	if srcPath == dstPath {
		return Result{}, ErrInvalidArgs("source and destination resolve to the same path")
	}
	// Refuse when the user-named leaf is a symlink (do not silently move the target).
	if isSymlinkLeaf(tc.WorkDir, tempDir, a.Source) {
		return Result{}, ErrPrecondition(fmt.Sprintf("%s is a symlink; refuse to move through symlinks", srcRel))
	}
	if isSymlinkLeaf(tc.WorkDir, tempDir, a.Destination) {
		return Result{}, ErrPrecondition(fmt.Sprintf("%s is a symlink; refuse to overwrite through symlinks", dstRel))
	}

	srcInfo, err := os.Lstat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, ErrPrecondition(fmt.Sprintf("%s does not exist", srcRel))
		}
		return Result{}, err
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		return Result{}, ErrPrecondition(fmt.Sprintf("%s is a symlink; refuse to move through symlinks", srcRel))
	}
	if !srcInfo.Mode().IsRegular() {
		return Result{}, ErrInvalidArgs(fmt.Sprintf("%s is not a regular file (directory moves are not supported)", srcRel))
	}

	if err := tc.Files.CheckFresh(srcPath, srcRel); err != nil {
		return Result{}, err
	}
	if err := CheckBaseHash(srcPath, a.BaseHash, srcRel); err != nil {
		return Result{}, err
	}
	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		return Result{}, err
	}

	dstExisted := FileExisted(dstPath)
	if dstExisted {
		dstInfo, lerr := os.Lstat(dstPath)
		if lerr != nil {
			return Result{}, lerr
		}
		if dstInfo.Mode()&os.ModeSymlink != 0 {
			return Result{}, ErrPrecondition(fmt.Sprintf("%s is a symlink; refuse to overwrite through symlinks", dstRel))
		}
		if dstInfo.IsDir() {
			return Result{}, ErrPrecondition(fmt.Sprintf("%s is a directory; refuse to overwrite with a file", dstRel))
		}
		if !a.Overwrite {
			return Result{}, ErrPrecondition(fmt.Sprintf("%s already exists; set overwrite=true to replace", dstRel))
		}
	}

	meta, _ := json.Marshal(map[string]any{
		"source":      srcRel,
		"destination": dstRel,
		"overwrite":   a.Overwrite && dstExisted,
		"bytes":       len(srcData),
	})
	if err := tc.Ask(ctx, AskRequest{
		Permission: "edit",
		Patterns:   []string{srcRel, dstRel},
		Always:     []string{"*"},
		Metadata:   meta,
	}); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// Claim both paths after permission so denied moves do not pollute ownership.
	overlapWarn, err := tc.ClaimWrite(srcPath, srcRel)
	if err != nil {
		return Result{}, err
	}
	w2, err := tc.ClaimWrite(dstPath, dstRel)
	if err != nil {
		return Result{}, err
	}
	overlapWarn = joinWarnings(overlapWarn, w2)

	// Close races between plan-time read and commit.
	if err := CheckContentUnchanged(srcPath, srcData, srcRel); err != nil {
		return Result{}, err
	}
	if err := tc.Files.CheckFresh(srcPath, srcRel); err != nil {
		return Result{}, err
	}
	if dstExisted {
		// Destination may have appeared/changed after the existence check.
		if fi, lerr := os.Lstat(dstPath); lerr == nil {
			if fi.Mode()&os.ModeSymlink != 0 || fi.IsDir() {
				return Result{}, ErrPrecondition(fmt.Sprintf("%s changed concurrently; re-check before moving", dstRel))
			}
			if !a.Overwrite {
				return Result{}, ErrPrecondition(fmt.Sprintf("%s already exists; set overwrite=true to replace", dstRel))
			}
		} else if !os.IsNotExist(lerr) {
			return Result{}, lerr
		}
	} else if FileExisted(dstPath) && !a.Overwrite {
		return Result{}, ErrPrecondition(fmt.Sprintf("%s already exists; set overwrite=true to replace", dstRel))
	}

	tc.SnapshotPath(srcPath)
	if dstExisted || FileExisted(dstPath) {
		tc.SnapshotPath(dstPath)
	}

	// Re-resolve immediately before commit (TOCTOU vs plan-time resolve).
	srcPath, srcRel, err = resolveAllowedPath(tc.WorkDir, tempDir, a.Source)
	if err != nil {
		return Result{}, err
	}
	dstPath, dstRel, err = resolveAllowedPath(tc.WorkDir, tempDir, a.Destination)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	crossFS, err := atomicMoveFile(tc.WorkDir, tempDir, a.Source, a.Destination, srcPath, dstPath, srcData)
	if err != nil {
		return Result{}, err
	}

	// Refresh destination path after commit.
	dstPath, dstRel, err = resolveAllowedPath(tc.WorkDir, tempDir, a.Destination)
	if err != nil {
		return Result{}, err
	}
	if info, statErr := os.Stat(dstPath); statErr == nil {
		tc.Files.RecordBytes(dstPath, info, srcData)
	}
	// Source is gone; drop any stale freshness snapshot.
	if tc.Files != nil {
		tc.Files.Forget(srcPath)
	}

	tc.NoteTurnChange(srcPath, true, true)
	tc.NoteTurnChange(dstPath, dstExisted, false)
	tc.NotifyFileSync(srcPath, "", true)
	tc.NotifyFileSync(dstPath, string(srcData), false)

	out := fmt.Sprintf("Moved %s → %s (%d bytes)", srcRel, dstRel, len(srcData))
	if crossFS {
		out += " (cross-filesystem copy+remove)"
	}
	out = AppendOverlapWarning(out, overlapWarn)
	res := Result{
		Title:    fmt.Sprintf("%s → %s", srcRel, dstRel),
		Output:   out,
		Metadata: meta,
	}
	return tc.AppendDiagnostics(ctx, res, dstPath), nil
}

func joinWarnings(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case a == b:
		return a
	default:
		return a + "; " + b
	}
}

// atomicMoveFile renames src→dst when on the same filesystem. On EXDEV it
// writes destination via allowedWriteFile (temp+rename) then removes source.
// Returns crossFS=true when the copy+remove path was used.
func atomicMoveFile(workDir, tempDir, srcUser, dstUser, srcAbs, dstAbs string, srcData []byte) (crossFS bool, err error) {
	// Ensure destination parent exists before rename.
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
		return false, err
	}
	// Re-resolve after MkdirAll (parent→symlink race).
	srcAbs, _, err = resolveAllowedPath(workDir, tempDir, srcUser)
	if err != nil {
		return false, err
	}
	dstAbs, _, err = resolveAllowedPath(workDir, tempDir, dstUser)
	if err != nil {
		return false, err
	}
	if fi, lerr := os.Lstat(srcAbs); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return false, ErrPrecondition(fmt.Sprintf("refusing to move symlink %q", srcAbs))
	}
	if fi, lerr := os.Lstat(dstAbs); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return false, ErrPrecondition(fmt.Sprintf("refusing to overwrite symlink %q", dstAbs))
	}

	if err := os.Rename(srcAbs, dstAbs); err == nil {
		return false, nil
	} else if !isEXDEV(err) {
		return false, err
	}

	// Cross-filesystem: write dest atomically, then unlink source.
	if err := allowedWriteFile(workDir, tempDir, dstUser, srcData); err != nil {
		return true, err
	}
	if err := os.Remove(srcAbs); err != nil {
		// Best-effort: destination is already written; report partial failure.
		return true, fmt.Errorf("cross-filesystem move wrote destination but failed to remove source %s: %w", srcAbs, err)
	}
	return true, nil
}

func isEXDEV(err error) bool {
	if err == nil {
		return false
	}
	var link *os.LinkError
	if errors.As(err, &link) {
		return link.Err == syscall.EXDEV
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err == syscall.EXDEV
	}
	return errors.Is(err, syscall.EXDEV)
}
