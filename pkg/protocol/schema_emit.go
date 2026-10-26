package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Generated artifacts. make protocol-schema rewrites them from this package.
const (
	schemaRelPath = "schemas/protocol/v1/schema.json"
	compatRelPath = "schemas/protocol/v1/COMPAT.md"
	goldenRelPath = "schemas/protocol/v1/golden-events.jsonl"
	rustRelPath   = "webui/protocol-schema/src/lib.rs"
	schemaID      = "https://raw.githubusercontent.com/jonathanung/strike/main/schemas/protocol/v1/schema.json"
)

var (
	timeType = reflect.TypeOf(time.Time{})
	rawType  = reflect.TypeOf(json.RawMessage(nil))
)

type emittedFile struct {
	rel  string
	body []byte
}

type fieldIR struct {
	JSONName  string
	RustName  string
	OmitEmpty bool
	Type      typeIR
}

type typeIR struct {
	// Kind is pointer, string, bool, int, float, raw, time, bytes, named,
	// struct, slice, or map.
	Kind  string
	Name  string
	Elem  *typeIR
	Known []string
}

type structIR struct {
	Name   string
	Doc    string
	Fields []fieldIR
}

type wireStruct struct {
	Wire string
	Name string
}

type builder struct {
	docs    map[string]string
	enums   map[string][]string
	structs map[string]structIR
	stack   []string
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("protocol schema: runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

func emitFiles() ([]emittedFile, error) {
	b, err := newBuilder()
	if err != nil {
		return nil, err
	}
	events, err := b.wireEvents()
	if err != nil {
		return nil, err
	}
	ops, err := b.wireOps()
	if err != nil {
		return nil, err
	}
	if err := b.addExtras(); err != nil {
		return nil, err
	}
	schema, err := b.schemaJSON(events, ops)
	if err != nil {
		return nil, err
	}
	rust, err := b.rustSource(events, ops)
	if err != nil {
		return nil, err
	}
	compat := b.compatMarkdown(events, ops)
	golden, err := goldenJSONL()
	if err != nil {
		return nil, err
	}
	return []emittedFile{
		{rel: schemaRelPath, body: schema},
		{rel: compatRelPath, body: compat},
		{rel: goldenRelPath, body: golden},
		{rel: rustRelPath, body: rust},
	}, nil
}

func newBuilder() (*builder, error) {
	docs, enums, err := parseProtocolSources()
	if err != nil {
		return nil, err
	}
	return &builder{
		docs:    docs,
		enums:   enums,
		structs: map[string]structIR{},
	}, nil
}

func (b *builder) wireEvents() ([]wireStruct, error) {
	var out []wireStruct
	seen := map[string]string{}
	for _, ev := range eventPrototypes() {
		name := reflect.TypeOf(ev).Name()
		wire := eventType(ev)
		if wire == "" {
			return nil, fmt.Errorf("protocol schema: event %s has no wire string", name)
		}
		if prev, ok := seen[wire]; ok {
			return nil, fmt.Errorf("protocol schema: wire %q used by %s and %s", wire, prev, name)
		}
		seen[wire] = name
		t := reflect.TypeOf(ev)
		if err := b.ensure(t); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, wireStruct{Wire: wire, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Wire < out[j].Wire })
	return out, nil
}

func (b *builder) wireOps() ([]wireStruct, error) {
	var out []wireStruct
	seen := map[string]string{}
	for _, op := range opPrototypes() {
		name := reflect.TypeOf(op).Name()
		wire := opType(op)
		if wire == "" {
			return nil, fmt.Errorf("protocol schema: op %s has no wire string", name)
		}
		if prev, ok := seen[wire]; ok {
			return nil, fmt.Errorf("protocol schema: op wire %q used by %s and %s", wire, prev, name)
		}
		seen[wire] = name
		t := reflect.TypeOf(op)
		if err := b.ensure(t); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, wireStruct{Wire: wire, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Wire < out[j].Wire })
	return out, nil
}

func (b *builder) addExtras() error {
	for _, v := range extraSchemaRoots() {
		t := reflect.TypeOf(v)
		if err := b.ensure(t); err != nil {
			return fmt.Errorf("%s: %w", t.Name(), err)
		}
	}
	return nil
}

func (b *builder) ensure(t reflect.Type) error {
	if t.Kind() != reflect.Struct {
		return fmt.Errorf("ensure: %s is %s", t, t.Kind())
	}
	name := t.Name()
	if name == "" || !ast.IsExported(name) {
		return fmt.Errorf("ensure: unexported struct %s", t)
	}
	if _, ok := b.structs[name]; ok {
		return nil
	}
	for _, pending := range b.stack {
		if pending == name {
			b.structs[name] = structIR{Name: name, Doc: b.docs[name]}
			return nil
		}
	}
	b.stack = append(b.stack, name)
	fields, err := b.collect(t)
	b.stack = b.stack[:len(b.stack)-1]
	if err != nil {
		return err
	}
	b.structs[name] = structIR{Name: name, Doc: b.docs[name], Fields: fields}
	return nil
}

func (b *builder) collect(t reflect.Type) ([]fieldIR, error) {
	var raw []collected
	if err := b.walk(t, 0, &raw); err != nil {
		return nil, err
	}
	best := map[string]collected{}
	var order []string
	for _, f := range raw {
		prev, ok := best[f.name]
		if !ok {
			best[f.name] = f
			order = append(order, f.name)
			continue
		}
		if f.depth < prev.depth {
			best[f.name] = f
			continue
		}
		if f.depth == prev.depth {
			return nil, fmt.Errorf("ambiguous json field %q on %s", f.name, t.Name())
		}
	}
	var fields []fieldIR
	usedRust := map[string]string{}
	for _, name := range order {
		f := best[name]
		rustName := rustFieldName(name)
		if prev, ok := usedRust[rustName]; ok {
			return nil, fmt.Errorf("rust field %s on %s collides (%s and %s)", rustName, t.Name(), prev, name)
		}
		usedRust[rustName] = name
		typ, err := b.classify(f.typ)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		fields = append(fields, fieldIR{
			JSONName:  name,
			RustName:  rustName,
			OmitEmpty: f.omit,
			Type:      typ,
		})
	}
	return fields, nil
}

type collected struct {
	name  string
	typ   reflect.Type
	omit  bool
	depth int
}

func (b *builder) walk(t reflect.Type, depth int, out *[]collected) error {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		name, omit, skip := parseJSONTag(sf.Tag.Get("json"))
		if skip {
			continue
		}
		ft := sf.Type
		if ft.Kind() == reflect.Pointer && sf.Anonymous {
			ft = ft.Elem()
		}
		inline := sf.Anonymous && name == "" && ft.Kind() == reflect.Struct && ft != timeType
		if inline {
			if ft.Name() != "" && ast.IsExported(ft.Name()) && ft.PkgPath() == timeType.PkgPath() {
				return fmt.Errorf("unexpected foreign embed %s", ft)
			}
			if ft.Name() != "" && ast.IsExported(ft.Name()) && ft.PkgPath() == reflect.TypeOf(Envelope{}).PkgPath() {
				if err := b.ensure(ft); err != nil {
					return err
				}
			}
			if err := b.walk(ft, depth+1, out); err != nil {
				return err
			}
			continue
		}
		if sf.PkgPath != "" {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		*out = append(*out, collected{name: name, typ: sf.Type, omit: omit, depth: depth})
	}
	return nil
}

func parseJSONTag(tag string) (name string, omit bool, skip bool) {
	if tag == "-" {
		return "", false, true
	}
	name, opt, _ := strings.Cut(tag, ",")
	for _, part := range strings.Split(opt, ",") {
		if part == "omitempty" || part == "omitzero" {
			omit = true
		}
	}
	return name, omit, false
}

func (b *builder) classify(t reflect.Type) (typeIR, error) {
	if t.Kind() == reflect.Pointer {
		inner, err := b.classify(t.Elem())
		if err != nil {
			return typeIR{}, err
		}
		return typeIR{Kind: "pointer", Elem: &inner}, nil
	}
	if t == timeType {
		return typeIR{Kind: "time"}, nil
	}
	if t == rawType {
		return typeIR{Kind: "raw"}, nil
	}
	switch t.Kind() {
	case reflect.String:
		if t.PkgPath() == "" || t.Name() == "" {
			return typeIR{Kind: "string"}, nil
		}
		if t.PkgPath() != reflect.TypeOf(Envelope{}).PkgPath() {
			return typeIR{}, fmt.Errorf("foreign string type %s", t)
		}
		return typeIR{Kind: "named", Name: t.Name(), Known: append([]string(nil), b.enums[t.Name()]...)}, nil
	case reflect.Bool:
		return typeIR{Kind: "bool"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return typeIR{Kind: "int"}, nil
	case reflect.Float32, reflect.Float64:
		return typeIR{Kind: "float"}, nil
	case reflect.Slice:
		if t.Name() == "" && t.Elem().Kind() == reflect.Uint8 {
			return typeIR{Kind: "bytes"}, nil
		}
		elem, err := b.classify(t.Elem())
		if err != nil {
			return typeIR{}, err
		}
		return typeIR{Kind: "slice", Elem: &elem}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return typeIR{}, fmt.Errorf("map key %s", t.Key())
		}
		elem, err := b.classify(t.Elem())
		if err != nil {
			return typeIR{}, err
		}
		return typeIR{Kind: "map", Elem: &elem}, nil
	case reflect.Struct:
		if t.PkgPath() != reflect.TypeOf(Envelope{}).PkgPath() {
			return typeIR{}, fmt.Errorf("foreign struct %s", t)
		}
		if err := b.ensure(t); err != nil {
			return typeIR{}, err
		}
		return typeIR{Kind: "struct", Name: t.Name()}, nil
	default:
		return typeIR{}, fmt.Errorf("unsupported type %s", t)
	}
}

func (b *builder) schemaJSON(events, ops []wireStruct) ([]byte, error) {
	defs := map[string]any{}
	names := make([]string, 0, len(b.structs))
	for name := range b.structs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		defs[name] = structSchema(b.structs[name])
	}
	for _, name := range sortedKeys(b.enums) {
		if _, ok := defs[name]; ok {
			return nil, fmt.Errorf("enum %s collides with struct", name)
		}
		defs[name] = namedStringSchema(b.enums[name])
	}
	defs["KnownEvent"] = oneOfSchema(events, true)
	defs["KnownOp"] = oneOfSchema(ops, false)

	doc := map[string]any{
		"$schema":                   "https://json-schema.org/draft/2020-12/schema",
		"$id":                       schemaID,
		"title":                     "Strike event envelope",
		"description":               "Forward-compatible Op/Event wire schema generated from pkg/protocol. The root schema is one event envelope: unknown type strings and unknown fields are valid. $defs/KnownEvent lists types this version writes. $defs/KnownOp is fail-closed: unknown op type strings are not valid. required is the writer contract; readers treat missing fields as zero values. See schemas/protocol/v1/COMPAT.md.",
		"x-strike-protocol-version": Version,
		"x-strike-legacy-version":   LegacyVersion,
		"x-strike-event-count":      len(events),
		"x-strike-op-count":         len(ops),
		"x-strike-unknown-event":    "valid",
		"x-strike-unknown-field":    "ignore",
		"x-strike-unknown-op":       "reject",
		"$ref":                      "#/$defs/Envelope",
		"$defs":                     defs,
	}
	return marshalSchema(doc)
}

func structSchema(s structIR) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range s.Fields {
		prop := schemaOf(f.Type)
		if m, ok := prop.(map[string]any); ok && (s.Name == "Envelope" || s.Name == "OpEnvelope") && f.JSONName == "type" {
			m["minLength"] = 1
		}
		props[f.JSONName] = prop
		if !f.OmitEmpty {
			required = append(required, f.JSONName)
		}
	}
	out := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties":           props,
	}
	if s.Doc != "" {
		out["description"] = s.Doc
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func namedStringSchema(known []string) map[string]any {
	out := map[string]any{
		"type":               "string",
		"x-strike-enum-open": true,
		"description":        "Open string. Writers use x-strike-known-values. Readers must accept other strings.",
	}
	if len(known) > 0 {
		out["x-strike-known-values"] = known
	}
	return out
}

func oneOfSchema(items []wireStruct, event bool) map[string]any {
	branches := make([]any, 0, len(items))
	for _, item := range items {
		props := map[string]any{
			"type": map[string]any{"const": item.Wire},
			"v":    map[string]any{"type": "string"},
			"data": map[string]any{"$ref": "#/$defs/" + item.Name},
		}
		required := []string{"type"}
		if event {
			props["time"] = map[string]any{"type": "string", "format": "date-time"}
			required = []string{"type", "time", "data"}
		}
		branches = append(branches, map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"properties":           props,
			"required":             required,
		})
	}
	desc := "Op envelopes this version accepts. Unknown op type strings are invalid."
	if event {
		desc = "Event envelopes this version writes. Consumers that must tolerate newer type strings should validate the root schema instead of this oneOf."
	}
	return map[string]any{
		"description": desc,
		"oneOf":       branches,
	}
}

