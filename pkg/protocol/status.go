package protocol

import "strings"

// Session status vocabulary shared by strike serve and the web client.
//
// These ids are not transcript events. They travel on
// GET /v1/status/sessions, GET /v1/status/events, and as additive fields on
// GET /v1/roots and GET /v1/sessions. See docs/webui-status.md and
// schemas/session-status.json. Do not rename an id; add an alias instead.
const (
	// StatusIdle is a session that can take input, or a gateway that is up
	// and quiet. A finished root turn returns here. It is not StatusDone.
	StatusIdle = "idle"
	// StatusRunning is a turn, tool, or thinking stretch in flight.
	// "thinking" is this id; it is not a separate value.
	StatusRunning = "running"
	// StatusWaiting is an approval ask, a question, or a stall that needs a person.
	StatusWaiting = "waiting"
	// StatusBlocked is a handoff the child could not finish (schema:handoff).
	StatusBlocked = "blocked"
	// StatusFailed is a turn, child, or provider that ended in error.
	StatusFailed = "failed"
	// StatusDone is a child or task that finished. A root that can take another
	// message is StatusIdle, not StatusDone.
	StatusDone = "done"
	// StatusStopped is a canceled turn or child.
	StatusStopped = "stopped"
	// StatusDisconnected is a gateway the client has not heard from.
	// The server does not report this about itself.
	StatusDisconnected = "disconnected"
)

// HeartbeatIntervalMs is how often GET /v1/status/events emits a heartbeat.
// HeartbeatStaleMs is how long a client waits without one before it shows
// StatusDisconnected. Three missed beats at the 2s interval.
const (
	HeartbeatIntervalMs int64 = 2000
	HeartbeatStaleMs    int64 = 6000
)

// StatusIDs is the canonical id list, in the order schemas/session-status.json uses.
func StatusIDs() []string {
	return []string{
		StatusIdle,
		StatusRunning,
		StatusWaiting,
		StatusBlocked,
		StatusFailed,
		StatusDone,
		StatusStopped,
		StatusDisconnected,
	}
}

// StatusLabel is the short word the status component shows for an id.
func StatusLabel(id string) string {
	switch NormalizeStatus(id) {
	case StatusRunning:
		return "Running"
	case StatusWaiting:
		return "Waiting"
	case StatusBlocked:
		return "Blocked"
	case StatusFailed:
		return "Failed"
	case StatusDone:
		return "Done"
	case StatusStopped:
		return "Stopped"
	case StatusDisconnected:
		return "Disconnected"
	default:
		return "Idle"
	}
}

// StatusTone is the CSS variable for an id (session surfaces).
// The gateway chip paints idle with StatusChipIdleTone instead.
func StatusTone(id string) string {
	switch NormalizeStatus(id) {
	case StatusRunning:
		return "--accent-alt"
	case StatusWaiting, StatusBlocked:
		return "--warning"
	case StatusFailed, StatusDisconnected:
		return "--signal"
	case StatusDone:
		return "--success"
	default:
		return "--muted"
	}
}

// StatusChipIdleTone is the gateway chip color when the daemon is up and quiet.
// Theme.md maps that chip to Success, which is distinct from a session gutter.
const StatusChipIdleTone = "--success"

// SessionStatus is one row on the daemon status board.
// Times are unix milliseconds. ElapsedS is set only while running or waiting.
type SessionStatus struct {
	SessionID      string `json:"sessionId"`
	Title          string `json:"title,omitempty"`
	Status         string `json:"status"`
	Since          int64  `json:"since,omitempty"`
	LastActivityAt int64  `json:"lastActivityAt,omitempty"`
	Unread         bool   `json:"unread"`
	ElapsedS       int64  `json:"elapsedS,omitempty"`
	Live           bool   `json:"live,omitempty"`
}

// LiveStatusInput is the live-root facts ClassifyLive folds into one id.
// Priority, first match wins: waiting, running, blocked, failed, stopped, idle.
// Disconnected is not a server-side classification.
type LiveStatusInput struct {
	Busy              bool
	PermissionPending bool
	QuestionPending   bool
	StopReason        string
	ChildRunning      bool
	ChildWaiting      bool
	ChildBlocked      bool
}

// ClassifyLive maps one live root onto a status id.
func ClassifyLive(in LiveStatusInput) string {
	if in.PermissionPending || in.QuestionPending || in.ChildWaiting {
		return StatusWaiting
	}
	if in.Busy || in.ChildRunning {
		return StatusRunning
	}
	if in.ChildBlocked {
		return StatusBlocked
	}
	switch NormalizeStop(in.StopReason) {
	case StatusFailed:
		return StatusFailed
	case StatusStopped:
		return StatusStopped
	default:
		return StatusIdle
	}
}

// NormalizeStatus maps a wire or UI string onto a canonical id.
// Unknown strings become StatusIdle. Use KnownStatus when unknown must stay unknown.
func NormalizeStatus(raw string) string {
	if id, ok := KnownStatus(raw); ok {
		return id
	}
	return StatusIdle
}

// KnownStatus reports whether raw is a canonical id or a documented alias.
func KnownStatus(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case StatusIdle, "ready", "saved":
		return StatusIdle, true
	case StatusRunning, "thinking", "working", "in_progress", "in-progress", "busy", "starting", "active":
		return StatusRunning, true
	case StatusWaiting, "waiting-for-approval", "approval", "permission", "question", "stalled":
		return StatusWaiting, true
	case StatusBlocked:
		return StatusBlocked, true
	case StatusFailed, "error":
		return StatusFailed, true
	case StatusDone, "completed", "complete":
		return StatusDone, true
	case StatusStopped, "canceled", "cancelled":
		return StatusStopped, true
	case StatusDisconnected, "unreachable":
		return StatusDisconnected, true
	default:
		return "", false
	}
}

// NormalizeStop maps a TurnCompleted stopReason onto StatusFailed, StatusStopped,
// or "" when the turn ended in a way that leaves the root idle.
func NormalizeStop(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "error", "failed", "provider_error", "budget_exhausted":
		return StatusFailed
	case "canceled", "cancelled", "interrupted", "abort", "aborted":
		return StatusStopped
	default:
		return ""
	}
}

// AggregateGateway folds session rows into the gateway chip id.
// The client replaces this with StatusDisconnected when heartbeats stop.
func AggregateGateway(rows []SessionStatus) string {
	var waiting, running, blocked, failed bool
	for _, row := range rows {
		switch NormalizeStatus(row.Status) {
		case StatusWaiting:
			waiting = true
		case StatusRunning:
			running = true
		case StatusBlocked:
			blocked = true
		case StatusFailed:
			failed = true
		}
	}
	switch {
	case waiting:
		return StatusWaiting
	case running:
		return StatusRunning
	case blocked:
		return StatusBlocked
	case failed:
		return StatusFailed
	default:
		return StatusIdle
	}
}
