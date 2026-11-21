package server

import (
	"fmt"

	"github.com/jonathanung/strike/internal/daemon/trigger"
	"github.com/jonathanung/strike/internal/persist/suggestion"
)

// In-process Ideas inbox producers (#1926). Each files through
// suggestion.Store.Deposit (never HTTP) under its reserved source with a
// stable evidence key, so a recurring failure is one idea, not one per
// run, and a dismissed idea does not come back. Muted sources/kinds,
// dismissed keys and quiet hours are enforced by the store; a refused
// deposit is not an error for the caller.

const (
	kindScheduleFailure = "schedule-failure"
	kindTriggerFailure  = "trigger-failure"
)

// DepositSuggestion files d from the in-process producer source. Nil-safe.
func (s *Server) DepositSuggestion(source string, d suggestion.Draft) (suggestion.Suggestion, bool, error) {
	if s == nil {
		return suggestion.Suggestion{}, false, suggestion.ErrInvalid
	}
	store, err := s.suggestions()
	if err != nil {
		return suggestion.Suggestion{}, false, err
	}
	return store.Deposit(source, d)
}

// scriptFailureDraft is the scheduler producer's idea for a failed script
// run. The key is per schedule: repeated failures dedupe onto one row.
// It carries no command or output, like the notification.
func scriptFailureDraft(scheduleID, runID, name string, exitCode *int) (suggestion.Draft, bool) {
	sch, ok := safeID(scheduleID)
	if !ok {
		return suggestion.Draft{}, false
	}
	run, ok := safeID(runID)
	if !ok {
		return suggestion.Draft{}, false
	}
	label := safeLabel(name, 60)
	if label == "" {
		label = "Script schedule"
	}
	code := "with no exit code"
	if exitCode != nil {
		code = fmt.Sprintf("with exit %d", *exitCode)
	}
	return suggestion.Draft{
		Title:       clampRunes("Look into failing schedule: "+label, 120),
		Why:         fmt.Sprintf("The scheduled script %q failed %s (run %s). Later failures of this schedule add to this idea instead of filing new ones.", label, code, run),
		Prompt:      fmt.Sprintf("The scheduled script %q (schedule %s) failed %s on run %s. Open that run's output in the Scheduler, find the cause, and propose a fix. Do not change the schedule without asking.", label, sch, code, run),
		Kind:        kindScheduleFailure,
		EvidenceKey: "schedule:" + sch + ":failed",
		Evidence:    []suggestion.Evidence{{Label: "Scheduler run " + run + " of " + label}},
	}, true
}

// SuggestScriptRunFailure deposits the scheduler producer's idea for a
// failed script run. Refusals (muted, dismissed, quiet) are dropped.
func (s *Server) SuggestScriptRunFailure(scheduleID, runID, name string, exitCode *int) {
	d, ok := scriptFailureDraft(scheduleID, runID, name, exitCode)
	if !ok {
		return
	}
	_, _, _ = s.DepositSuggestion(suggestion.SourceScheduler, d)
}

// triggerProblemDraft is the trigger producer's idea for a delivery whose
// session did not start. The key is per trigger.
func triggerProblemDraft(p trigger.Problem) (suggestion.Draft, bool) {
	id, ok := safeID(p.TriggerID)
	if !ok {
		return suggestion.Draft{}, false
	}
	name := safeLabel(p.TriggerName, 60)
	if name == "" {
		name = id
	}
	delivery := "a delivery"
	if event := safeLabel(p.Event, 60); event != "" {
		delivery = fmt.Sprintf("a %q delivery", event)
	}
	ev := suggestion.Evidence{Label: "Trigger " + name}
	if did, ok := safeID(p.DeliveryID); ok {
		ev.Label += " delivery " + did
	}
	return suggestion.Draft{
		Title:       clampRunes("Trigger could not start a session: "+name, 120),
		Why:         fmt.Sprintf("The trigger %q accepted %s but its session did not start, so the work it asked for did not happen.", name, delivery),
		Prompt:      fmt.Sprintf("The trigger %q (id %s) accepted %s but the session did not start. Check the trigger's prompt, permission mode and the daemon log, then replay the delivery from Triggers once it is fixed.", name, id, delivery),
		Kind:        kindTriggerFailure,
		EvidenceKey: "trigger:" + id + ":start-failed",
		Evidence:    []suggestion.Evidence{ev},
	}, true
}

// SuggestTriggerProblem deposits the trigger producer's idea. Refusals are
// dropped.
func (s *Server) SuggestTriggerProblem(p trigger.Problem) {
	d, ok := triggerProblemDraft(p)
	if !ok {
		return
	}
	_, _, _ = s.DepositSuggestion(suggestion.SourceTrigger, d)
}
