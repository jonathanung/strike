package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

type writeTool struct{}

func NewWrite() Tool { return writeTool{} }

func (writeTool) Name() string { return "write" }

func (writeTool) Description() string {
	return `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- If this is an existing file, you MUST use the read tool first. Prefer the edit tool for modifying existing files.
- ALWAYS prefer editing existing files in the codebase. NEVER write new files unless explicitly required.
- NEVER proactively create documentation files (*.md) or README files. Only create documentation files if explicitly requested by the user.
- Only use emojis if the user explicitly requests it.`
}

func (writeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file to write"},
			"content": {"type": "string", "description": "Full content to write"}
		},
		"required": ["filePath", "content"]
	}`)
}

type writeArgs struct {
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
}

func (writeTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a writeArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	path, rel, err := resolveInWorkspace(tc.WorkDir, a.FilePath)
	if err != nil {
		return Result{}, err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		if err := tc.Files.CheckFresh(path, rel); err != nil {
			return Result{}, err
		}
	}

	existing, readErr := os.ReadFile(path)
	meta, _ := json.Marshal(map[string]any{
		"exists":  readErr == nil,
		"oldSize": len(existing),
		"newSize": len(a.Content),
	})
	if err := tc.Ask(ctx, AskRequest{Permission: "write", Patterns: []string{rel}, Always: []string{"*"}, Metadata: meta}); err != nil {
		return Result{}, err
	}
	tc.SnapshotPath(path)
	// Re-validate + O_NOFOLLOW at exec time (TOCTOU: symlink planted after resolve).
	if err := workspaceWriteFile(tc.WorkDir, a.FilePath, []byte(a.Content)); err != nil {
		return Result{}, err
	}
	// Refresh path after secure write (may differ if a prior symlink was resolved).
	path, _, err = resolveInWorkspace(tc.WorkDir, a.FilePath)
	if err != nil {
		return Result{}, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		tc.Files.Record(path, info)
	}
	tc.NotifyFileSync(path, a.Content, false)
	verb := "Created"
	if readErr == nil {
		verb = "Overwrote"
	}
	res := Result{
		Title:    rel,
		Output:   fmt.Sprintf("%s %s (%d bytes)", verb, rel, len(a.Content)),
		Metadata: meta,
	}
	return tc.AppendDiagnostics(ctx, res, path), nil
}
