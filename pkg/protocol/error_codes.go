package protocol

// Stable tool/engine error codes for model-facing results and timeline events.
// Soft-coord with the broader tool-contract work (#793): cancel/timeout land
// here first; additional codes (permission_denied, invalid_args, …) extend
// the same string vocabulary without renaming these values.
const (
	// ErrorCodeCanceled is a user/parent interrupt of an in-flight tool or turn.
	ErrorCodeCanceled = "canceled"
	// ErrorCodeTimeout is a per-tool or per-turn deadline expiry.
	ErrorCodeTimeout = "timeout"
	// ErrorCodeQueueFull is backpressure rejection when a bounded queue is full
	// (e.g. mid-turn user-input buffer). Callers should retry after the turn.
	ErrorCodeQueueFull = "queue_full"
)
