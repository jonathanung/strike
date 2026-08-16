package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Idempotency cache bounds (RFC §10.1).
const (
	teamIdempotencyMaxKeys = 512
	teamIdempotencyTTL     = 24 * time.Hour
)

// Permission names for human orchestration Ops (RFC §8).
const (
	permTeamSpawn     = "team.spawn"
	permTeamMessage   = "team.message"
	permTeamInterrupt = "team.interrupt"
	permTeamBoard     = "team.board"
)

type teamIdempotencyEntry struct {
	bodyHash string
	outcome  protocol.TeamOpOutcome
	storedAt time.Time
}

// teamIdempotencyCache is a bounded per-engine LRU-ish map (evict oldest on cap).
type teamIdempotencyCache struct {
	mu      sync.Mutex
	entries map[string]teamIdempotencyEntry
	order   []string // oldest first
}

func newTeamIdempotencyCache() *teamIdempotencyCache {
	return &teamIdempotencyCache{entries: make(map[string]teamIdempotencyEntry, 32)}
}

func (c *teamIdempotencyCache) get(key string) (teamIdempotencyEntry, bool) {
	if c == nil || key == "" {
		return teamIdempotencyEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return teamIdempotencyEntry{}, false
	}
	if time.Since(e.storedAt) > teamIdempotencyTTL {
		delete(c.entries, key)
		c.removeOrderLocked(key)
		return teamIdempotencyEntry{}, false
	}
	return e, true
}

func (c *teamIdempotencyCache) put(key, bodyHash string, outcome protocol.TeamOpOutcome) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = teamIdempotencyEntry{bodyHash: bodyHash, outcome: outcome, storedAt: time.Now()}
	for len(c.order) > teamIdempotencyMaxKeys {
		old := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, old)
	}
}

func (c *teamIdempotencyCache) removeOrderLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func (e *Engine) teamIdemCache() *teamIdempotencyCache {
	if e == nil {
		return nil
	}
	e.teamIdemMu.Lock()
	defer e.teamIdemMu.Unlock()
	if e.teamIdem == nil {
		e.teamIdem = newTeamIdempotencyCache()
	}
	return e.teamIdem
}

