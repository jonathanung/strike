package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// Progressive task actions (model-facing delegation API).
// Empty / "create" is the default spawn path (prompt-only or advanced fields).
const (
	ProgressiveCreate     = "create"
	ProgressiveGet        = "get"
	ProgressiveList       = "list"
	ProgressiveStatus     = "status"
	ProgressiveRead       = "read"
	ProgressiveMessage    = "message"
	ProgressiveTransition = "transition"
	ProgressiveCancel     = "cancel"
	ProgressiveWait       = "wait"
)

// Compat tool names that forward into the progressive task API.
// Tracked for staged deprecation (counts + slog); tools remain registered.
const (
	CompatToolDelegate      = "delegate"
	CompatToolTaskStatus    = "task_status"
	CompatToolTaskRead      = "task_read"
	CompatToolTaskMessage   = "task_message"
	CompatToolTaskInterrupt = "task_interrupt"
	CompatToolWait          = "wait"
)

// progressiveArgs is the unified argument surface for task (and compat shims).
type progressiveArgs struct {
	Action string `json:"action"`

	// Create / spawn fields (also used when action omitted and prompt set).
	Prompt        string            `json:"prompt"`
	Name          string            `json:"name"`
	Agent         string            `json:"agent"`
	Model         string            `json:"model"`
	Effort        string            `json:"effort"`
	Route         string            `json:"route"`
	Specialty     string            `json:"specialty"`
	Capabilities  []string          `json:"capabilities"`
	MaxCostClass  string            `json:"max_cost_class"`
	Models        []string          `json:"models"`
	MaxConcurrent int               `json:"max_concurrent"`
	Criteria      []string          `json:"criteria"`
	Deps          []string          `json:"deps"`
	Subscribe     []string          `json:"subscribe"`
	Assignee      string            `json:"assignee"`
	Verify        []VerifyGate      `json:"verify"`
	Budget        AgentBudgetLimits `json:"budget"`
	ContextBundle ContextBundle     `json:"context_bundle"`
	// ForceDelegate overrides soft local-prefer policy (#876).
	ForceDelegate bool `json:"force_delegate"`

	// Identity for get/status/read/message/transition/cancel/wait.
	// id accepts delegation id, session id, or name; session_id is an alias.
	ID        string `json:"id"`
	SessionID string `json:"session_id"`

	// status
	IncludeRecent bool `json:"include_recent"`

	// read
	Offset           int   `json:"offset"`
	Limit            int   `json:"limit"`
	Last             int   `json:"last"`
	IncludeTools     *bool `json:"include_tools"`
	IncludeReasoning bool  `json:"include_reasoning"`

	// message
	Text string `json:"text"`

	// transition
	State           string `json:"state"`
	Reason          string `json:"reason"`
	ExpectedVersion int    `json:"expected_version"`

	// wait
	Events         []string `json:"events"`
	TimeoutSeconds float64  `json:"timeout_seconds"`
}

// resolveID returns the primary identity from id or session_id.
func (a progressiveArgs) resolveID() string {
	if id := strings.TrimSpace(a.ID); id != "" {
		return id
	}
	return strings.TrimSpace(a.SessionID)
}

// normalizeProgressiveAction maps omitted action → create (progressive default);
// validates known actions. Empty prompt is rejected later in progressiveCreate.
func normalizeProgressiveAction(action, prompt string) (string, error) {
	_ = prompt // prompt emptiness is validated on the create path
	a := strings.ToLower(strings.TrimSpace(action))
	if a == "" {
		return ProgressiveCreate, nil
	}
	switch a {
	case ProgressiveCreate, ProgressiveGet, ProgressiveList, ProgressiveStatus,
		ProgressiveRead, ProgressiveMessage, ProgressiveTransition,
		ProgressiveCancel, ProgressiveWait:
		return a, nil
	// Aliases
	case "interrupt", "cancel_task":
		return ProgressiveCancel, nil
	case "spawn":
		return ProgressiveCreate, nil
	default:
		return "", fmt.Errorf("action must be create, get, list, status, read, message, transition, cancel, or wait")
	}
}

