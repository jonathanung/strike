package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// formatContract is the on-disk session shape frozen by
// testdata/session_format_golden.json. Types are JSON shapes, not Go kinds:
// number covers every integer width.
type formatContract struct {
	LogSchemaVersion int         `json:"logSchemaVersion"`
	HeaderType       string      `json:"headerType"`
	Envelope         []fieldSpec `json:"envelope"`
	Header           []fieldSpec `json:"header"`
	Sidecar          []fieldSpec `json:"sidecar"`
	Events           []eventSpec `json:"events"`
}

type eventSpec struct {
	Type     string      `json:"type"`
	Go       string      `json:"go"`
	Fields   []fieldSpec `json:"fields"`
	Shadowed []string    `json:"shadowed,omitempty"`
}

type fieldSpec struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"`
	OmitEmpty bool        `json:"omitEmpty,omitempty"`
	Elem      string      `json:"elem,omitempty"`
	Fields    []fieldSpec `json:"fields,omitempty"`
}

type fieldInfo struct {
	Name      string
	OmitEmpty bool
	Type      reflect.Type
	Index     []int
}

var (
	schemaTimeType = reflect.TypeOf(time.Time{})
	schemaRawType  = reflect.TypeOf(json.RawMessage(nil))
)

func schemaOf(t reflect.Type, depth int) ([]fieldSpec, error) {
	fields, _, err := dominantFields(t, depth)
	if err != nil {
		return nil, err
	}
	return specsFrom(fields, depth)
}

func specsFrom(fields []fieldInfo, depth int) ([]fieldSpec, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	out := make([]fieldSpec, 0, len(fields))
	for _, f := range fields {
		spec, err := specOf(f, depth)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

func specOf(f fieldInfo, depth int) (fieldSpec, error) {
	typeName, elem, nested, err := describeType(f.Type, depth)
	if err != nil {
		return fieldSpec{}, err
	}
	return fieldSpec{
		Name:      f.Name,
		Type:      typeName,
		OmitEmpty: f.OmitEmpty,
		Elem:      elem,
		Fields:    nested,
	}, nil
}

func describeType(t reflect.Type, depth int) (typeName, elem string, fields []fieldSpec, err error) {
	t = derefType(t)
	if t == schemaTimeType {
		return "time", "", nil, nil
	}
	if t == schemaRawType {
		return "raw", "", nil, nil
	}
	switch t.Kind() {
	case reflect.String:
		return "string", "", nil, nil
	case reflect.Bool:
		return "bool", "", nil, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "number", "", nil, nil
	case reflect.Float32, reflect.Float64:
		return "float", "", nil, nil
	case reflect.Slice, reflect.Array:
		en, _, nested, err := describeType(t.Elem(), depth+1)
		if err != nil {
			return "", "", nil, err
		}
		if en == "object" {
			return "array", "object", nested, nil
		}
		return "array", en, nil, nil
	case reflect.Map:
		en, _, _, err := describeType(t.Elem(), depth+1)
		if err != nil {
			return "", "", nil, err
		}
		return "map", en, nil, nil
	case reflect.Struct:
		fields, err := schemaOf(t, depth+1)
		if err != nil {
			return "", "", nil, err
		}
		return "object", "", fields, nil
	default:
		return t.Kind().String(), "", nil, nil
	}
}

func dominantFields(t reflect.Type, depth int) ([]fieldInfo, []string, error) {
	if depth > 16 {
		return nil, nil, fmt.Errorf("session schema: nesting too deep at %s", t)
	}
	t = derefType(t)
	if t.Kind() != reflect.Struct {
		return nil, nil, fmt.Errorf("session schema: %s is not a struct", t)
	}
	filled := reflect.New(t).Elem()
	fillValue(filled)
	raw, err := json.Marshal(filled.Interface())
	if err != nil {
		return nil, nil, fmt.Errorf("marshal %s: %w", t, err)
	}
	keys, err := objectKeys(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", t, err)
	}
	cands := collectFields(t, nil)
	byName := map[string][]fieldInfo{}
	var nameOrder []string
	seen := map[string]bool{}
	for _, c := range cands {
		if !seen[c.Name] {
			seen[c.Name] = true
			nameOrder = append(nameOrder, c.Name)
		}
		byName[c.Name] = append(byName[c.Name], c)
	}
	keySet := map[string]bool{}
	for _, k := range keys {
		keySet[k] = true
	}
	var shadowed []string
	for _, name := range nameOrder {
		group := byName[name]
		if len(group) == 1 {
			if !keySet[name] {
				return nil, nil, fmt.Errorf("%s field %q did not marshal", t, name)
			}
			continue
		}
		winner := -1
		for i, c := range group {
			ok, err := fieldEmits(t, c)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				winner = i
				break
			}
		}
		if winner < 0 {
			return nil, nil, fmt.Errorf("%s field %q has no emitting Go field", t, name)
		}
		shadowed = append(shadowed, name)
	}
	var out []fieldInfo
	for _, k := range keys {
		group := byName[k]
		if len(group) == 0 {
			return nil, nil, fmt.Errorf("%s JSON key %q has no Go field", t, k)
		}
		if len(group) == 1 {
			out = append(out, group[0])
			continue
		}
		for _, c := range group {
			ok, err := fieldEmits(t, c)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				out = append(out, c)
				break
			}
		}
	}
	return out, uniqueStrings(shadowed), nil
}

