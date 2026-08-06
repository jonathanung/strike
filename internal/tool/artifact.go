package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/artifact"
)

// ArtifactStore is the durable typed-artifact surface used by artifact tools.
type ArtifactStore interface {
	Create(in artifact.CreateInput) (artifact.Artifact, error)
	Get(id, actorSession, actorRoot string) (artifact.Artifact, bool, error)
	GetVersion(id string, version int, actorSession, actorRoot string) (artifact.Artifact, bool, error)
	List(actorSession, actorRoot string, filter artifact.ListFilter) ([]artifact.Meta, error)
	Update(id, actorSession, actorRoot string, expectedVersion int, in artifact.UpdateInput) (artifact.Artifact, error)
}

// ArtifactNotify is optional engine callback after create/update (protocol event).
type ArtifactNotify func(op string, a artifact.Artifact)

// artifactView is the tool/JSON projection (id+version for handoff refs).
type artifactView struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Title        string     `json:"title,omitempty"`
	Content      string     `json:"content,omitempty"`
	Version      int        `json:"version"`
	Scope        string     `json:"scope"`
	SessionID    string     `json:"session_id,omitempty"`
	Access       string     `json:"access"`
	OwnerSession string     `json:"owner_session"`
	OwnerRoot    string     `json:"owner_root"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Conflict     bool       `json:"conflict,omitempty"`
	Error        string     `json:"error,omitempty"`
}

type artifactMetaView struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Title        string     `json:"title,omitempty"`
	Version      int        `json:"version"`
	Scope        string     `json:"scope"`
	SessionID    string     `json:"session_id,omitempty"`
	Access       string     `json:"access"`
	OwnerSession string     `json:"owner_session"`
	OwnerRoot    string     `json:"owner_root"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

func toArtifactView(a artifact.Artifact) artifactView {
	return artifactView{
		ID:           a.ID,
		Type:         a.Type,
		Title:        a.Title,
		Content:      a.Content,
		Version:      a.Version,
		Scope:        a.Scope,
		SessionID:    a.SessionID,
		Access:       a.Access,
		OwnerSession: a.OwnerSession,
		OwnerRoot:    a.OwnerRoot,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
		ExpiresAt:    a.ExpiresAt,
	}
}

func toArtifactMetaView(m artifact.Meta) artifactMetaView {
	return artifactMetaView{
		ID:           m.ID,
		Type:         m.Type,
		Title:        m.Title,
		Version:      m.Version,
		Scope:        m.Scope,
		SessionID:    m.SessionID,
		Access:       m.Access,
		OwnerSession: m.OwnerSession,
		OwnerRoot:    m.OwnerRoot,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
		ExpiresAt:    m.ExpiresAt,
	}
}

func artifactActor(tc *Context) (sessionID, rootID string, err error) {
	if tc == nil {
		return "", "", errors.New("artifact: tool context is required")
	}
	sessionID = strings.TrimSpace(tc.SessionID)
	rootID = strings.TrimSpace(tc.RootSessionID)
	if rootID == "" {
		rootID = sessionID
	}
	if sessionID == "" {
		return "", "", errors.New("artifact: session identity is required")
	}
	return sessionID, rootID, nil
}

func artifactResultJSON(v any, title string) (Result, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return Result{}, err
	}
	meta, _ := json.Marshal(v)
	return Result{Title: title, Output: string(out), Metadata: meta}, nil
}

