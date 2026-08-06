package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// delegate handles the delegate tool (create/get/list/transition).
func (e *Engine) delegate(ctx context.Context, req tool.DelegateRequest) (tool.DelegateResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.DelegateResult{}, err
	}
	if e == nil || e.team == nil {
		return tool.DelegateResult{}, fmt.Errorf("delegate is not available (no team)")
	}
	actor := strings.TrimSpace(e.opts.SessionID)
	if actor == "" {
		return tool.DelegateResult{}, fmt.Errorf("session id is required")
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	out := tool.DelegateResult{
		LeadID: e.team.LeadID(),
		Action: action,
	}

	switch action {
	case "list":
		out.Items = delegationsToTool(e.team.Delegations())
		return out, nil

	case "get":
		item, ok := e.team.GetDelegation(req.ID)
		if !ok {
			return tool.DelegateResult{}, fmt.Errorf("delegation %q not found", req.ID)
		}
		out.Item = delegationToTool(item)
		return out, nil

	case "create":
		res, err := e.spawnChild(ctx, tool.TaskRequest{
			Prompt:    req.Prompt,
			Name:      req.Name,
			Agent:     req.Agent,
			Model:     req.Model,
			Effort:    req.Effort,
			Criteria:  req.Criteria,
			Deps:      req.Deps,
			Subscribe: req.Subscribe,
			Assignee:  req.Assignee,
		})
		if err != nil {
			return tool.DelegateResult{}, err
		}
		out.Status = res.Status
		out.SessionID = res.SessionID
		if res.DelegationID != "" {
			if item, ok := e.team.GetDelegation(res.DelegationID); ok {
				out.Item = delegationToTool(item)
			}
		}
		out.Detail = res.Output
		return out, nil

	case "transition":
		to := protocol.DelegationState(strings.ToLower(strings.TrimSpace(req.State)))
		item, err := e.team.TransitionDelegation(req.ID, actor, to, req.Reason, req.ExpectedVersion)
		if err != nil {
			return delegationConflictResult(out, err)
		}
		e.emitDelegationChanged(item, "", req.Reason)
		// If transition requested working with spawn pending, try to start.
		if item.SpawnPending && item.SessionID == "" && to == protocol.DelegationWorking {
			if spawned, err := e.spawnPendingDelegation(ctx, item); err != nil {
				out.Detail = err.Error()
				// Keep transition success but report spawn failure.
				if cur, ok := e.team.GetDelegation(item.ID); ok {
					out.Item = delegationToTool(cur)
				} else {
					out.Item = delegationToTool(item)
				}
				return out, nil
			} else if spawned.DelegationID != "" {
				if cur, ok := e.team.GetDelegation(spawned.DelegationID); ok {
					out.Item = delegationToTool(cur)
					out.SessionID = cur.SessionID
					out.Status = "started"
					return out, nil
				}
			}
		}
		// Release other pending spawns owned by this engine (deps may have completed).
		e.flushPendingDelegationSpawns(ctx)
		if cur, ok := e.team.GetDelegation(item.ID); ok {
			out.Item = delegationToTool(cur)
		} else {
			out.Item = delegationToTool(item)
		}
		return out, nil

	default:
		return tool.DelegateResult{}, fmt.Errorf("action must be create, get, list, or transition")
	}
}

func delegationConflictResult(out tool.DelegateResult, err error) (tool.DelegateResult, error) {
	var conf *DelegationConflictError
	if errors.As(err, &conf) {
		out.Conflict = true
		out.Detail = conf.Reason
		out.Item = delegationToTool(conf.Item)
		return out, nil
	}
	var tr *DelegationTransitionError
	if errors.As(err, &tr) {
		return tool.DelegateResult{}, err
	}
	return tool.DelegateResult{}, err
}

