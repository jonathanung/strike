package telemetry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SchemaVersion is the export document / registry schema version.
// Independent of pkg/protocol.Version (Op/Event wire).
const SchemaVersion = "1.0.0"

// Core family IDs (stable for exporters and audit sink).
const (
	FamilyTool       = "tool"
	FamilyPermission = "permission"
	FamilySandbox    = "sandbox"
	FamilyUsage      = "usage"
	FamilyError      = "error"
	FamilyEgress     = "egress"
	FamilyAdmission  = "admission"
)

// CoreFamilyIDs is the ordered list of families required in v1.
var CoreFamilyIDs = []string{
	FamilyTool,
	FamilyPermission,
	FamilySandbox,
	FamilyUsage,
	FamilyError,
	FamilyEgress,
	FamilyAdmission,
}

// Redaction policy names (must match registry redactionPolicies keys).
const (
	RedactNone  = "none"
	RedactScrub = "scrub"
	RedactHash  = "hash"
	RedactClass = "class"
	RedactOmit  = "omit"
)

//go:embed registry.json
var embeddedRegistryJSON []byte

// FieldSpec is one field in a family registry entry.
type FieldSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Redact      string `json:"redact"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

// FamilySpec is one telemetry family in the registry.
type FamilySpec struct {
	ID          string      `json:"id"`
	Description string      `json:"description,omitempty"`
	Fields      []FieldSpec `json:"fields"`
}

// Registry is the versioned family catalog.
type Registry struct {
	SchemaVersion     string            `json:"schemaVersion"`
	Description       string            `json:"description,omitempty"`
	ExtensionPolicy   string            `json:"extensionPolicy,omitempty"`
	RedactionPolicies map[string]string `json:"redactionPolicies,omitempty"`
	Families          []FamilySpec      `json:"families"`
}

// LoadRegistry parses registry JSON (typically the embedded v1 document).
func LoadRegistry(data []byte) (Registry, error) {
	var reg Registry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reg); err != nil {
		return Registry{}, fmt.Errorf("telemetry registry: %w", err)
	}
	if err := reg.Validate(); err != nil {
		return Registry{}, err
	}
	return reg, nil
}

// EmbeddedRegistry returns the v1 registry compiled into the binary.
func EmbeddedRegistry() (Registry, error) {
	return LoadRegistry(embeddedRegistryJSON)
}

// MustEmbeddedRegistry panics if the embedded registry is invalid.
// Intended for package init paths and tests that require a valid catalog.
func MustEmbeddedRegistry() Registry {
	reg, err := EmbeddedRegistry()
	if err != nil {
		panic(err)
	}
	return reg
}

// Validate checks structural invariants of the registry document.
func (r Registry) Validate() error {
	if strings.TrimSpace(r.SchemaVersion) == "" {
		return fmt.Errorf("telemetry registry: schemaVersion is required")
	}
	if len(r.Families) == 0 {
		return fmt.Errorf("telemetry registry: families must be non-empty")
	}
	seen := make(map[string]struct{}, len(r.Families))
	for _, f := range r.Families {
		id := strings.TrimSpace(f.ID)
		if id == "" {
			return fmt.Errorf("telemetry registry: family with empty id")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("telemetry registry: duplicate family %q", id)
		}
		seen[id] = struct{}{}
		if len(f.Fields) == 0 {
			return fmt.Errorf("telemetry registry: family %q has no fields", id)
		}
		fseen := make(map[string]struct{}, len(f.Fields))
		for _, field := range f.Fields {
			name := strings.TrimSpace(field.Name)
			if name == "" {
				return fmt.Errorf("telemetry registry: family %q has field with empty name", id)
			}
			if _, ok := fseen[name]; ok {
				return fmt.Errorf("telemetry registry: family %q duplicate field %q", id, name)
			}
			fseen[name] = struct{}{}
			if strings.TrimSpace(field.Type) == "" {
				return fmt.Errorf("telemetry registry: family %q field %q missing type", id, name)
			}
			if err := validateRedact(field.Redact); err != nil {
				return fmt.Errorf("telemetry registry: family %q field %q: %w", id, name, err)
			}
		}
	}
	for _, id := range CoreFamilyIDs {
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("telemetry registry: missing core family %q", id)
		}
	}
	return nil
}

func validateRedact(policy string) error {
	switch strings.TrimSpace(policy) {
	case RedactNone, RedactScrub, RedactHash, RedactClass, RedactOmit:
		return nil
	case "":
		return fmt.Errorf("redact policy is required")
	default:
		return fmt.Errorf("unknown redact policy %q", policy)
	}
}

// Family returns the spec for id, or false when unknown.
func (r Registry) Family(id string) (FamilySpec, bool) {
	id = strings.TrimSpace(id)
	for _, f := range r.Families {
		if f.ID == id {
			return f, true
		}
	}
	return FamilySpec{}, false
}

// FamilyIDs returns sorted family ids.
func (r Registry) FamilyIDs() []string {
	out := make([]string, 0, len(r.Families))
	for _, f := range r.Families {
		out = append(out, f.ID)
	}
	sort.Strings(out)
	return out
}

// FieldByName looks up a field on the family.
func (f FamilySpec) FieldByName(name string) (FieldSpec, bool) {
	name = strings.TrimSpace(name)
	for _, field := range f.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return FieldSpec{}, false
}