func artifactSoftError(title, msg string, extra map[string]any) (Result, error) {
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

func artifactConflictResult(store ArtifactStore, id, session, root string, cause error) (Result, error) {
	cur, ok, err := store.Get(id, session, root)
	if err != nil {
		if errors.Is(err, artifact.ErrDenied) {
			return artifactSoftError("artifact conflict", cause.Error(), map[string]any{
				"id":       id,
				"conflict": true,
				"denied":   true,
			})
		}
		return Result{}, err
	}
	if !ok {
		return artifactSoftError("artifact conflict", cause.Error(), map[string]any{
			"id":       id,
			"conflict": true,
		})
	}
	view := toArtifactView(cur)
	view.Conflict = true
	view.Error = cause.Error()
	return artifactResultJSON(view, fmt.Sprintf("artifact %s conflict v%d", shortArtifactID(cur.ID), cur.Version))
}

func shortArtifactID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func notifyArtifact(tc *Context, op string, a artifact.Artifact) {
	if tc != nil && tc.NotifyArtifact != nil {
		tc.NotifyArtifact(op, a)
	}
}

// --- artifact_write ---

type artifactWriteTool struct {
	store ArtifactStore
}

// NewArtifactWrite builds the artifact_write tool. store must be non-nil.
func NewArtifactWrite(store ArtifactStore) Tool {
	return &artifactWriteTool{store: store}
}

func (t *artifactWriteTool) Name() string { return "artifact_write" }

func (t *artifactWriteTool) Contract() Contract {
	return staticContract(SideEffectExternal, IdempotencyConditional)
}

func (t *artifactWriteTool) Description() string {
	return `Create or CAS-update a shared typed artifact (versioned multi-agent work product).

Types: findings, patch, test_report, contract, plan (lightweight blob — use
plan_write for structured multi-section plans with lifecycle).

Artifacts are addressable by id+version. Updates require expected_version and
reject stale writers (conflict=true + current artifact). Access is owner
(creator session only) or team (same root session lineage; default).

Scope:
  - project (default): durable project-wide
  - session: tagged to the creating session; still durable for resume/replay

Actions:
  - create: type + content required; optional title, scope, access, ttl_seconds
  - update: id + expected_version; optional title, content, access, ttl_seconds

Returns JSON with id, type, version for handoff artifact_refs / task messages.
Not a replacement for memory (untyped KV), issues (tracked tickets), or the
structured plan store.`
}

func (t *artifactWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "create | update",
				"enum": ["create", "update"]
			},
			"type": {
				"type": "string",
				"description": "Artifact type on create: findings|patch|test_report|contract|plan"
			},
			"id": {"type": "string", "description": "Artifact id (update)"},
			"expected_version": {
				"type": "integer",
				"description": "CAS version that must match (update)"
			},
			"title": {"type": "string", "description": "Short label"},
			"content": {"type": "string", "description": "Body (text, JSON, patch, …)"},
			"scope": {
				"type": "string",
				"description": "project (default) | session",
				"enum": ["project", "session"]
			},
			"access": {
				"type": "string",
				"description": "team (default) | owner",
				"enum": ["team", "owner"]
			},
			"ttl_seconds": {
				"type": "integer",
				"description": "Optional TTL from now; 0 on update clears expiry"
			}
		},
		"required": ["action"]
	}`)
}

func (t *artifactWriteTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if t.store == nil {
		return Result{}, errors.New("artifact store is unavailable")
	}
	var in struct {
		Action          string  `json:"action"`
		Type            string  `json:"type"`
		ID              string  `json:"id"`
		ExpectedVersion *int    `json:"expected_version"`
		Title           *string `json:"title"`
		Content         *string `json:"content"`
		Scope           string  `json:"scope"`
		Access          *string `json:"access"`
		TTLSeconds      *int    `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	sessionID, rootID, err := artifactActor(tc)
	if err != nil {
		return Result{}, err
	}

	pattern := action
	if id := strings.TrimSpace(in.ID); id != "" {
		pattern = id
	} else if typ := strings.TrimSpace(in.Type); typ != "" {
		pattern = typ
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "artifact_write",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	accessStr := ""
	if in.Access != nil {
		accessStr = *in.Access
	}
	switch action {
	case "create":
		return t.create(in.Type, in.Title, in.Content, in.Scope, accessStr, in.TTLSeconds, sessionID, rootID, tc)
	case "update":
		return t.update(in.ID, in.ExpectedVersion, in.Title, in.Content, in.Access, in.TTLSeconds, sessionID, rootID, tc)
	default:
		return Result{}, fmt.Errorf("action must be create or update, got %q", in.Action)
	}
}

func (t *artifactWriteTool) create(typ string, title, content *string, scope, access string, ttlSec *int, sessionID, rootID string, tc *Context) (Result, error) {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return Result{}, errors.New("type is required for create")
	}
	if content == nil {
		return Result{}, errors.New("content is required for create")
	}
	var titleStr string
	if title != nil {
		titleStr = *title
	}
	in := artifact.CreateInput{
		Type:         typ,
		Title:        titleStr,
		Content:      *content,
		Scope:        strings.TrimSpace(scope),
		SessionID:    sessionID,
		Access:       strings.TrimSpace(access),
		OwnerSession: sessionID,
		OwnerRoot:    rootID,
	}
	if ttlSec != nil && *ttlSec > 0 {
		in.TTL = time.Duration(*ttlSec) * time.Second
	}
	a, err := t.store.Create(in)
	if err != nil {
		if errors.Is(err, artifact.ErrDenied) {
			return artifactSoftError("artifact denied", err.Error(), nil)
		}
		return Result{}, err
	}
	notifyArtifact(tc, "create", a)
	return artifactResultJSON(toArtifactView(a), fmt.Sprintf("artifact %s %s v%d", a.Type, shortArtifactID(a.ID), a.Version))
}

