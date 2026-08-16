package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"

	"github.com/jonathanung/strike-cli/internal/memory"
)

// MemoryStore is the durable project memory surface used by memory tools.
type MemoryStore interface {
	Get(key string) (memory.Entry, bool, error)
	List(tag string) ([]memory.Entry, error)
	Put(key, value string, tags []string) error
	Delete(key string) error
}

type memoryWriteTool struct {
	store MemoryStore
}

// NewMemoryWrite builds the memory_write tool. store must be non-nil.
func NewMemoryWrite(store MemoryStore) tool.Tool {
	return &memoryWriteTool{store: store}
}

func (t *memoryWriteTool) Name() string { return "memory_write" }

func (t *memoryWriteTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectExternal, tool.IdempotencyConditional)
}

func (t *memoryWriteTool) Description() string {
	return `Write a project-local memory entry that persists across sessions.

Use this to remember durable facts about the current project (conventions,
endpoints, decisions). Entries are keyed strings with optional tags.

Usage notes:
  - key is required and identifies the entry (upsert on repeat writes).
  - value is the stored text; pass delete=true to remove an entry instead.
  - tags are optional labels for later filtered reads via memory_read.
  - Tags instruction, preference, or project-convention auto-load into context
    each turn (capped). Other tags and untagged notes stay on-demand only.
  - Memory is project-scoped only — never global across repos.`
}

func (t *memoryWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"key": {"type": "string", "description": "Memory key"},
			"value": {"type": "string", "description": "Memory value (required unless delete is true)"},
			"tags": {
				"type": "array",
				"description": "Optional tags for filtering",
				"items": {"type": "string"}
			},
			"delete": {"type": "boolean", "description": "When true, delete the key instead of writing"}
		},
		"required": ["key"]
	}`)
}

func (t *memoryWriteTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if t.store == nil {
		return tool.Result{}, errors.New("memory store is unavailable")
	}
	var in struct {
		Key    string   `json:"key"`
		Value  *string  `json:"value"`
		Tags   []string `json:"tags"`
		Delete bool     `json:"delete"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return tool.Result{}, errors.New("key is required")
	}

	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "memory_write",
		Patterns:   []string{key},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}

	if in.Delete {
		if err := t.store.Delete(key); err != nil {
			if errors.Is(err, memory.ErrNotFound) {
				return tool.Result{
					Title:  "memory delete miss",
					Output: fmt.Sprintf("no memory entry for %q", key),
				}, nil
			}
			return tool.Result{}, err
		}
		meta, _ := json.Marshal(map[string]any{"key": key, "deleted": true})
		return tool.Result{
			Title:    "memory deleted",
			Output:   fmt.Sprintf("deleted %q", key),
			Metadata: meta,
		}, nil
	}

	if in.Value == nil {
		return tool.Result{}, errors.New("value is required unless delete is true")
	}
	if err := t.store.Put(key, *in.Value, in.Tags); err != nil {
		return tool.Result{}, err
	}
	entry, ok, err := t.store.Get(key)
	if err != nil {
		return tool.Result{}, err
	}
	if !ok {
		return tool.Result{}, errors.New("memory write did not persist")
	}
	out, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	meta, _ := json.Marshal(map[string]any{
		"key":   entry.Key,
		"tags":  entry.Tags,
		"value": entry.Value,
	})
	return tool.Result{
		Title:    fmt.Sprintf("memory %s", entry.Key),
		Output:   string(out),
		Metadata: meta,
	}, nil
}

type memoryReadTool struct {
	store MemoryStore
}

// NewMemoryRead builds the memory_read tool. store must be non-nil.
func NewMemoryRead(store MemoryStore) tool.Tool {
	return &memoryReadTool{store: store}
}

func (t *memoryReadTool) Name() string { return "memory_read" }

func (t *memoryReadTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectRead, tool.IdempotencySafeRetry)
}

func (t *memoryReadTool) Description() string {
	return `Read project-local memory entries that persist across sessions.

Usage notes:
  - Provide key to fetch one entry.
  - Omit key to list entries; optional tag filters the list.
  - Returns JSON. Empty list when nothing matches.`
}

func (t *memoryReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"key": {"type": "string", "description": "Fetch a single memory key"},
			"tag": {"type": "string", "description": "When listing, only entries with this tag"}
		}
	}`)
}

func (t *memoryReadTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if t.store == nil {
		return tool.Result{}, errors.New("memory store is unavailable")
	}
	var in struct {
		Key string `json:"key"`
		Tag string `json:"tag"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	key := strings.TrimSpace(in.Key)
	tag := strings.TrimSpace(in.Tag)

	pattern := "*"
	if key != "" {
		pattern = key
	} else if tag != "" {
		pattern = "tag:" + tag
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "memory_read",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}

	if key != "" {
		entry, ok, err := t.store.Get(key)
		if err != nil {
			return tool.Result{}, err
		}
		if !ok {
			return tool.Result{
				Title:  "memory miss",
				Output: fmt.Sprintf("no memory entry for %q", key),
			}, nil
		}
		out, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return tool.Result{}, err
		}
		meta, _ := json.Marshal(map[string]any{"key": entry.Key, "entry": entry})
		return tool.Result{
			Title:    fmt.Sprintf("memory %s", entry.Key),
			Output:   string(out),
			Metadata: meta,
		}, nil
	}

	entries, err := t.store.List(tag)
	if err != nil {
		return tool.Result{}, err
	}
	if entries == nil {
		entries = []memory.Entry{}
	}
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	meta, _ := json.Marshal(map[string]any{"count": len(entries), "tag": tag, "entries": entries})
	title := fmt.Sprintf("%d memories", len(entries))
	if tag != "" {
		title = fmt.Sprintf("%d memories tag:%s", len(entries), tag)
	}
	return tool.Result{
		Title:    title,
		Output:   string(out),
		Metadata: meta,
	}, nil
}
