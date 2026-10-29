package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/rootstart"
)

// tickInput is the world at one daemon tick. planTick does not touch disk
// or start a session.
type tickInput struct {
	Now             time.Time
	Startup         bool
	Busy            bool
	Fingerprint     string
	FingerprintErr  error
	Goal            *GoalRef
	GoalErr         error
	WorkflowFound   bool
	WorkflowValid   bool
	WorkflowChecked bool
	WorkspaceMode   string
}

type tickResult struct {
	Action      string
	NextFire    time.Time
	Enabled     bool
	Fingerprint string
	LastStatus  string
	LastError   string
	Run         *Run
	Req         *rootstart.Request
	// ClearNext is set when a one-shot has no further fire.
	ClearNext bool
}

func planTick(sch Schedule, in tickInput) tickResult {
	res := tickResult{
		Action:      "none",
		NextFire:    sch.NextFire,
		Enabled:     sch.Enabled,
		Fingerprint: sch.Fingerprint,
	}
	if !sch.Enabled || sch.NextFire.IsZero() || in.Now.Before(sch.NextFire) {
		return res
	}

	// A queued hold is a promise to run once when the previous session goes
	// idle. It is not a missed slot: skip must not drop it after the interval
	// has passed, and the calendar jumps forward only after that one fire.
	if sch.LastStatus == StatusQueued && !in.Busy {
		return fire(sch, in, res, true, false)
	}
	if in.Busy && sch.Overlap == OverlapQueue {
		res.Action = "queue"
		res.LastStatus = StatusQueued
		res.LastError = ""
		return res
	}
	if in.Busy && sch.Overlap == OverlapSkip {
		return finish(sch, in, res, "skipped", StatusSkipped, "", true)
	}

	late := dueCount(sch, in.Now)
	if (in.Startup || late >= 2) && sch.Missed == MissedSkip {
		res = finish(sch, in, res, "missed", StatusMissed, "", true)
		// A skipped heartbeat rebases the quiet baseline so downtime is not
		// announced as a change on the next check.
		if sch.Kind == KindHeartbeat && in.FingerprintErr == nil && in.Fingerprint != "" {
			res.Fingerprint = in.Fingerprint
		}
		return res
	}

	jump := in.Startup || late >= 2
	return fire(sch, in, res, jump, false)
}

// planRunNow builds one explicit fire. The calendar (next, enabled) is left
// as it was; a heartbeat runs even when the tree is unchanged.
func planRunNow(sch Schedule, in tickInput) tickResult {
	savedNext := sch.NextFire
	savedEnabled := sch.Enabled
	savedFP := sch.Fingerprint
	sch.NextFire = in.Now
	res := tickResult{Enabled: savedEnabled, NextFire: savedNext, Fingerprint: savedFP}
	out := fire(sch, in, res, false, true)
	out.NextFire = savedNext
	out.Enabled = savedEnabled
	out.ClearNext = false
	return out
}

func fire(sch Schedule, in tickInput, res tickResult, jump, force bool) tickResult {
	mode, err := rootstart.ResolveMode(sch.PermissionMode, in.WorkspaceMode)
	if err != nil {
		return finish(sch, in, res, "error", StatusError, err.Error(), jump)
	}
	if sch.Kind == KindHeartbeat {
		if in.FingerprintErr != nil {
			return finish(sch, in, res, "error", StatusError, in.FingerprintErr.Error(), jump)
		}
		same := sch.Fingerprint != "" && sch.Fingerprint == in.Fingerprint
		res.Fingerprint = in.Fingerprint
		if !force && (sch.Fingerprint == "" || same) {
			quiet := finish(sch, in, res, "quiet", StatusQuiet, "", jump)
			quiet.Fingerprint = in.Fingerprint
			return quiet
		}
	}

	prompt, wf, goalID, errText := promptFor(sch, in)
	if errText != "" {
		return finish(sch, in, res, "error", StatusError, errText, jump)
	}
	res.Action = "start"
	res.LastStatus = StatusStarted
	res.LastError = ""
	res.Req = &rootstart.Request{
		Source:         rootstart.SourceSchedule,
		SourceID:       sch.ID,
		Title:          sch.Name,
		Prompt:         prompt,
		GoalID:         goalID,
		Workflow:       wf,
		WorkspaceID:    sch.WorkspaceID,
		PermissionMode: mode,
		Unattended:     true,
	}
	res.Run = &Run{
		ScheduleID:     sch.ID,
		FiredAt:        in.Now,
		ScheduledFor:   sch.NextFire,
		Status:         StatusStarted,
		PermissionMode: mode,
	}
	applyAdvance(&res, sch, in.Now, jump)
	return res
}

func promptFor(sch Schedule, in tickInput) (prompt, workflow, goalID, errText string) {
	switch sch.Source {
	case SourceGoal:
		if in.GoalErr != nil {
			return "", "", "", in.GoalErr.Error()
		}
		if in.Goal == nil {
			return "", "", "", fmt.Sprintf("goal %q not found", sch.GoalID)
		}
		return goalPrompt(*in.Goal, sch.Prompt), "", in.Goal.ID, ""
	case SourceWorkflow:
		if in.WorkflowChecked && !in.WorkflowFound {
			return "", "", "", fmt.Sprintf("workflow %q not found", sch.Workflow)
		}
		if in.WorkflowChecked && !in.WorkflowValid {
			return "", "", "", fmt.Sprintf("workflow %q is invalid", sch.Workflow)
		}
		text := sch.Prompt
		if text == "" {
			text = fmt.Sprintf("Follow the workflow %q. Begin at its first phase. Stay inside the session permission mode.", sch.Workflow)
		}
		return text, sch.Workflow, "", ""
	default:
		if strings.TrimSpace(sch.Prompt) == "" {
			return "", "", "", "prompt is required"
		}
		return sch.Prompt, "", "", ""
	}
}

func goalPrompt(g GoalRef, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Work on goal %s until the criteria are met.\n\n%s\n", g.ID, g.Description)
	if len(g.Criteria) > 0 {
		b.WriteString("\nSuccess criteria:\n")
		for _, c := range g.Criteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if strings.TrimSpace(extra) != "" {
		fmt.Fprintf(&b, "\nExtra instructions:\n%s\n", strings.TrimSpace(extra))
	}
	b.WriteString("\nStay inside the session permission mode. If you are blocked, stop and say what you need.\n")
	return b.String()
}

func finish(sch Schedule, in tickInput, res tickResult, action, status, errText string, jump bool) tickResult {
	res.Action = action
	res.LastStatus = status
	res.LastError = errText
	res.Run = &Run{
		ScheduleID:   sch.ID,
		FiredAt:      in.Now,
		ScheduledFor: sch.NextFire,
		Status:       status,
		Error:        errText,
	}
	applyAdvance(&res, sch, in.Now, jump)
	return res
}

func applyAdvance(res *tickResult, sch Schedule, now time.Time, jump bool) {
	after := sch.NextFire
	if jump {
		after = now
	}
	next, err := nextStrictlyAfter(sch, after)
	if err != nil {
		res.Action = "error"
		res.LastStatus = StatusError
		res.LastError = err.Error()
		if res.Run != nil {
			res.Run.Status = StatusError
			res.Run.Error = err.Error()
		}
		return
	}
	if next.IsZero() {
		res.Enabled = false
		res.ClearNext = true
		res.NextFire = time.Time{}
		return
	}
	res.NextFire = next
	res.Enabled = sch.Enabled
}
