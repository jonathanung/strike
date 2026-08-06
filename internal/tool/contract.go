package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ContractVersion is the current built-in tool contract schema version.
// Bump when Contract fields or ErrorCode vocabulary change meaning.
const ContractVersion = 1

// SideEffect classifies what a tool may do outside pure computation.
// Values are stable wire/API tokens (kebab-case).
type SideEffect string

const (
	SideEffectNone              SideEffect = "none"
	SideEffectRead              SideEffect = "read"
	SideEffectWorkspaceMutative SideEffect = "workspace-mutative"
	SideEffectProcess           SideEffect = "process"
	SideEffectNetwork           SideEffect = "network"
	SideEffectExternal          SideEffect = "external"
)

// Idempotency describes whether a failed or interrupted call is safe to retry.
type Idempotency string

const (
	// IdempotencySafeRetry: repeating the call with the same args is safe
	// (reads, pure queries, no durable side effects).
	IdempotencySafeRetry Idempotency = "safe-retry"
	// IdempotencyConditional: retry may be safe after checking state
	// (e.g. edit when oldString still matches).
	IdempotencyConditional Idempotency = "conditional"
	// IdempotencyUnsafe: retry can double-apply side effects (bash, network POSTs).
	IdempotencyUnsafe Idempotency = "unsafe"
)

// ErrorCode is a stable machine-readable failure class on the tool result path.
type ErrorCode string

const (
	CodePermissionDenied   ErrorCode = "permission_denied"
	CodeInvalidArgs        ErrorCode = "invalid_args"
	CodePreconditionFailed ErrorCode = "precondition_failed"
	CodeCanceled           ErrorCode = "canceled"
	CodeTimeout            ErrorCode = "timeout"
	CodeTransient          ErrorCode = "transient"
	CodeInternal           ErrorCode = "internal"
	// CodeBlocked is a non-permission policy block (hooks, phase gates).
	CodeBlocked ErrorCode = "blocked"
)

// ValidSideEffect reports whether s is a known side-effect class.
func ValidSideEffect(s SideEffect) bool {
	switch s {
	case SideEffectNone, SideEffectRead, SideEffectWorkspaceMutative,
		SideEffectProcess, SideEffectNetwork, SideEffectExternal:
		return true
	}
	return false
}

// ValidIdempotency reports whether i is a known idempotency class.
func ValidIdempotency(i Idempotency) bool {
	switch i {
	case IdempotencySafeRetry, IdempotencyConditional, IdempotencyUnsafe:
		return true
	}
	return false
}

// ValidErrorCode reports whether c is a known stable error code.
func ValidErrorCode(c ErrorCode) bool {
	switch c {
	case CodePermissionDenied, CodeInvalidArgs, CodePreconditionFailed,
		CodeCanceled, CodeTimeout, CodeTransient, CodeInternal, CodeBlocked:
		return true
	}
	return false
}

// Contract is the static declaration for one tool: versioned side-effect and
// idempotency metadata. Input JSON Schema remains Tool.Schema(); optional
// output schema may be added in a later contract version.
type Contract struct {
	Version     int         `json:"version"`
	SideEffect  SideEffect  `json:"sideEffect"`
	Idempotency Idempotency `json:"idempotency"`
}

// Validate checks contract field vocabulary and version.
func (c Contract) Validate() error {
	if c.Version < 1 {
		return fmt.Errorf("contract version must be >= 1")
	}
	if !ValidSideEffect(c.SideEffect) {
		return fmt.Errorf("unknown sideEffect %q", c.SideEffect)
	}
	if !ValidIdempotency(c.Idempotency) {
		return fmt.Errorf("unknown idempotency %q", c.Idempotency)
	}
	return nil
}

// Contractor is optionally implemented by tools that declare a static Contract.
// Tools without Contractor receive DefaultContract via LookupContract.
type Contractor interface {
	Contract() Contract
}

// DefaultContract is used when a tool does not implement Contractor
// (unknown/plugin tools default to external + conditional).
func DefaultContract() Contract {
	return Contract{
		Version:     ContractVersion,
		SideEffect:  SideEffectExternal,
		Idempotency: IdempotencyConditional,
	}
}