// --- Deprecation telemetry (staged compatibility) ---

var (
	compatMu     sync.Mutex
	compatCounts = map[string]*atomic.Int64{}
)

// RecordCompatUse increments the deprecation counter for a legacy tool name
// and emits a debug log. Safe for concurrent use; intended for tests + ops.
func RecordCompatUse(toolName string) {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return
	}
	compatMu.Lock()
	c, ok := compatCounts[name]
	if !ok {
		c = &atomic.Int64{}
		compatCounts[name] = c
	}
	compatMu.Unlock()
	c.Add(1)
	slog.Debug("deprecated delegation tool used; prefer progressive task API",
		"tool", name,
		"prefer", "task",
	)
}

// CompatUseCount returns how many times RecordCompatUse was called for name.
func CompatUseCount(toolName string) int64 {
	compatMu.Lock()
	c := compatCounts[strings.TrimSpace(toolName)]
	compatMu.Unlock()
	if c == nil {
		return 0
	}
	return c.Load()
}

// ResetCompatUseCounts clears deprecation counters (tests only).
func ResetCompatUseCounts() {
	compatMu.Lock()
	compatCounts = map[string]*atomic.Int64{}
	compatMu.Unlock()
}

// CompatUseSnapshot returns a copy of all deprecation counters.
func CompatUseSnapshot() map[string]int64 {
	compatMu.Lock()
	defer compatMu.Unlock()
	out := make(map[string]int64, len(compatCounts))
	for k, c := range compatCounts {
		out[k] = c.Load()
	}
	return out
}

// attachCompatMeta merges deprecation metadata into a result when source is a
// legacy tool name (not "task").
func attachCompatMeta(res Result, source string) Result {
	source = strings.TrimSpace(source)
	if source == "" || source == "task" {
		return res
	}
	meta := map[string]any{}
	if len(res.Metadata) > 0 {
		_ = json.Unmarshal(res.Metadata, &meta)
	}
	meta["deprecatedTool"] = source
	meta["prefer"] = "task"
	b, err := json.Marshal(meta)
	if err != nil {
		return res
	}
	res.Metadata = b
	return res
}

// executeProgressive runs one progressive task action. source is "task" or a
// compat tool name (for telemetry). permission is the Ask permission key.
func executeProgressive(ctx context.Context, source, permission string, a progressiveArgs, tc *Context) (Result, error) {
	action, err := normalizeProgressiveAction(a.Action, a.Prompt)
	if err != nil {
		return Result{}, err
	}
	if source != "" && source != "task" {
		RecordCompatUse(source)
	}

	switch action {
	case ProgressiveCreate:
		return progressiveCreate(ctx, source, permission, a, tc)
	case ProgressiveGet, ProgressiveList, ProgressiveTransition:
		return progressiveLifecycle(ctx, source, permission, action, a, tc)
	case ProgressiveStatus:
		return progressiveStatus(ctx, source, permission, a, tc)
	case ProgressiveRead:
		return progressiveRead(ctx, source, permission, a, tc)
	case ProgressiveMessage:
		return progressiveMessage(ctx, source, permission, a, tc)
	case ProgressiveCancel:
		return progressiveCancel(ctx, source, permission, a, tc)
	case ProgressiveWait:
		return progressiveWait(ctx, source, permission, a, tc)
	default:
		return Result{}, fmt.Errorf("action must be create, get, list, status, read, message, transition, cancel, or wait")
	}
}

