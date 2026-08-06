package protocol

// Stable tool/engine error codes for model-facing results and timeline events.
// Keep in lockstep with internal/tool.ErrorCode / CodedError vocabulary (#793).
const (
	// ErrorCodePermissionDenied is a hard ruleset deny or interactive reject.
	ErrorCodePermissionDenied = "permission_denied"
	// ErrorCodeInvalidArgs is malformed or semantically invalid tool input.
	ErrorCodeInvalidArgs = "invalid_args"
	// ErrorCodePreconditionFailed is a failed state check (freshness, baseHash, …).
	ErrorCodePreconditionFailed = "precondition_failed"
	// ErrorCodeCanceled is a user/parent interrupt of an in-flight tool or turn.
	ErrorCodeCanceled = "canceled"
	// ErrorCodeTimeout is a per-tool or per-turn deadline expiry.
	ErrorCodeTimeout = "timeout"
	// ErrorCodeTransient is a retryable infrastructure/network failure.
	ErrorCodeTransient = "transient"
	// ErrorCodeInternal is the fallback for unknown failures.
	ErrorCodeInternal = "internal"
	// ErrorCodeBlocked is a non-permission policy block (hooks, phase gates).
	ErrorCodeBlocked = "blocked"
	// ErrorCodeSandboxDenied is an OS sandbox capability block (bwrap/seatbelt
	// path/syscall denial). Distinct from permission_denied (ruleset/ask).
	ErrorCodeSandboxDenied = "sandbox_denied"
	// ErrorCodeQueueFull is backpressure rejection when a bounded queue is full
	// (e.g. mid-turn user-input buffer). Callers should retry after the turn.
	ErrorCodeQueueFull = "queue_full"
)