func schemaOf(t typeIR) any {
	switch t.Kind {
	case "pointer":
		return map[string]any{
			"anyOf":               []any{schemaOf(*t.Elem), map[string]any{"type": "null"}},
			"x-strike-go-pointer": true,
		}
	case "string":
		return map[string]any{"type": "string"}
	case "named":
		return map[string]any{"$ref": "#/$defs/" + t.Name}
	case "bool":
		return map[string]any{"type": "bool"}
	case "int":
		return map[string]any{"type": "integer"}
	case "float":
		return map[string]any{"type": "number"}
	case "raw":
		return map[string]any{"description": "Any JSON value (Go json.RawMessage)."}
	case "time":
		return map[string]any{"type": "string", "format": "date-time"}
	case "bytes":
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case "struct":
		return map[string]any{"$ref": "#/$defs/" + t.Name}
	case "slice":
		return map[string]any{"type": "array", "items": schemaOf(*t.Elem)}
	case "map":
		return map[string]any{"type": "object", "additionalProperties": schemaOf(*t.Elem)}
	default:
		return map[string]any{"description": "unsupported " + t.Kind}
	}
}

func marshalSchema(doc map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (b *builder) compatMarkdown(events, ops []wireStruct) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Strike protocol schema compatibility\n\n")
	fmt.Fprintf(&buf, "Generated from `pkg/protocol`. Do not edit by hand. Regenerate with `make protocol-schema`.\n\n")
	fmt.Fprintf(&buf, "- Protocol version: `%s`\n", Version)
	fmt.Fprintf(&buf, "- Legacy version when an envelope omits `v`: `%s`\n", LegacyVersion)
	fmt.Fprintf(&buf, "- JSON Schema: [`schema.json`](schema.json) (`%s`)\n", schemaID)
	fmt.Fprintf(&buf, "- Rust types: `webui/protocol-schema` (crate `protocol-schema`)\n\n")
	fmt.Fprintf(&buf, "## Rules\n\n")
	fmt.Fprintf(&buf, "- **Unknown event type strings are valid.** Decode them as an unknown event and keep the raw `data`. Do not fail the stream.\n")
	fmt.Fprintf(&buf, "- **Unknown JSON fields are ignored** on events, ops, and nested objects.\n")
	fmt.Fprintf(&buf, "- **Missing fields are zero values.** `required` in the schema is the writer contract for this version.\n")
	fmt.Fprintf(&buf, "- **Unknown op type strings are rejected.** Client input stays fail-closed.\n")
	fmt.Fprintf(&buf, "- **Named string fields are open.** `x-strike-known-values` lists what this version writes. Readers accept other strings.\n")
	fmt.Fprintf(&buf, "- **Minor** `protocol.Version` bumps add types or optional fields. **Major** bumps rename, remove, or change meaning. **Patch** bumps do not change encoded JSON.\n")
	fmt.Fprintf(&buf, "- Nil Go slices and maps encode as JSON `null`. Readers treat `null` like an empty list or map.\n\n")
	fmt.Fprintf(&buf, "## Event types (%d)\n\n", len(events))
	for _, ev := range events {
		fmt.Fprintf(&buf, "- `%s` → `%s`\n", ev.Wire, ev.Name)
	}
	fmt.Fprintf(&buf, "\n## Op types (%d)\n\n", len(ops))
	for _, op := range ops {
		fmt.Fprintf(&buf, "- `%s` → `%s`\n", op.Wire, op.Name)
	}
	fmt.Fprintf(&buf, "\n## Versioning\n\n")
	fmt.Fprintf(&buf, "Human rules and the drift check: [docs/protocol-schema.md](../../../docs/protocol-schema.md).\n")
	return buf.Bytes()
}

