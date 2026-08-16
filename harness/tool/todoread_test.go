package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTodoReadAfterWriteSameStore(t *testing.T) {
	store := NewTodoStore()
	tw := NewTodoWrite(store)
	tr := NewTodoRead(store)
	tc := allowAll(t.TempDir())

	_, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"todos": []map[string]any{
			{"id": "1", "content": "shared item", "status": "pending"},
			{"id": "2", "content": "done", "status": "completed"},
		},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}

	res, err := tr.Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "2 todos" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "shared item") || !strings.Contains(res.Output, `"id": "2"`) {
		t.Errorf("output = %s", res.Output)
	}
	var meta struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Todos) != 2 || meta.Todos[0].Content != "shared item" {
		t.Errorf("metadata = %#v", meta.Todos)
	}
}

func TestTodoReadEmptyList(t *testing.T) {
	tr := NewTodoRead(NewTodoStore())
	res, err := tr.Execute(context.Background(), mustJSON(t, map[string]any{}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "0 todos" {
		t.Errorf("title = %q", res.Title)
	}
	if strings.TrimSpace(res.Output) != "[]" {
		t.Errorf("output = %q", res.Output)
	}
}
