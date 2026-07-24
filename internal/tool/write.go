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
	return "Write content to a file, creating it (and parent directories) if needed, overwriting if it exists. Prefer the edit tool for modifying existing files."
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
