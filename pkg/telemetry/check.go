package telemetry

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// goTypeName maps Go reflect kinds/types onto registry type strings.
func goTypeName(t reflect.Type) string {
	if t == nil {
		return ""
	}
	// Pointers to int/int64 used for optional numerics.
	if t.Kind() == reflect.Pointer {
		return goTypeName(t.Elem())
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int32:
		return "int"
	case reflect.Int64:
		return "int64"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return "string[]"
		}
		return "array"
	default:
		return t.Kind().String()
	}
}

// jsonFieldName extracts the JSON object key from a struct tag.
func jsonFieldName(tag string) string {
	if tag == "" || tag == "-" {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	return strings.TrimSpace(name)
}

// CheckDrift compares the registry families to the Go export structs.
// Returns a multi-line error when names, types, or redact policies diverge.
func CheckDrift(reg Registry) error {
	types := goTypes()
	var errs []string

	// Every registry family must have a Go type.
	for _, fam := range reg.Families {
		gv, ok := types[fam.ID]
		if !ok {
			errs = append(errs, fmt.Sprintf("family %q: no Go struct registered in goTypes()", fam.ID))
			continue
		}
		rt := reflect.TypeOf(gv)
		if rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			errs = append(errs, fmt.Sprintf("family %q: Go type is not a struct", fam.ID))
			continue
		}

		// Build map of json name → (go type name, redact policy)
		type fieldInfo struct {
			goType string
			redact string
			found  bool
		}
		goFields := make(map[string]*fieldInfo)
		for i := 0; i < rt.NumField(); i++ {
			sf := rt.Field(i)
			if sf.PkgPath != "" {
				continue
			}
			name := jsonFieldName(sf.Tag.Get("json"))
			if name == "" {
				continue
			}
			policy, _ := redactPolicyFromTag(sf.Tag.Get("telemetry"))
			goFields[name] = &fieldInfo{
				goType: goTypeName(sf.Type),
				redact: policy,
			}
		}

		for _, fs := range fam.Fields {
			info, ok := goFields[fs.Name]
			if !ok {
				errs = append(errs, fmt.Sprintf("family %q field %q: in registry but missing on Go struct", fam.ID, fs.Name))
				continue
			}
			info.found = true
			if info.goType != fs.Type {
				errs = append(errs, fmt.Sprintf("family %q field %q: type registry=%s go=%s", fam.ID, fs.Name, fs.Type, info.goType))
			}
			if info.redact == "" {
				errs = append(errs, fmt.Sprintf("family %q field %q: Go struct missing telemetry:\"redact=…\" tag", fam.ID, fs.Name))
			} else if info.redact != fs.Redact {
				errs = append(errs, fmt.Sprintf("family %q field %q: redact registry=%s go=%s", fam.ID, fs.Name, fs.Redact, info.redact))
			}
		}
		for name, info := range goFields {
			if !info.found {
				errs = append(errs, fmt.Sprintf("family %q field %q: on Go struct but missing from registry", fam.ID, name))
			}
		}
	}

	// Every goTypes entry must appear in the registry.
	for id := range types {
		if _, ok := reg.Family(id); !ok {
			errs = append(errs, fmt.Sprintf("goTypes has %q but registry does not", id))
		}
	}

	if reg.SchemaVersion != SchemaVersion {
		errs = append(errs, fmt.Sprintf("schemaVersion registry=%s package const=%s", reg.SchemaVersion, SchemaVersion))
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("telemetry schema drift:\n  - %s", strings.Join(errs, "\n  - "))
}

// CheckEmbeddedFile ensures schemas/telemetry/v1/registry.json matches the
// embedded copy when repoRoot is a strike checkout (CI / local make target).
// When the file is missing (module cache consumers), the check is skipped.
func CheckEmbeddedFile(repoRoot string) error {
	path := filepath.Join(repoRoot, "schemas", "telemetry", "v1", "registry.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Normalize: both must parse to equal canonical JSON via LoadRegistry + remarshal.
	disk, err := LoadRegistry(data)
	if err != nil {
		return fmt.Errorf("disk registry: %w", err)
	}
	emb, err := EmbeddedRegistry()
	if err != nil {
		return fmt.Errorf("embedded registry: %w", err)
	}
	if err := CheckDrift(disk); err != nil {
		return fmt.Errorf("disk registry drift: %w", err)
	}
	if err := CheckDrift(emb); err != nil {
		return fmt.Errorf("embedded registry drift: %w", err)
	}
	// Family id sets must match.
	if strings.Join(disk.FamilyIDs(), ",") != strings.Join(emb.FamilyIDs(), ",") {
		return fmt.Errorf("disk vs embedded family ids differ: disk=%v embedded=%v", disk.FamilyIDs(), emb.FamilyIDs())
	}
	if disk.SchemaVersion != emb.SchemaVersion {
		return fmt.Errorf("disk schemaVersion %s != embedded %s", disk.SchemaVersion, emb.SchemaVersion)
	}
	// Byte-level equality after trim space keeps embed copy honest.
	if strings.TrimSpace(string(data)) != strings.TrimSpace(string(embeddedRegistryJSON)) {
		return fmt.Errorf("schemas/telemetry/v1/registry.json differs from pkg/telemetry/registry.json (embedded); copy disk → package after edits")
	}
	return nil
}
