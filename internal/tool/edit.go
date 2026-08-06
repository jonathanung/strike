package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type editTool struct{}

func NewEdit() Tool { return editTool{} }

func (editTool) Name() string { return "edit" }

func (editTool) Description() string {
	return `Performs exact string replacements in files.

Usage:
- Prefer reading the file with the read tool before editing so oldString matches current content.
- When editing text from read tool output, preserve exact indentation. Match the file content only — never include the line-number prefix from read output in oldString or newString.
- ALWAYS prefer editing existing files. NEVER write new files unless explicitly required.
- Only use emojis if the user explicitly requests it.
- Fails if oldString is not found, or if it matches multiple locations and replaceAll is false. Provide more surrounding context to make the match unique, or set replaceAll.
- Use replaceAll when renaming a symbol or replacing every occurrence in the file.`
}

func (editTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file to modify"},
			"oldString": {"type": "string", "description": "Exact text to replace"},
			"newString": {"type": "string", "description": "Replacement text"},
			"replaceAll": {"type": "boolean", "description": "Replace every occurrence (default false)"}
		},
		"required": ["filePath", "oldString", "newString"]
	}`)
}

type editArgs struct {
	FilePath   string `json:"filePath"`
	OldString  string `json:"oldString"`
	NewString  string `json:"newString"`
	ReplaceAll bool   `json:"replaceAll"`
}

func (editTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a editArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if a.OldString == a.NewString {
		return Result{}, fmt.Errorf("oldString and newString are identical")
	}
	path, rel, err := resolveInWorkspace(tc.WorkDir, a.FilePath)
	if err != nil {
		return Result{}, err
	}
	if err := tc.Files.CheckFresh(path, rel); err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	content := string(data)
	count := strings.Count(content, a.OldString)
	if count == 0 {
		return Result{}, fmt.Errorf("oldString not found in %s; read the file and match the content exactly", rel)
	}
	if count > 1 && !a.ReplaceAll {
		return Result{}, fmt.Errorf("oldString matches %d locations in %s; provide more surrounding context to make it unique, or set replaceAll", count, rel)
	}
	// Metadata carries the change for UI diff rendering, independent of the
	// model-facing output.
	meta, _ := json.Marshal(map[string]any{"oldString": a.OldString, "newString": a.NewString, "count": count})
	if err := tc.Ask(ctx, AskRequest{Permission: "edit", Patterns: []string{rel}, Always: []string{"*"}, Metadata: meta}); err != nil {
		return Result{}, err
	}
	var updated string
	replaced := 1
	if a.ReplaceAll {
		updated = strings.ReplaceAll(content, a.OldString, a.NewString)
		replaced = count
	} else {
		updated = strings.Replace(content, a.OldString, a.NewString, 1)
	}
	tc.SnapshotPath(path)
	// Re-validate + O_NOFOLLOW at exec time (TOCTOU: symlink planted after resolve).
	if err := workspaceWriteFile(tc.WorkDir, a.FilePath, []byte(updated)); err != nil {
		return Result{}, err
	}
	path, _, err = resolveInWorkspace(tc.WorkDir, a.FilePath)
	if err != nil {
		return Result{}, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		tc.Files.Record(path, info)
	}
	tc.NotifyFileSync(path, updated, false)
	return Result{
		Title:    rel,
		Output:   fmt.Sprintf("Edited %s (%d replacement(s))", rel, replaced),
		Metadata: meta,
	}, nil
}
