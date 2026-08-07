package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

// RedactRecord applies field-level redaction policies from struct tags
// (telemetry:"redact=…") in place on a pointer to a family event struct.
// Unknown policies error. Non-pointer or nil values error.
func RedactRecord(v any) error {
	if v == nil {
		return fmt.Errorf("telemetry: RedactRecord nil")
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("telemetry: RedactRecord requires non-nil pointer")
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("telemetry: RedactRecord requires pointer to struct")
	}
	return redactStruct(rv)
}

func redactStruct(rv reflect.Value) error {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		policy, ok := redactPolicyFromTag(sf.Tag.Get("telemetry"))
		if !ok {
			// Nested structs without tags: recurse if exported struct.
			fv := rv.Field(i)
			if fv.Kind() == reflect.Struct {
				if err := redactStruct(fv); err != nil {
					return err
				}
			}
			continue
		}
		if err := applyRedact(rv.Field(i), policy); err != nil {
			return fmt.Errorf("field %s: %w", sf.Name, err)
		}
	}
	return nil
}

func redactPolicyFromTag(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", false
	}
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "redact=") {
			return strings.TrimSpace(strings.TrimPrefix(part, "redact=")), true
		}
	}
	return "", false
}

func applyRedact(fv reflect.Value, policy string) error {
	if !fv.CanSet() {
		return nil
	}
	switch policy {
	case RedactNone, RedactClass:
		// class: value is already a coarse label; leave as-is.
		return nil
	case RedactOmit:
		zero(fv)
		return nil
	case RedactScrub:
		return scrubValue(fv)
	case RedactHash:
		return hashValue(fv)
	default:
		return fmt.Errorf("unknown redact policy %q", policy)
	}
}

func zero(fv reflect.Value) {
	fv.Set(reflect.Zero(fv.Type()))
}

func scrubValue(fv reflect.Value) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(redact.String(fv.String()))
		return nil
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("scrub supports string or []string only, got %s", fv.Type())
		}
		if fv.IsNil() {
			return nil
		}
		out := make([]string, fv.Len())
		for i := 0; i < fv.Len(); i++ {
			out[i] = redact.String(fv.Index(i).String())
		}
		fv.Set(reflect.ValueOf(out))
		return nil
	case reflect.Pointer:
		if fv.IsNil() {
			return nil
		}
		return scrubValue(fv.Elem())
	default:
		return fmt.Errorf("scrub supports string or []string only, got %s", fv.Type())
	}
}

func hashValue(fv reflect.Value) error {
	switch fv.Kind() {
	case reflect.String:
		s := fv.String()
		if s == "" {
			return nil
		}
		sum := sha256.Sum256([]byte(s))
		fv.SetString(hex.EncodeToString(sum[:]))
		return nil
	case reflect.Pointer:
		if fv.IsNil() {
			return nil
		}
		return hashValue(fv.Elem())
	default:
		return fmt.Errorf("hash supports string only, got %s", fv.Type())
	}
}

// HashString returns sha256 hex of s (empty → empty).
func HashString(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
