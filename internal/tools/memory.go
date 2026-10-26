package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonathanung/strike/harness/tool"
	"strings"

	"github.com/jonathanung/strike/internal/persist/memory"
)

// MemoryStore is the durable project memory surface used by memory tools.
type MemoryStore interface {
	Get(key string) (memory.Entry, bool, error)
	List(tag string) ([]memory.Entry, error)
	Put(key, value string, tags []string) error
	Delete(key string) error
}

type memoryWriteTool struct {
	store  MemoryStore
	global MemoryStore
}

// NewMemoryWrite builds the memory_write tool. store must be non-nil.
func NewMemoryWrite(store MemoryStore) tool.Tool {
	return &memoryWriteTool{store: store}
}

// NewMemoryWriteScoped writes the project store by default and the global
// store when the call passes scope=global.
func NewMemoryWriteScoped(project, global MemoryStore) tool.Tool {
	return &memoryWriteTool{store: project, global: global}
}

func (t *memoryWriteTool) Name() string { return "memory_write" }

func (t *memoryWriteTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectExternal, tool.IdempotencyConditional)
}

func (t *memoryWriteTool) Description() string {
	return `Write a durable memory entry that persists across sessions and restarts.

Use this to remember facts, preferences, and lessons. Entries are keyed
strings with optional tags. Stored text is credential-scrubbed.

Usage notes:
  - key is required and identifies the entry (upsert on repeat writes).
  - value is the stored text; pass delete=true to remove an entry instead.
  - tags are optional labels for later filtered reads via memory_read.
  - Tags instruction, preference, or project-convention auto-load into context
    each turn, including the start of a session (capped). Other tags stay
    on demand.
  - scope is "project" (default, this repo) or "global" (every project on
    this machine).
  - Incognito sessions can read memory but this tool refuses writes.
    Temporary sessions have no memory.`
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
			"delete": {"type": "boolean", "description": "When true, delete the key instead of writing"},
			"scope": {"type": "string", "description": "project (default) or global"}
		},
		"required": ["key"]
	}`)
}

func (t *memoryWriteTool) storeFor(scope string) (MemoryStore, error) {
	switch strings.TrimSpace(scope) {
	case "", "project":
		if t.store == nil {
			return nil, errors.New("memory store is unavailable")
		}
		return t.store, nil
	case "global":
		if t.global == nil {
			return nil, errors.New("global memory is not configured")
		}
		return t.global, nil
	default:
		return nil, fmt.Errorf("scope %q is unknown", scope)
	}
}

func (t *memoryWriteTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if err := rejectMemoryWrite(tc); err != nil {
		return tool.Result{}, err
	}
	var in struct {
		Key    string   `json:"key"`
		Value  *string  `json:"value"`
		Tags   []string `json:"tags"`
		Delete bool     `json:"delete"`
		Scope  string   `json:"scope"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	store, err := t.storeFor(in.Scope)
	if err != nil {
		return tool.Result{}, err
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
		if err := store.Delete(key); err != nil {
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
	author := ""
	if tc != nil {
		author = strings.TrimSpace(tc.SessionID)
	}
	if authored, ok := store.(interface {
		PutAuthored(key, value, author string, tags []string) error
	}); ok {
		err = authored.PutAuthored(key, *in.Value, author, in.Tags)
	} else {
		err = store.Put(key, *in.Value, in.Tags)
	}
	if err != nil {
		return tool.Result{}, err
	}
	entry, ok, err := store.Get(key)
	if err == nil && !ok {
		entry, ok, err = store.Get(memory.ScrubKey(key))
	}
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
	store  MemoryStore
	global MemoryStore
}

// NewMemoryRead builds the memory_read tool. store must be non-nil.
func NewMemoryRead(store MemoryStore) tool.Tool {
	return &memoryReadTool{store: store}
}

// NewMemoryReadScoped reads the project store by default and the global
// store when the call passes scope=global.
func NewMemoryReadScoped(project, global MemoryStore) tool.Tool {
	return &memoryReadTool{store: project, global: global}
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
			"tag": {"type": "string", "description": "When listing, only entries with this tag"},
			"scope": {"type": "string", "description": "project (default) or global"}
		}
	}`)
}

func (t *memoryReadTool) storeFor(scope string) (MemoryStore, error) {
	switch strings.TrimSpace(scope) {
	case "", "project":
		if t.store == nil {
			return nil, errors.New("memory store is unavailable")
		}
		return t.store, nil
	case "global":
		if t.global == nil {
			return nil, errors.New("global memory is not configured")
		}
		return t.global, nil
	default:
		return nil, fmt.Errorf("scope %q is unknown", scope)
	}
}

func (t *memoryReadTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if err := rejectMemoryRead(tc); err != nil {
		return tool.Result{}, err
	}
	var in struct {
		Key   string `json:"key"`
		Tag   string `json:"tag"`
		Scope string `json:"scope"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	store, err := t.storeFor(in.Scope)
	if err != nil {
		return tool.Result{}, err
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
		entry, ok, err := store.Get(key)
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

	entries, err := store.List(tag)
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
