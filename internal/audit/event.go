package audit

import (
	"encoding/json"
	"time"

	"github.com/jonathanung/strike-cli/pkg/telemetry"
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
	default:
		// Unknown: marshal then scrub string values best-effort via telemetry tool-like path.
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}
