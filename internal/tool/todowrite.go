package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// TodoStore holds the session todo list shared by todowrite and todoread.
type TodoStore struct {
	mu    sync.Mutex
	todos []TodoItem
}

func NewTodoStore() *TodoStore { return &TodoStore{} }

// Get returns a copy of the current todo list.
func (s *TodoStore) Get() []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TodoItem, len(s.todos))
	copy(out, s.todos)
	return out
}

// Replace sets the todo list to a copy of items.
func (s *TodoStore) Replace(items []TodoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := make([]TodoItem, len(items))
	copy(stored, items)
	s.todos = stored
}

type todoWriteTool struct {
	store *TodoStore
}

func NewTodoWrite(store *TodoStore) Tool {
	if store == nil {
		store = NewTodoStore()
	}
	return &todoWriteTool{store: store}
}

func (t *todoWriteTool) Name() string { return "todowrite" }

func (t *todoWriteTool) Description() string {
	return `Create and maintain a structured task list for the current coding session.

Use this tool to track progress during multi-step work and keep todo statuses current.

Usage notes:
  - Provide the full todo list on every call; the list is replaced entirely (pass an empty array to clear).
  - Each item needs a unique id, non-empty content, and status: pending, in_progress, completed, or cancelled.
  - Use todoread to inspect the current list without changing it; this write also returns the full list.
  - Prefer this for multi-step tasks so progress stays visible across turns.`
}

func (t *todoWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"todos": {
				"type": "array",
				"description": "The complete updated todo list (full replace)",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "string", "description": "Unique todo identifier"},
						"content": {"type": "string", "description": "Todo description"},
						"status": {"type": "string", "enum": ["pending", "in_progress", "completed", "cancelled"], "description": "Todo status"}
					},
					"required": ["id", "content", "status"]
				}
			}
		},
		"required": ["todos"]
	}`)
}

func (t *todoWriteTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	// Require an explicit "todos" key; absent or JSON null must not silently clear.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	todosRaw, ok := raw["todos"]
	if !ok {
		return Result{}, fmt.Errorf("todos is required")
	}
	if len(todosRaw) == 0 || string(todosRaw) == "null" {
		return Result{}, fmt.Errorf("todos must not be null")
	}
	var todos []TodoItem
	if err := json.Unmarshal(todosRaw, &todos); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if todos == nil {
		// Defensive: non-null JSON that unmarshaled to nil (should not happen for arrays).
		todos = []TodoItem{}
	}
	seen := make(map[string]struct{}, len(todos))
	for i, item := range todos {
		if item.ID == "" {
			return Result{}, fmt.Errorf("todos[%d].id is required", i)
		}
		if item.Content == "" {
			return Result{}, fmt.Errorf("todos[%d].content is required", i)
		}
		switch item.Status {
		case "pending", "in_progress", "completed", "cancelled":
		default:
			return Result{}, fmt.Errorf("todos[%d].status must be pending, in_progress, completed, or cancelled", i)
		}
		if _, dup := seen[item.ID]; dup {
			return Result{}, fmt.Errorf("duplicate todo id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
	}

	if err := tc.Ask(ctx, AskRequest{
		Permission: "todowrite",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	t.store.Replace(todos)
	stored := t.store.Get()

	out, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return Result{}, err
	}
	active := 0
	for _, item := range stored {
		if item.Status != "completed" {
			active++
		}
	}
	meta, _ := json.Marshal(map[string]any{"todos": stored})
	return Result{
		Title:    fmt.Sprintf("%d todos", active),
		Output:   string(out),
		Metadata: meta,
	}, nil
}
