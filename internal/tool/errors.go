package tool

import (
	"errors"
	"strings"
)

// Stable tool error codes (aligned with #793 tool contracts). FS transaction
// safety (#797) uses precondition_failed for base-hash / freshness mismatches
// and concurrent modification. Full side-effect/idempotency metadata lands with #793.
const (
	CodeInvalidArgs        = "invalid_args"
	CodePermissionDenied   = "permission_denied"
	CodePreconditionFailed = "precondition_failed"
	CodeCanceled           = "canceled"
	CodeTimeout            = "timeout"
	CodeTransient          = "transient"
	CodeInternal           = "internal"
)

// CodedError is a tool failure with a stable machine-readable code.
// Error() formats as "code: message" so model-facing feedback stays greppable
// before full structured tool-result contracts (#793) ship.
type CodedError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *CodedError) Error() string {
	if e == nil {
		return ""
	}
	msg := strings.TrimSpace(e.Message)
	code := strings.TrimSpace(e.Code)
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

// PreconditionFailed returns a non-retryable precondition_failed error.
func PreconditionFailed(msg string) error {
	return &CodedError{Code: CodePreconditionFailed, Message: strings.TrimSpace(msg), Retryable: false}
}

// InvalidArgs returns a non-retryable invalid_args error.
func InvalidArgs(msg string) error {
	return &CodedError{Code: CodeInvalidArgs, Message: strings.TrimSpace(msg), Retryable: false}
}

// CodeOf extracts a stable code from err when present.
func CodeOf(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) && ce != nil {
		return ce.Code
	}
	return ""
}
