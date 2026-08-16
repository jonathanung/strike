package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/persist/ledger"
)

// LedgerStore is the durable decision-ledger surface used by ledger tools.
type LedgerStore interface {
	Append(in ledger.AppendInput) (ledger.Entry, error)
	Get(id string) (ledger.Entry, bool, error)
	List(filter ledger.ListFilter) ([]ledger.Entry, error)
	ActiveSlice(path, taskID string) ([]ledger.Entry, error)
	Invalidate(id string, in ledger.InvalidateInput) (ledger.Entry, error)
	Supersede(priorID string, in ledger.AppendInput) (ledger.Entry, error)
	Revalidate(id string, pins []ledger.EvidencePin) (ledger.Entry, error)
}

type ledgerView struct {
	ID                 string               `json:"id"`
	Kind               string               `json:"kind"`
	Statement          string               `json:"statement"`
	Confidence         string               `json:"confidence,omitempty"`
	EvidenceRefs       []string             `json:"evidence_refs,omitempty"`
	EvidencePins       []ledger.EvidencePin `json:"evidence_pins,omitempty"`
	Freshness          string               `json:"freshness,omitempty"`
	StaleReason        string               `json:"stale_reason,omitempty"`
	ChangedEvidence    []string             `json:"changed_evidence,omitempty"`
	Status             string               `json:"status"`
	ScopePaths         []string             `json:"scope_paths,omitempty"`
	ScopeTaskIDs       []string             `json:"scope_task_ids,omitempty"`
	AuthorSession      string               `json:"author_session"`
	AuthorAgent        string               `json:"author_agent,omitempty"`
	AuthorRoot         string               `json:"author_root,omitempty"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
	InvalidateReason   string               `json:"invalidate_reason,omitempty"`
	InvalidateEvidence []string             `json:"invalidate_evidence,omitempty"`
	InvalidatedAt      *time.Time           `json:"invalidated_at,omitempty"`
	SupersededBy       string               `json:"superseded_by,omitempty"`
	Supersedes         string               `json:"supersedes,omitempty"`
	Error              string               `json:"error,omitempty"`
}

func toLedgerView(e ledger.Entry) ledgerView {
	return toLedgerViewFresh(e, "")
}

func toLedgerViewFresh(e ledger.Entry, workDir string) ledgerView {
	fr := ledger.AssessFreshness(e, workDir)
	v := ledgerView{
		ID:                 e.ID,
		Kind:               e.Kind,
		Statement:          e.Statement,
		Confidence:         e.Confidence,
		EvidenceRefs:       e.EvidenceRefs,
		EvidencePins:       e.EvidencePins,
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
	if fr.State != "" && fr.State != ledger.FreshNotApplicable {
		v.Freshness = fr.State
		v.StaleReason = fr.Reason
		v.ChangedEvidence = fr.ChangedEvidence
	}
	return v
}

func ledgerActor(tc *tool.Context) (sessionID, rootID, agent string, err error) {
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

func ledgerResultJSON(v any, title string) (tool.Result, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	meta, _ := json.Marshal(v)
	return tool.Result{Title: title, Output: string(out), Metadata: meta}, nil
}

func ledgerSoftError(title, msg string, extra map[string]any) (tool.Result, error) {
	payload := map[string]any{"error": msg}
	for k, v := range extra {
		payload[k] = v
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	meta, _ := json.Marshal(payload)
	return tool.Result{Title: title, Output: string(out), Metadata: meta}, nil
}

func notifyLedger(tc *tool.Context, op string, e ledger.Entry) {
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
func NewLedgerWrite(store LedgerStore) tool.Tool {
	return &ledgerWriteTool{store: store}
}

func (t *ledgerWriteTool) Name() string { return "ledger_write" }

func (t *ledgerWriteTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectExternal, tool.IdempotencyConditional)
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
    evidence_pins, scope_paths, scope_task_ids, supersedes
  - invalidate: id + reason required; optional evidence (contradicting refs)
  - supersede: id (prior) + kind + statement — marks prior superseded and appends
    the replacement in one step
  - revalidate: id required; optional evidence_pins (default: re-hash existing
    path pins). Status stays active. Use for stale assumptions.

Assumptions with evidence_pins go stale at inject time when path/symbol
evidence changed or is missing. Decisions and constraints are never auto-staled.

Emits ledger.updated on the session event stream (op=append|invalidate|supersede|revalidate)
so wait/subscribe consumers can observe invalidation.

Not a replacement for memory (untyped KV), artifacts (work products), issues, or plans.`
}

