package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

type todoReadTool struct {
	store *TodoStore
}

func NewTodoRead(store *TodoStore) Tool {
	if store == nil {
		store = NewTodoStore()
	}
	return &todoReadTool{store: store}
}

func (t *todoReadTool) Name() string { return "todoread" }

func (t *todoReadTool) Contract() Contract {
	return staticContract(SideEffectNone, IdempotencySafeRetry)
}

func (t *todoReadTool) Description() string {
	return `Read the current session todo list without modifying it.

Returns the full list as JSON (empty array when none). Use todowrite to create or replace the list.`
}

func (t *todoReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

func (t *todoReadTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if err := tc.Ask(ctx, AskRequest{
		Permission: "todoread",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	todos := t.store.Get()
	if todos == nil {
		todos = []TodoItem{}
	}
	out, err := json.MarshalIndent(todos, "", "  ")
	if err != nil {
		return Result{}, err
	}
	meta, _ := json.Marshal(map[string]any{"todos": todos})
	return Result{
		Title:    fmt.Sprintf("%d todos", len(todos)),
		Output:   string(out),
		Metadata: meta,
	}, nil
}