func (b *builder) rustSource(events, ops []wireStruct) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("// Code generated from pkg/protocol. DO NOT EDIT.\n")
	buf.WriteString("// Regenerate with `make protocol-schema`.\n")
	buf.WriteString("//\n")
	buf.WriteString("// Unknown event type strings become Event::Unknown.\n")
	buf.WriteString("// Unknown JSON fields are ignored. Missing fields are defaults.\n")
	buf.WriteString("// Unknown op type strings return Err.\n")
	fmt.Fprintf(&buf, "// protocol.Version %s\n\n", Version)
	buf.WriteString("use std::collections::HashMap;\n\n")
	buf.WriteString("use serde::de::DeserializeOwned;\n")
	buf.WriteString("use serde::{Deserialize, Deserializer, Serialize};\n")
	buf.WriteString("use serde_json::Value;\n\n")
	fmt.Fprintf(&buf, "pub const PROTOCOL_VERSION: &str = %q;\n", Version)
	fmt.Fprintf(&buf, "pub const LEGACY_VERSION: &str = %q;\n\n", LegacyVersion)

	enumNames := sortedKeys(b.enums)
	for _, name := range enumNames {
		fmt.Fprintf(&buf, "/// Open string. Known writer values live in the JSON Schema.\n")
		buf.WriteString("#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]\n")
		buf.WriteString("#[serde(transparent)]\n")
		fmt.Fprintf(&buf, "pub struct %s(pub String);\n\n", name)
	}

	structNames := make([]string, 0, len(b.structs))
	for name := range b.structs {
		structNames = append(structNames, name)
	}
	sort.Strings(structNames)
	for _, name := range structNames {
		writeRustStruct(&buf, b.structs[name])
	}

	buf.WriteString("#[derive(Clone, Debug, PartialEq)]\n")
	buf.WriteString("pub enum Event {\n")
	for _, ev := range events {
		fmt.Fprintf(&buf, "    %s(%s),\n", ev.Name, ev.Name)
	}
	buf.WriteString("    Unknown { type_name: String, data: Value },\n")
	buf.WriteString("}\n\n")

	buf.WriteString("#[derive(Clone, Debug, PartialEq)]\n")
	buf.WriteString("pub enum Op {\n")
	for _, op := range ops {
		fmt.Fprintf(&buf, "    %s(%s),\n", op.Name, op.Name)
	}
	buf.WriteString("}\n\n")

	buf.WriteString("#[derive(Debug, Clone, PartialEq, Eq)]\n")
	buf.WriteString("pub struct DecodeError {\n")
	buf.WriteString("    message: String,\n")
	buf.WriteString("}\n\n")
	buf.WriteString("impl DecodeError {\n")
	buf.WriteString("    fn new(message: impl Into<String>) -> Self {\n")
	buf.WriteString("        Self { message: message.into() }\n")
	buf.WriteString("    }\n")
	buf.WriteString("}\n\n")
	buf.WriteString("impl std::fmt::Display for DecodeError {\n")
	buf.WriteString("    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {\n")
	buf.WriteString("        f.write_str(&self.message)\n")
	buf.WriteString("    }\n")
	buf.WriteString("}\n\n")
	buf.WriteString("impl std::error::Error for DecodeError {}\n\n")

	buf.WriteString("fn null_as_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>\n")
	buf.WriteString("where\n")
	buf.WriteString("    D: Deserializer<'de>,\n")
	buf.WriteString("    T: Deserialize<'de> + Default,\n")
	buf.WriteString("{\n")
	buf.WriteString("    let opt = Option::<T>::deserialize(deserializer)?;\n")
	buf.WriteString("    Ok(opt.unwrap_or_default())\n")
	buf.WriteString("}\n\n")

	buf.WriteString("/// Decode one event envelope. Unknown type strings succeed as\n")
	buf.WriteString("/// [`Event::Unknown`]. An empty type string, malformed JSON, or a\n")
	buf.WriteString("/// known type whose data does not match the generated struct is an error.\n")
	buf.WriteString("pub fn decode_event(raw: &str) -> Result<Event, DecodeError> {\n")
	buf.WriteString("    let env: Envelope = serde_json::from_str(raw).map_err(|err| DecodeError::new(err.to_string()))?;\n")
	buf.WriteString("    if env.r#type.is_empty() {\n")
	buf.WriteString("        return Err(DecodeError::new(\"empty event type\"));\n")
	buf.WriteString("    }\n")
	buf.WriteString("    match env.r#type.as_str() {\n")
	for _, ev := range events {
		fmt.Fprintf(&buf, "        %q => map_event::<%s>(%q, &env.data, Event::%s),\n", ev.Wire, ev.Name, ev.Wire, ev.Name)
	}
	buf.WriteString("        other => {\n")
	buf.WriteString("            let data = if env.data.is_null() {\n")
	buf.WriteString("                serde_json::json!({})\n")
	buf.WriteString("            } else {\n")
	buf.WriteString("                env.data\n")
	buf.WriteString("            };\n")
	buf.WriteString("            Ok(Event::Unknown { type_name: other.to_string(), data })\n")
	buf.WriteString("        }\n")
	buf.WriteString("    }\n")
	buf.WriteString("}\n\n")

	buf.WriteString("fn map_event<T>(wire: &str, data: &Value, wrap: fn(T) -> Event) -> Result<Event, DecodeError>\n")
	buf.WriteString("where\n")
	buf.WriteString("    T: DeserializeOwned,\n")
	buf.WriteString("{\n")
	buf.WriteString("    if data.is_null() {\n")
	buf.WriteString("        return Err(DecodeError::new(format!(\"event {wire} missing data\")));\n")
	buf.WriteString("    }\n")
	buf.WriteString("    let value: T = serde_json::from_value(data.clone()).map_err(|err| DecodeError::new(format!(\"event {wire}: {err}\")))?;\n")
	buf.WriteString("    Ok(wrap(value))\n")
	buf.WriteString("}\n\n")

	buf.WriteString("/// Decode one op envelope. Unknown type strings are errors.\n")
	buf.WriteString("/// Missing `data` decodes as an empty object so dataless ops\n")
	buf.WriteString("/// (`interrupt`, inspect, workflow.stop) still parse.\n")
	buf.WriteString("pub fn decode_op(raw: &str) -> Result<Op, DecodeError> {\n")
	buf.WriteString("    let env: OpEnvelope = serde_json::from_str(raw).map_err(|err| DecodeError::new(err.to_string()))?;\n")
	buf.WriteString("    if env.r#type.is_empty() {\n")
	buf.WriteString("        return Err(DecodeError::new(\"empty op type\"));\n")
	buf.WriteString("    }\n")
	buf.WriteString("    let data = if env.data.is_null() {\n")
	buf.WriteString("        serde_json::json!({})\n")
	buf.WriteString("    } else {\n")
	buf.WriteString("        env.data\n")
	buf.WriteString("    };\n")
	buf.WriteString("    match env.r#type.as_str() {\n")
	for _, op := range ops {
		fmt.Fprintf(&buf, "        %q => map_op::<%s>(%q, data.clone(), Op::%s),\n", op.Wire, op.Name, op.Wire, op.Name)
	}
	buf.WriteString("        other => Err(DecodeError::new(format!(\"unknown op type {other}\"))),\n")
	buf.WriteString("    }\n")
	buf.WriteString("}\n\n")

	buf.WriteString("fn map_op<T>(wire: &str, data: Value, wrap: fn(T) -> Op) -> Result<Op, DecodeError>\n")
	buf.WriteString("where\n")
	buf.WriteString("    T: DeserializeOwned,\n")
	buf.WriteString("{\n")
	buf.WriteString("    let value: T = serde_json::from_value(data).map_err(|err| DecodeError::new(format!(\"op {wire}: {err}\")))?;\n")
	buf.WriteString("    Ok(wrap(value))\n")
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

