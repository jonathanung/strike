package tui

import "encoding/json"

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