func delegationToTool(d Delegation) *tool.DelegationItem {
	out := &tool.DelegationItem{
		ID:             d.ID,
		Prompt:         d.Prompt,
		Criteria:       append([]string(nil), d.Criteria...),
		Deps:           append([]string(nil), d.Deps...),
		Subscribe:      append([]string(nil), d.Subscribe...),
		OwnerSessionID: d.OwnerSessionID,
		Assignee:       d.Assignee,
		Agent:          d.Agent,
		Model:          d.Model,
		Effort:         d.Effort,
		Name:           d.Name,
		SessionID:      d.SessionID,
		State:          string(d.State),
		Version:        d.Version,
		BlockReason:    d.BlockReason,
		SpawnPending:   d.SpawnPending,
	}
	if !d.CreatedAt.IsZero() {
		out.CreatedAt = d.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !d.UpdatedAt.IsZero() {
		out.UpdatedAt = d.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func delegationsToTool(items []Delegation) []tool.DelegationItem {
	if items == nil {
		return []tool.DelegationItem{}
	}
	out := make([]tool.DelegationItem, 0, len(items))
	for _, item := range items {
		out = append(out, *delegationToTool(item))
	}
	return out
}

// createDelegationForTask registers a lifecycle object for a task spawn.
// Returns the item and whether the caller should spawn immediately.
func (e *Engine) createDelegationForTask(req tool.TaskRequest) (Delegation, bool, error) {
	if e == nil || e.team == nil {
		return Delegation{}, true, nil // no team → spawn without tracking
	}
	owner := strings.TrimSpace(e.opts.SessionID)
	item, err := e.team.CreateDelegation(CreateDelegationSpec{
		Prompt:         req.Prompt,
		Criteria:       req.Criteria,
		Deps:           req.Deps,
		Subscribe:      req.Subscribe,
		OwnerSessionID: owner,
		Assignee:       req.Assignee,
		Agent:          req.Agent,
		Model:          req.Model,
		Effort:         req.Effort,
		Name:           req.Name,
	})
	if err != nil {
		return Delegation{}, false, err
	}
	e.emitDelegationChanged(item, "", "created")
	shouldSpawn := item.SpawnPending || (len(item.Deps) == 0 && item.SessionID == "")
	// CreateDelegation sets SpawnPending when deps met; unmet → false.
	if len(item.Deps) > 0 {
		shouldSpawn = item.SpawnPending
	}
	return item, shouldSpawn, nil
}

func (e *Engine) emitDelegationChanged(d Delegation, prev protocol.DelegationState, reason string) {
	if e == nil {
		return
	}
	ev := protocol.DelegationChanged{
		Correlation: protocol.Correlation{
			SessionID:       d.OwnerSessionID,
			ParentSessionID: e.opts.ParentSessionID,
			Depth:           e.opts.Depth,
		},
		ID:             d.ID,
		State:          d.State,
		Prev:           prev,
		Version:        d.Version,
		SessionID:      d.SessionID,
		Name:           d.Name,
		Reason:         strings.TrimSpace(reason),
		OwnerSessionID: d.OwnerSessionID,
	}
	e.emit(ev)
	e.maybeNotifyDelegationSubscribers(d, prev)
}

func (e *Engine) maybeNotifyDelegationSubscribers(d Delegation, prev protocol.DelegationState) {
	if e == nil || e.team == nil || len(d.Subscribe) == 0 {
		return
	}
	state := string(d.State)
	want := false
	for _, s := range d.Subscribe {
		if s == state {
			want = true
			break
		}
	}
	if !want || prev == d.State {
		return
	}
	// Notify owner via mailbox when live; lead as fallback.
	body := fmt.Sprintf(
		"[delegation.%s id=%s version=%d session=%s] %s",
		state, d.ID, d.Version, d.SessionID, strings.TrimSpace(d.BlockReason),
	)
	if d.BlockReason == "" && reasonFromState(d.State) != "" {
		body = fmt.Sprintf("[delegation.%s id=%s version=%d session=%s]", state, d.ID, d.Version, d.SessionID)
	}
	to := d.OwnerSessionID
	if to == "" {
		to = e.team.LeadID()
	}
	// Best-effort peer deliver; ignore failures (owner may be mid-turn).
	_ = e.team.Deliver(e.opts.SessionID, to, strings.TrimSpace(body))
}

func reasonFromState(s protocol.DelegationState) string {
	return string(s)
}

// applyDelegationLifecycle attaches lifecycle fields onto a task_status result.
func (e *Engine) applyDelegationLifecycle(res *tool.TaskStatusResult, ref string) {
	if e == nil || e.team == nil || res == nil {
		return
	}
	d, ok := e.team.GetDelegation(ref)
	if !ok && res.SessionID != "" {
		d, ok = e.team.GetDelegation(res.SessionID)
	}
	if !ok {
		return
	}
	res.DelegationID = d.ID
	res.Lifecycle = string(d.State)
	res.Criteria = append([]string(nil), d.Criteria...)
	res.Deps = append([]string(nil), d.Deps...)
	res.Version = d.Version
	res.BlockReason = d.BlockReason
	if res.SessionID == "" {
		res.SessionID = d.SessionID
	}
	// Queued with no child: surface state as queued for coherence.
	if d.SessionID == "" && d.State == protocol.DelegationQueued {
		res.State = "queued"
	}
	if d.State == protocol.DelegationBlocked && res.State != "needs_attention" {
		// Keep child pulse if live; lifecycle carries blocked.
	}
}

// onChildDelegationTerminal updates lifecycle when a child finishes.
func (e *Engine) onChildDelegationTerminal(sessionID string, status protocol.ChildStatus) {
	if e == nil || e.team == nil {
		return
	}
	prev := protocol.DelegationState("")
	if cur, ok := e.team.GetDelegation(sessionID); ok {
		prev = cur.State
	}
	d, ok := e.team.BindDelegationOnChildCompleted(sessionID, status)
	if !ok {
		return
	}
	e.emitDelegationChanged(d, prev, "child."+string(status))
	// Deps may have unblocked other work owned by this engine.
	e.flushPendingDelegationSpawns(context.Background())
}

// flushPendingDelegationSpawns starts queued delegations owned by this engine
// whose dependencies are satisfied.
func (e *Engine) flushPendingDelegationSpawns(ctx context.Context) {
	if e == nil || e.team == nil {
		return
	}
	owner := strings.TrimSpace(e.opts.SessionID)
	pending := e.team.TakeSpawnPending(owner)
	for _, d := range pending {
		if _, err := e.spawnPendingDelegation(ctx, d); err != nil {
			// Leave a block reason via transition when spawn fails hard.
			e.team.ClearSpawnPending(d.ID)
			_, _ = e.team.TransitionDelegation(d.ID, owner, protocol.DelegationBlocked, "spawn failed: "+err.Error(), 0)
			if cur, ok := e.team.GetDelegation(d.ID); ok {
				e.emitDelegationChanged(cur, d.State, "spawn failed")
			}
		}
	}
}

// spawnPendingDelegation starts a child for an existing queued delegation.
func (e *Engine) spawnPendingDelegation(ctx context.Context, d Delegation) (tool.TaskResult, error) {
	if e == nil {
		return tool.TaskResult{}, fmt.Errorf("no engine")
	}
	// Re-enter spawnChild with the stored request, carrying the existing id
	// via a side channel on the request through spawnChild's delegation path.
	return e.spawnChildForDelegation(ctx, d)
}

// failDelegationSpawn marks a pre-spawn delegation failed when child start aborts.
func (e *Engine) failDelegationSpawn(delegID, reason string) {
	if e == nil || e.team == nil || strings.TrimSpace(delegID) == "" {
		return
	}
	actor := strings.TrimSpace(e.opts.SessionID)
	if actor == "" {
		actor = e.team.LeadID()
	}
	prev := protocol.DelegationState("")
	if cur, ok := e.team.GetDelegation(delegID); ok {
		prev = cur.State
	}
	e.team.ClearSpawnPending(delegID)
	item, err := e.team.TransitionDelegation(delegID, actor, protocol.DelegationFailed, reason, 0)
	if err != nil {
		// Best-effort: still clear pending.
		return
	}
	e.emitDelegationChanged(item, prev, reason)
}

// attachLifecycleToStatus is used by childStatus after building the snapshot.
func attachLifecycleFields(res tool.TaskStatusResult, d Delegation) tool.TaskStatusResult {
	res.DelegationID = d.ID
	res.Lifecycle = string(d.State)
	res.Criteria = append([]string(nil), d.Criteria...)
	res.Deps = append([]string(nil), d.Deps...)
	res.Version = d.Version
	res.BlockReason = d.BlockReason
	if res.SessionID == "" {
		res.SessionID = d.SessionID
	}
	if d.SessionID == "" && (d.State == protocol.DelegationQueued || d.State == protocol.DelegationBlocked) {
		res.State = string(d.State)
	}
	return res
}
