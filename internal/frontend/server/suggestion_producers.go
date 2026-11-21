package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/internal/daemon/trigger"
	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/suggestion"
	"github.com/jonathanung/strike/internal/protocol"
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
	kindChildFollowUp   = "child-follow-up"
	kindGoalStopped     = "goal-stopped"

	// maxEvidenceKey mirrors the store's evidence-key limit.
	maxEvidenceKey = 200
	// maxChildBlockers bounds how many child blockers reach the why text.
	maxChildBlockers = 3
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

// childFollowUpDraft is the child-agent producer's idea for a finished
// child whose handoff names a next step or unresolved blockers (#1950).
// A clean finish with nothing left to do, and a cancel, file nothing. The
// key is per (parent session, child name), so a re-run teammate updates
// one row. rootSession is the drain's root id, used when the event has no
// parent correlation. Only the handoff's next action and blockers are
// copied, scrubbed and clamped: no patch, files or verification output.
func childFollowUpDraft(rootSession string, cc protocol.ChildCompleted) (suggestion.Draft, bool) {
	status := strings.ToLower(strings.TrimSpace(string(cc.Status)))
	switch protocol.ChildStatus(status) {
	case protocol.ChildStatusCompleted, protocol.ChildStatusBlocked, protocol.ChildStatusFailed:
	default:
		return suggestion.Draft{}, false
	}
	parent := firstSafe(cc.ParentSessionID, rootSession)
	if parent == "" {
		return suggestion.Draft{}, false
	}
	child := firstSafe(cc.Name, cc.SessionID)
	if child == "" {
		return suggestion.Draft{}, false
	}
	next := safeLabel(cc.Handoff.RecommendedNextAction, 300)
	var blockers []string
	for _, b := range cc.Handoff.Blockers {
		if len(blockers) == maxChildBlockers {
			break
		}
		if v := safeLabel(b, 200); v != "" {
			blockers = append(blockers, v)
		}
	}
	if next == "" && len(blockers) == 0 {
		return suggestion.Draft{}, false
	}
	who := safeLabel(cc.Name, 40)
	if who == "" {
		who = "Delegated child"
	}
	key := "child:" + parent + ":" + child
	if len(key) > maxEvidenceKey {
		sum := sha256.Sum256([]byte(parent + "\n" + child))
		key = "child:" + hex.EncodeToString(sum[:16])
	}
	var title, why, prompt string
	if next != "" {
		title = who + ": " + next
		why = fmt.Sprintf("%s %s and recommended a next step: %s", who, status, next)
		prompt = fmt.Sprintf("A delegated child (%s) of session %s %s and recommended: %s", who, parent, status, next)
	} else {
		title = who + " is blocked"
		why = fmt.Sprintf("%s %s with unresolved blockers", who, status)
		prompt = fmt.Sprintf("A delegated child (%s) of session %s %s with unresolved blockers", who, parent, status)
	}
	if len(blockers) > 0 {
		why += ". Blockers: " + strings.Join(blockers, "; ")
		prompt += ". Blockers: " + strings.Join(blockers, "; ")
	}
	why += "."
	prompt += ". Open that session's handoff, confirm it still applies, and propose how to do it. Ask before changing anything."
	return suggestion.Draft{
		Title:       clampRunes(title, 120),
		Why:         why,
		Prompt:      prompt,
		Kind:        kindChildFollowUp,
		EvidenceKey: key,
		Evidence:    []suggestion.Evidence{{Label: "Handoff from " + who, SessionID: parent}},
	}, true
}

// SuggestChildFollowUp deposits the child-agent producer's idea for one
// child.completed event. Refusals are dropped.
func (s *Server) SuggestChildFollowUp(rootSession string, cc protocol.ChildCompleted) {
	d, ok := childFollowUpDraft(rootSession, cc)
	if !ok {
		return
	}
	_, _, _ = s.DepositSuggestion(suggestion.SourceChildAgent, d)
}

// goalStoppedDraft is the goal-autopilot producer's idea for an autopilot
// goal that stopped without finishing: failed (a guard tripped) or blocked
// (waiting on a person). The key is goal:<id>:<status>, so a goal that
// stops the same way again does not file twice. Manual goals, finished,
// paused and aborted ones file nothing: the person already acted.
func goalStoppedDraft(g host.Goal) (suggestion.Draft, bool) {
	if !g.Autopilot {
		return suggestion.Draft{}, false
	}
	status := strings.ToLower(strings.TrimSpace(g.Status))
	if status != "failed" && status != "blocked" {
		return suggestion.Draft{}, false
	}
	id, ok := safeID(g.ID)
	if !ok {
		return suggestion.Draft{}, false
	}
	desc := safeLabel(g.Description, 80)
	if desc == "" {
		desc = "Goal " + id
	}
	reason := safeLabel(g.FailReason, 300)
	verb := "failed"
	ask := "Read the goal's iteration log, find why it stopped, and propose a change to the goal or its limits. Do not restart it without asking."
	if status == "blocked" {
		verb = "is waiting on you"
		ask = "Read the goal's iteration log, work out what it needs from a person, and propose the answer. Do not resume it without asking."
	}
	why := fmt.Sprintf("The autopilot goal %q %s after %d iteration(s).", desc, verb, g.LastIteration)
	if reason != "" {
		why += " Reason: " + reason
	}
	prompt := fmt.Sprintf("The autopilot goal %q (id %s) %s after %d iteration(s).", desc, id, verb, g.LastIteration)
	if reason != "" {
		prompt += " Reason: " + reason + "."
	}
	return suggestion.Draft{
		Title:       clampRunes("Autopilot goal "+verb+": "+desc, 120),
		Why:         why,
		Prompt:      prompt + " " + ask,
		Kind:        kindGoalStopped,
		EvidenceKey: "goal:" + id + ":" + status,
		Evidence:    []suggestion.Evidence{{Label: "Goal " + desc}},
	}, true
}

// SuggestGoalOutcome deposits the goal-autopilot producer's idea after a
// goal run returns. Refusals are dropped.
func (s *Server) SuggestGoalOutcome(g host.Goal) {
	d, ok := goalStoppedDraft(g)
	if !ok {
		return
	}
	_, _, _ = s.DepositSuggestion(suggestion.SourceAutopilot, d)
}
