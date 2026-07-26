package tui

import (
	"encoding/json"
	"os"
	"strings"
)

const (
	// Keep modal hunk short so permission choices stay visible on ~24-row terminals.
	diffPreviewMaxLinesModal = 8
	diffPreviewMaxLinesCell  = 8
)

// editDiffMeta is the edit-tool-shaped metadata carried on PermissionAsked
// and ToolCallEnd for unified diff previews.
type editDiffMeta struct {
	OldString string
	NewString string
	Count     int
}

// parseEditMetadata returns ok only when raw JSON has both oldString and
// newString as string types (empty strings allowed). Fails on empty raw, bad
// JSON, missing keys, wrong types, or write-shaped meta.
func parseEditMetadata(raw json.RawMessage) (editDiffMeta, bool) {
	if len(raw) == 0 {
		return editDiffMeta{}, false
	}
	var parsed struct {
		OldString *string `json:"oldString"`
		NewString *string `json:"newString"`
		Count     int     `json:"count"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return editDiffMeta{}, false
	}
	if parsed.OldString == nil || parsed.NewString == nil {
		return editDiffMeta{}, false
	}
	return editDiffMeta{
		OldString: *parsed.OldString,
		NewString: *parsed.NewString,
		Count:     parsed.Count,
	}, true
}

// reviewable reports whether this finished tool touched a file that can be
// opened for post-edit review (v).
func (c *toolCell) reviewable() bool {
	return c != nil && c.done && !c.isError && toolTouchedPath(c) != ""
}

// reviewTarget returns the workdir-relative path and 1-based line of the first
// changed hunk for post-edit review. ok is false when the cell is not reviewable.
func (c *toolCell) reviewTarget(workDir string) (path string, line int, ok bool) {
	if !c.reviewable() {
		return "", 0, false
	}
	path = toolTouchedPath(c)
	line = 1
	if meta, has := parseEditMetadata(c.metadata); has {
		abs := absPathInWorkDir(workDir, path)
		if data, err := os.ReadFile(abs); err == nil {
			line = firstChangedLine(meta.OldString, meta.NewString, string(data))
		}
	}
	return path, line, true
}

// toolTouchedPath extracts the primary file path from a file-mutating tool cell.
func toolTouchedPath(c *toolCell) string {
	if c == nil {
		return ""
	}
	switch c.name {
	case "edit", "write", "notebook_edit":
		if c.title != "" {
			return c.title
		}
		return filePathFromArgs(c.args)
	case "apply_patch":
		return firstPatchPath(c.metadata)
	default:
		return ""
	}
}

func filePathFromArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var parsed struct {
		FilePath string `json:"filePath"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.FilePath)
}

func firstPatchPath(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var ops []struct {
		Type   string `json:"type"`
		Path   string `json:"path"`
		MoveTo string `json:"moveTo"`
	}
	if err := json.Unmarshal(raw, &ops); err != nil || len(ops) == 0 {
		return ""
	}
	op := ops[0]
	if op.Type == "move" && strings.TrimSpace(op.MoveTo) != "" {
		return strings.TrimSpace(op.MoveTo)
	}
	return strings.TrimSpace(op.Path)
}

// firstChangedLine returns the 1-based line of the first changed hunk in the
// post-edit file. It locates newString in fileContent and walks past any
// leading equal lines shared with oldString. Falls back to 1.
func firstChangedLine(oldStr, newStr, fileContent string) int {
	if newStr == "" {
		return 1
	}
	idx := strings.Index(fileContent, newStr)
	if idx < 0 {
		return 1
	}
	base := 1 + strings.Count(fileContent[:idx], "\n")
	oldLines := splitEditLines(oldStr)
	newLines := splitEditLines(newStr)
	pref := 0
	for pref < len(oldLines) && pref < len(newLines) && oldLines[pref] == newLines[pref] {
		pref++
	}
	return base + pref
}

func splitEditLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