func teamOpBodyHash(op protocol.Op) string {
	// Hash canonical JSON of the op without Reply channel.
	var bare any
	switch v := op.(type) {
	case protocol.TeamSpawn:
		v.Reply = nil
		bare = v
	case protocol.TeamMessage:
		v.Reply = nil
		bare = v
	case protocol.TeamBroadcast:
		v.Reply = nil
		bare = v
	case protocol.TeamChildInterrupt:
		v.Reply = nil
		bare = v
	case protocol.TeamTaskTransition:
		v.Reply = nil
		bare = v
	case protocol.TeamBoardCreate:
		v.Reply = nil
		bare = v
	case protocol.TeamBoardClaim:
		v.Reply = nil
		bare = v
	case protocol.TeamBoardComplete:
		v.Reply = nil
		bare = v
	default:
		bare = op
	}
	raw, err := json.Marshal(bare)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func replyTeamOp(op protocol.Op, out protocol.TeamOpOutcome) {
	ch := protocol.TeamControlReply(op)
	if ch == nil {
		return
	}
	select {
	case ch <- out:
	default:
	}
}

func (e *Engine) failTeamOp(op protocol.Op, code, msg string) {
	if msg == "" {
		msg = code
	}
	out := protocol.TeamOpOutcome{OK: false, Code: code, Error: msg}
	// Surface failures on the event stream for fire-and-forget clients.
	e.emit(protocol.EngineError{
		Correlation: e.sessionCorr(),
		Code:        code,
		Message:     msg,
	})
	replyTeamOp(op, out)
}

func (e *Engine) okTeamOp(op protocol.Op, out protocol.TeamOpOutcome) {
	out.OK = true
	if key := strings.TrimSpace(protocol.TeamControlIdempotencyKey(op)); key != "" {
		e.teamIdemCache().put(key, teamOpBodyHash(op), out)
	}
	replyTeamOp(op, out)
}

// beginTeamControl validates common gates and idempotency. On hit, replies and
// returns false (caller must stop). On miss, returns true to proceed.
func (e *Engine) beginTeamControl(op protocol.Op, permName string) bool {
	if e == nil {
		replyTeamOp(op, protocol.TeamOpOutcome{OK: false, Code: protocol.ErrTeamUnavailable, Error: protocol.ErrTeamUnavailable})
		return false
	}
	// Lead-only: nested engines never accept human team control.
	if e.opts.Depth > 0 {
		e.failTeamOp(op, protocol.ErrTeamNotLead, protocol.ErrTeamNotLead)
		return false
	}
	if e.team == nil || e.team.Len() == 0 {
		e.failTeamOp(op, protocol.ErrTeamUnavailable, protocol.ErrTeamUnavailable)
		return false
	}
	// Cross-root: optional rootSessionId must match this engine session.
	if root := strings.TrimSpace(protocol.TeamControlRootSessionID(op)); root != "" {
		if root != e.opts.SessionID {
			e.failTeamOp(op, protocol.ErrTeamCrossRoot, protocol.ErrTeamCrossRoot)
			return false
		}
	}
	key := strings.TrimSpace(protocol.TeamControlIdempotencyKey(op))
	if key == "" {
		e.failTeamOp(op, protocol.ErrTeamValidation, "idempotencyKey is required")
		return false
	}
	hash := teamOpBodyHash(op)
	if prev, ok := e.teamIdemCache().get(key); ok {
		if prev.bodyHash != hash {
			e.failTeamOp(op, protocol.ErrTeamIdempotencyConflict, protocol.ErrTeamIdempotencyConflict)
			return false
		}
		// Replay original success without re-mutating.
		replyTeamOp(op, prev.outcome)
		return false
	}
	if err := e.checkTeamPermission(permName); err != nil {
		e.failTeamOp(op, protocol.ErrTeamPermissionDenied, err.Error())
		return false
	}
	return true
}

// checkTeamPermission enforces managed deny ceilings. Human submission is the
// authorization for ask/allow postures (no interactive ask loop).
func (e *Engine) checkTeamPermission(name string) error {
	if e == nil || e.perms == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	act := e.perms.Peek(name, "*")
	if act == permission.Deny {
		return fmt.Errorf("%s: %s denied by policy", protocol.ErrTeamPermissionDenied, name)
	}
	return nil
}

func (e *Engine) handleTeamSpawn(ctx context.Context, op protocol.TeamSpawn) {
	if !e.beginTeamControl(op, permTeamSpawn) {
		return
	}
	objective := strings.TrimSpace(op.Objective)
	if objective == "" {
		e.failTeamOp(op, protocol.ErrTeamValidation, "objective is required")
		return
	}
	iso := strings.TrimSpace(op.Isolation)
	if iso != "" && iso != "shared" && iso != "worktree" {
		e.failTeamOp(op, protocol.ErrTeamValidation, "isolation must be shared or worktree")
		return
	}
	req := tool.TaskRequest{
		Prompt:        objective,
		Agent:         strings.TrimSpace(op.Agent),
		Name:          strings.TrimSpace(op.Name),
		Isolation:     iso,
		ForceDelegate: true, // human explicitly requested spawn
	}
	if op.Budget != nil {
		if op.Budget.MaxTurns < 0 || op.Budget.MaxToolCalls < 0 || op.Budget.MaxTokens < 0 {
			e.failTeamOp(op, protocol.ErrTeamValidation, "budget fields must be >= 0")
			return
		}
		req.Budget = tool.AgentBudgetLimits{
			MaxToolCalls: op.Budget.MaxToolCalls,
			MaxTokens:    op.Budget.MaxTokens,
		}
		// maxTurns has no native ceiling; fold into tool-call budget when only turns set.
		if op.Budget.MaxTurns > 0 && req.Budget.MaxToolCalls == 0 {
			req.Budget.MaxToolCalls = op.Budget.MaxTurns
		}
	}
	delegID := strings.TrimSpace(op.DelegationID)
	var res tool.TaskResult
	var err error
	if delegID != "" {
		d, ok := e.team.GetDelegation(delegID)
		if !ok {
			e.failTeamOp(op, protocol.ErrTeamValidation, "unknown delegationId")
			return
		}
		res, err = e.spawnChildForDelegation(ctx, d)
	} else {
		res, err = e.spawnChild(ctx, req)
	}
	if err != nil {
		code := protocol.ErrTeamValidation
		msg := err.Error()
		if strings.Contains(msg, "depth limit") || strings.Contains(msg, "denied") {
			code = protocol.ErrTeamPermissionDenied
		}
		if strings.Contains(msg, "dissolved") || strings.Contains(msg, "no team") {
			code = protocol.ErrTeamUnavailable
		}
		e.failTeamOp(op, code, msg)
		return
	}
	if res.Status == "local" {
		// Soft policy should not apply with ForceDelegate; treat as validation.
		e.failTeamOp(op, protocol.ErrTeamValidation, strings.TrimSpace(res.Output))
		return
	}
	e.okTeamOp(op, protocol.TeamOpOutcome{
		ChildSessionID: res.SessionID,
		Name:           res.Name,
		DelegationID:   res.DelegationID,
	})
}

func (e *Engine) handleTeamMessage(ctx context.Context, op protocol.TeamMessage) {
	if !e.beginTeamControl(op, permTeamMessage) {
		return
	}
	res, err := e.agentMessage(ctx, tool.AgentMessageRequest{
		To:      strings.TrimSpace(op.To),
		Body:    op.Body,
		Kind:    strings.TrimSpace(op.Kind),
		Urgency: strings.TrimSpace(op.Urgency),
		TaskID:  strings.TrimSpace(op.TaskID),
	})
	if err != nil {
		code := protocol.ErrTeamValidation
		if strings.Contains(err.Error(), "no team") {
			code = protocol.ErrTeamUnavailable
		}
		e.failTeamOp(op, code, err.Error())
		return
	}
	if res.Status == "rejected" {
		e.failTeamOp(op, protocol.ErrTeamValidation, res.Detail)
		return
	}
	e.okTeamOp(op, protocol.TeamOpOutcome{MessageID: res.MessageID})
}

func (e *Engine) handleTeamBroadcast(ctx context.Context, op protocol.TeamBroadcast) {
	if !e.beginTeamControl(op, permTeamMessage) {
		return
	}
	// Broadcast is lead-only at the tool layer as well.
	if e.team != nil && e.team.LeadID() != e.opts.SessionID {
		e.failTeamOp(op, protocol.ErrTeamNotLead, protocol.ErrTeamNotLead)
		return
	}
	res, err := e.agentBroadcast(ctx, tool.AgentBroadcastRequest{
		Body:    op.Body,
		Urgency: strings.TrimSpace(op.Urgency),
		TaskID:  strings.TrimSpace(op.TaskID),
	})
	if err != nil {
		code := protocol.ErrTeamValidation
		if strings.Contains(err.Error(), "no team") {
			code = protocol.ErrTeamUnavailable
		}
		if strings.Contains(err.Error(), "lead") {
			code = protocol.ErrTeamNotLead
		}
		e.failTeamOp(op, code, err.Error())
		return
	}
	_ = res
	e.okTeamOp(op, protocol.TeamOpOutcome{})
}

func (e *Engine) handleTeamChildInterrupt(ctx context.Context, op protocol.TeamChildInterrupt) {
	if !e.beginTeamControl(op, permTeamInterrupt) {
		return
	}
	childID := strings.TrimSpace(op.ChildSessionID)
	if childID == "" {
		e.failTeamOp(op, protocol.ErrTeamValidation, "childSessionId is required")
		return
	}
	res, err := e.childInterrupt(ctx, tool.TaskInterruptRequest{SessionID: childID})
	if err != nil {
		code := protocol.ErrTeamValidation
		msg := err.Error()
		if strings.Contains(msg, "unknown") || strings.Contains(msg, "inaccessible") {
			code = protocol.ErrTeamValidation
		}
		e.failTeamOp(op, code, msg)
		return
	}
	already := strings.Contains(strings.ToLower(res.Detail), "already finished") ||
		res.State == string(protocol.ChildStatusCompleted) ||
		res.State == string(protocol.ChildStatusFailed) ||
		res.State == string(protocol.ChildStatusCanceled) ||
		res.State == string(protocol.DelegationCanceled) ||
		res.State == string(protocol.DelegationDone) ||
		res.State == string(protocol.DelegationFailed)
	// Idempotent interrupt after terminal → soft success (RFC §7.4).
	if already && strings.Contains(strings.ToLower(res.Detail), "already") {
		e.okTeamOp(op, protocol.TeamOpOutcome{
			ChildSessionID:  res.SessionID,
			AlreadyTerminal: true,
		})
		return
	}
	e.okTeamOp(op, protocol.TeamOpOutcome{
		ChildSessionID:  res.SessionID,
		AlreadyTerminal: already && res.Detail == "child already finished",
	})
}

func (e *Engine) handleTeamTaskTransition(ctx context.Context, op protocol.TeamTaskTransition) {
	if !e.beginTeamControl(op, permTeamBoard) {
		return
	}
	if err := ctx.Err(); err != nil {
		e.failTeamOp(op, protocol.ErrTeamValidation, err.Error())
		return
	}
	id := strings.TrimSpace(op.DelegationID)
	if id == "" {
		e.failTeamOp(op, protocol.ErrTeamValidation, "delegationId is required")
		return
	}
	to, err := normalizeHumanDelegationState(op.ToState)
	if err != nil {
		e.failTeamOp(op, protocol.ErrTeamValidation, err.Error())
		return
	}
	actor := strings.TrimSpace(e.opts.SessionID)
	prev := protocol.DelegationState("")
	if d, ok := e.team.GetDelegation(id); ok {
		prev = d.State
	}
	item, err := e.team.TransitionDelegation(id, actor, to, strings.TrimSpace(op.Reason), op.ExpectedVersion)
	if err != nil {
		var conf *DelegationConflictError
		if errors.As(err, &conf) {
			cur := 0
			if conf != nil && conf.Item.ID != "" {
				cur = conf.Item.Version
			} else if d, ok := e.team.GetDelegation(id); ok {
				cur = d.Version
			}
			out := protocol.TeamOpOutcome{
				OK:             false,
				Code:           protocol.ErrTeamConflict,
				Error:          protocol.ErrTeamConflict,
				CurrentVersion: cur,
				DelegationID:   id,
			}
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Code:        protocol.ErrTeamConflict,
				Message:     protocol.ErrTeamConflict,
			})
			replyTeamOp(op, out)
			return
		}
		var tr *DelegationTransitionError
		if errors.As(err, &tr) {
			e.failTeamOp(op, protocol.ErrTeamValidation, err.Error())
			return
		}
		e.failTeamOp(op, protocol.ErrTeamValidation, err.Error())
		return
	}
	e.emitDelegationChanged(item, prev, strings.TrimSpace(op.Reason))
	e.okTeamOp(op, protocol.TeamOpOutcome{
		DelegationID: item.ID,
		Version:      item.Version,
	})
}

