package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestTodoWriteReplaceSuccess(t *testing.T) {
	tw := NewTodoWrite()
	tc := allowAll(t.TempDir())
	todos := []map[string]any{
		{"id": "1", "content": "first", "status": "pending"},
		{"id": "2", "content": "second", "status": "in_progress"},
		{"id": "3", "content": "done item", "status": "completed"},
	}
	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{"todos": todos}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "2 todos" {
		t.Errorf("Title = %q, want 2 todos (active count excludes completed)", res.Title)
	}
	if !strings.Contains(res.Output, `"id": "1"`) || !strings.Contains(res.Output, `"content": "first"`) {
		t.Errorf("output = %s", res.Output)
	}
	var meta struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Todos) != 3 {
		t.Fatalf("metadata todos = %d", len(meta.Todos))
	}
	if meta.Todos[1].Status != "in_progress" {
		t.Errorf("meta[1].status = %q", meta.Todos[1].Status)
	}

	// Full replace on second call.
	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"todos": []map[string]any{
			{"id": "a", "content": "only", "status": "cancelled"},
		},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "first") {
		t.Errorf("old todos leaked: %s", res.Output)
	}
	if !strings.Contains(res.Output, `"id": "a"`) {
		t.Errorf("output = %s", res.Output)
	}
	if res.Title != "1 todos" {
		t.Errorf("Title = %q", res.Title)
	}
}

func TestTodoWriteEmptyListClears(t *testing.T) {
	tw := NewTodoWrite()
	tc := allowAll(t.TempDir())
	_, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"todos": []map[string]any{
			{"id": "1", "content": "x", "status": "pending"},
		},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"todos": []any{},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "0 todos" {
		t.Errorf("Title = %q", res.Title)
	}
	if strings.TrimSpace(res.Output) != "[]" {
		t.Errorf("output = %q", res.Output)
	}
}

// Missing or null todos must error (not silently clear). Only an explicit
// empty array clears the list — see TestTodoWriteEmptyListClears.
func TestTodoWriteMissingOrNullTodos(t *testing.T) {
	tw := NewTodoWrite()
	tc := allowAll(t.TempDir())

	// Seed so a buggy clear-on-nil path would wipe observable state.
	seed, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"todos": []map[string]any{
			{"id": "keep", "content": "must remain", "status": "pending"},
		},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seed.Output, "keep") {
		t.Fatalf("seed failed: %s", seed.Output)
	}

	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"empty object", json.RawMessage(`{}`)},
		{"null todos", json.RawMessage(`{"todos": null}`)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tw.Execute(context.Background(), tt.raw, tc)
			if err == nil {
				t.Fatal("expected error for missing/null todos (must not clear)")
			}
			if !strings.Contains(strings.ToLower(err.Error()), "todo") {
				t.Errorf("err = %v, want mention of todos", err)
			}
		})
	}

	// State must be unchanged after the rejected calls: full-replace with the
	// same single item still works, and a prior silent clear would have left
	// empty internal state only visible on the next successful write's output
	// if we had a read API. Re-write empty array to confirm clear still works
	// when explicitly requested.
	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"todos": []any{},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Output) != "[]" {
		t.Errorf("explicit empty clear output = %q", res.Output)
	}
}

func TestTodoWriteValidation(t *testing.T) {
	tw := NewTodoWrite()
	tc := allowAll(t.TempDir())
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "invalid status",
			args: map[string]any{
				"todos": []map[string]any{
					{"id": "1", "content": "x", "status": "done"},
				},
			},
			want: "status",
		},
		{
			name: "empty id",
			args: map[string]any{
				"todos": []map[string]any{
					{"id": "", "content": "x", "status": "pending"},
				},
			},
			want: "id",
		},
		{
			name: "empty content",
			args: map[string]any{
				"todos": []map[string]any{
					{"id": "1", "content": "", "status": "pending"},
				},
			},
			want: "content",
		},
		{
			name: "duplicate ids",
			args: map[string]any{
				"todos": []map[string]any{
					{"id": "dup", "content": "a", "status": "pending"},
					{"id": "dup", "content": "b", "status": "completed"},
				},
			},
			want: "duplicate",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tw.Execute(context.Background(), mustJSON(t, tt.args), tc)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Errorf("err = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestTodoWritePermissionDenied(t *testing.T) {
	tw := NewTodoWrite()
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
	}
	_, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"todos": []map[string]any{
			{"id": "1", "content": "x", "status": "pending"},
		},
	}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestTodoWriteConcurrentExecute(t *testing.T) {
	tw := NewTodoWrite()
	tc := allowAll(t.TempDir())
	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i%10))
			_, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
				"todos": []map[string]any{
					{"id": id, "content": "c", "status": "pending"},
				},
			}), tc)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Final state is some valid single-item list.
	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"todos": []map[string]any{
			{"id": "final", "content": "ok", "status": "completed"},
		},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "final") {
		t.Errorf("output = %s", res.Output)
	}
}
