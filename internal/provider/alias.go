package provider

import (
	"encoding/json"
	"time"

	pub "github.com/jonathanung/strike-cli/provider"
)

type (
	Role            = pub.Role
	Image           = pub.Image
	Message         = pub.Message
	ToolCall        = pub.ToolCall
	ToolResult      = pub.ToolResult
	ToolSchema      = pub.ToolSchema
	Request         = pub.Request
	StreamEventType = pub.StreamEventType
	Usage           = pub.Usage
	StreamEvent     = pub.StreamEvent
	Effort          = pub.Effort
	Provider        = pub.Provider
)

const (
	RoleUser      = pub.RoleUser
	RoleAssistant = pub.RoleAssistant
	RoleTool      = pub.RoleTool

	EventTextDelta = pub.EventTextDelta
	EventToolCall  = pub.EventToolCall
	EventReasoning = pub.EventReasoning
	EventDone      = pub.EventDone
	EventError     = pub.EventError

	EffortDefault = pub.EffortDefault
	EffortOff     = pub.EffortOff
	EffortLow     = pub.EffortLow
	EffortMedium  = pub.EffortMedium
	EffortHigh    = pub.EffortHigh
	EffortXHigh   = pub.EffortXHigh
	EffortMax     = pub.EffortMax
)

var ErrIncompleteStream = pub.ErrIncompleteStream

func ReasoningText(raw json.RawMessage) string { return pub.ReasoningText(raw) }
func Efforts() []Effort                        { return pub.Efforts() }
func ParseEffort(value string) (Effort, bool)  { return pub.ParseEffort(value) }
func NormalizeStream(in <-chan StreamEvent) <-chan StreamEvent {
	return pub.NormalizeStream(in)
}
func RetryAfter(err error) (time.Duration, bool) { return pub.RetryAfter(err) }
func IsRetryable(err error) bool                 { return pub.IsRetryable(err) }
func IsContextOverflow(err error) bool           { return pub.IsContextOverflow(err) }
