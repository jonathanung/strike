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

func (editTool) Contract() Contract {
	return staticContract(SideEffectWorkspaceMutative, IdempotencyConditional)
}

func (editTool) Description() string {
	return `Performs exact string replacements in files.

Usage:
- Prefer reading the file with the read tool before editing so oldString matches current content.
- When editing text from read tool output, preserve exact indentation. Match the file content only — never include the line-number prefix from read output in oldString or newString.
- ALWAYS prefer editing existing files. NEVER write new files unless explicitly required.
- Only use emojis if the user explicitly requests it.
- Paths must stay inside the workspace root, or inside the session temporary directory shown in the environment block (absolute paths only for temp).
- Fails if oldString is not found, or if it matches multiple locations and replaceAll is false. Provide more surrounding context to make the match unique, or set replaceAll.
- Use replaceAll when renaming a symbol or replacing every occurrence in the file.
- Optional baseHash (sha256 hex of the full file at plan time) fails closed with precondition_failed if the file changed.`
}

func (editTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file to modify"},
			"oldString": {"type": "string", "description": "Exact text to replace"},
			"newString": {"type": "string", "description": "Replacement text"},
			"replaceAll": {"type": "boolean", "description": "Replace every occurrence (default false)"},
			"baseHash": {"type": "string", "description": "Optional sha256 hex of expected current file content; fails with precondition_failed on mismatch"}
		},
		"required": ["filePath", "oldString", "newString"]
	}`)
}

type editArgs struct {
	FilePath   string `json:"filePath"`
	OldString  string `json:"oldString"`
	NewString  string `json:"newString"`
	ReplaceAll bool   `json:"replaceAll"`
	BaseHash   string `json:"baseHash"`
}

func (editTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a editArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, ErrInvalidArgs(fmt.Sprintf("invalid arguments: %v", err))
	}
	if a.OldString == a.NewString {
		return Result{}, ErrInvalidArgs("oldString and newString are identical")
	}
	tempDir := ""
	if tc != nil {
		tempDir = tc.SessionTempDir
	}
	path, rel, err := resolveAllowedPath(tc.WorkDir, tempDir, a.FilePath)
	if err != nil {
		return Result{}, err
	}
	if err := tc.Files.CheckFresh(path, rel); err != nil {
		return Result{}, err
	}
	if err := CheckBaseHash(path, a.BaseHash, rel); err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	content := string(data)
	count := strings.Count(content, a.OldString)
	if count == 0 {
		return Result{}, ErrPrecondition(fmt.Sprintf("oldString not found in %s; read the file and match the content exactly", rel))
	}
	if count > 1 && !a.ReplaceAll {
		return Result{}, ErrPrecondition(fmt.Sprintf("oldString matches %d locations in %s; provide more surrounding context to make it unique, or set replaceAll", count, rel))
	}
	// Metadata carries the change for UI diff rendering, independent of the
	// model-facing output.
	meta, _ := json.Marshal(map[string]any{"oldString": a.OldString, "newString": a.NewString, "count": count})
	if err := tc.Ask(ctx, AskRequest{Permission: "edit", Patterns: []string{rel}, Always: []string{"*"}, Metadata: meta}); err != nil {
		return Result{}, err
	}
	// Claim after validation + permission so failed matches do not pollute ownership.
	overlapWarn, err := tc.ClaimWrite(path, rel)
	if err != nil {
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
	// Close the race between plan-time read and commit.
	if err := CheckContentUnchanged(path, data, rel); err != nil {
		return Result{}, err
	}
	if err := tc.Files.CheckFresh(path, rel); err != nil {
		return Result{}, err
	}
	existed := FileExisted(path)
	tc.SnapshotPath(path)
	// Re-validate + atomic temp/rename at exec time.
	if err := allowedWriteFile(tc.WorkDir, tempDir, a.FilePath, []byte(updated)); err != nil {
		return Result{}, err
	}
	path, _, err = resolveAllowedPath(tc.WorkDir, tempDir, a.FilePath)
	if err != nil {
		return Result{}, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		tc.Files.RecordBytes(path, info, []byte(updated))
	}
	tc.NoteTurnChange(path, existed, false)
	tc.NotifyFileSync(path, updated, false)
	out := fmt.Sprintf("Edited %s (%d replacement(s))", rel, replaced)
	out = AppendOverlapWarning(out, overlapWarn)
	res := Result{
		Title:    rel,
		Output:   out,
		Metadata: meta,
	}
	return tc.AppendDiagnostics(ctx, res, path), nil
}
