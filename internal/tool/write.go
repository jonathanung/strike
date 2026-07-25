package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	path := absPath(tc.WorkDir, a.FilePath)
	rel := relPath(tc.WorkDir, path)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
		return Result{}, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		tc.Files.Record(path, info)
	}
	verb := "Created"
	if readErr == nil {
		verb = "Overwrote"
	}
	return Result{
		Title:    rel,
		Output:   fmt.Sprintf("%s %s (%d bytes)", verb, rel, len(a.Content)),
		Metadata: meta,
	}, nil
}
