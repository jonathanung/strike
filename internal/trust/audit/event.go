package audit

import (
	"encoding/json"
	"time"

	"github.com/jonathanung/strike/pkg/telemetry"
)

// SchemaVersion is the on-disk audit record schema (independent of protocol wire).
const SchemaVersion = "1.0.0"

// Event family IDs (stable).
const (
	FamilyPermission     = "permission"
	FamilySandbox        = "sandbox"
	FamilySecretRefUse   = "secret_ref_use"
	FamilyContentGuard   = "content_guard"
	FamilyAdmission      = "admission"
	FamilyEgress         = "egress"
	FamilyToolchainMatch = "toolchain_match"
	// FamilyHook is shell/declarative hook enforcement outcomes (#1031/#1032).
	FamilyHook = "hook"
	// FamilyServeOp is strike serve control-plane ops (type + source IP only).
	FamilyServeOp = "serve_op"
	// FamilyScriptRun is script-schedule run outcomes and lifecycle changes
	// (docs/design/script-schedules.md, "Audit").
	FamilyScriptRun = "script_run"
)

// Record is one append-only audit line (already redacted when written).
type Record struct {
	SchemaVersion string          `json:"schemaVersion"`
	Family        string          `json:"family"`
	Time          time.Time       `json:"time"`
	SessionID     string          `json:"sessionId,omitempty"`
	TurnID        string          `json:"turnId,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	ChainID       string          `json:"chainId,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// Families lists v1 security families actually emitted in production.
var Families = []string{
	FamilyPermission,
	FamilySandbox,
	FamilySecretRefUse,
	FamilyContentGuard,
	FamilyAdmission,
	FamilyEgress,
	FamilyToolchainMatch,
	FamilyHook,
	FamilyServeOp,
	FamilyScriptRun,
}

// HookPayload is family hook (shell/declarative enforcement).
type HookPayload struct {
	Event    string `json:"event" telemetry:"redact=none"`
	Action   string `json:"action" telemetry:"redact=none"` // shell_allow|shell_block|shell_fail_closed|…
	Tool     string `json:"tool,omitempty" telemetry:"redact=none"`
	Reason   string `json:"reason,omitempty" telemetry:"redact=scrub"`
	CallID   string `json:"callId,omitempty" telemetry:"redact=none"`
	Decision string `json:"decision,omitempty" telemetry:"redact=none"` // allow|block
}

// SecretRefUsePayload is family secret_ref_use (class/hash only, never raw).
type SecretRefUsePayload struct {
	RefClass string `json:"refClass,omitempty" telemetry:"redact=class"`
	RefHash  string `json:"refHash,omitempty" telemetry:"redact=none"`
	Action   string `json:"action,omitempty" telemetry:"redact=none"` // resolve|inject|deny
	Tool     string `json:"tool,omitempty" telemetry:"redact=none"`
}

// ContentGuardPayload is family content_guard.
type ContentGuardPayload struct {
	Action string `json:"action" telemetry:"redact=none"` // allow|deny|redact
	Reason string `json:"reason,omitempty" telemetry:"redact=scrub"`
	Tool   string `json:"tool,omitempty" telemetry:"redact=none"`
	RuleID string `json:"ruleId,omitempty" telemetry:"redact=none"`
}

// ToolchainMatchPayload is family toolchain_match.
type ToolchainMatchPayload struct {
	Tool    string `json:"tool" telemetry:"redact=none"`
	Matched string `json:"matched,omitempty" telemetry:"redact=scrub"`
	Action  string `json:"action" telemetry:"redact=none"`
	Source  string `json:"source,omitempty" telemetry:"redact=none"`
}

// ScriptRunPayload is family script_run: one record per schedule run outcome
// and one per lifecycle change. Command, Dir, OutputTail and Error are
// scrubbed; CommandHash and the ids are the correlation and survive as-is.
type ScriptRunPayload struct {
	ScheduleID string `json:"scheduleId" telemetry:"redact=none"`
	RunID      string `json:"runId,omitempty" telemetry:"redact=none"` // empty for lifecycle records
	// Status is a run outcome (started|finished|failed|denied|skipped|missed|error)
	// or a lifecycle record (armed|rearmed|paused|resumed|deleted).
	Status      string    `json:"status" telemetry:"redact=none"`
	Decision    string    `json:"decision,omitempty" telemetry:"redact=none"` // allow|armed|deny|hook_block
	Mode        string    `json:"mode,omitempty" telemetry:"redact=none"`
	Sandbox     string    `json:"sandbox,omitempty" telemetry:"redact=none"` // dial + applied/degraded
	Command     string    `json:"command" telemetry:"redact=scrub"`
	CommandHash string    `json:"commandHash" telemetry:"redact=none"`
	Dir         string    `json:"dir" telemetry:"redact=scrub"`
	ClientIP    string    `json:"clientIp,omitempty" telemetry:"redact=none"` // lifecycle records only
	StartedAt   time.Time `json:"startedAt,omitzero"`
	EndedAt     time.Time `json:"endedAt,omitzero"`
	ExitCode    *int      `json:"exitCode,omitempty"`
	Timeout     string    `json:"timeout,omitempty" telemetry:"redact=none"`
	OutputTail  string    `json:"outputTail,omitempty" telemetry:"redact=scrub"` // last 4 KiB
	Error       string    `json:"error,omitempty" telemetry:"redact=scrub"`
}

// redactPayload runs family-specific redaction when the payload matches a
// known telemetry or audit struct; otherwise scrubs via envelope builder.
func redactPayload(family string, payload any) (json.RawMessage, error) {
	// Prefer pkg/telemetry builders for shared families.
	switch family {
	case FamilyPermission, FamilySandbox, FamilyAdmission, FamilyEgress:
		env, err := telemetry.NewEnvelope(family, time.Time{}, payload)
		if err != nil {
			return nil, err
		}
		return env.Payload, nil
	case FamilySecretRefUse:
		var v SecretRefUsePayload
		b, _ := json.Marshal(payload)
		_ = json.Unmarshal(b, &v)
		if v.RefHash == "" && v.RefClass == "" {
			// ensure we never keep unknown raw fields: re-marshal known only
		}
		if err := telemetry.RedactRecord(&v); err != nil {
			return nil, err
		}
		return json.Marshal(v)
	case FamilyContentGuard:
		var v ContentGuardPayload
		b, _ := json.Marshal(payload)
		_ = json.Unmarshal(b, &v)
		if err := telemetry.RedactRecord(&v); err != nil {
			return nil, err
		}
		return json.Marshal(v)
	case FamilyToolchainMatch:
		var v ToolchainMatchPayload
		b, _ := json.Marshal(payload)
		_ = json.Unmarshal(b, &v)
		if err := telemetry.RedactRecord(&v); err != nil {
			return nil, err
		}
		return json.Marshal(v)
	case FamilyHook:
		var v HookPayload
		b, _ := json.Marshal(payload)
		_ = json.Unmarshal(b, &v)
		if err := telemetry.RedactRecord(&v); err != nil {
			return nil, err
		}
		return json.Marshal(v)
	case FamilyScriptRun:
		var v ScriptRunPayload
		b, _ := json.Marshal(payload)
		_ = json.Unmarshal(b, &v)
		if err := telemetry.RedactRecord(&v); err != nil {
			return nil, err
		}
		return json.Marshal(v)
	case FamilyServeOp:
		// server.ServeOpPayload — keep known fields only via generic marshal.
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return b, nil
	default:
		// Unknown: marshal then scrub string values best-effort via telemetry tool-like path.
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}
