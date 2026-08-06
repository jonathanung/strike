package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/ledger"
)

// LedgerStore is the durable decision-ledger surface used by ledger tools.
type LedgerStore interface {
	Append(in ledger.AppendInput) (ledger.Entry, error)
	Get(id string) (ledger.Entry, bool, error)
	List(filter ledger.ListFilter) ([]ledger.Entry, error)
	ActiveSlice(path, taskID string) ([]ledger.Entry, error)
	Invalidate(id string, in ledger.InvalidateInput) (ledger.Entry, error)
	Supersede(priorID string, in ledger.AppendInput) (ledger.Entry, error)
}

// LedgerNotify is optional engine callback after append/invalidate/supersede.
// op is "append" | "invalidate" | "supersede".
type LedgerNotify func(op string, e ledger.Entry)

type ledgerView struct {
	ID                 string     `json:"id"`
	Kind               string     `json:"kind"`
	Statement          string     `json:"statement"`
	Confidence         string     `json:"confidence,omitempty"`
	EvidenceRefs       []string   `json:"evidence_refs,omitempty"`
	Status             string     `json:"status"`
	ScopePaths         []string   `json:"scope_paths,omitempty"`
	ScopeTaskIDs       []string   `json:"scope_task_ids,omitempty"`
	AuthorSession      string     `json:"author_session"`
	AuthorAgent        string     `json:"author_agent,omitempty"`
	AuthorRoot         string     `json:"author_root,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	InvalidateReason   string     `json:"invalidate_reason,omitempty"`
	InvalidateEvidence []string   `json:"invalidate_evidence,omitempty"`
	InvalidatedAt      *time.Time `json:"invalidated_at,omitempty"`
	SupersededBy       string     `json:"superseded_by,omitempty"`
	Supersedes         string     `json:"supersedes,omitempty"`
	Error              string     `json:"error,omitempty"`
}

func toLedgerView(e ledger.Entry) ledgerView {
	return ledgerView{
		ID:                 e.ID,
		Kind:               e.Kind,
		Statement:          e.Statement,
		Confidence:         e.Confidence,
		EvidenceRefs:       e.EvidenceRefs,
		Status:             e.Status,
		ScopePaths:         e.ScopePaths,
		ScopeTaskIDs:       e.ScopeTaskIDs,
		AuthorSession:      e.AuthorSession,
		AuthorAgent:        e.AuthorAgent,
		AuthorRoot:         e.AuthorRoot,
		CreatedAt:          e.CreatedAt,
		UpdatedAt:          e.UpdatedAt,
		InvalidateReason:   e.InvalidateReason,
		InvalidateEvidence: e.InvalidateEvidence,
		InvalidatedAt:      e.InvalidatedAt,
		SupersededBy:       e.SupersededBy,
		Supersedes:         e.Supersedes,
	}
}

func ledgerActor(tc *Context) (sessionID, rootID, agent string, err error) {
	if tc == nil {
		return "", "", "", errors.New("ledger: tool context is required")
	}
	sessionID = strings.TrimSpace(tc.SessionID)
	rootID = strings.TrimSpace(tc.RootSessionID)
	if rootID == "" {
		rootID = sessionID
	}
	if sessionID == "" {
		return "", "", "", errors.New("ledger: session identity is required")
	}
	agent = strings.TrimSpace(tc.MemberName)
	return sessionID, rootID, agent, nil
}

func ledgerResultJSON(v any, title string) (Result, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return Result{}, err
	}
	meta, _ := json.Marshal(v)
	return Result{Title: title, Output: string(out), Metadata: meta}, nil
}

func ledgerSoftError(title, msg string, extra map[string]any) (Result, error) {
	payload := map[string]any{"error": msg}
	for k, v := range extra {
		payload[k] = v
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return Result{}, err
	}
	meta, _ := json.Marshal(payload)
	return Result{Title: title, Output: string(out), Metadata: meta}, nil
}

func notifyLedger(tc *Context, op string, e ledger.Entry) {
	if tc != nil && tc.NotifyLedger != nil {
		tc.NotifyLedger(op, e)
	}
}

func shortLedgerID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// --- ledger_write ---

type ledgerWriteTool struct {
	store LedgerStore
}

// NewLedgerWrite builds the ledger_write tool. store must be non-nil.
func NewLedgerWrite(store LedgerStore) Tool {
	return &ledgerWriteTool{store: store}
}

func (t *ledgerWriteTool) Name() string { return "ledger_write" }

func (t *ledgerWriteTool) Contract() Contract {
	return staticContract(SideEffectExternal, IdempotencyConditional)
}

func (t *ledgerWriteTool) Description() string {
	return `Append, invalidate, or supersede entries in the shared decision/assumption ledger.

Prefer this over burying critical decisions, assumptions, or constraints only in
chat prose. Multi-agent leads audit the trail; when evidence contradicts an
active assumption, invalidate (or supersede) it so dependents can react.

Kinds: decision | assumption | constraint
Confidence: low | medium (default) | high
Status lifecycle: active → invalidated | superseded (history preserved; never deleted)

Actions:
  - append: kind + statement required; optional confidence, evidence_refs,
    scope_paths, scope_task_ids, supersedes (id of prior active entry to replace)
  - invalidate: id + reason required; optional evidence (contradicting refs)
  - supersede: id (prior) + kind + statement — marks prior superseded and appends
    the replacement in one step

Emits ledger.updated on the session event stream (op=append|invalidate|supersede)
so wait/subscribe consumers can observe invalidation.

Not a replacement for memory (untyped KV), artifacts (work products), issues, or plans.`
}

func (t *ledgerWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "append | invalidate | supersede",
				"enum": ["append", "invalidate", "supersede"]
			},
			"kind": {
				"type": "string",
				"description": "decision | assumption | constraint (append/supersede)",
				"enum": ["decision", "assumption", "constraint"]
			},
			"statement": {"type": "string", "description": "The decision/assumption/constraint text"},
			"confidence": {
				"type": "string",
				"description": "low | medium | high (default medium)",
				"enum": ["low", "medium", "high"]
			},
			"evidence_refs": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Artifact ids, file revs, message ids, URLs, …"
			},
			"scope_paths": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional path scopes (empty = global)"
			},
			"scope_task_ids": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional task/delegation id scopes"
			},
			"id": {"type": "string", "description": "Entry id (invalidate/supersede; also supersedes on append)"},
			"supersedes": {"type": "string", "description": "Prior active id to mark superseded (append)"},
			"reason": {"type": "string", "description": "Why invalidated (invalidate)"},
			"evidence": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Contradicting evidence refs (invalidate)"
			}
		},
		"required": ["action"]
	}`)
}