func progressiveCreate(ctx context.Context, source, permission string, a progressiveArgs, tc *Context) (Result, error) {
	if strings.TrimSpace(a.Prompt) == "" {
		return Result{}, fmt.Errorf("prompt is empty")
	}
	gates, err := normalizeTaskVerify(a.Verify)
	if err != nil {
		return Result{}, err
	}
	bundle, err := NormalizeContextBundle(a.ContextBundle)
	if err != nil {
		return Result{}, err
	}
	perm := permission
	if perm == "" {
		perm = "task"
	}
	if err := tc.Ask(ctx, AskRequest{Permission: perm, Patterns: []string{"create", "*"}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.SpawnTask == nil {
		return Result{}, fmt.Errorf("task is not available")
	}
	res, err := tc.SpawnTask(ctx, TaskRequest{
		Prompt:        a.Prompt,
		Name:          a.Name,
		Agent:         a.Agent,
		Model:         a.Model,
		Effort:        a.Effort,
		Route:         a.Route,
		Specialty:     a.Specialty,
		Capabilities:  a.Capabilities,
		MaxCostClass:  a.MaxCostClass,
		Models:        a.Models,
		MaxConcurrent: a.MaxConcurrent,
		Criteria:      a.Criteria,
		Deps:          a.Deps,
		Subscribe:     a.Subscribe,
		Assignee:      a.Assignee,
		Verify:        gates,
		Budget:        a.Budget,
		ContextBundle: bundle,
		ForceDelegate: a.ForceDelegate,
	})
	if err != nil {
		return Result{}, err
	}
	out := res.Output
	title := "task"
	if n := strings.TrimSpace(res.Name); n != "" {
		title = "task " + n
	} else if res.DelegationID != "" {
		title = "task " + res.DelegationID
	} else if res.SessionID != "" {
		title = "task " + shortID(res.SessionID)
	}
	if res.Lifecycle != "" {
		title += " " + res.Lifecycle
	} else if res.Status == "local" {
		title += " local"
	}
	meta := taskMetadata(res)
	var result Result
	switch res.Status {
	case "started", "completed", "queued", "local":
		result = Result{Title: title, Output: out, Metadata: meta}
	case "failed", "canceled":
		if out == "" {
			out = "task " + res.Status
		}
		result = Result{Title: title, Output: out, Metadata: meta}
		return attachCompatMeta(result, source), fmt.Errorf("%s", out)
	default:
		if out == "" {
			out = "task failed"
		}
		result = Result{Title: title, Output: out, Metadata: meta}
		return attachCompatMeta(result, source), fmt.Errorf("%s", out)
	}
	return attachCompatMeta(result, source), nil
}

func progressiveLifecycle(ctx context.Context, source, permission, action string, a progressiveArgs, tc *Context) (Result, error) {
	id := a.resolveID()
	gates, err := normalizeTaskVerify(a.Verify)
	if err != nil {
		return Result{}, err
	}
	bundle, err := NormalizeContextBundle(a.ContextBundle)
	if err != nil {
		return Result{}, err
	}
	req := DelegateRequest{
		Action:          action,
		ID:              id,
		Prompt:          a.Prompt,
		Name:            a.Name,
		Agent:           a.Agent,
		Model:           a.Model,
		Effort:          a.Effort,
		Route:           a.Route,
		Specialty:       a.Specialty,
		Capabilities:    a.Capabilities,
		MaxCostClass:    a.MaxCostClass,
		Models:          a.Models,
		MaxConcurrent:   a.MaxConcurrent,
		Assignee:        a.Assignee,
		Criteria:        a.Criteria,
		Deps:            a.Deps,
		Subscribe:       a.Subscribe,
		Verify:          gates,
		Budget:          a.Budget,
		ContextBundle:   bundle,
		ForceDelegate:   a.ForceDelegate,
		State:           strings.TrimSpace(a.State),
		Reason:          a.Reason,
		ExpectedVersion: a.ExpectedVersion,
	}
	switch action {
	case ProgressiveList:
		// no id
	case ProgressiveGet, ProgressiveTransition:
		if req.ID == "" {
			return Result{}, fmt.Errorf("id is required for %s", action)
		}
		if action == ProgressiveTransition && req.State == "" {
			return Result{}, fmt.Errorf("state is required for transition")
		}
	}

	perm := permission
	if perm == "" {
		perm = "task"
	}
	// Lifecycle ops historically used permission "delegate". Progressive task
	// uses "task"; compat delegate tool passes permission "delegate".
	pat := action
	if req.ID != "" {
		pat = req.ID
	}
	if err := tc.Ask(ctx, AskRequest{Permission: perm, Patterns: []string{pat}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.Delegate == nil {
		return Result{}, fmt.Errorf("lifecycle ops are not available")
	}
	res, err := tc.Delegate(ctx, req)
	if err != nil {
		return Result{}, err
	}
	out, err := json.Marshal(res)
	if err != nil {
		return Result{}, err
	}
	prefix := "task"
	if source != "" && source != "task" {
		prefix = source
	}
	title := prefix + " " + action
	if res.Conflict {
		title += " conflict"
	} else if res.Item != nil && res.Item.ID != "" {
		title += " " + res.Item.ID
		if res.Item.State != "" {
			title += " " + res.Item.State
		}
	} else if action == ProgressiveList {
		title = fmt.Sprintf("%s list %d", prefix, len(res.Items))
	}
	return attachCompatMeta(Result{Title: title, Output: string(out)}, source), nil
}

func progressiveStatus(ctx context.Context, source, permission string, a progressiveArgs, tc *Context) (Result, error) {
	id := a.resolveID()
	if id == "" {
		return Result{}, fmt.Errorf("id or session_id is required")
	}
	perm := permission
	if perm == "" {
		perm = "task"
	}
	if err := tc.Ask(ctx, AskRequest{Permission: perm, Patterns: []string{id}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TaskStatus == nil {
		return Result{}, fmt.Errorf("task status is not available")
	}
	res, err := tc.TaskStatus(ctx, TaskStatusRequest{SessionID: id, IncludeRecent: a.IncludeRecent})
	if err != nil {
		return Result{}, err
	}
	payload := map[string]any{
		"session_id":       res.SessionID,
		"state":            res.State,
		"elapsed":          res.Elapsed,
		"current_tool":     res.CurrentTool,
		"latest_activity":  res.LatestActivity,
		"terminal_summary": nullIfEmpty(res.TerminalSummary),
	}
	if res.HasHandoff {
		payload["handoff"] = res.Handoff
	}
	if res.HasVerification {
		payload["verification"] = res.Verification
	}
	if len(res.QueuePools) > 0 {
		payload["queue_pools"] = res.QueuePools
	}
	if res.QueueLabel != "" {
		payload["queue_label"] = res.QueueLabel
	}
	if res.DelegationID != "" {
		payload["delegation_id"] = res.DelegationID
	}
	if res.Lifecycle != "" {
		payload["lifecycle"] = res.Lifecycle
	}
	if len(res.Criteria) > 0 {
		payload["criteria"] = res.Criteria
	}
	if len(res.Deps) > 0 {
		payload["deps"] = res.Deps
	}
	if res.Version > 0 {
		payload["version"] = res.Version
	}
	if res.BlockReason != "" {
		payload["block_reason"] = res.BlockReason
	}
	if res.Objective != "" {
		payload["objective"] = res.Objective
	}
	if res.LastAction != "" {
		payload["last_action"] = res.LastAction
	}
	if len(res.FilesTouched) > 0 {
		payload["files_touched"] = res.FilesTouched
	}
	if res.HasBudget {
		payload["budget"] = res.Budget
	}
	out, _ := json.Marshal(payload)
	title := "task status " + shortID(id) + " " + res.State
	if source != "" && source != "task" {
		title = source + " " + shortID(id) + " " + res.State
	}
	if res.QueueLabel != "" {
		title += " queued:" + res.QueueLabel
	}
	return attachCompatMeta(Result{Title: title, Output: string(out)}, source), nil
}

func progressiveRead(ctx context.Context, source, permission string, a progressiveArgs, tc *Context) (Result, error) {
	id := a.resolveID()
	if id == "" {
		return Result{}, fmt.Errorf("id or session_id is required")
	}
	perm := permission
	if perm == "" {
		perm = "task"
	}
	if err := tc.Ask(ctx, AskRequest{Permission: perm, Patterns: []string{id}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TaskRead == nil {
		return Result{}, fmt.Errorf("task read is not available")
	}
	includeTools := true
	if a.IncludeTools != nil {
		includeTools = *a.IncludeTools
	}
	res, err := tc.TaskRead(ctx, TaskReadRequest{
		SessionID:        id,
		Offset:           a.Offset,
		Limit:            a.Limit,
		Last:             a.Last,
		IncludeTools:     includeTools,
		IncludeReasoning: a.IncludeReasoning,
	})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(res)
	title := fmt.Sprintf("task read %s %d/%d", shortID(id), len(res.Entries), res.Total)
	if source != "" && source != "task" {
		title = fmt.Sprintf("%s %s %d/%d", source, shortID(id), len(res.Entries), res.Total)
	}
	return attachCompatMeta(Result{Title: title, Output: string(out)}, source), nil
}

func progressiveMessage(ctx context.Context, source, permission string, a progressiveArgs, tc *Context) (Result, error) {
	id := a.resolveID()
	text := strings.TrimSpace(a.Text)
	if id == "" {
		return Result{}, fmt.Errorf("id or session_id is required")
	}
	if text == "" {
		return Result{}, fmt.Errorf("text is required")
	}
	perm := permission
	if perm == "" {
		perm = "task"
	}
	if err := tc.Ask(ctx, AskRequest{Permission: perm, Patterns: []string{id}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TaskMessage == nil {
		return Result{}, fmt.Errorf("task message is not available")
	}
	res, err := tc.TaskMessage(ctx, TaskMessageRequest{SessionID: id, Text: text})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(map[string]any{
		"session_id": res.SessionID,
		"status":     res.Status,
		"state":      res.State,
		"detail":     res.Detail,
	})
	title := "task message " + shortID(id) + " " + res.Status
	if source != "" && source != "task" {
		title = source + " " + shortID(id) + " " + res.Status
	}
	result := Result{Title: title, Output: string(out)}
	if res.Status == "rejected" {
		return attachCompatMeta(result, source), fmt.Errorf("%s", res.Detail)
	}
	return attachCompatMeta(result, source), nil
}

func progressiveCancel(ctx context.Context, source, permission string, a progressiveArgs, tc *Context) (Result, error) {
	id := a.resolveID()
	if id == "" {
		return Result{}, fmt.Errorf("id or session_id is required")
	}
	perm := permission
	if perm == "" {
		perm = "task"
	}
	if err := tc.Ask(ctx, AskRequest{Permission: perm, Patterns: []string{id}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TaskInterrupt == nil {
		return Result{}, fmt.Errorf("task cancel is not available")
	}
	res, err := tc.TaskInterrupt(ctx, TaskInterruptRequest{SessionID: id})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(map[string]any{
		"session_id": res.SessionID,
		"state":      res.State,
		"detail":     res.Detail,
	})
	title := "task cancel " + shortID(id) + " " + res.State
	if source != "" && source != "task" {
		title = source + " " + shortID(id) + " " + res.State
	}
	return attachCompatMeta(Result{Title: title, Output: string(out)}, source), nil
}

func progressiveWait(ctx context.Context, source, permission string, a progressiveArgs, tc *Context) (Result, error) {
	if a.TimeoutSeconds <= 0 || a.TimeoutSeconds > waitMaxSeconds {
		return Result{}, fmt.Errorf("timeout_seconds must be in (0, 300]")
	}
	canonical, err := NormalizeWaitEvents(a.Events)
	if err != nil {
		return Result{}, err
	}
	id := a.resolveID()
	pat := id
	if pat == "" {
		pat = "*"
	}
	perm := permission
	if perm == "" {
		perm = "task"
	}
	if err := tc.Ask(ctx, AskRequest{Permission: perm, Patterns: []string{pat}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.Wait == nil {
		return Result{}, fmt.Errorf("wait is not available")
	}
	res, err := tc.Wait(ctx, WaitRequest{
		Events:         canonical,
		SessionID:      id,
		TimeoutSeconds: a.TimeoutSeconds,
	})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(res)
	title := "task wait " + res.Outcome
	if source != "" && source != "task" {
		title = "wait " + res.Outcome
	}
	if res.Event != "" {
		title += " " + res.Event
	}
	if res.SessionID != "" {
		title += " " + shortID(res.SessionID)
	}
	return attachCompatMeta(Result{Title: title, Output: string(out)}, source), nil
}
