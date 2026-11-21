package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/pkg/protocol"
)

// modelSlot is one provider/model pair the engine can stream against.
type modelSlot struct {
	name  string
	prov  provider.Provider
	model string
}

func (s *modelSlot) label() string {
	if s == nil {
		return ""
	}
	if s.model == "" {
		return s.name
	}
	return s.name + "/" + s.model
}

// handleSetFallback resolves and stores the session fallback model (#1350).
// An empty provider clears it. The pick is confirmed with
// FallbackModelSelected; a provider that cannot be selected is an error and
// leaves the previous fallback in place.
func (e *Engine) handleSetFallback(op protocol.SetFallbackModel) {
	if e.turnActive() {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "cannot change the fallback model while a turn is running",
		})
		return
	}
	name := strings.TrimSpace(op.Provider)
	if name == "" {
		// A clear (or, below, a new pick) ends a session-sticky fallback
		// (#1922); the primary answers the next turn.
		e.endStickyFallback()
		e.fallback = nil
		e.emitSelected(protocol.FallbackModelSelected{Correlation: e.sessionCorr()})
		return
	}
	if e.opts.Select == nil {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "provider selection is not available",
		})
		return
	}
	name = CanonicalProviderID(name)
	p, defaultModel, err := e.opts.Select(name)
	if err != nil {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("fallback model: %v", err),
		})
		return
	}
	model := stripMatchingProviderPrefixes(name, resolveSelectModel(name, op.Model, defaultModel))
	e.endStickyFallback()
	e.fallback = &modelSlot{name: name, prov: p, model: model}
	e.emitSelected(protocol.FallbackModelSelected{
		Correlation: e.sessionCorr(),
		Provider:    name,
		Model:       model,
	})
}

// fallbackEligible reports whether a failed primary request may move to the
// fallback: a fallback is set, this turn is not already on it, it differs
// from the primary, and the failure is the provider's (not a cancel or a
// context overflow, which compaction owns).
func (e *Engine) fallbackEligible(ctx context.Context, err error) bool {
	if e.fallback == nil || e.primaryHold != nil || err == nil {
		return false
	}
	if ctx.Err() != nil || provider.IsContextOverflow(err) {
		return false
	}
	if e.fallback.name == e.provName && e.fallback.model == e.model {
		return false
	}
	return true
}

// switchToFallback moves the rest of this turn onto the fallback model and
// announces it. Usage, context window and later tool rounds in the turn all
// use the fallback, so cost is attributed to the model that answered. No
// ModelSelected is emitted: this is not a user model switch.
func (e *Engine) switchToFallback(corr protocol.Correlation, primaryErr error) {
	e.switchToFallbackFor(corr, protocol.ModelFallbackReasonUnavailable, primaryErr, false)
}

// switchToFallbackFor moves onto the fallback for reason. sticky keeps the
// session there after this turn until ReturnToPrimary (refusals, #1922).
func (e *Engine) switchToFallbackFor(corr protocol.Correlation, reason string, primaryErr error, sticky bool) {
	primary := &modelSlot{name: e.provName, prov: e.prov, model: e.model}
	e.primaryHold = primary
	e.fallbackSticky = sticky
	e.prov, e.provName, e.model = e.fallback.prov, e.fallback.name, e.fallback.model
	e.refreshContextWindow()
	msg := ""
	if primaryErr != nil {
		msg = primaryErr.Error()
	}
	e.emit(protocol.ModelFallback{
		Correlation:      corr,
		PrimaryProvider:  primary.name,
		PrimaryModel:     primary.model,
		FallbackProvider: e.fallback.name,
		FallbackModel:    e.fallback.model,
		Reason:           reason,
		Error:            msg,
		Sticky:           sticky,
	})
}

// endStickyFallback puts the primary back if the session was held on the
// fallback after a refusal, and reports whether it was.
func (e *Engine) endStickyFallback() bool {
	if !e.fallbackSticky {
		return false
	}
	e.fallbackSticky = false
	e.restorePrimary()
	return true
}

// handleReturnToPrimary ends a session-sticky fallback (#1922) and confirms
// the fallback setting with Active false. Already on the primary, it still
// confirms so a stale client view converges.
func (e *Engine) handleReturnToPrimary() {
	if e.turnActive() {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "cannot return to the primary model while a turn is running",
		})
		return
	}
	e.endStickyFallback()
	e.emitFallbackSelected()
}

// emitFallbackSelected confirms the current fallback setting and whether
// the session is running on it.
func (e *Engine) emitFallbackSelected() {
	sel := protocol.FallbackModelSelected{Correlation: e.sessionCorr(), Active: e.fallbackSticky}
	if e.fallback != nil {
		sel.Provider, sel.Model = e.fallback.name, e.fallback.model
	}
	e.emitSelected(sel)
}

// restorePrimary puts the primary back after a fallback turn; the next turn
// tries the primary again. Safe to call when no fallback is active.
func (e *Engine) restorePrimary() {
	if e.primaryHold == nil {
		return
	}
	p := e.primaryHold
	e.primaryHold = nil
	e.prov, e.provName, e.model = p.prov, p.name, p.model
	e.refreshContextWindow()
}

// bothFailed names both models when the fallback also fails, so the turn
// fails visibly and no answer is attributed to the wrong model.
func bothFailed(primary *modelSlot, primaryErr error, fallback *modelSlot, fallbackErr error) error {
	return fmt.Errorf("primary %s failed: %v; fallback %s failed: %w", primary.label(), primaryErr, fallback.label(), fallbackErr)
}

// errPrimaryRefused is the primary's "failure" when it declined the request.
var errPrimaryRefused = errors.New("refused the request")

// streamWithFallback wraps streamModel: when the primary request fails and
// a fallback is eligible, the fallback answers this turn. When the primary
// refuses (provider.StopReasonRefusal, no tool calls), the fallback answers
// and the session stays on it until ReturnToPrimary (#1922).
func (e *Engine) streamWithFallback(ctx context.Context, turnID string) (streamOutcome, protocol.Correlation, error) {
	outcome, corr, err := e.streamModel(ctx, turnID)
	if err == nil && provider.IsRefusal(outcome.stopReason) && len(outcome.calls) == 0 &&
		e.fallbackEligible(ctx, errPrimaryRefused) {
		e.switchToFallbackFor(corr, protocol.ModelFallbackReasonRefused, nil, true)
		fbOutcome, fbCorr, fbErr := e.streamModel(ctx, turnID)
		if fbErr != nil {
			// The fallback could not answer either: do not hold the session
			// on a model that just failed.
			e.fallbackSticky = false
			if ctx.Err() != nil {
				return streamOutcome{}, fbCorr, fbErr
			}
			return streamOutcome{}, fbCorr, bothFailed(e.primaryHold, errPrimaryRefused, e.fallback, fbErr)
		}
		return fbOutcome, fbCorr, nil
	}
	if err == nil || !e.fallbackEligible(ctx, err) {
		return outcome, corr, err
	}
	e.switchToFallback(corr, err)
	outcome, fbCorr, fbErr := e.streamModel(ctx, turnID)
	if fbErr != nil {
		if ctx.Err() != nil {
			return streamOutcome{}, fbCorr, fbErr
		}
		return streamOutcome{}, fbCorr, bothFailed(e.primaryHold, err, e.fallback, fbErr)
	}
	return outcome, fbCorr, nil
}