func normalizeHumanDelegationState(raw string) (protocol.DelegationState, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return "", fmt.Errorf("toState is required")
	case "completed": // RFC alias → engine done
		return protocol.DelegationDone, nil
	case "working":
		return protocol.DelegationWorking, nil
	case "blocked":
		return protocol.DelegationBlocked, nil
	case "review":
		return protocol.DelegationReview, nil
	case "done":
		return protocol.DelegationDone, nil
	case "failed":
		return protocol.DelegationFailed, nil
	case "canceled", "cancelled":
		return protocol.DelegationCanceled, nil
	case "queued":
		return protocol.DelegationQueued, nil
	default:
		return "", fmt.Errorf("toState %q is not supported", raw)
	}
}

func (e *Engine) handleTeamBoardCreate(ctx context.Context, op protocol.TeamBoardCreate) {
	if !e.beginTeamControl(op, permTeamBoard) {
		return
	}
	title := strings.TrimSpace(op.Title)
	if title == "" {
		e.failTeamOp(op, protocol.ErrTeamValidation, "title is required")
		return
	}
	content := title
	if body := strings.TrimSpace(op.Body); body != "" {
		content = title + "\n\n" + body
	}
	res, err := e.teamTask(ctx, tool.TeamTaskRequest{
		Action:  "create",
		Content: content,
	})
	if err != nil {
		code := protocol.ErrTeamValidation
		if strings.Contains(err.Error(), "no team") || strings.Contains(err.Error(), "dissolved") {
			code = protocol.ErrTeamUnavailable
		}
		e.failTeamOp(op, code, err.Error())
		return
	}
	taskID := ""
	ver := 0
	if res.Task != nil {
		taskID = res.Task.ID
		ver = res.Task.Version
	}
	// Optional assignee: claim immediately when provided.
	if asg := strings.TrimSpace(op.Assignee); asg != "" && taskID != "" {
		// Human board create assignee is best-effort claim as lead when assignee is self/lead.
		if asg == e.opts.SessionID || asg == "lead" || asg == e.team.LeadID() {
			claimed, cerr := e.teamTask(ctx, tool.TeamTaskRequest{
				Action:          "claim",
				ID:              taskID,
				ExpectedVersion: ver,
			})
			if cerr == nil && !claimed.Conflict && claimed.Task != nil {
				taskID = claimed.Task.ID
				ver = claimed.Task.Version
			}
		}
	}
	e.okTeamOp(op, protocol.TeamOpOutcome{TaskID: taskID, Version: ver})
}

