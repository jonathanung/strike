package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// waitSignal is one discrete orchestration event for wait matching.
type waitSignal struct {
	Kind       string
	SessionID  string
	Name       string
	Status     string
	Summary    string
	Handoff    protocol.CompletionHandoff
	HasHandoff bool
}

// waitSub is one in-flight wait subscription.
type waitSub struct {
	id       string
	kinds    map[string]struct{}
	filterID string // empty = any owned child
	ch       chan waitSignal
}

// childWait blocks until an owned-child event matches, times out, or ctx ends.
// Emits wait.started / wait.resolved on the parent event stream.
func (e *Engine) childWait(ctx context.Context, req tool.WaitRequest) (tool.WaitResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.WaitResult{}, err
	}
	if e == nil {
		return tool.WaitResult{}, fmt.Errorf("wait is not available")
	}
	if req.TimeoutSeconds <= 0 {
		return tool.WaitResult{}, fmt.Errorf("timeout_seconds must be in (0, 300]")
	}
	kinds, err := tool.NormalizeWaitEvents(req.Events)
	if err != nil {
		return tool.WaitResult{}, err
	}
	kindSet := make(map[string]struct{}, len(kinds))
	for _, k := range kinds {
		kindSet[k] = struct{}{}
	}

	filterRef := strings.TrimSpace(req.SessionID)
	filterID := ""
	if filterRef != "" {
		filterID = e.resolveOwnedChildRef(filterRef)
		// Fail closed: filter must name an owned live or terminal child.
		e.childMu.Lock()
		_, live := e.children[filterID]
		_, hist := e.childHistory[filterID]
		e.childMu.Unlock()
		if !live && !hist {
			return tool.WaitResult{}, fmt.Errorf("unknown or inaccessible child session %q", filterRef)
		}
	}

	waitID := rand.Text()
	if len(waitID) > 12 {
		waitID = waitID[:12]
	}
	timeoutMs := int(req.TimeoutSeconds * 1000)
	if timeoutMs < 1 {
		timeoutMs = 1
	}

	e.emit(protocol.WaitStarted{
		Correlation:     e.sessionCorr(),
		WaitID:          waitID,
		Events:          append([]string(nil), kinds...),
		TargetSessionID: filterID,
		TimeoutMs:       timeoutMs,
	})

	sub := &waitSub{
		id:       waitID,
		kinds:    kindSet,
		filterID: filterID,
		ch:       make(chan waitSignal, 1),
	}
	e.registerWaitSub(sub)
	defer e.unregisterWaitSub(sub.id)

	// Snapshot after subscribe so a completion between snapshot and register
	// is still delivered on the channel; a completion before subscribe is
	// caught by the snapshot.
	if sig, ok := e.snapshotWaitMatch(filterID, kindSet); ok {
		return e.finishWaitMatched(waitID, req.TimeoutSeconds, sig), nil
	}

	timer := time.NewTimer(time.Duration(req.TimeoutSeconds * float64(time.Second)))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		res := tool.WaitResult{
			Outcome:        tool.WaitOutcomeCanceled,
			WaitID:         waitID,
			TimeoutSeconds: req.TimeoutSeconds,
			Detail:         "wait canceled",
		}
		// Structured cancel outcome (not a hard error) so models see
		// matched|timeout|canceled consistently. Turn-level interrupt still
		// hits executeTool's ctx.Err() path around Execute.
		e.emitWaitResolved(waitID, res, waitSignal{})
		return res, nil
	case <-timer.C:
		res := tool.WaitResult{
			Outcome:        tool.WaitOutcomeTimeout,
			WaitID:         waitID,
			TimeoutSeconds: req.TimeoutSeconds,
			Detail:         "wait timed out",
		}
		e.emitWaitResolved(waitID, res, waitSignal{})
		return res, nil
	case sig := <-sub.ch:
		return e.finishWaitMatched(waitID, req.TimeoutSeconds, sig), nil
	}
}

func (e *Engine) finishWaitMatched(waitID string, timeoutSec float64, sig waitSignal) tool.WaitResult {
	res := tool.WaitResult{
		Outcome:        tool.WaitOutcomeMatched,
		Event:          sig.Kind,
		SessionID:      sig.SessionID,
		Name:           sig.Name,
		Status:         sig.Status,
		Summary:        sig.Summary,
		WaitID:         waitID,
		TimeoutSeconds: timeoutSec,
		Detail:         "event matched",
	}
	if sig.HasHandoff {
		res.HasHandoff = true
		res.Handoff = toolHandoff(sig.Handoff)
	}
	e.emitWaitResolved(waitID, res, sig)
	return res
}

func (e *Engine) emitWaitResolved(waitID string, res tool.WaitResult, sig waitSignal) {
	ev := protocol.WaitResolved{
		Correlation:     e.sessionCorr(),
		WaitID:          waitID,
		Outcome:         res.Outcome,
		Event:           res.Event,
		TargetSessionID: res.SessionID,
		TargetName:      res.Name,
		Status:          res.Status,
		Summary:         res.Summary,
		HasHandoff:      res.HasHandoff,
	}
	if res.HasHandoff {
		ev.Handoff = sig.Handoff
	}
	e.emit(ev)
}

func (e *Engine) registerWaitSub(sub *waitSub) {
	e.waitMu.Lock()
	defer e.waitMu.Unlock()
	if e.waitSubs == nil {
		e.waitSubs = make(map[string]*waitSub)
	}
	e.waitSubs[sub.id] = sub
}