func fieldEmits(t reflect.Type, f fieldInfo) (bool, error) {
	v := reflect.New(t).Elem()
	fillValue(v.FieldByIndex(f.Index))
	raw, err := json.Marshal(v.Interface())
	if err != nil {
		return false, err
	}
	keys, err := objectKeys(raw)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if k == f.Name {
			return true, nil
		}
	}
	return false, nil
}

func collectFields(t reflect.Type, prefix []int) []fieldInfo {
	t = derefType(t)
	var out []fieldInfo
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		idx := append(append([]int{}, prefix...), i)
		if embeddedFlatten(sf) {
			out = append(out, collectFields(sf.Type, idx)...)
			continue
		}
		if sf.PkgPath != "" {
			continue
		}
		name, omit, skip := jsonFieldName(sf)
		if skip {
			continue
		}
		out = append(out, fieldInfo{Name: name, OmitEmpty: omit, Type: sf.Type, Index: idx})
	}
	return out
}

func embeddedFlatten(sf reflect.StructField) bool {
	if !sf.Anonymous {
		return false
	}
	t := derefType(sf.Type)
	if t.Kind() != reflect.Struct || t == schemaTimeType {
		return false
	}
	tag := sf.Tag.Get("json")
	return tag == "" || strings.HasPrefix(tag, ",")
}

