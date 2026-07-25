package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNotebook(t *testing.T, dir, name string, nb map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(nb, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readNotebook(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var nb map[string]any
	if err := json.Unmarshal(data, &nb); err != nil {
		t.Fatal(err)
	}
	return nb
}

func sampleNotebook() map[string]any {
	return map[string]any{
		"nbformat":      4,
		"nbformat_minor": 5,
		"metadata": map[string]any{
			"kernelspec": map[string]any{
				"name": "python3",
			},
			"custom_root_key": "preserve-me",
		},
		"cells": []any{
			map[string]any{
				"id":       "cell-aaa",
				"cell_type": "markdown",
				"metadata": map[string]any{},
				"source":   "# Title",
			},
			map[string]any{
				"id":              "cell-bbb",
				"cell_type":       "code",
				"metadata":        map[string]any{},
				"source":          "print(1)",
				"outputs":         []any{map[string]any{"text": "1\n"}},
				"execution_count": float64(1),
			},
		},
	}
}

func TestNotebookEditReplaceByIDAndIndex(t *testing.T) {
	dir := t.TempDir()
	path := writeNotebook(t, dir, "n.ipynb", sampleNotebook())
	tc := allowAll(dir)
	tool := NewNotebookEdit()

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "n.ipynb",
		"cell_id":       "cell-bbb",
		"new_source":    "print(2)",
		"edit_mode":     "replace",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Replaced cell at index 1") {
		t.Errorf("output = %q", res.Output)
	}
	nb := readNotebook(t, path)
	cells := nb["cells"].([]any)
	cell := cells[1].(map[string]any)
	if cell["source"] != "print(2)" {
		t.Errorf("source = %#v", cell["source"])
	}
	// Code cell outputs cleared on replace.
	outs, _ := cell["outputs"].([]any)
	if len(outs) != 0 {
		t.Errorf("outputs = %#v, want cleared", cell["outputs"])
	}
	if cell["execution_count"] != nil {
		t.Errorf("execution_count = %#v, want nil", cell["execution_count"])
	}

	res, err = tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": path,
		"cell_id":       "cell-0",
		"new_source":    "## New",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Replaced cell at index 0") {
		t.Errorf("output = %q", res.Output)
	}
	nb = readNotebook(t, path)
	cells = nb["cells"].([]any)
	if cells[0].(map[string]any)["source"] != "## New" {
		t.Errorf("cell0 source = %#v", cells[0].(map[string]any)["source"])
	}
}

func TestNotebookEditInsertAfterIDAndAtStart(t *testing.T) {
	dir := t.TempDir()
	path := writeNotebook(t, dir, "n.ipynb", sampleNotebook())
	tc := allowAll(dir)
	tool := NewNotebookEdit()

	// Insert after first cell by id → index 1.
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "n.ipynb",
		"cell_id":       "cell-aaa",
		"new_source":    "inserted",
		"cell_type":     "markdown",
		"edit_mode":     "insert",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Inserted markdown cell at index 1") {
		t.Errorf("output = %q", res.Output)
	}
	nb := readNotebook(t, path)
	cells := nb["cells"].([]any)
	if len(cells) != 3 {
		t.Fatalf("len cells = %d", len(cells))
	}
	if cells[1].(map[string]any)["source"] != "inserted" {
		t.Errorf("cells[1] = %#v", cells[1])
	}

	// Insert at start when cell_id omitted.
	res, err = tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "n.ipynb",
		"new_source":    "first",
		"cell_type":     "code",
		"edit_mode":     "insert",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Inserted code cell at index 0") {
		t.Errorf("output = %q", res.Output)
	}
	nb = readNotebook(t, path)
	cells = nb["cells"].([]any)
	if len(cells) != 4 {
		t.Fatalf("len cells = %d", len(cells))
	}
	c0 := cells[0].(map[string]any)
	if c0["source"] != "first" || c0["cell_type"] != "code" {
		t.Errorf("cells[0] = %#v", c0)
	}
	if _, ok := c0["id"]; !ok {
		t.Error("expected cell id assigned for nbformat 4.5+")
	}
}

func TestNotebookEditDelete(t *testing.T) {
	dir := t.TempDir()
	path := writeNotebook(t, dir, "n.ipynb", sampleNotebook())
	tc := allowAll(dir)

	res, err := NewNotebookEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "n.ipynb",
		"cell_id":       "1",
		"edit_mode":     "delete",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Deleted cell at index 1") {
		t.Errorf("output = %q", res.Output)
	}
	nb := readNotebook(t, path)
	cells := nb["cells"].([]any)
	if len(cells) != 1 {
		t.Fatalf("len = %d", len(cells))
	}
	if cells[0].(map[string]any)["id"] != "cell-aaa" {
		t.Errorf("remaining = %#v", cells[0])
	}
}

func TestNotebookEditErrors(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	tool := NewNotebookEdit()

	// Invalid JSON
	badJSON := filepath.Join(dir, "bad.ipynb")
	if err := os.WriteFile(badJSON, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "bad.ipynb",
		"cell_id":       "0",
		"new_source":    "x",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Not ipynb extension
	txt := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(txt, []byte(`{"cells":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "note.txt",
		"cell_id":       "0",
		"new_source":    "x",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), ".ipynb") {
		t.Fatalf("not ipynb: %v", err)
	}

	// Missing cell
	writeNotebook(t, dir, "ok.ipynb", sampleNotebook())
	_, err = tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "ok.ipynb",
		"cell_id":       "no-such-cell",
		"new_source":    "x",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing cell: %v", err)
	}

	// Out of range index
	_, err = tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "ok.ipynb",
		"cell_id":       "cell-99",
		"new_source":    "x",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("oor: %v", err)
	}
}

func TestNotebookEditAskUsesEditPermission(t *testing.T) {
	dir := t.TempDir()
	writeNotebook(t, dir, "n.ipynb", sampleNotebook())
	var saw AskRequest
	tc := &Context{
		WorkDir: dir,
		Ask: func(_ context.Context, req AskRequest) error {
			saw = req
			return nil
		},
	}
	_, err := NewNotebookEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "n.ipynb",
		"cell_id":       "cell-aaa",
		"new_source":    "x",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if saw.Permission != "edit" {
		t.Errorf("Permission = %q, want edit", saw.Permission)
	}
	if len(saw.Patterns) != 1 || saw.Patterns[0] != "n.ipynb" {
		t.Errorf("Patterns = %#v", saw.Patterns)
	}
}

func TestNotebookEditPermissionDenied(t *testing.T) {
	dir := t.TempDir()
	writeNotebook(t, dir, "n.ipynb", sampleNotebook())
	tc := &Context{
		WorkDir: dir,
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
	}
	_, err := NewNotebookEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "n.ipynb",
		"cell_id":       "0",
		"new_source":    "x",
	}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestNotebookEditPreservesUnrelatedRootKeys(t *testing.T) {
	dir := t.TempDir()
	path := writeNotebook(t, dir, "n.ipynb", sampleNotebook())
	_, err := NewNotebookEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"notebook_path": "n.ipynb",
		"cell_id":       "cell-0",
		"new_source":    "changed",
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	nb := readNotebook(t, path)
	meta, ok := nb["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata type %T", nb["metadata"])
	}
	if meta["custom_root_key"] != "preserve-me" {
		t.Errorf("custom_root_key = %#v", meta["custom_root_key"])
	}
	if nb["nbformat"] != float64(4) {
		t.Errorf("nbformat = %#v", nb["nbformat"])
	}
}