func (t *artifactWriteTool) update(id string, expected *int, title, content, access *string, ttlSec *int, sessionID, rootID string, tc *Context) (Result, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Result{}, errors.New("id is required for update")
	}
	if expected == nil {
		return Result{}, errors.New("expected_version is required for update")
	}
	if title == nil && content == nil && access == nil && ttlSec == nil {
		return Result{}, errors.New("update requires title, content, access, and/or ttl_seconds")
	}
	up := artifact.UpdateInput{
		Title:   title,
		Content: content,
		Access:  access,
	}
	if ttlSec != nil {
		d := time.Duration(*ttlSec) * time.Second
		up.TTL = &d
	}
	a, err := t.store.Update(id, sessionID, rootID, *expected, up)
	if err != nil {
		if errors.Is(err, artifact.ErrConflict) {
			return artifactConflictResult(t.store, id, sessionID, rootID, err)
		}
		if errors.Is(err, artifact.ErrDenied) || errors.Is(err, artifact.ErrNotFound) || errors.Is(err, artifact.ErrExpired) {
			return artifactSoftError("artifact update failed", err.Error(), map[string]any{"id": id})
		}
		return Result{}, err
	}
	notifyArtifact(tc, "update", a)
	return artifactResultJSON(toArtifactView(a), fmt.Sprintf("artifact %s %s v%d", a.Type, shortArtifactID(a.ID), a.Version))
}

// --- artifact_read ---

type artifactReadTool struct {
	store ArtifactStore
}

// NewArtifactRead builds the artifact_read tool. store must be non-nil.
func NewArtifactRead(store ArtifactStore) Tool {
	return &artifactReadTool{store: store}
}

func (t *artifactReadTool) Name() string { return "artifact_read" }

func (t *artifactReadTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (t *artifactReadTool) Description() string {
	return `Fetch or list shared typed artifacts by id (+ optional version) or filters.

Usage:
  - id: fetch one artifact (optional version for exact CAS pin)
  - omit id: list metadata (optional type, scope, session_id filters)

Returns JSON. Content is included on get; list returns metadata only.
Respects owner/team access — denied ids look like misses to non-owners where applicable.`
}

func (t *artifactReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Fetch a single artifact"},
			"version": {
				"type": "integer",
				"description": "When fetching by id, require this exact version"
			},
			"type": {
				"type": "string",
				"description": "When listing, only this type"
			},
			"scope": {
				"type": "string",
				"description": "When listing: project | session",
				"enum": ["project", "session"]
			},
			"session_id": {
				"type": "string",
				"description": "When listing, only session-scoped artifacts for this session"
			}
		}
	}`)
}

func (t *artifactReadTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if t.store == nil {
		return Result{}, errors.New("artifact store is unavailable")
	}
	var in struct {
		ID        string `json:"id"`
		Version   int    `json:"version"`
		Type      string `json:"type"`
		Scope     string `json:"scope"`
		SessionID string `json:"session_id"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	sessionID, rootID, err := artifactActor(tc)
	if err != nil {
		return Result{}, err
	}

	pattern := "*"
	if id := strings.TrimSpace(in.ID); id != "" {
		pattern = id
	} else if typ := strings.TrimSpace(in.Type); typ != "" {
		pattern = "type:" + typ
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "artifact_read",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	id := strings.TrimSpace(in.ID)
	if id != "" {
		var (
			a  artifact.Artifact
			ok bool
		)
		if in.Version > 0 {
			a, ok, err = t.store.GetVersion(id, in.Version, sessionID, rootID)
		} else {
			a, ok, err = t.store.Get(id, sessionID, rootID)
		}
		if err != nil {
			if errors.Is(err, artifact.ErrDenied) {
				return artifactSoftError("artifact denied", err.Error(), map[string]any{"id": id})
			}
			return Result{}, err
		}
		if !ok {
			msg := fmt.Sprintf("no artifact %q", id)
			if in.Version > 0 {
				msg = fmt.Sprintf("no artifact %q at version %d", id, in.Version)
			}
			return Result{Title: "artifact miss", Output: msg}, nil
		}
		return artifactResultJSON(toArtifactView(a), fmt.Sprintf("artifact %s %s v%d", a.Type, shortArtifactID(a.ID), a.Version))
	}

	list, err := t.store.List(sessionID, rootID, artifact.ListFilter{
		Type:      strings.TrimSpace(in.Type),
		Scope:     strings.TrimSpace(in.Scope),
		SessionID: strings.TrimSpace(in.SessionID),
	})
	if err != nil {
		return Result{}, err
	}
	views := make([]artifactMetaView, 0, len(list))
	for _, m := range list {
		views = append(views, toArtifactMetaView(m))
	}
	title := fmt.Sprintf("%d artifacts", len(views))
	if typ := strings.TrimSpace(in.Type); typ != "" {
		title = fmt.Sprintf("%d artifacts type:%s", len(views), typ)
	}
	return artifactResultJSON(views, title)
}