func (e *Engine) unregisterWaitSub(id string) {
	e.waitMu.Lock()
	defer e.waitMu.Unlock()
	delete(e.waitSubs, id)
}

// notifyWaiters delivers sig to every matching subscription (non-blocking per sub).
func (e *Engine) notifyWaiters(sig waitSignal) {
	if e == nil || sig.Kind == "" {
		return
	}
	e.waitMu.Lock()
	subs := make([]*waitSub, 0, len(e.waitSubs))
	for _, s := range e.waitSubs {
		subs = append(subs, s)
	}
	e.waitMu.Unlock()

	for _, s := range subs {
		if s.filterID != "" && s.filterID != sig.SessionID {
			continue
		}
		if _, ok := s.kinds[sig.Kind]; !ok {
			continue
		}
		select {
		case s.ch <- sig:
		default:
			// Already has a pending match; first wins.
		}
	}
}

// snapshotWaitMatch checks live/history children for an already-true predicate.
func (e *Engine) snapshotWaitMatch(filterID string, kinds map[string]struct{}) (waitSignal, bool) {
	e.childMu.Lock()
	defer e.childMu.Unlock()

	checkLive := func(h *childHandle) (waitSignal, bool) {
		if h == nil {
			return waitSignal{}, false
		}
		if filterID != "" && h.id != filterID {
			return waitSignal{}, false
		}
		h.mu.Lock()
		// Refresh soft stall so snapshot waits see current idle state.
		if h.budget != nil {
			_, _, _, _ = h.budget.evaluate(time.Now(), h.startedAt)
		}
		blocked := h.awaitingPerm || h.awaitingQ
		stale := h.budget != nil && h.budget.softStall && !h.budget.escalated
		var staleReason string
		if stale {
			staleReason = h.budget.softStallReason(time.Now(), h.startedAt)
		}
		name := h.name
		id := h.id
		h.mu.Unlock()
		if stale {
			if _, ok := kinds[tool.WaitEventTaskStale]; ok {
				return waitSignal{
					Kind:      tool.WaitEventTaskStale,
					SessionID: id,
					Name:      name,
					Status:    "needs_attention",
					Summary:   staleReason,
				}, true
			}
			if _, ok := kinds[tool.WaitEventTaskBlocked]; ok {
				return waitSignal{
					Kind:      tool.WaitEventTaskBlocked,
					SessionID: id,
					Name:      name,
					Status:    "needs_attention",
					Summary:   staleReason,
				}, true
			}
		}
		if blocked {
			if _, ok := kinds[tool.WaitEventTaskBlocked]; ok {
				return waitSignal{
					Kind:      tool.WaitEventTaskBlocked,
					SessionID: id,
					Name:      name,
					Status:    "needs_attention",
					Summary:   "child needs attention",
				}, true
			}
		}
		return waitSignal{}, false
	}

	checkHist := func(rec *childRecord) (waitSignal, bool) {
		if rec == nil {
			return waitSignal{}, false
		}
		if filterID != "" && rec.id != filterID {
			return waitSignal{}, false
		}
		kind := waitKindFromChildStatus(rec.status)
		if kind == "" {
			return waitSignal{}, false
		}
		if _, ok := kinds[kind]; !ok {
			return waitSignal{}, false
		}
		return waitSignal{
			Kind:       kind,
			SessionID:  rec.id,
			Name:       rec.name,
			Status:     string(rec.status),
			Summary:    rec.summary,
			Handoff:    rec.handoff,
			HasHandoff: true,
		}, true
	}

	if filterID != "" {
		if sig, ok := checkLive(e.children[filterID]); ok {
			return sig, true
		}
		if sig, ok := checkHist(e.childHistory[filterID]); ok {
			return sig, true
		}
		return waitSignal{}, false
	}

	for _, h := range e.children {
		if sig, ok := checkLive(h); ok {
			return sig, true
		}
	}
	for _, rec := range e.childHistory {
		if sig, ok := checkHist(rec); ok {
			return sig, true
		}
	}
	return waitSignal{}, false
}

func waitKindFromChildStatus(status protocol.ChildStatus) string {
	switch status {
	case protocol.ChildStatusCompleted:
		return tool.WaitEventTaskDone
	case protocol.ChildStatusFailed:
		return tool.WaitEventTaskFailed
	case protocol.ChildStatusCanceled:
		return tool.WaitEventTaskCanceled
	case protocol.ChildStatusBlocked:
		// Independent verification gate failure (or other terminal blocked).
		return tool.WaitEventTaskBlocked
	default:
		return ""
	}
}

// notifyWaitersFromCompleted maps a ChildCompleted onto wait signals.
func (e *Engine) notifyWaitersFromCompleted(c protocol.ChildCompleted) {
	kind := waitKindFromChildStatus(c.Status)
	if kind == "" {
		return
	}
	e.notifyWaiters(waitSignal{
		Kind:       kind,
		SessionID:  c.SessionID,
		Name:       c.Name,
		Status:     string(c.Status),
		Summary:    c.Summary,
		Handoff:    c.Handoff,
		HasHandoff: true,
	})
}

// notifyWaitersBlocked signals task.blocked for a child that needs attention.
func (e *Engine) notifyWaitersBlocked(sessionID, name string) {
	e.notifyWaiters(waitSignal{
		Kind:      tool.WaitEventTaskBlocked,
		SessionID: sessionID,
		Name:      name,
		Status:    "needs_attention",
		Summary:   "child needs attention",
	})
}