func (t *ledgerWriteTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if t.store == nil {
		return Result{}, errors.New("ledger store is unavailable")
	}
	var in struct {
		Action       string   `json:"action"`
		Kind         string   `json:"kind"`
		Statement    string   `json:"statement"`
		Confidence   string   `json:"confidence"`
		EvidenceRefs []string `json:"evidence_refs"`
		ScopePaths   []string `json:"scope_paths"`
		ScopeTaskIDs []string `json:"scope_task_ids"`
		ID           string   `json:"id"`
		Supersedes   string   `json:"supersedes"`
		Reason       string   `json:"reason"`
		Evidence     []string `json:"evidence"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	sessionID, rootID, agent, err := ledgerActor(tc)
	if err != nil {
		return Result{}, err
	}

	pattern := action
	if id := strings.TrimSpace(in.ID); id != "" {
		pattern = id
	} else if k := strings.TrimSpace(in.Kind); k != "" {
		pattern = k
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "ledger_write",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	switch action {
	case "append":
		return t.append(in.Kind, in.Statement, in.Confidence, in.EvidenceRefs, in.ScopePaths, in.ScopeTaskIDs, in.Supersedes, in.ID, sessionID, rootID, agent, tc)
	case "invalidate":
		return t.invalidate(in.ID, in.Reason, in.Evidence, tc)
	case "supersede":
		return t.supersede(in.ID, in.Kind, in.Statement, in.Confidence, in.EvidenceRefs, in.ScopePaths, in.ScopeTaskIDs, sessionID, rootID, agent, tc)
	default:
		return Result{}, fmt.Errorf("action must be append, invalidate, or supersede, got %q", in.Action)
	}
}

func (t *ledgerWriteTool) append(kind, statement, confidence string, evidence, paths, tasks []string, supersedes, idField, sessionID, rootID, agent string, tc *Context) (Result, error) {
	sup := strings.TrimSpace(supersedes)
	if sup == "" {
		sup = strings.TrimSpace(idField)
	}
	e, err := t.store.Append(ledger.AppendInput{
		Kind:          kind,
		Statement:     statement,
		Confidence:    confidence,
		EvidenceRefs:  evidence,
		ScopePaths:    paths,
		ScopeTaskIDs:  tasks,
		AuthorSession: sessionID,
		AuthorAgent:   agent,
		AuthorRoot:    rootID,
		Supersedes:    sup,
	})
	if err != nil {
		if errors.Is(err, ledger.ErrNotFound) {
			return ledgerSoftError("ledger append failed", err.Error(), map[string]any{"supersedes": sup})
		}
		return Result{}, err
	}
	op := "append"
	if e.Supersedes != "" {
		op = "supersede"
	}
	notifyLedger(tc, op, e)
	return ledgerResultJSON(toLedgerView(e), fmt.Sprintf("ledger %s %s %s", e.Kind, shortLedgerID(e.ID), e.Status))
}

func (t *ledgerWriteTool) invalidate(id, reason string, evidence []string, tc *Context) (Result, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Result{}, errors.New("id is required for invalidate")
	}
	e, err := t.store.Invalidate(id, ledger.InvalidateInput{
		Reason:   reason,
		Evidence: evidence,
	})
	if err != nil {
		if errors.Is(err, ledger.ErrNotFound) {
			return ledgerSoftError("ledger invalidate failed", err.Error(), map[string]any{"id": id})
		}
		return Result{}, err
	}
	notifyLedger(tc, "invalidate", e)
	return ledgerResultJSON(toLedgerView(e), fmt.Sprintf("ledger invalidated %s", shortLedgerID(e.ID)))
}

func (t *ledgerWriteTool) supersede(id, kind, statement, confidence string, evidence, paths, tasks []string, sessionID, rootID, agent string, tc *Context) (Result, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Result{}, errors.New("id is required for supersede")
	}
	e, err := t.store.Supersede(id, ledger.AppendInput{
		Kind:          kind,
		Statement:     statement,
		Confidence:    confidence,
		EvidenceRefs:  evidence,
		ScopePaths:    paths,
		ScopeTaskIDs:  tasks,
		AuthorSession: sessionID,
		AuthorAgent:   agent,
		AuthorRoot:    rootID,
	})
	if err != nil {
		if errors.Is(err, ledger.ErrNotFound) {
			return ledgerSoftError("ledger supersede failed", err.Error(), map[string]any{"id": id})
		}
		return Result{}, err
	}
	notifyLedger(tc, "supersede", e)
	return ledgerResultJSON(toLedgerView(e), fmt.Sprintf("ledger supersede %s → %s", shortLedgerID(id), shortLedgerID(e.ID)))
}

// --- ledger_read ---

type ledgerReadTool struct {
	store LedgerStore
}

// NewLedgerRead builds the ledger_read tool. store must be non-nil.
func NewLedgerRead(store LedgerStore) Tool {
	return &ledgerReadTool{store: store}
}

func (t *ledgerReadTool) Name() string { return "ledger_read" }

func (t *ledgerReadTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (t *ledgerReadTool) Description() string {
	return `Fetch or list decision/assumption ledger entries.

Usage:
  - id: fetch one entry (full history fields including invalidate meta)
  - omit id: list (optional status, kind, path, task_id, author_session)
  - active_only=true (default when listing without status): only active entries
  - path / task_id: scope filter for context bundles (global entries always match)

Returns JSON. Prefer ledger_write for durable choices; use this to audit or
pull the active slice for a path/task before acting on assumptions.`
}

func (t *ledgerReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Fetch a single entry"},
			"status": {
				"type": "string",
				"description": "When listing: active | invalidated | superseded",
				"enum": ["active", "invalidated", "superseded"]
			},
			"kind": {
				"type": "string",
				"description": "When listing: decision | assumption | constraint",
				"enum": ["decision", "assumption", "constraint"]
			},
			"path": {"type": "string", "description": "Scope filter: path prefix match + globals"},
			"task_id": {"type": "string", "description": "Scope filter: task/delegation id + globals"},
			"author_session": {"type": "string", "description": "When listing, only this author session"},
			"active_only": {
				"type": "boolean",
				"description": "When listing without status, default true — set false for full history"
			}
		}
	}`)
}