// LookupContract returns t's declared contract, or DefaultContract.
func LookupContract(t Tool) Contract {
	if t == nil {
		return DefaultContract()
	}
	if c, ok := t.(Contractor); ok {
		return c.Contract()
	}
	return DefaultContract()
}

// staticContract builds a v1 contract (shared by built-in Contract methods).
func staticContract(se SideEffect, id Idempotency) Contract {
	return Contract{Version: ContractVersion, SideEffect: se, Idempotency: id}
}

// Error is a structured tool failure with a stable code for orchestrators
// and model-facing settlement. Message remains human-readable; Code is the
// machine token. Implements error.
type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Details   json.RawMessage
}

func (e *Error) Error() string {
	if e == nil {
		return string(CodeInternal)
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Code != "" {
		return string(e.Code)
	}
	return string(CodeInternal)
}

// IsRetryable reports whether e may be retried (false on nil).
func (e *Error) IsRetryable() bool {
	return e != nil && e.Retryable
}

// ErrInvalidArgs returns a non-retryable invalid_args error.
func ErrInvalidArgs(msg string) *Error {
	return &Error{Code: CodeInvalidArgs, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrPrecondition returns a non-retryable precondition_failed error.
func ErrPrecondition(msg string) *Error {
	return &Error{Code: CodePreconditionFailed, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrPermissionDenied returns a non-retryable permission_denied error.
func ErrPermissionDenied(msg string) *Error {
	return &Error{Code: CodePermissionDenied, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrTimeout returns a retryable timeout error.
func ErrTimeout(msg string) *Error {
	return &Error{Code: CodeTimeout, Message: strings.TrimSpace(msg), Retryable: true}
}

// ErrTransient returns a retryable transient error.
func ErrTransient(msg string) *Error {
	return &Error{Code: CodeTransient, Message: strings.TrimSpace(msg), Retryable: true}
}

// ErrCanceled returns a non-retryable canceled error.
func ErrCanceled(msg string) *Error {
	return &Error{Code: CodeCanceled, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrInternal returns a non-retryable internal error (fallback).
func ErrInternal(msg string) *Error {
	return &Error{Code: CodeInternal, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrBlocked returns a non-retryable blocked error (hooks/policy).
func ErrBlocked(msg string) *Error {
	return &Error{Code: CodeBlocked, Message: strings.TrimSpace(msg), Retryable: false}
}

// Classify maps an arbitrary error onto a structured *Error.
// Already-structured *Error values are returned as-is (cloned message only).
// Unknown errors become CodeInternal (non-retryable) so callers never panic.
func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	var te *Error
	if errors.As(err, &te) && te != nil {
		return te
	}
	var escape *WorkspaceEscapeError
	if errors.As(err, &escape) {
		return ErrPrecondition(err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errTimeoutSentinel) {
		return ErrTimeout(err.Error())
	}
	if errors.Is(err, context.Canceled) {
		return ErrCanceled(err.Error())
	}
	// Heuristic for unmigrated tools that still use fmt.Errorf("invalid arguments: …").
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "invalid argument") || strings.HasPrefix(lower, "invalid arguments") {
		return ErrInvalidArgs(msg)
	}
	return ErrInternal(msg)
}

// errTimeoutSentinel lets callers mark timeouts without importing net/http.
// time.ErrNoDeadline is not right; use errors.Is with context or wrap with Timeout.
var errTimeoutSentinel = errors.New("tool: timeout sentinel")

// AsTimeout wraps err as a timeout-class failure for Classify (optional helper).
func AsTimeout(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", errTimeoutSentinel, err)
}

// IsTimeout reports whether err is a deadline/timeout class (context or marked).
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errTimeoutSentinel) {
		return true
	}
	var te *Error
	if errors.As(err, &te) && te != nil && te.Code == CodeTimeout {
		return true
	}
	// net.Error Timeout() without importing net: message heuristic only as last resort.
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "timed out") || strings.Contains(lower, "deadline exceeded")
}

// RetryableForCode returns the default retryability for a stable code.
func RetryableForCode(code ErrorCode) bool {
	switch code {
	case CodeTimeout, CodeTransient:
		return true
	default:
		return false
	}
}