func (e *Engine) handleTeamBoardClaim(ctx context.Context, op protocol.TeamBoardClaim) {
	if !e.beginTeamControl(op, permTeamBoard) {
		return
	}
	id := strings.TrimSpace(op.TaskID)
	if id == "" {
		e.failTeamOp(op, protocol.ErrTeamValidation, "taskId is required")
		return
	}
	res, err := e.teamTask(ctx, tool.TeamTaskRequest{
		Action:          "claim",
		ID:              id,
		ExpectedVersion: op.ExpectedVersion,
	})
	if err != nil {
		code := protocol.ErrTeamValidation
		if strings.Contains(err.Error(), "no team") || strings.Contains(err.Error(), "dissolved") {
			code = protocol.ErrTeamUnavailable
		}
		e.failTeamOp(op, code, err.Error())
		return
	}
	if res.Conflict {
		cur := 0
		if res.Task != nil {
			cur = res.Task.Version
		}
		out := protocol.TeamOpOutcome{
			OK:             false,
			Code:           protocol.ErrTeamConflict,
			Error:          protocol.ErrTeamConflict,
			CurrentVersion: cur,
			TaskID:         id,
		}
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Code:        protocol.ErrTeamConflict,
			Message:     protocol.ErrTeamConflict,
		})
		replyTeamOp(op, out)
		return
	}
	ver := 0
	if res.Task != nil {
		ver = res.Task.Version
		id = res.Task.ID
	}
	e.okTeamOp(op, protocol.TeamOpOutcome{TaskID: id, Version: ver})
}