func (t *ledgerWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "append | invalidate | supersede | revalidate",
				"enum": ["append", "invalidate", "supersede", "revalidate"]
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
			"evidence_pins": {
				"type": "array",
				"description": "Optional repo pins: path+hash, symbol, or recorded command",
				"items": {
					"type": "object",
					"properties": {
						"kind": {"type": "string", "enum": ["path", "symbol", "command"]},
						"path": {"type": "string"},
						"hash": {"type": "string", "description": "sha256:<hex>; auto-filled for path pins when omitted"},
						"symbol": {"type": "string"},
						"command": {"type": "string"}
					}
				}
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

func (t *ledgerWriteTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if t.store == nil {
		return tool.Result{}, errors.New("ledger store is unavailable")
	}
	var in struct {
		Action       string               `json:"action"`
		Kind         string               `json:"kind"`
		Statement    string               `json:"statement"`
		Confidence   string               `json:"confidence"`
		EvidenceRefs []string             `json:"evidence_refs"`
		EvidencePins []ledger.EvidencePin `json:"evidence_pins"`
		ScopePaths   []string             `json:"scope_paths"`
		ScopeTaskIDs []string             `json:"scope_task_ids"`
		ID           string               `json:"id"`
		Supersedes   string               `json:"supersedes"`
		Reason       string               `json:"reason"`
		Evidence     []string             `json:"evidence"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	sessionID, rootID, agent, err := ledgerActor(tc)
	if err != nil {
		return tool.Result{}, err
	}

	pattern := action
	if id := strings.TrimSpace(in.ID); id != "" {
		pattern = id
	} else if k := strings.TrimSpace(in.Kind); k != "" {
		pattern = k
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "ledger_write",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}

	switch action {
	case "append":
		return t.append(in.Kind, in.Statement, in.Confidence, in.EvidenceRefs, in.EvidencePins, in.ScopePaths, in.ScopeTaskIDs, in.Supersedes, sessionID, rootID, agent, tc)
	case "invalidate":
		return t.invalidate(in.ID, in.Reason, in.Evidence, tc)
	case "supersede":
		return t.supersede(in.ID, in.Kind, in.Statement, in.Confidence, in.EvidenceRefs, in.EvidencePins, in.ScopePaths, in.ScopeTaskIDs, sessionID, rootID, agent, tc)
	case "revalidate":
		return t.revalidate(in.ID, in.EvidencePins, tc)
	default:
		return tool.Result{}, fmt.Errorf("action must be append, invalidate, supersede, or revalidate, got %q", in.Action)
	}
}

func (t *ledgerWriteTool) append(kind, statement, confidence string, evidence []string, pins []ledger.EvidencePin, paths, tasks []string, supersedes, sessionID, rootID, agent string, tc *tool.Context) (tool.Result, error) {
	// Only explicit supersedes on append (id is reserved for invalidate/supersede actions).
	sup := strings.TrimSpace(supersedes)
	workDir := toolWorkDir(tc)
	pins = fillPinHashes(pins, workDir)
	e, err := t.store.Append(ledger.AppendInput{
		Kind:          kind,
		Statement:     statement,
		Confidence:    confidence,
		EvidenceRefs:  evidence,
		EvidencePins:  pins,
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
		return tool.Result{}, err
	}
	op := "append"
	if e.Supersedes != "" {
		op = "supersede"
	}
	notifyLedger(tc, op, e)
	return ledgerResultJSON(toLedgerViewFresh(e, toolWorkDir(tc)), fmt.Sprintf("ledger %s %s %s", e.Kind, shortLedgerID(e.ID), e.Status))
}

func (t *ledgerWriteTool) invalidate(id, reason string, evidence []string, tc *tool.Context) (tool.Result, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return tool.Result{}, errors.New("id is required for invalidate")
	}
	e, err := t.store.Invalidate(id, ledger.InvalidateInput{
		Reason:   reason,
		Evidence: evidence,
	})
	if err != nil {
		if errors.Is(err, ledger.ErrNotFound) {
			return ledgerSoftError("ledger invalidate failed", err.Error(), map[string]any{"id": id})
		}
		return tool.Result{}, err
	}
	notifyLedger(tc, "invalidate", e)
	return ledgerResultJSON(toLedgerViewFresh(e, toolWorkDir(tc)), fmt.Sprintf("ledger invalidated %s", shortLedgerID(e.ID)))
}

func (t *ledgerWriteTool) supersede(id, kind, statement, confidence string, evidence []string, pins []ledger.EvidencePin, paths, tasks []string, sessionID, rootID, agent string, tc *tool.Context) (tool.Result, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return tool.Result{}, errors.New("id is required for supersede")
	}
	workDir := toolWorkDir(tc)
	pins = fillPinHashes(pins, workDir)
	e, err := t.store.Supersede(id, ledger.AppendInput{
		Kind:          kind,
		Statement:     statement,
		Confidence:    confidence,
		EvidenceRefs:  evidence,
		EvidencePins:  pins,
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
		return tool.Result{}, err
	}
	notifyLedger(tc, "supersede", e)
	return ledgerResultJSON(toLedgerViewFresh(e, toolWorkDir(tc)), fmt.Sprintf("ledger supersede %s → %s", shortLedgerID(id), shortLedgerID(e.ID)))
}

func (t *ledgerWriteTool) revalidate(id string, pins []ledger.EvidencePin, tc *tool.Context) (tool.Result, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return tool.Result{}, errors.New("id is required for revalidate")
	}
	workDir := toolWorkDir(tc)
	if len(pins) == 0 {
		cur, ok, err := t.store.Get(id)
		if err != nil {
			return tool.Result{}, err
		}
		if !ok {
			return ledgerSoftError("ledger revalidate failed", ledger.ErrNotFound.Error(), map[string]any{"id": id})
		}
		pins = refreshPinHashes(cur.EvidencePins, workDir)
	} else {
		pins = fillPinHashes(pins, workDir)
	}
	e, err := t.store.Revalidate(id, pins)
	if err != nil {
		if errors.Is(err, ledger.ErrNotFound) {
			return ledgerSoftError("ledger revalidate failed", err.Error(), map[string]any{"id": id})
		}
		return tool.Result{}, err
	}
	notifyLedger(tc, "revalidate", e)
	return ledgerResultJSON(toLedgerViewFresh(e, workDir), fmt.Sprintf("ledger revalidated %s", shortLedgerID(e.ID)))
}

func toolWorkDir(tc *tool.Context) string {
	if tc == nil {
		return ""
	}
	return tc.WorkDir
}

func fillPinHashes(pins []ledger.EvidencePin, workDir string) []ledger.EvidencePin {
	return applyPinHashes(pins, workDir, false)
}

func refreshPinHashes(pins []ledger.EvidencePin, workDir string) []ledger.EvidencePin {
	return applyPinHashes(pins, workDir, true)
}

func applyPinHashes(pins []ledger.EvidencePin, workDir string, overwrite bool) []ledger.EvidencePin {
	if len(pins) == 0 || strings.TrimSpace(workDir) == "" {
		return pins
	}
	out := append([]ledger.EvidencePin(nil), pins...)
	for i, p := range out {
		kind := strings.ToLower(strings.TrimSpace(p.Kind))
		if kind == "" {
			kind = ledger.PinKindPath
		}
		if kind != ledger.PinKindPath && kind != ledger.PinKindSymbol {
			continue
		}
		if strings.TrimSpace(p.Path) == "" {
			continue
		}
		if !overwrite && strings.TrimSpace(p.Hash) != "" {
			continue
		}
		snap, err := ledger.SnapshotPathPin(workDir, p.Path)
		if err != nil {
			continue
		}
		out[i].Hash = snap.Hash
		if strings.TrimSpace(out[i].Kind) == "" {
			out[i].Kind = kind
		}
	}
	return out
}

// --- ledger_read ---

type ledgerReadTool struct {
	store LedgerStore
}

// NewLedgerRead builds the ledger_read tool. store must be non-nil.
func NewLedgerRead(store LedgerStore) tool.Tool {
	return &ledgerReadTool{store: store}
}

func (t *ledgerReadTool) Name() string { return "ledger_read" }

func (t *ledgerReadTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectRead, tool.IdempotencySafeRetry)
}

func (t *ledgerReadTool) Description() string {
	return `Fetch or list decision/assumption ledger entries.

Usage:
  - id: fetch one entry (full history fields including invalidate meta)
  - omit id: list (optional status, kind, path, task_id, author_session)
  - active_only=true (default when listing without status): only active entries
  - path / task_id: scope filter for context bundles (global entries always match)

Returns JSON including computed freshness for assumptions with evidence_pins
(validated | stale | unpinned). Stale means pinned repo evidence changed or is
missing — revalidate, invalidate, or supersede; do not treat as current fact.

Prefer ledger_write for durable choices; use this to audit or pull the active
slice for a path/task before acting on assumptions.`
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

func (t *ledgerReadTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if t.store == nil {
		return tool.Result{}, errors.New("ledger store is unavailable")
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
			return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if _, _, _, err := ledgerActor(tc); err != nil {
		return tool.Result{}, err
	}

	pattern := "*"
	if id := strings.TrimSpace(in.ID); id != "" {
		pattern = id
	} else if k := strings.TrimSpace(in.Kind); k != "" {
		pattern = "kind:" + k
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "ledger_read",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}

	id := strings.TrimSpace(in.ID)
	if id != "" {
		e, ok, err := t.store.Get(id)
		if err != nil {
			return tool.Result{}, err
		}
		if !ok {
			return tool.Result{Title: "ledger miss", Output: fmt.Sprintf("no ledger entry %q", id)}, nil
		}
		return ledgerResultJSON(toLedgerViewFresh(e, toolWorkDir(tc)), fmt.Sprintf("ledger %s %s %s", e.Kind, shortLedgerID(e.ID), e.Status))
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
		return tool.Result{}, err
	}
	views := make([]ledgerView, 0, len(list))
	for _, e := range list {
		views = append(views, toLedgerViewFresh(e, toolWorkDir(tc)))
	}
	title := fmt.Sprintf("%d ledger entries", len(views))
	if status != "" {
		title = fmt.Sprintf("%d ledger %s", len(views), status)
	}
	return ledgerResultJSON(views, title)
}