func writeRustStruct(buf *bytes.Buffer, s structIR) {
	if s.Doc != "" {
		fmt.Fprintf(buf, "/// %s\n", strings.ReplaceAll(s.Doc, "\n", " "))
	}
	buf.WriteString("#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]\n")
	fmt.Fprintf(buf, "pub struct %s {\n", s.Name)
	for _, f := range s.Fields {
		fmt.Fprintf(buf, "    %s\n", serdeAttr(f))
		fmt.Fprintf(buf, "    pub %s: %s,\n", f.RustName, rustType(f.Type))
	}
	buf.WriteString("}\n\n")
}

func serdeAttr(f fieldIR) string {
	parts := []string{"default", fmt.Sprintf("rename = %q", f.JSONName)}
	if f.Type.Kind == "slice" || f.Type.Kind == "map" {
		parts = append(parts, `deserialize_with = "null_as_default"`)
	}
	return "#[serde(" + strings.Join(parts, ", ") + ")]"
}

func rustType(t typeIR) string {
	switch t.Kind {
	case "pointer":
		return "Option<" + rustType(*t.Elem) + ">"
	case "string", "time", "bytes":
		return "String"
	case "bool":
		return "bool"
	case "int":
		return "i64"
	case "float":
		return "f64"
	case "raw":
		return "Value"
	case "named", "struct":
		return t.Name
	case "slice":
		return "Vec<" + rustType(*t.Elem) + ">"
	case "map":
		return "HashMap<String, " + rustType(*t.Elem) + ">"
	default:
		return "Value"
	}
}

