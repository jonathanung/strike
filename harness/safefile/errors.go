package safefile

import (
	"errors"
	"fmt"
)

// Stable error codes for rejected path/file types (tool contracts).
const (
	CodeSpecialFile = "special_file"
	CodeSymlink     = "symlink_refused"
	CodeTimeout     = "timeout"
	CodeNotRegular  = "not_regular"
	CodeInvalidPath = "invalid_path"
)

// Error is a classified safefile failure.
type Error struct {
	Code    string
	Path    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return "safefile error"
	}
	if e.Message != "" && e.Code != "" {
		return e.Code + ": " + e.Message
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// IsCode reports whether err is a safefile.Error with the given code.
func IsCode(err error, code string) bool {
	var se *Error
	if !errors.As(err, &se) || se == nil {
		return false
	}
	return se.Code == code
}

func errf(code, path, format string, args ...any) *Error {
	return &Error{Code: code, Path: path, Message: fmt.Sprintf(format, args...)}
}