func (e *Engine) handleTeamBoardComplete(ctx context.Context, op protocol.TeamBoardComplete) {
	if !e.beginTeamControl(op, permTeamBoard) {
		return
	}
	id := strings.TrimSpace(op.TaskID)
	if id == "" {
		e.failTeamOp(op, protocol.ErrTeamValidation, "taskId is required")
		return
	}
	res, err := e.teamTask(ctx, tool.TeamTaskRequest{
		Action:          "complete",
		ID:              id,
		ExpectedVersion: op.ExpectedVersion,
	})
	if err != nil {
		code := protocol.ErrTeamValidation
		if strings.Contains(err.Error(), "no team") || strings.Contains(err.Error(), "dissolved") {
			code = protocol.ErrTeamUnavailable
		}
		e.failTeamOp(op, code, err.Error())
		return
	}
	if res.Conflict {
		cur := 0
		if res.Task != nil {
			cur = res.Task.Version
		}
		out := protocol.TeamOpOutcome{
			OK:             false,
			Code:           protocol.ErrTeamConflict,
			Error:          protocol.ErrTeamConflict,
			CurrentVersion: cur,
			TaskID:         id,
		}
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Code:        protocol.ErrTeamConflict,
			Message:     protocol.ErrTeamConflict,
		})
		replyTeamOp(op, out)
		return
	}
	ver := 0
	if res.Task != nil {
		ver = res.Task.Version
		id = res.Task.ID
	}
	_ = op.Summary // optional; board complete has no summary field on tool
	e.okTeamOp(op, protocol.TeamOpOutcome{TaskID: id, Version: ver})
}
