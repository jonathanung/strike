package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type notebookEditTool struct{}

func NewNotebookEdit() Tool { return notebookEditTool{} }

func (notebookEditTool) Name() string { return "notebook_edit" }

func (notebookEditTool) Description() string {
	return `Edits cells in a Jupyter notebook (.ipynb) file.

Usage notes:
  - notebook_path must point to a .ipynb file (absolute or relative to the working directory).
  - edit_mode is replace (default), insert, or delete.
  - cell_id identifies a cell by its id field, by "cell-N" (0-based index), or by a bare integer index.
  - replace/delete require cell_id. insert places the new cell after cell_id, or at index 0 if cell_id is omitted; cell_type is required for insert.
  - replace sets the cell source; for code cells, outputs and execution_count are cleared.
  - Prefer this tool over raw JSON edit/write for notebook structure.`
}

func (notebookEditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"notebook_path": {"type": "string", "description": "Path to the .ipynb notebook file"},
			"cell_id": {"type": "string", "description": "Cell id, cell-N index, or integer index"},
			"new_source": {"type": "string", "description": "New cell source (required for replace/insert)"},
			"cell_type": {"type": "string", "enum": ["code", "markdown"], "description": "Cell type (required for insert)"},
			"edit_mode": {"type": "string", "enum": ["replace", "insert", "delete"], "description": "Edit mode (default replace)"}
		},
		"required": ["notebook_path"]
	}`)
}

type notebookEditArgs struct {
	NotebookPath string `json:"notebook_path"`
	CellID       string `json:"cell_id"`
	NewSource    string `json:"new_source"`
	CellType     string `json:"cell_type"`
	EditMode     string `json:"edit_mode"`
}

func (notebookEditTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a notebookEditArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.NotebookPath) == "" {
		return Result{}, fmt.Errorf("notebook_path is required")
	}
	mode := strings.ToLower(strings.TrimSpace(a.EditMode))
	if mode == "" {
		mode = "replace"
	}
	switch mode {
	case "replace", "insert", "delete":
	default:
		return Result{}, fmt.Errorf("edit_mode must be replace, insert, or delete")
	}
	cellType := strings.ToLower(strings.TrimSpace(a.CellType))
	if cellType != "" && cellType != "code" && cellType != "markdown" {
		return Result{}, fmt.Errorf("cell_type must be code or markdown")
	}
	if mode == "insert" && cellType == "" {
		return Result{}, fmt.Errorf("cell_type is required when edit_mode=insert")
	}
	if mode != "insert" && strings.TrimSpace(a.CellID) == "" {
		return Result{}, fmt.Errorf("cell_id is required for edit_mode=%s", mode)
	}

	path := absPath(tc.WorkDir, a.NotebookPath)
	rel := relPath(tc.WorkDir, path)
	if !strings.EqualFold(filepath.Ext(path), ".ipynb") {
		return Result{}, fmt.Errorf("file must be a Jupyter notebook (.ipynb)")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var nb map[string]any
	if err := json.Unmarshal(data, &nb); err != nil {
		return Result{}, fmt.Errorf("notebook is not valid JSON: %w", err)
	}
	cellsRaw, ok := nb["cells"]
	if !ok {
		return Result{}, fmt.Errorf("notebook missing cells array")
	}
	cells, ok := cellsRaw.([]any)
	if !ok {
		return Result{}, fmt.Errorf("notebook cells is not an array")
	}

	idx, err := resolveNotebookCellIndex(cells, a.CellID, mode)
	if err != nil {
		return Result{}, err
	}

	meta, _ := json.Marshal(map[string]any{
		"notebook_path": rel,
		"edit_mode":     mode,
		"cell_id":       a.CellID,
		"cell_type":     cellType,
	})
	if err := tc.Ask(ctx, AskRequest{
		Permission: "edit",
		Patterns:   []string{rel},
		Always:     []string{"*"},
		Metadata:   meta,
	}); err != nil {
		return Result{}, err
	}

	var outMsg string
	switch mode {
	case "delete":
		cells = append(cells[:idx], cells[idx+1:]...)
		outMsg = fmt.Sprintf("Deleted cell at index %d in %s", idx, rel)
	case "insert":
		cell := newNotebookCell(cellType, a.NewSource, shouldAssignCellID(nb))
		// insert after resolved index when cell_id was set; resolveNotebookCellIndex
		// already returns the insertion index.
		if idx < 0 {
			idx = 0
		}
		if idx > len(cells) {
			idx = len(cells)
		}
		cells = append(cells, nil)
		copy(cells[idx+1:], cells[idx:])
		cells[idx] = cell
		outMsg = fmt.Sprintf("Inserted %s cell at index %d in %s", cellType, idx, rel)
	default: // replace
		cellMap, ok := cells[idx].(map[string]any)
		if !ok {
			return Result{}, fmt.Errorf("cell at index %d is not an object", idx)
		}
		cellMap["source"] = a.NewSource
		if cellType != "" {
			cellMap["cell_type"] = cellType
		}
		ct, _ := cellMap["cell_type"].(string)
		if ct == "code" || cellType == "code" {
			cellMap["outputs"] = []any{}
			cellMap["execution_count"] = nil
		}
		cells[idx] = cellMap
		outMsg = fmt.Sprintf("Replaced cell at index %d in %s", idx, rel)
	}
	nb["cells"] = cells

	out, err := json.MarshalIndent(nb, "", " ")
	if err != nil {
		return Result{}, err
	}
	out = append(out, '\n')
	tc.SnapshotPath(path)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return Result{}, err
	}
	return Result{
		Title:    rel,
		Output:   outMsg,
		Metadata: meta,
	}, nil
}

func resolveNotebookCellIndex(cells []any, cellID, mode string) (int, error) {
	if strings.TrimSpace(cellID) == "" {
		if mode == "insert" {
			return 0, nil
		}
		return 0, fmt.Errorf("cell_id is required")
	}
	// Match by cell id field first.
	for i, c := range cells {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := m["id"].(string); ok && id == cellID {
			if mode == "insert" {
				return i + 1, nil
			}
			return i, nil
		}
	}
	// cell-N or bare integer index.
	idx, ok := parseCellIndex(cellID)
	if !ok {
		return 0, fmt.Errorf("cell %q not found", cellID)
	}
	if mode == "insert" {
		// Insert after the referenced cell when using an index id.
		insertAt := idx + 1
		if idx < 0 || idx >= len(cells) {
			// Allow insert at end via index == len-1; reject out of range base.
			if idx == -1 {
				return 0, nil
			}
			if idx >= len(cells) {
				return 0, fmt.Errorf("cell index %d out of range (%d cells)", idx, len(cells))
			}
		}
		return insertAt, nil
	}
	if idx < 0 || idx >= len(cells) {
		return 0, fmt.Errorf("cell index %d out of range (%d cells)", idx, len(cells))
	}
	return idx, nil
}

func parseCellIndex(cellID string) (int, bool) {
	s := cellID
	if strings.HasPrefix(strings.ToLower(s), "cell-") {
		s = s[5:]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func shouldAssignCellID(nb map[string]any) bool {
	// nbformat 4.5+ recommends cell ids.
	major, _ := asInt(nb["nbformat"])
	minor, _ := asInt(nb["nbformat_minor"])
	return major > 4 || (major == 4 && minor >= 5)
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case int:
		return n, true
	default:
		return 0, false
	}
}

func newNotebookCell(cellType, source string, withID bool) map[string]any {
	cell := map[string]any{
		"cell_type": cellType,
		"metadata":  map[string]any{},
		"source":    source,
	}
	if withID {
		cell["id"] = randomCellID()
	}
	if cellType == "code" {
		cell["outputs"] = []any{}
		cell["execution_count"] = nil
	}
	return cell
}

func randomCellID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("cell%x", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}
