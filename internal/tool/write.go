package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type writeTool struct{}

func NewWrite() Tool { return writeTool{} }

func (writeTool) Name() string { return "write" }

func (writeTool) Contract() Contract {
	return staticContract(SideEffectWorkspaceMutative, IdempotencyConditional)
}

func (writeTool) Description() string {
	return `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- If this is an existing file, you MUST use the read tool first. Prefer the edit tool for modifying existing files.
- ALWAYS prefer editing existing files in the codebase. NEVER write new files unless explicitly required.
- NEVER proactively create documentation files (*.md) or README files. Only create documentation files if explicitly requested by the user.
- Only use emojis if the user explicitly requests it.
- Paths must stay inside the workspace root, or inside the session temporary directory shown in the environment block (absolute paths only for temp). Use the session temp dir for short-lived scratch, request bodies, or command interchange files — not for durable project output.
- Optional baseHash (sha256 hex of expected current content when overwriting) fails closed with precondition_failed on mismatch.
- Writes are atomic (temp + rename) on local POSIX filesystems.`
}

func (writeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file to write"},
			"content": {"type": "string", "description": "Full content to write"},
			"baseHash": {"type": "string", "description": "Optional sha256 hex of expected current content when overwriting; fails with precondition_failed on mismatch"}
		},
		"required": ["filePath", "content"]
	}`)
}

type writeArgs struct {
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
	BaseHash string `json:"baseHash"`
}

func (writeTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a writeArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, ErrInvalidArgs(fmt.Sprintf("invalid arguments: %v", err))
	}
	tempDir := ""
	if tc != nil {
		tempDir = tc.SessionTempDir
	}
	path, rel, err := resolveAllowedPath(tc.WorkDir, tempDir, a.FilePath)
	if err != nil {
		return Result{}, err
	}
	existed := FileExisted(path)
	if existed {
		if err := tc.Files.CheckFresh(path, rel); err != nil {
			return Result{}, err
		}
		if err := CheckBaseHash(path, a.BaseHash, rel); err != nil {
			return Result{}, err
		}
	} else if strings.TrimSpace(a.BaseHash) != "" {
		// baseHash on a missing file is a failed precondition (expected content absent).
		return Result{}, PreconditionFailed(fmt.Sprintf("%s: baseHash precondition failed (file missing)", rel))
	}

	existing, readErr := os.ReadFile(path)
	// Content guard before permission ask so secret-shaped writes never prompt
	// as ordinary write approvals (and never reach disk).
	if err := checkContentGuard(ctx, tc, rel, a.Content); err != nil {
		return Result{}, err
	}
	meta, _ := json.Marshal(map[string]any{
		"exists":  readErr == nil,
		"oldSize": len(existing),
		"newSize": len(a.Content),
	})
	if err := tc.Ask(ctx, AskRequest{Permission: "write", Patterns: []string{rel}, Always: []string{"*"}, Metadata: meta}); err != nil {
		return Result{}, err
	}
	// Claim after permission so denied writes do not pollute ownership.
	overlapWarn, err := tc.ClaimWrite(path, rel)
	if err != nil {
		return Result{}, err
	}
	// Re-check freshness / content race before commit when overwriting.
	if readErr == nil {
		if err := CheckContentUnchanged(path, existing, rel); err != nil {
			return Result{}, err
		}
		if err := tc.Files.CheckFresh(path, rel); err != nil {
			return Result{}, err
		}
	}
	tc.SnapshotPath(path)
	// Re-validate + atomic temp/rename at exec time.
	if err := allowedWriteFile(tc.WorkDir, tempDir, a.FilePath, []byte(a.Content)); err != nil {
		return Result{}, err
	}
	// Refresh path after secure write (may differ if a prior symlink was resolved).
	path, _, err = resolveAllowedPath(tc.WorkDir, tempDir, a.FilePath)
	if err != nil {
		return Result{}, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		tc.Files.RecordBytes(path, info, []byte(a.Content))
	}
	tc.NoteTurnChange(path, existed, false)
	tc.NotifyFileSync(path, a.Content, false)
	verb := "Created"
	if readErr == nil {
		verb = "Overwrote"
	}
	out := fmt.Sprintf("%s %s (%d bytes)", verb, rel, len(a.Content))
	out = AppendOverlapWarning(out, overlapWarn)
	res := Result{
		Title:    rel,
		Output:   out,
		Metadata: meta,
	}
	return tc.AppendDiagnostics(ctx, res, path), nil
}
