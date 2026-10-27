// Package rootstart is the small seam shared by anything that starts a root
// session from outside the cockpit: the scheduler (#1276) and inbound
// triggers (#1282). Goals (#1284) and the task runner (#1277) should call
// this instead of spawning an engine themselves.
//
// Unattended callers leave PermissionMode empty only when they want the
// safest posture. ResolveMode applies a workspace mode when one is set,
// then an explicit mode, then plan. Plan denies writes and edits, so a
// clock or a webhook cannot mutate the tree while nobody is present.
// Implementations must not inherit the process --auto / yolo flag.
package rootstart

import (
	"context"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/internal/protocol"
)

const (
	// SourceSchedule is a clock fire (cron, interval, one-shot, heartbeat).
	SourceSchedule = "schedule"
	// SourceTrigger is an inbound webhook, GitHub event, or channel message.
	SourceTrigger = "trigger"
	// SourceGoal is a goal loop asking for its own root.
	SourceGoal = "goal"
	// SourceTask is a task-runner section asking for its own root.
	SourceTask = "task"

	// SafestMode is the unattended default: hard read-only.
	SafestMode = string(protocol.PermissionModePlan)
)

// Request is one root to open. Prompt, GoalID, and Workflow may be combined:
// a workflow run still needs a user turn or the session sits idle, and a
// goal id is recorded even when Prompt already carries the goal text.
type Request struct {
	Source         string
	SourceID       string
	Title          string
	Prompt         string
	GoalID         string
	Workflow       string
	WorkspaceID    string
	PermissionMode string
	// Unattended marks clock and inbound events. When PermissionMode is
	// empty, StartRoot implementations fill SafestMode.
	Unattended bool
}

// Result is the durable session id. SessionID may be set even when
// StartRoot returns an error, if the root was created but a follow-up
// op (permission mode, workflow, prompt) failed.
type Result struct {
	SessionID string
}

// Starter opens one root session and, when asked, sets its permission
// posture and first turn. Implementations must not switch the cockpit's
// active root; unattended work stays in the background.
type Starter interface {
	StartRoot(ctx context.Context, req Request) (Result, error)
}

// ResolveMode picks the posture for an unattended run.
//
// A workspace mode wins when it is set: the run obeys that policy, including
// a permissive one the user stored on the workspace. Otherwise an explicit
// mode on the schedule or trigger is used. Otherwise the result is plan.
func ResolveMode(explicit, workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	explicit = strings.TrimSpace(explicit)
	chosen := explicit
	if workspace != "" {
		chosen = workspace
	}
	if chosen == "" {
		return SafestMode, nil
	}
	mode, ok := protocol.ParsePermissionMode(chosen)
	if !ok {
		return "", fmt.Errorf("unknown permission mode %q", chosen)
	}
	return string(mode), nil
}