func jsonFieldName(sf reflect.StructField) (name string, omit, skip bool) {
	tag := sf.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	name = sf.Name
	if tag == "" {
		return name, false, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "-" && len(parts) == 1 {
		return "", false, true
	}
	if parts[0] != "" {
		name = parts[0]
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omit = true
		}
	}
	return name, omit, false
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func fillValue(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			if !v.CanSet() {
				return
			}
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillValue(v.Elem())
	case reflect.Struct:
		if v.Type() == schemaTimeType {
			if v.CanSet() {
				v.Set(reflect.ValueOf(time.Unix(1, 0).UTC()))
			}
			return
		}
		for i := 0; i < v.NumField(); i++ {
			fillValue(v.Field(i))
		}
	case reflect.Slice:
		if v.Type() == schemaRawType {
			if v.CanSet() {
				v.Set(reflect.ValueOf(json.RawMessage(`{"k":1}`)))
			}
			return
		}
		if !v.CanSet() {
			return
		}
		elem := reflect.New(v.Type().Elem()).Elem()
		fillValue(elem)
		v.Set(reflect.Append(v, elem))
	case reflect.Map:
		if !v.CanSet() {
			return
		}
		v.Set(reflect.MakeMap(v.Type()))
		key := reflect.New(v.Type().Key()).Elem()
		fillValue(key)
		elem := reflect.New(v.Type().Elem()).Elem()
		fillValue(elem)
		v.SetMapIndex(key, elem)
	case reflect.String:
		if v.CanSet() {
			v.SetString("x")
		}
	case reflect.Bool:
		if v.CanSet() {
			v.SetBool(true)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.CanSet() {
			v.SetInt(1)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.CanSet() {
			v.SetUint(1)
		}
	case reflect.Float32, reflect.Float64:
		if v.CanSet() {
			v.SetFloat(1)
		}
	}
}

func objectKeys(raw []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("expected JSON object, got %v", tok)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected object key, got %T", tok)
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func marshalContract(c formatContract) ([]byte, error) {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func renderCatalog(c formatContract) string {
	var b strings.Builder
	b.WriteString("## Normative field list\n\n")
	b.WriteString("This section is generated from `pkg/protocol/codec.go` and the session writer. ")
	b.WriteString("`TestSessionFormatGolden` fails when a JSON field or event type is renamed, removed, or added until this section and `internal/persist/session/testdata/session_format_golden.json` are updated together.\n\n")
	b.WriteString("`time` is RFC3339 UTC. `raw` is a JSON value. `number` is a JSON number (any integer width). ")
	b.WriteString("`omitempty` marks a key that is absent at the zero value. Embedded `Correlation` is flattened onto each event. ")
	b.WriteString("A shadowed name means an outer field uses that key, so `encoding/json` does not also emit the embedded field.\n\n")
	writeFlatTable(&b, "### Envelope\n\n`protocol.Envelope`. Empty `v` means legacy schema `1.0.0`.\n\n", c.Envelope)
	fmt.Fprintf(&b, "### `%s`\n\nNot an event. First line of logs created by current writers. `schemaVersion` is `%d`.\n\n", c.HeaderType, c.LogSchemaVersion)
	writeFlatTable(&b, "", c.Header)
	b.WriteString("### Sidecar `Meta`\n\n`<id>.meta.json`. Missing keys stay zero. `projectKey` is the first key when it is set.\n\n")
	writeFlatTable(&b, "", c.Sidecar)
	b.WriteString("### Events\n\nEach table is the object inside envelope `data`.\n\n")
	for _, ev := range c.Events {
		fmt.Fprintf(&b, "#### `%s`\n\nGo `%s`.\n\n", ev.Type, ev.Go)
		if len(ev.Shadowed) > 0 {
			fmt.Fprintf(&b, "Shadowed (outer field wins): %s.\n\n", backtickJoin(ev.Shadowed))
		}
		writeFlatTable(&b, "", ev.Fields)
	}
	return b.String()
}

func writeFlatTable(b *strings.Builder, heading string, fields []fieldSpec) {
	b.WriteString(heading)
	rows := flattenFields(fields, "")
	if len(rows) == 0 {
		b.WriteString("_No fields._\n\n")
		return
	}
	b.WriteString("| Field | Type | omitempty |\n|---|---|---|\n")
	for _, row := range rows {
		omit := ""
		if row.omit {
			omit = "yes"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", row.name, row.typ, omit)
	}
	b.WriteString("\n")
}

type flatField struct {
	name string
	typ  string
	omit bool
}

func flattenFields(fields []fieldSpec, prefix string) []flatField {
	var out []flatField
	for _, f := range fields {
		name := f.Name
		if prefix != "" {
			name = prefix + "." + f.Name
		}
		switch {
		case f.Type == "array" && len(f.Fields) > 0:
			out = append(out, flatField{name: name, typ: "array of object", omit: f.OmitEmpty})
			out = append(out, flattenFields(f.Fields, name+"[]")...)
		case f.Type == "object" && len(f.Fields) > 0:
			out = append(out, flatField{name: name, typ: "object", omit: f.OmitEmpty})
			out = append(out, flattenFields(f.Fields, name)...)
		case f.Type == "array" && f.Elem != "":
			out = append(out, flatField{name: name, typ: "array of " + f.Elem, omit: f.OmitEmpty})
		case f.Type == "map" && f.Elem != "":
			out = append(out, flatField{name: name, typ: "map of " + f.Elem, omit: f.OmitEmpty})
		default:
			out = append(out, flatField{name: name, typ: f.Type, omit: f.OmitEmpty})
		}
	}
	return out
}

func backtickJoin(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = "`" + s + "`"
	}
	return strings.Join(parts, ", ")
}
