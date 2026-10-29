// Package rootstart is the one way unattended daemon work starts or continues
// a root session. Event triggers (#1282) and the scheduler (#1276) both call
// Launcher. Do not add a second spawn path in either feature.
//
// An empty PermissionMode resolves to plan, which denies write and edit.
// Callers that want a looser posture set it explicitly on the request.
package rootstart

import (
	"context"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/internal/protocol"
)

const (
	// SourceTrigger is Request.Source for an inbound trigger.
	SourceTrigger = "trigger"
	// SourceSchedule is Request.Source for a scheduler fire (#1276).
	SourceSchedule = "schedule"
)

// Request is one unattended prompt. Prompt is required. ContinueID resumes
// that root instead of creating one; a failed resume must not fall back to a
// new session (the caller decides).
type Request struct {
	Source         string
	SourceID       string
	Prompt         string
	PermissionMode string
	ContinueID     string
	Title          string
}

// Result is the root the launcher enqueued the prompt on.
type Result struct {
	SessionID      string
	Created        bool
	PermissionMode string
}

// Launcher starts or continues a root and enqueues the prompt.
type Launcher interface {
	StartRoot(ctx context.Context, req Request) (Result, error)
}

// ResolveMode returns the posture for an unattended run. Empty is plan.
func ResolveMode(raw string) (protocol.PermissionMode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return protocol.PermissionModePlan, nil
	}
	mode, ok := protocol.ParsePermissionMode(raw)
	if !ok {
		return "", fmt.Errorf("unknown permission mode %q", raw)
	}
	return mode, nil
}
