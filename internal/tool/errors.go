package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrorCode is a stable machine-readable failure class on the tool result path.
// Values match protocol.ToolResultError.code and CodedError.Code.
type ErrorCode string

// Stable tool error codes (tool contracts #793; FS tx #797 uses precondition_failed).
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

// ValidErrorCode reports whether c is a known stable error code.
func ValidErrorCode(c ErrorCode) bool {
	switch c {
	case CodePermissionDenied, CodeInvalidArgs, CodePreconditionFailed,
		CodeCanceled, CodeTimeout, CodeTransient, CodeInternal, CodeBlocked:
		return true
	}
	return false
}

// CodedError is a tool failure with a stable machine-readable code.
// Error() formats as "code: message" so model-facing Output stays greppable;
// engine settlement also copies Code/Retryable onto protocol.ToolCallEnd.error
// and provider.ToolResult.
type CodedError struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Details   json.RawMessage
}

func (e *CodedError) Error() string {
	if e == nil {
		return string(CodeInternal)
	}
	msg := strings.TrimSpace(e.Message)
	code := strings.TrimSpace(string(e.Code))
	switch {
	case code == "" && msg == "":
		return "tool error"
	case code == "":
		return msg
	case msg == "":
		return code
	default:
		return code + ": " + msg
	}
}

// IsRetryable reports whether e may be retried (false on nil).
func (e *CodedError) IsRetryable() bool {
	return e != nil && e.Retryable
}

// CodeOf extracts a stable code from err when present (empty otherwise).
func CodeOf(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) && ce != nil {
		return string(ce.Code)
	}
	return ""
}

// InvalidArgs returns a non-retryable invalid_args error.
func InvalidArgs(msg string) error {
	return ErrInvalidArgs(msg)
}

// PreconditionFailed returns a non-retryable precondition_failed error.
func PreconditionFailed(msg string) error {
	return ErrPrecondition(msg)
}

// ErrInvalidArgs returns a non-retryable invalid_args error.
func ErrInvalidArgs(msg string) *CodedError {
	return &CodedError{Code: CodeInvalidArgs, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrPrecondition returns a non-retryable precondition_failed error.
func ErrPrecondition(msg string) *CodedError {
	return &CodedError{Code: CodePreconditionFailed, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrPermissionDenied returns a non-retryable permission_denied error.
func ErrPermissionDenied(msg string) *CodedError {
	return &CodedError{Code: CodePermissionDenied, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrTimeout returns a retryable timeout error.
func ErrTimeout(msg string) *CodedError {
	return &CodedError{Code: CodeTimeout, Message: strings.TrimSpace(msg), Retryable: true}
}

// ErrTransient returns a retryable transient error.
func ErrTransient(msg string) *CodedError {
	return &CodedError{Code: CodeTransient, Message: strings.TrimSpace(msg), Retryable: true}
}

// ErrCanceled returns a non-retryable canceled error.
func ErrCanceled(msg string) *CodedError {
	return &CodedError{Code: CodeCanceled, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrInternal returns a non-retryable internal error (fallback).
func ErrInternal(msg string) *CodedError {
	return &CodedError{Code: CodeInternal, Message: strings.TrimSpace(msg), Retryable: false}
}

// ErrBlocked returns a non-retryable blocked error (hooks/policy).
func ErrBlocked(msg string) *CodedError {
	return &CodedError{Code: CodeBlocked, Message: strings.TrimSpace(msg), Retryable: false}
}

// Classify maps an arbitrary error onto a structured *CodedError.
// Already-structured *CodedError values are returned as-is.
// Unknown errors become CodeInternal (non-retryable) so callers never panic.
func Classify(err error) *CodedError {
	if err == nil {
		return nil
	}
	var ce *CodedError
	if errors.As(err, &ce) && ce != nil {
		return ce
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

// errTimeoutSentinel lets callers mark timeouts without importing net.
var errTimeoutSentinel = errors.New("tool: timeout sentinel")

// AsTimeout wraps err as a timeout-class failure for Classify.
func AsTimeout(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", errTimeoutSentinel, err)
}

// IsTimeout reports whether err is a deadline/timeout class.
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errTimeoutSentinel) {
		return true
	}
	var ce *CodedError
	if errors.As(err, &ce) && ce != nil && ce.Code == CodeTimeout {
		return true
	}
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
