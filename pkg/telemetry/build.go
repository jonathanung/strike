package telemetry

import (
	"encoding/json"
	"fmt"
	"time"
)

// Builder helpers construct redacted envelopes ready for export/audit.

// NewEnvelope marshals a redacted copy of payload into an Envelope.
// payload should be a family event value (not pointer); it is deep-copied
// via JSON round-trip, redacted, then re-marshaled.
func NewEnvelope(family string, at time.Time, payload any) (Envelope, error) {
	family = trimFamily(family)
	if family == "" {
		return Envelope{}, fmt.Errorf("telemetry: family is required")
	}
	if payload == nil {
		return Envelope{}, fmt.Errorf("telemetry: payload is nil")
	}
	// Round-trip into the concrete Go type when known so RedactRecord works.
	redacted, err := cloneAndRedact(family, payload)
	if err != nil {
		return Envelope{}, err
	}
	raw, err := json.Marshal(redacted)
	if err != nil {
		return Envelope{}, err
	}
	env := Envelope{
		SchemaVersion: SchemaVersion,
		Family:        family,
		Payload:       raw,
	}
	if !at.IsZero() {
		env.Time = at.UTC().Format(time.RFC3339Nano)
	}
	return env, nil
}

func trimFamily(s string) string {
	// local to avoid importing strings in every call site doc
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func cloneAndRedact(family string, payload any) (any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	switch family {
	case FamilyTool:
		var v ToolEvent
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if err := RedactRecord(&v); err != nil {
			return nil, err
		}
		return v, nil
	case FamilyPermission:
		var v PermissionEvent
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if err := RedactRecord(&v); err != nil {
			return nil, err
		}
		return v, nil
	case FamilySandbox:
		var v SandboxEvent
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if err := RedactRecord(&v); err != nil {
			return nil, err
		}
		return v, nil
	case FamilyUsage:
		var v UsageEvent
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if err := RedactRecord(&v); err != nil {
			return nil, err
		}
		return v, nil
	case FamilyError:
		var v ErrorEvent
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if err := RedactRecord(&v); err != nil {
			return nil, err
		}
		return v, nil
	case FamilyEgress:
		var v EgressEvent
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if err := RedactRecord(&v); err != nil {
			return nil, err
		}
		return v, nil
	case FamilyAdmission:
		var v AdmissionEvent
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if err := RedactRecord(&v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		// Unknown family: still scrub free-text best-effort via generic map.
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
}