func (t *ledgerReadTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if t.store == nil {
		return Result{}, errors.New("ledger store is unavailable")
	}
	var in struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		Kind          string `json:"kind"`
		Path          string `json:"path"`
		TaskID        string `json:"task_id"`
		AuthorSession string `json:"author_session"`
		ActiveOnly    *bool  `json:"active_only"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if _, _, _, err := ledgerActor(tc); err != nil {
		return Result{}, err
	}

	pattern := "*"
	if id := strings.TrimSpace(in.ID); id != "" {
		pattern = id
	} else if k := strings.TrimSpace(in.Kind); k != "" {
		pattern = "kind:" + k
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "ledger_read",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	id := strings.TrimSpace(in.ID)
	if id != "" {
		e, ok, err := t.store.Get(id)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{Title: "ledger miss", Output: fmt.Sprintf("no ledger entry %q", id)}, nil
		}
		return ledgerResultJSON(toLedgerView(e), fmt.Sprintf("ledger %s %s %s", e.Kind, shortLedgerID(e.ID), e.Status))
	}

	status := strings.TrimSpace(in.Status)
	if status == "" {
		activeOnly := true
		if in.ActiveOnly != nil {
			activeOnly = *in.ActiveOnly
		}
		if activeOnly {
			status = ledger.StatusActive
		}
	}
	list, err := t.store.List(ledger.ListFilter{
		Status:        status,
		Kind:          strings.TrimSpace(in.Kind),
		Path:          strings.TrimSpace(in.Path),
		TaskID:        strings.TrimSpace(in.TaskID),
		AuthorSession: strings.TrimSpace(in.AuthorSession),
	})
	if err != nil {
		return Result{}, err
	}
	views := make([]ledgerView, 0, len(list))
	for _, e := range list {
		views = append(views, toLedgerView(e))
	}
	title := fmt.Sprintf("%d ledger entries", len(views))
	if status != "" {
		title = fmt.Sprintf("%d ledger %s", len(views), status)
	}
	return ledgerResultJSON(views, title)
}
