package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/internal/persist/knowledge"
)

// KnowledgeStore is the pin surface used by knowledge tools.
type KnowledgeStore interface {
	List() ([]knowledge.Pin, error)
	Put(path, note, author string) (knowledge.Pin, error)
	Delete(path string) error
}

type knowledgeTool struct {
	project KnowledgeStore
	global  KnowledgeStore
}

// NewKnowledge builds the knowledge tool. project must be non-nil.
func NewKnowledge(project, global KnowledgeStore) tool.Tool {
	return &knowledgeTool{project: project, global: global}
}

func (t *knowledgeTool) Name() string { return "knowledge" }

func (t *knowledgeTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectExternal, tool.IdempotencyConditional)
}

func (t *knowledgeTool) Description() string {
	return `Pin or list knowledge paths for this project or the global set.

Pins are paths (and a short note), not copies of the file. They are read
into the prompt as a path list at session start. scope is "project"
(default) or "global". Pass delete=true to unpin. Incognito sessions can
read pins but cannot change them. Temporary sessions have no knowledge.`
}

func (t *knowledgeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Path to pin or unpin. Omit to list."},
			"note": {"type": "string", "description": "Why this path is pinned"},
			"delete": {"type": "boolean", "description": "Unpin path"},
			"scope": {"type": "string", "description": "project (default) or global"}
		}
	}`)
}

func (t *knowledgeTool) storeFor(scope string) (KnowledgeStore, error) {
	switch strings.TrimSpace(scope) {
	case "", "project":
		if t.project == nil {
			return nil, errors.New("knowledge store is unavailable")
		}
		return t.project, nil
	case "global":
		if t.global == nil {
			return nil, errors.New("global knowledge is not configured")
		}
		return t.global, nil
	default:
		return nil, fmt.Errorf("scope %q is unknown", scope)
	}
}

func (t *knowledgeTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var in struct {
		Path   string `json:"path"`
		Note   string `json:"note"`
		Delete bool   `json:"delete"`
		Scope  string `json:"scope"`
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
	writing := strings.TrimSpace(in.Path) != ""
	if writing {
		if err := rejectMemoryWrite(tc); err != nil {
			return tool.Result{}, err
		}
	} else if err := rejectMemoryRead(tc); err != nil {
		return tool.Result{}, err
	}
	perm := "knowledge_read"
	if writing {
		perm = "knowledge_write"
	}
	pattern := "*"
	if strings.TrimSpace(in.Path) != "" {
		pattern = strings.TrimSpace(in.Path)
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: perm,
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}
	if !writing {
		pins, err := store.List()
		if err != nil {
			return tool.Result{}, err
		}
		if pins == nil {
			pins = []knowledge.Pin{}
		}
		out, err := json.MarshalIndent(pins, "", "  ")
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Title: fmt.Sprintf("%d pins", len(pins)), Output: string(out)}, nil
	}
	if in.Delete {
		if err := store.Delete(in.Path); err != nil {
			if errors.Is(err, knowledge.ErrNotFound) {
				return tool.Result{Title: "knowledge miss", Output: fmt.Sprintf("no pin for %q", in.Path)}, nil
			}
			return tool.Result{}, err
		}
		return tool.Result{Title: "knowledge unpinned", Output: fmt.Sprintf("unpinned %q", in.Path)}, nil
	}
	author := ""
	if tc != nil {
		author = strings.TrimSpace(tc.SessionID)
	}
	pin, err := store.Put(in.Path, in.Note, author)
	if err != nil {
		return tool.Result{}, err
	}
	out, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Title: "knowledge pinned", Output: string(out)}, nil
}
