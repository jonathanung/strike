package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/internal/persist/memory"
)

// LessonProposer records pending lesson proposals (*memory.Store).
type LessonProposer interface {
	Propose(p memory.Proposal) (memory.Proposal, error)
}

type lessonProposeTool struct {
	store   LessonProposer
	trusted bool
}

// NewLessonPropose builds lesson_propose (#1874): the agent suggests a
// lesson that waits, pending, until the operator approves it in the
// cockpit Memory panel. Nothing is stored as memory or loaded into a prompt
// by this tool. trusted is the workspace trust decision (#1352); an
// untrusted workspace cannot propose.
func NewLessonPropose(store LessonProposer, trusted bool) tool.Tool {
	return &lessonProposeTool{store: store, trusted: trusted}
}

func (t *lessonProposeTool) Name() string { return "lesson_propose" }

func (t *lessonProposeTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectExternal, tool.IdempotencyConditional)
}

func (t *lessonProposeTool) Description() string {
	return `Propose a durable lesson for the operator to approve.

Use this when the user corrected you in a way future sessions should
remember (a test command, a convention, a preference). The proposal waits,
pending, in the cockpit Memory panel. It is NOT stored as memory and NOT
loaded into any prompt until the operator approves it there; they may also
edit or dismiss it.

Usage notes:
  - key is a short stable identifier (e.g. lesson-test-command).
  - value is the lesson text, written as an instruction for future sessions.
  - scope is "project" (default, this repo) or "global".
  - Text is credential-scrubbed. Never include secrets.
  - Incognito and temporary sessions, and untrusted workspaces, cannot propose.`
}

func (t *lessonProposeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"key": {"type": "string", "description": "Short stable lesson key"},
			"value": {"type": "string", "description": "The lesson text"},
			"scope": {"type": "string", "description": "project (default) or global"}
		},
		"required": ["key", "value"]
	}`)
}

func (t *lessonProposeTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if err := rejectMemoryWrite(tc); err != nil {
		return tool.Result{}, err
	}
	if !t.trusted {
		return tool.Result{}, errors.New("untrusted workspace: lessons cannot be proposed until the workspace is trusted")
	}
	if t.store == nil {
		return tool.Result{}, errors.New("memory store is unavailable")
	}
	var in struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return tool.Result{}, errors.New("key is required")
	}
	if tc != nil && tc.Ask != nil {
		if err := tc.Ask(ctx, tool.AskRequest{
			Permission: "memory_write",
			Patterns:   []string{key},
			Always:     []string{"*"},
		}); err != nil {
			return tool.Result{}, err
		}
	}
	session := ""
	if tc != nil {
		session = tc.SessionID
	}
	p, err := t.store.Propose(memory.Proposal{Key: key, Value: in.Value, Scope: in.Scope, Session: session})
	if err != nil {
		return tool.Result{}, err
	}
	meta, _ := json.Marshal(map[string]any{"id": p.ID, "key": p.Key, "scope": p.Scope, "pending": true})
	return tool.Result{
		Title:    fmt.Sprintf("lesson proposed %s", p.Key),
		Output:   fmt.Sprintf("Proposed lesson %q (%s scope, id %s). It is pending operator approval in the Memory panel and is not stored or loaded until approved.", p.Key, p.Scope, p.ID),
		Metadata: meta,
	}, nil
}
