package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// RetryDecision is the harness action for one failed tool attempt.
// Values are stable policy tokens (not wire event types).
type RetryDecision string

const (
	// DecisionFail settles the error with no auto-retry.
	DecisionFail RetryDecision = "fail"
	// DecisionRetry auto-retries the same call after backoff (safe-retry only).
	DecisionRetry RetryDecision = "retry"
	// DecisionRecover settles without auto-retry and attaches a recovery hint
	// so the model can replan (e.g. precondition_failed on conditional tools).
	DecisionRecover RetryDecision = "recover"
)

// DecideRetry implements the error-code × idempotency policy table.
//
//	                 | safe-retry | conditional | unsafe
//	-----------------+------------+-------------+--------
//	transient        | retry      | fail        | fail
//	timeout          | retry      | fail        | fail
//	precondition_*   | recover    | recover     | fail
//	permission_*     | fail       | fail        | fail
//	sandbox_denied   | fail       | fail        | fail
//	invalid_args     | fail       | fail        | fail
//	canceled         | fail       | fail        | fail
//	blocked          | fail       | fail        | fail
//	internal         | fail       | fail        | fail
//	(other/empty)    | fail       | fail        | fail
//
// Mutative / process tools (conditional or unsafe) never auto-retry on
// generic or transient failure — that prevents double-apply of apply_patch,
// edit, write, bash, etc. Only IdempotencySafeRetry tools may auto-retry,
// and only for transient/timeout codes.
func DecideRetry(code ErrorCode, id Idempotency) RetryDecision {
	// Unsafe never auto-retries (bash, network POSTs, unknown external).
	if id == IdempotencyUnsafe {
		return DecisionFail
	}
	switch code {
	case CodeTransient, CodeTimeout:
		if id == IdempotencySafeRetry {
			return DecisionRetry
		}
		// conditional + workspace-mutative: no blind mutation retry
		return DecisionFail
	case CodePreconditionFailed:
		// Re-validate / replan rather than blind retry (edit oldString mismatch).
		if id == IdempotencySafeRetry || id == IdempotencyConditional {
			return DecisionRecover
		}
		return DecisionFail
	case CodePermissionDenied, CodeSandboxDenied, CodeInvalidArgs, CodeCanceled,
		CodeBlocked, CodeInternal:
		return DecisionFail
	default:
		return DecisionFail
	}
}

// RecoveryHint returns model-facing guidance when decision is recover.
// Empty when no recovery path applies.
func RecoveryHint(code ErrorCode, decision RetryDecision) string {
	if decision != DecisionRecover {
		return ""
	}
	switch code {
	case CodePreconditionFailed:
		return "[recovery: re-read current state and replan; do not retry the same args unchanged]"
	default:
		return "[recovery: change approach or escalate to the user; do not blind-retry]"
	}
}

// AppendRecoveryHint appends a recovery line to model-facing output when needed.
func AppendRecoveryHint(output string, code ErrorCode, decision RetryDecision) string {
	hint := RecoveryHint(code, decision)
	if hint == "" {
		return output
	}
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return hint
	}
	if strings.Contains(output, "[recovery:") {
		return output
	}
	return output + "\n" + hint
}

// Default tool-retry knobs (engine/config may override).
const (
	DefaultToolRetryMaxAttempts = 3
	DefaultToolRetryBaseDelay   = 200 * time.Millisecond
	DefaultToolRetryMaxDelay    = 2 * time.Second
	DefaultToolLoopThreshold    = 3
	DefaultToolLoopWindow       = 8
)

// ToolRetryDelay returns exponential backoff before nextAttempt (1-based, >=2),
// with full jitter in [0, delay]. Caps at maxDelay. base/max <=0 use defaults.
func ToolRetryDelay(nextAttempt int, base, maxDelay time.Duration) time.Duration {
	if base <= 0 {
		base = DefaultToolRetryBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = DefaultToolRetryMaxDelay
	}
	shift := nextAttempt - 2
	if shift < 0 {
		shift = 0
	}
	d := base << shift
	if d > maxDelay {
		d = maxDelay
	}
	if d <= 0 {
		return 0
	}
	// Full jitter: uniform [0, d] so concurrent agents do not thundering-herd.
	return time.Duration(rand.Int63n(int64(d) + 1))
}

// ArgsFingerprint is a stable short hash of tool args for loop detection.
// Empty/invalid JSON still fingerprints the raw bytes.
func ArgsFingerprint(args json.RawMessage) string {
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:8])
}

// CallSignature identifies one logical tool invocation for loop detection.
func CallSignature(name string, args json.RawMessage) string {
	return fmt.Sprintf("%s\x00%s", strings.TrimSpace(name), ArgsFingerprint(args))
}
