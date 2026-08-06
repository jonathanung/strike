// Package secret provides secret-ref indirection and protocol-event redaction
// on top of pkg/redact (shared string scrubbing with timeline export #790).
//
// Resolve secret refs only at process exec or provider call time — never embed
// resolved values in model-facing tool output, apply_patch hunks, or fixtures.
package secret

import (
	"encoding/json"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

// Placeholder mirrors pkg/redact for callers that import only this package.
const (
	Placeholder            = redact.Placeholder
	PlaceholderHighEntropy = redact.PlaceholderHighEntropy
)

// Redact replaces credential-shaped substrings via pkg/redact.String.
func Redact(s string) string { return redact.String(s) }

// Contains reports whether s matches a known secret pattern.
func Contains(s string) bool { return redact.ContainsSecret(s) }

// ScrubToolOutput redacts known patterns and high-entropy tool-result tokens.
func ScrubToolOutput(s string) string { return redact.ScrubToolOutput(s) }

// RedactError returns a short, log-safe error string.
func RedactError(err error) string { return redact.Error(err) }

// RedactJSON walks JSON and redacts string leaves / credential fields.
func RedactJSON(raw json.RawMessage) json.RawMessage { return redact.JSON(raw) }
