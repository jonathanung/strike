package protocol

import "time"

// Public session lifecycle contract shared by HTTP, RPC, ACP, SDK, and TUI.
//
// Engine ops (Rewind) mutate the live session transcript. Host-level methods
// (list/get/fork/load) operate on durable session logs and are exposed as
// JSON-RPC methods on strike rpc, HTTP routes on strike serve, and helpers in
// pkg/sdk. Capability bits advertise which operations a frontend supports;
// unsupported calls return structured LifecycleError codes.

// Lifecycle method names (JSON-RPC / capability keys).
const (
	LifecycleMethodCapabilities = "session.capabilities"
	LifecycleMethodList         = "session.list"
	LifecycleMethodGet          = "session.get"
	LifecycleMethodFork         = "session.fork"
	LifecycleMethodForkAt       = "session.fork_at"
	LifecycleMethodLoad         = "session.load"
	LifecycleMethodRewindPoints = "session.rewind_points"
	LifecycleMethodReplay       = "session.replay"
)

// Lifecycle error codes (LifecycleError.Code / JSON-RPC error data.code).
const (
	// ErrorCodeSessionNotFound is an unknown or deleted session id.
	ErrorCodeSessionNotFound = "session_not_found"
	// ErrorCodeSessionBusy is an ownership/active-session conflict (e.g. delete
	// without force while open, or load of a different live session).
	ErrorCodeSessionBusy = "session_busy"
	// ErrorCodeSessionCorrupt is a failed replay/integrity check on the log.
	ErrorCodeSessionCorrupt = "session_corrupt"
	// ErrorCodeUnsupported is a known method the frontend does not implement.
	ErrorCodeUnsupported = "unsupported"
	// ErrorCodeInvalidSession is a rejected id (empty, child where root required).
	ErrorCodeInvalidSession = "invalid_session"
)

// LifecycleCapabilities advertises which host-level session operations a
// frontend supports. Engine-level Rewind remains a protocol op ("rewind") and
// is listed separately under EngineRewind when the live session accepts ops.
type LifecycleCapabilities struct {
	List         bool `json:"list"`
	Get          bool `json:"get"`
	Fork         bool `json:"fork"`
	ForkAt       bool `json:"forkAt"`
	Load         bool `json:"load"`
	RewindPoints bool `json:"rewindPoints"`
	Replay       bool `json:"replay"`
	// EngineRewind is true when the live session accepts protocol.Rewind ops.
	EngineRewind bool `json:"engineRewind"`
	// ActiveSessionID is the live session bound to this connection, if any.
	ActiveSessionID string `json:"activeSessionId,omitempty"`
}

// SessionSummary is the public inspect/list row for one durable session.
// Mirrors host.Session / HTTP session payloads without importing internal/.
type SessionSummary struct {
	ID         string    `json:"id"`
	ParentID   string    `json:"parentId,omitempty"`
	Title      string    `json:"title,omitempty"`
	ForkedFrom string    `json:"forkedFrom,omitempty"`
	ProjectKey string    `json:"projectKey,omitempty"`
	Open       bool      `json:"open,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt,omitempty"`
	// EventCount is set when known (inspect/get); omit on cheap list rows.
	EventCount int `json:"eventCount,omitempty"`
}

// LifecycleError is a structured host-level session lifecycle failure.
// Frontends should surface Code to automation and Message to humans.
type LifecycleError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// SessionID is the id that failed, when applicable.
	SessionID string `json:"sessionId,omitempty"`
}

func (e *LifecycleError) Error() string {
	if e == nil {
		return "lifecycle error"
	}
	if e.Code == "" {
		return e.Message
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// NewLifecycleError builds a LifecycleError.
func NewLifecycleError(code, message, sessionID string) *LifecycleError {
	return &LifecycleError{Code: code, Message: message, SessionID: sessionID}
}

// SessionListParams is the body for session.list.
type SessionListParams struct {
	// RootsOnly defaults to true when omitted on some frontends; RPC treats
	// false explicitly. When true, only sessions without a parent are returned.
	RootsOnly bool `json:"rootsOnly"`
}

// SessionListResult is the body for session.list.
type SessionListResult struct {
	Sessions []SessionSummary `json:"sessions"`
}

// SessionIDParams is a single-id request (get/load/replay/fork).
type SessionIDParams struct {
	ID string `json:"id"`
}

// SessionForkAtParams forks a prefix of a session log.
type SessionForkAtParams struct {
	ID string `json:"id"`
	// KeepEvents is how many leading events to copy. Negative means full log.
	KeepEvents int `json:"keepEvents"`
}

// SessionRewindPointsResult lists fork-at-turn candidates for a session.
type SessionRewindPointsResult struct {
	Points []RewindPoint `json:"points"`
}

// SessionReplayResult returns the raw JSONL event log.
type SessionReplayResult struct {
	ID    string `json:"id"`
	JSONL []byte `json:"jsonl"`
}

// SessionLoadResult confirms a load/resume binding.
type SessionLoadResult struct {
	ID string `json:"id"`
	// Active is true when the session is the live engine binding.
	Active bool `json:"active"`
	// Note explains deferred load (e.g. restart required) when Active is false.
	Note string `json:"note,omitempty"`
}
