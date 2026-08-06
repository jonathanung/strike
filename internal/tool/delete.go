package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type deleteTool struct{}

func NewDelete() Tool { return deleteTool{} }

func (deleteTool) Name() string { return "delete" }

func (deleteTool) Contract() Contract {
	return staticContract(SideEffectWorkspaceMutative, IdempotencyConditional)
}

func (deleteTool) Description() string {
	return `Deletes a file (or an explicitly allowed directory) within allowed roots.

Usage:
- Prefer this tool over bash rm for ordinary deletions so path validation, permissions, ownership, freshness, and TurnDiff apply.
- Paths must stay inside the workspace root, or inside the session temporary directory (absolute paths only for temp).
- Regular files are deleted with os.Remove (unlinks the leaf; does not follow a leaf symlink — symlink leaves are refused).
- Directories: empty directories may be deleted; non-empty directories require recursive=true. Never deletes the workspace or session-temp root itself.
- Optional baseHash (sha256 hex of expected file content) fails closed with precondition_failed on mismatch (files only; ignored for directories).
- Deletion of a single file is atomic at the inode level on local POSIX filesystems.`
}

func (deleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Path of the file or directory to delete"},
			"recursive": {"type": "boolean", "description": "Allow deleting a non-empty directory (default false)"},
			"baseHash": {"type": "string", "description": "Optional sha256 hex of expected file content; fails with precondition_failed on mismatch (files only)"}
		},
		"required": ["path"]
	}`)
}

type deleteArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	BaseHash  string `json:"baseHash"`
}

func (deleteTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a deleteArgs
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
	path, rel, err := resolveAllowedPath(tc.WorkDir, tempDir, a.Path)
	if err != nil {
		return Result{}, err
	}

	// Never delete an allowed root itself.
	if err := refuseRootDelete(tc.WorkDir, tempDir, path, rel); err != nil {
		return Result{}, err
	}
	// Refuse when the user-named leaf is a symlink (do not silently delete the target).
	if isSymlinkLeaf(tc.WorkDir, tempDir, a.Path) {
		return Result{}, ErrPrecondition(fmt.Sprintf("%s is a symlink; refuse to delete through symlinks", rel))
	}

	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, ErrPrecondition(fmt.Sprintf("%s does not exist", rel))
		}
		return Result{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Result{}, ErrPrecondition(fmt.Sprintf("%s is a symlink; refuse to delete through symlinks", rel))
	}

	isDir := info.IsDir()
	var fileData []byte
	if !isDir {
		if !info.Mode().IsRegular() {
			return Result{}, ErrInvalidArgs(fmt.Sprintf("%s is not a regular file or directory", rel))
		}
		if err := tc.Files.CheckFresh(path, rel); err != nil {
			return Result{}, err
		}
		if err := CheckBaseHash(path, a.BaseHash, rel); err != nil {
			return Result{}, err
		}
		fileData, err = os.ReadFile(path)
		if err != nil {
			return Result{}, err
		}
	} else {
		if strings.TrimSpace(a.BaseHash) != "" {
			return Result{}, ErrInvalidArgs("baseHash is only supported for file deletes, not directories")
		}
		empty, err := dirIsEmpty(path)
		if err != nil {
			return Result{}, err
		}
		if !empty && !a.Recursive {
			return Result{}, ErrPrecondition(fmt.Sprintf("%s is a non-empty directory; set recursive=true to delete", rel))
		}
	}

	meta, _ := json.Marshal(map[string]any{
		"path":      rel,
		"directory": isDir,
		"recursive": a.Recursive && isDir,
	})
	if err := tc.Ask(ctx, AskRequest{
		Permission: "edit",
		Patterns:   []string{rel},
		Always:     []string{"*"},
		Metadata:   meta,
	}); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	overlapWarn, err := tc.ClaimWrite(path, rel)
	if err != nil {
		return Result{}, err
	}

	// Re-check before commit.
	if !isDir {
		if err := CheckContentUnchanged(path, fileData, rel); err != nil {
			return Result{}, err
		}
		if err := tc.Files.CheckFresh(path, rel); err != nil {
			return Result{}, err
		}
	} else {
		// Directory may have gained children after the empty check.
		if !a.Recursive {
			empty, err := dirIsEmpty(path)
			if err != nil {
				return Result{}, err
			}
			if !empty {
				return Result{}, ErrPrecondition(fmt.Sprintf("%s is a non-empty directory; set recursive=true to delete", rel))
			}
		}
	}

	tc.SnapshotPath(path)

	// Re-resolve + re-validate root immediately before unlink.
	path, rel, err = resolveAllowedPath(tc.WorkDir, tempDir, a.Path)
	if err != nil {
		return Result{}, err
	}
	if err := refuseRootDelete(tc.WorkDir, tempDir, path, rel); err != nil {
		return Result{}, err
	}
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return Result{}, ErrPrecondition(fmt.Sprintf("%s is a symlink; refuse to delete through symlinks", rel))
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if isDir && a.Recursive {
		if err := os.RemoveAll(path); err != nil {
			return Result{}, err
		}
	} else {
		// os.Remove unlinks the final component (does not follow a leaf symlink).
		if err := os.Remove(path); err != nil {
			return Result{}, err
		}
	}

	if tc.Files != nil {
		tc.Files.Forget(path)
	}
	tc.NoteTurnChange(path, true, true)
	tc.NotifyFileSync(path, "", true)

	kind := "file"
	if isDir {
		kind = "directory"
	}
	out := fmt.Sprintf("Deleted %s %s", kind, rel)
	out = AppendOverlapWarning(out, overlapWarn)
	res := Result{
		Title:    rel,
		Output:   out,
		Metadata: meta,
	}
	// Diagnostics on a deleted path are usually empty; still call for consistency.
	return tc.AppendDiagnostics(ctx, res, path), nil
}

func refuseRootDelete(workDir, tempDir, abs, display string) error {
	abs = filepath.Clean(abs)
	if workDir != "" {
		if root, err := workspaceRootReal(workDir); err == nil && filepath.Clean(root) == abs {
			return ErrPrecondition(fmt.Sprintf("refusing to delete workspace root %s", display))
		}
	}
	tempDir = strings.TrimSpace(tempDir)
	if tempDir != "" {
		if root, err := workspaceRootReal(tempDir); err == nil && filepath.Clean(root) == abs {
			return ErrPrecondition(fmt.Sprintf("refusing to delete session temp root %s", display))
		}
	}
	return nil
}

func dirIsEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		return false, err
	}
	return len(names) == 0, nil
}
