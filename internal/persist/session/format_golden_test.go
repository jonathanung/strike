package session

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
)

const (
	catalogBegin = "<!-- session-format:catalog:begin -->"
	catalogEnd   = "<!-- session-format:catalog:end -->"
)

// TestSessionFormatGolden freezes the on-disk session contract.
// UPDATE_SESSION_FORMAT=1 rewrites the golden fixture and the catalog in
// docs/session-format.md; leave it unset in CI.
func TestSessionFormatGolden(t *testing.T) {
	got, err := buildFormatContract(t)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := marshalContract(got)
	if err != nil {
		t.Fatal(err)
	}
	root := repoRoot(t)
	goldenPath := filepath.Join(root, "internal", "persist", "session", "testdata", "session_format_golden.json")
	docPath := filepath.Join(root, "docs", "session-format.md")
	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog := renderCatalog(got)
	if os.Getenv("UPDATE_SESSION_FORMAT") == "1" {
		if err := os.WriteFile(goldenPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		next, err := replaceCatalog(string(doc), catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(docPath, []byte(next), 0o644); err != nil {
			t.Fatal(err)
		}
		doc = []byte(next)
	}
	assertNoSecrets(t, goldenPath, raw)

	have, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(have))
	dec.DisallowUnknownFields()
	var frozen formatContract
	if err := dec.Decode(&frozen); err != nil {
		t.Fatalf("golden fixture does not match the contract struct (renamed or extra field): %v", err)
	}
	frozenRaw, err := marshalContract(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, frozenRaw) {
		t.Fatalf("session format drifted from %s (renamed, removed, or added field or event type)\n%s", goldenPath, firstDiff(string(frozenRaw), string(raw)))
	}

	gotCatalog, err := catalogSection(string(doc))
	if err != nil {
		t.Fatal(err)
	}
	if gotCatalog != catalog {
		t.Fatalf("docs/session-format.md catalog drifted (undocumented or stale event type or field)\n%s", firstDiff(gotCatalog, catalog))
	}
	for _, ev := range got.Events {
		if !strings.Contains(gotCatalog, "#### `"+ev.Type+"`") {
			t.Fatalf("event type %q is missing from docs/session-format.md", ev.Type)
		}
	}
	if !strings.Contains(string(doc), "`session.header`") {
		t.Fatal("docs/session-format.md does not document session.header")
	}
}

func TestSyntheticSessionFixture(t *testing.T) {
	root := repoRoot(t)
	logPath := filepath.Join(root, "internal", "persist", "session", "testdata", "synthetic_session.jsonl")
	metaPath := filepath.Join(root, "internal", "persist", "session", "testdata", "synthetic_legacy.meta.json")
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	metaRaw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, logPath, logRaw)
	assertNoSecrets(t, metaPath, metaRaw)
	if bytes.Contains(logRaw, []byte(`"/`)) || bytes.Contains(metaRaw, []byte(`"/`)) {
		t.Fatal("synthetic fixture contains a JSON string that starts with /")
	}

	contract, err := buildFormatContract(t)
	if err != nil {
		t.Fatal(err)
	}
	byType := map[string]eventSpec{}
	for _, ev := range contract.Events {
		byType[ev.Type] = ev
	}

	dir := t.TempDir()
	const id = "sess-synthetic"
	if err := os.WriteFile(LogPath(dir, id), logRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MetaPath(dir, id), metaRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	meta, err := ReadMeta(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.SessionID != "" || meta.Model != "" {
		t.Fatalf("legacy sidecar loaded new fields: %+v", meta)
	}
	if meta.ProjectKey != "proj-synthetic" || meta.Title != "synthetic" || meta.ParentSessionID != "sess-parent" {
		t.Fatalf("legacy sidecar = %+v", meta)
	}
	if meta.LeadSessionID != "sess-lead" || meta.ForkedFrom != "sess-fork-src" || meta.WorktreeBranch != "branch-synthetic" {
		t.Fatalf("legacy sidecar lineage = %+v", meta)
	}
	if strings.HasPrefix(meta.ProjectKey, "/") || strings.HasPrefix(meta.WorktreePath, "/") {
		t.Fatalf("fixture used a real path: %+v", meta)
	}

	events, err := NewManager(dir).Replay(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("replayed %d events, want 5 (header is not an event)", len(events))
	}

	lines := bytes.Split(bytes.TrimSuffix(logRaw, []byte("\n")), []byte("\n"))
	if len(lines) != 6 {
		t.Fatalf("fixture lines = %d, want 6", len(lines))
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatal(err)
	}
	assertSubset(t, contract.Header, mustObject(t, lines[0]), "session.header")
	if string(header["type"]) != `"`+headerType+`"` {
		t.Fatalf("header type = %s", header["type"])
	}
	var ver int
	if err := json.Unmarshal(header["schemaVersion"], &ver); err != nil {
		t.Fatal(err)
	}
	if ver != LogSchemaVersion {
		t.Fatalf("schemaVersion = %d, want %d", ver, LogSchemaVersion)
	}
	for i, line := range lines[1:] {
		var env protocol.Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("line %d: %v", i+2, err)
		}
		ev, ok := byType[env.Type]
		if !ok {
			t.Fatalf("line %d type %q is not a codec.go event", i+2, env.Type)
		}
		assertSubset(t, ev.Fields, env.Data, env.Type)
		if _, err := env.Decode(); err != nil {
			t.Fatalf("decode %s: %v", env.Type, err)
		}
	}
}

func buildFormatContract(t *testing.T) (formatContract, error) {
	t.Helper()
	root := repoRoot(t)
	codecPath := filepath.Join(root, "pkg", "protocol", "codec.go")
	emitted, decoded, err := codecEventTypes(codecPath)
	if err != nil {
		return formatContract{}, err
	}
	if strings.Join(emitted, "\n") != strings.Join(decoded, "\n") {
		return formatContract{}, errString("eventType and Decode disagree\n" + firstDiff(strings.Join(decoded, "\n"), strings.Join(emitted, "\n")))
	}
	envFields, err := schemaOf(reflect.TypeOf(protocol.Envelope{}), 0)
	if err != nil {
		return formatContract{}, err
	}
	headerFields, err := schemaOf(reflect.TypeOf(logHeader{}), 0)
	if err != nil {
		return formatContract{}, err
	}
	sideFields, shadowed, err := dominantFields(reflect.TypeOf(Meta{}), 0)
	if err != nil {
		return formatContract{}, err
	}
	if len(shadowed) != 0 {
		return formatContract{}, errString("sidecar Meta unexpectedly shadows " + strings.Join(shadowed, ","))
	}
	sideSpecs, err := specsFrom(sideFields, 0)
	if err != nil {
		return formatContract{}, err
	}
	c := formatContract{
		LogSchemaVersion: LogSchemaVersion,
		HeaderType:       headerType,
		Envelope:         envFields,
		Header:           headerFields,
		Sidecar:          sideSpecs,
	}
	seen := map[string]bool{}
	for _, typ := range emitted {
		if seen[typ] {
			return formatContract{}, errString("duplicate event type " + typ)
		}
		seen[typ] = true
		ev, err := (protocol.Envelope{Type: typ, Data: json.RawMessage(`{}`)}).Decode()
		if err != nil {
			return formatContract{}, errString(typ + ": " + err.Error())
		}
		if protocol.IsUnknown(ev) {
			return formatContract{}, errString(typ + " decoded as UnknownEvent")
		}
		rt := reflect.TypeOf(ev)
		zero := reflect.Zero(rt).Interface().(protocol.Event)
		wrapped, err := protocol.Wrap(zero)
		if err != nil {
			return formatContract{}, errString("wrap " + typ + ": " + err.Error())
		}
		if wrapped.Type != typ {
			return formatContract{}, errString("wrap " + rt.Name() + " => " + wrapped.Type + ", want " + typ)
		}
		fields, shadow, err := dominantFields(rt, 0)
		if err != nil {
			return formatContract{}, err
		}
		specs, err := specsFrom(fields, 0)
		if err != nil {
			return formatContract{}, err
		}
		c.Events = append(c.Events, eventSpec{
			Type:     typ,
			Go:       rt.Name(),
			Fields:   specs,
			Shadowed: shadow,
		})
	}
	return c, nil
}

func codecEventTypes(path string) (emitted, decoded []string, err error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, nil, err
	}
	eventType := findFunc(file, "eventType")
	decode := findFunc(file, "Decode")
	if eventType == nil || decode == nil {
		return nil, nil, errString("codec.go missing eventType or Decode")
	}
	return returnStrings(eventType), caseStrings(decode), nil
}