func rustFieldName(jsonName string) string {
	var b strings.Builder
	for i, r := range jsonName {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	name := b.String()
	if rustKeywords[name] {
		return "r#" + name
	}
	return name
}

var rustKeywords = map[string]bool{
	"as": true, "async": true, "await": true, "break": true, "const": true,
	"continue": true, "crate": true, "dyn": true, "else": true, "enum": true,
	"extern": true, "false": true, "fn": true, "for": true, "if": true,
	"impl": true, "in": true, "let": true, "loop": true, "match": true,
	"mod": true, "move": true, "mut": true, "pub": true, "ref": true,
	"return": true, "self": true, "Self": true, "static": true, "struct": true,
	"super": true, "trait": true, "true": true, "type": true, "unsafe": true,
	"use": true, "where": true, "while": true, "abstract": true, "become": true,
	"box": true, "do": true, "final": true, "macro": true, "override": true,
	"priv": true, "typeof": true, "unsized": true, "virtual": true, "yield": true,
	"try": true, "gen": true, "union": true,
}

func goldenJSONL() ([]byte, error) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := []Event{
		UserMessage{Text: "hello"},
		TextDelta{Text: "hi"},
		ToolCallBegin{Name: "bash", CallID: "c1", Args: json.RawMessage(`{"command":"echo"}`)},
		TurnCompleted{StopReason: "end_turn"},
		ChildCompleted{
			Status:  ChildStatusCompleted,
			Summary: "done",
			Handoff: CompletionHandoff{Summary: "done"},
		},
	}
	var buf bytes.Buffer
	for i, ev := range rows {
		env, err := Wrap(ev)
		if err != nil {
			return nil, err
		}
		env.Time = fixed
		if i == 0 {
			env.Data = injectJSONField(env.Data, "futureField", "1")
		}
		raw, err := json.Marshal(env)
		if err != nil {
			return nil, err
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	unknown := Envelope{
		Type:    "future.extension",
		Time:    fixed,
		Version: Version,
		Data:    json.RawMessage(`{"ok":true,"span":"gate"}`),
	}
	raw, err := json.Marshal(unknown)
	if err != nil {
		return nil, err
	}
	buf.Write(raw)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func injectJSONField(data json.RawMessage, key, rawValue string) json.RawMessage {
	s := bytes.TrimSpace(data)
	s = bytes.TrimSuffix(s, []byte("}"))
	out := append([]byte{}, s...)
	out = append(out, []byte(`,"`+key+`":`+rawValue+`}`)...)
	return out
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func parseProtocolSources() (docs map[string]string, enums map[string][]string, err error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, nil, fmt.Errorf("protocol schema: caller failed")
	}
	dir := filepath.Dir(file)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var names []string
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, f)
	}
	docs = map[string]string{}
	stringTypes := map[string]bool{}
	enums = map[string][]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gen.Tok {
			case token.TYPE:
				for _, spec := range gen.Specs {
					ts := spec.(*ast.TypeSpec)
					if id, ok := ts.Type.(*ast.Ident); ok && id.Name == "string" {
						stringTypes[ts.Name.Name] = true
					}
					if ts.Name.IsExported() {
						if text := docText(ts.Doc, gen.Doc); text != "" {
							docs[ts.Name.Name] = text
						}
					}
				}
			case token.CONST:
				current := ""
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					if vs.Type != nil {
						if id, ok := vs.Type.(*ast.Ident); ok {
							current = id.Name
						} else {
							current = ""
						}
					}
					if current == "" || len(vs.Values) == 0 {
						continue
					}
					lit, ok := vs.Values[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					val, err := strconv.Unquote(lit.Value)
					if err != nil {
						return nil, nil, err
					}
					enums[current] = append(enums[current], val)
				}
			}
		}
	}
	for name := range enums {
		if !stringTypes[name] {
			delete(enums, name)
		}
	}
	for name := range stringTypes {
		if _, ok := enums[name]; !ok {
			enums[name] = nil
		}
	}
	return docs, enums, nil
}

func docText(docs ...*ast.CommentGroup) string {
	for _, g := range docs {
		if g == nil {
			continue
		}
		text := strings.TrimSpace(g.Text())
		if text == "" {
			continue
		}
		para, _, _ := strings.Cut(text, "\n\n")
		return strings.Join(strings.Fields(para), " ")
	}
	return ""
}
