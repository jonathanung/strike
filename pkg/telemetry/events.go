package telemetry

import "encoding/json"

// Export record structs for security/harness telemetry families.
// JSON tags MUST match schemas/telemetry/v1/registry.json field names.
// telemetry tags carry redaction policy for drift checks and RedactRecord.
//
// Pointer fields mean "optional / omit when unset" for numeric zeros that
// are meaningful when present (token counts, durations).

// ToolEvent is family "tool".
type ToolEvent struct {
	CallID        string `json:"callId" telemetry:"redact=none"`
	Name          string `json:"name" telemetry:"redact=none"`
	SessionID     string `json:"sessionId,omitempty" telemetry:"redact=none"`
	TurnID        string `json:"turnId,omitempty" telemetry:"redact=none"`
	State         string `json:"state,omitempty" telemetry:"redact=none"`
	ArgsPreview   string `json:"argsPreview,omitempty" telemetry:"redact=scrub"`
	OutputPreview string `json:"outputPreview,omitempty" telemetry:"redact=scrub"`
	Error         string `json:"error,omitempty" telemetry:"redact=scrub"`
	ErrorCode     string `json:"errorCode,omitempty" telemetry:"redact=none"`
	DurationMs    *int64 `json:"durationMs,omitempty" telemetry:"redact=none"`
}

// PermissionEvent is family "permission".
type PermissionEvent struct {
	RequestID      string   `json:"requestId,omitempty" telemetry:"redact=none"`
	Permission     string   `json:"permission" telemetry:"redact=none"`
	Patterns       []string `json:"patterns,omitempty" telemetry:"redact=scrub"`
	Action         string   `json:"action" telemetry:"redact=none"`
	Decision       string   `json:"decision,omitempty" telemetry:"redact=none"`
	Layer          string   `json:"layer,omitempty" telemetry:"redact=none"`
	RulePermission string   `json:"rulePermission,omitempty" telemetry:"redact=none"`
	RulePattern    string   `json:"rulePattern,omitempty" telemetry:"redact=scrub"`
	RuleAction     string   `json:"ruleAction,omitempty" telemetry:"redact=none"`
	SessionID      string   `json:"sessionId,omitempty" telemetry:"redact=none"`
	TurnID         string   `json:"turnId,omitempty" telemetry:"redact=none"`
	ToolCallID     string   `json:"toolCallId,omitempty" telemetry:"redact=none"`
}

// SandboxEvent is family "sandbox".
type SandboxEvent struct {
	Mode             string `json:"mode" telemetry:"redact=none"`
	NoNetwork        bool   `json:"noNetwork,omitempty" telemetry:"redact=none"`
	NoWorkspaceWrite bool   `json:"noWorkspaceWrite,omitempty" telemetry:"redact=none"`
	DeniedPath       string `json:"deniedPath,omitempty" telemetry:"redact=scrub"`
	Reason           string `json:"reason,omitempty" telemetry:"redact=scrub"`
	ErrorCode        string `json:"errorCode,omitempty" telemetry:"redact=none"`
	SessionID        string `json:"sessionId,omitempty" telemetry:"redact=none"`
	TurnID           string `json:"turnId,omitempty" telemetry:"redact=none"`
	ToolCallID       string `json:"toolCallId,omitempty" telemetry:"redact=none"`
}

// UsageEvent is family "usage".
type UsageEvent struct {
	SessionID           string `json:"sessionId,omitempty" telemetry:"redact=none"`
	TurnID              string `json:"turnId,omitempty" telemetry:"redact=none"`
	ProviderRequestID   string `json:"providerRequestId,omitempty" telemetry:"redact=none"`
	InputTokens         *int   `json:"inputTokens,omitempty" telemetry:"redact=none"`
	OutputTokens        *int   `json:"outputTokens,omitempty" telemetry:"redact=none"`
	CacheReadTokens     *int   `json:"cacheReadTokens,omitempty" telemetry:"redact=none"`
	CacheCreationTokens *int   `json:"cacheCreationTokens,omitempty" telemetry:"redact=none"`
	Source              string `json:"source,omitempty" telemetry:"redact=none"`
	Model               string `json:"model,omitempty" telemetry:"redact=none"`
}

// ErrorEvent is family "error".
type ErrorEvent struct {
	Code       string `json:"code" telemetry:"redact=none"`
	Message    string `json:"message" telemetry:"redact=scrub"`
	Retryable  bool   `json:"retryable,omitempty" telemetry:"redact=none"`
	SessionID  string `json:"sessionId,omitempty" telemetry:"redact=none"`
	TurnID     string `json:"turnId,omitempty" telemetry:"redact=none"`
	ToolCallID string `json:"toolCallId,omitempty" telemetry:"redact=none"`
	Component  string `json:"component,omitempty" telemetry:"redact=none"`
}

// EgressEvent is family "egress".
type EgressEvent struct {
	Destination      string `json:"destination,omitempty" telemetry:"redact=scrub"`
	DestinationClass string `json:"destinationClass,omitempty" telemetry:"redact=class"`
	Tool             string `json:"tool" telemetry:"redact=none"`
	Action           string `json:"action" telemetry:"redact=none"`
	Reason           string `json:"reason,omitempty" telemetry:"redact=scrub"`
	SessionID        string `json:"sessionId,omitempty" telemetry:"redact=none"`
	TurnID           string `json:"turnId,omitempty" telemetry:"redact=none"`
	ToolCallID       string `json:"toolCallId,omitempty" telemetry:"redact=none"`
}

// AdmissionEvent is family "admission".
type AdmissionEvent struct {
	Pool       string `json:"pool,omitempty" telemetry:"redact=none"`
	State      string `json:"state" telemetry:"redact=none"`
	Reason     string `json:"reason,omitempty" telemetry:"redact=scrub"`
	WaitMs     *int64 `json:"waitMs,omitempty" telemetry:"redact=none"`
	SessionID  string `json:"sessionId,omitempty" telemetry:"redact=none"`
	TurnID     string `json:"turnId,omitempty" telemetry:"redact=none"`
	ToolCallID string `json:"toolCallId,omitempty" telemetry:"redact=none"`
	ChainID    string `json:"chainId,omitempty" telemetry:"redact=none"`
}

// Envelope is one exported telemetry record with family discriminator.
// Used by audit sink and machine-readable bundles; not a protocol Event.
type Envelope struct {
	SchemaVersion string          `json:"schemaVersion"`
	Family        string          `json:"family"`
	Time          string          `json:"time,omitempty"` // RFC3339Nano UTC
	Payload       json.RawMessage `json:"payload"`
}

// goTypes maps family id → zero value used for reflection drift checks.
func goTypes() map[string]any {
	return map[string]any{
		FamilyTool:       ToolEvent{},
		FamilyPermission: PermissionEvent{},
		FamilySandbox:    SandboxEvent{},
		FamilyUsage:      UsageEvent{},
		FamilyError:      ErrorEvent{},
		FamilyEgress:     EgressEvent{},
		FamilyAdmission:  AdmissionEvent{},
	}
}