func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func returnStrings(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		if s, ok := stringLit(ret.Results[0]); ok && s != "" {
			out = append(out, s)
		}
		return true
	})
	return out
}

func caseStrings(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range cc.List {
			if s, ok := stringLit(expr); ok && s != "" {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func catalogSection(doc string) (string, error) {
	i := strings.Index(doc, catalogBegin)
	j := strings.Index(doc, catalogEnd)
	if i < 0 || j < 0 || j < i {
		return "", errString("docs/session-format.md missing catalog markers")
	}
	body := doc[i+len(catalogBegin) : j]
	body = strings.TrimPrefix(body, "\n")
	return body, nil
}

func replaceCatalog(doc, catalog string) (string, error) {
	i := strings.Index(doc, catalogBegin)
	j := strings.Index(doc, catalogEnd)
	if i < 0 || j < 0 || j < i {
		return "", errString("docs/session-format.md missing catalog markers")
	}
	if !strings.HasSuffix(catalog, "\n") {
		catalog += "\n"
	}
	return doc[:i+len(catalogBegin)] + "\n" + catalog + doc[j:], nil
}

func assertSubset(t *testing.T, fields []fieldSpec, raw json.RawMessage, path string) {
	t.Helper()
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	byName := map[string]fieldSpec{}
	for _, f := range fields {
		byName[f.Name] = f
	}
	for key, val := range obj {
		f, ok := byName[key]
		if !ok {
			t.Fatalf("%s field %q is not in the session schema (renamed or removed)", path, key)
		}
		switch f.Type {
		case "object":
			assertSubset(t, f.Fields, val, path+"."+key)
		case "array":
			if len(f.Fields) == 0 {
				continue
			}
			var items []json.RawMessage
			if err := json.Unmarshal(val, &items); err != nil {
				t.Fatalf("%s.%s: %v", path, key, err)
			}
			for i, item := range items {
				assertSubset(t, f.Fields, item, path+"."+key+"[]")
				if i > 0 {
					break
				}
			}
		}
	}
}

func mustObject(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("invalid JSON %s", raw)
	}
	return json.RawMessage(raw)
}

func assertNoSecrets(t *testing.T, path string, body []byte) {
	t.Helper()
	for _, needle := range []string{
		"/home/", "/Users/", "sk-", "ghp_", "AKIA", "Bearer ", "BEGIN OPENSSH", "BEGIN PRIVATE", "xoxb-", "password=",
	} {
		if bytes.Contains(body, []byte(needle)) {
			t.Fatalf("%s contains %q", path, needle)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "pkg", "protocol", "codec.go")); err != nil {
		t.Fatalf("repo root %s: %v", root, err)
	}
	return root
}

func firstDiff(have, want string) string {
	hl := strings.Split(have, "\n")
	wl := strings.Split(want, "\n")
	n := len(hl)
	if len(wl) > n {
		n = len(wl)
	}
	var b strings.Builder
	shown := 0
	for i := 0; i < n && shown < 40; i++ {
		var left, right string
		if i < len(hl) {
			left = hl[i]
		}
		if i < len(wl) {
			right = wl[i]
		}
		if left == right {
			continue
		}
		fmtLine(&b, i+1, left, right)
		shown++
	}
	if shown == 0 {
		return "(no line diff)"
	}
	return b.String()
}

func fmtLine(b *strings.Builder, n int, have, want string) {
	b.WriteString("line ")
	b.WriteString(strconv.Itoa(n))
	b.WriteString("\n- ")
	b.WriteString(have)
	b.WriteString("\n+ ")
	b.WriteString(want)
	b.WriteString("\n")
}

type errString string

func (e errString) Error() string { return string(e) }
