package protocol

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestSchemaDrift(t *testing.T) {
	files, err := emitFiles()
	if err != nil {
		t.Fatal(err)
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	update := os.Getenv("UPDATE_SCHEMA") == "1"
	for _, f := range files {
		path := filepath.Join(root, f.rel)
		if update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, f.body, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		disk, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (run make protocol-schema)", f.rel, err)
		}
		if !bytes.Equal(disk, f.body) {
			t.Errorf("%s drifted from pkg/protocol; run make protocol-schema", f.rel)
		}
	}
}

func TestSchemaShape(t *testing.T) {
	files, err := emitFiles()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	for _, f := range files {
		if f.rel != schemaRelPath {
			continue
		}
		if err := json.Unmarshal(f.body, &schema); err != nil {
			t.Fatal(err)
		}
	}
	if schema["x-strike-protocol-version"] != Version {
		t.Fatalf("version %v", schema["x-strike-protocol-version"])
	}
	if schema["x-strike-legacy-version"] != LegacyVersion {
		t.Fatalf("legacy %v", schema["x-strike-legacy-version"])
	}
	if schema["x-strike-unknown-event"] != "valid" || schema["x-strike-unknown-op"] != "reject" {
		t.Fatalf("compat flags %#v", schema)
	}
	defs := schema["$defs"].(map[string]any)
	spawn := defs["TeamSpawn"].(map[string]any)
	props := spawn["properties"].(map[string]any)
	if _, ok := props["idempotencyKey"]; !ok {
		t.Fatal("TeamSpawn missing flattened idempotencyKey")
	}
	if _, ok := props["objective"]; !ok {
		t.Fatal("TeamSpawn missing objective")
	}
	if _, ok := props["Reply"]; ok {
		t.Fatal("TeamSpawn leaked in-process Reply field")
	}
	effort := defs["Effort"].(map[string]any)
	if effort["x-strike-enum-open"] != true {
		t.Fatalf("Effort closed: %#v", effort["x-strike-enum-open"])
	}
	var sawHigh, sawEmpty bool
	for _, v := range effort["x-strike-known-values"].([]any) {
		switch v {
		case "high":
			sawHigh = true
		case "":
			sawEmpty = true
		}
	}
	if !sawHigh || !sawEmpty {
		t.Fatalf("Effort known values %#v", effort["x-strike-known-values"])
	}
	diag := defs["DiagnosticConfig"].(map[string]any)
	digests := diag["properties"].(map[string]any)["digests"].(map[string]any)
	add := digests["additionalProperties"].(map[string]any)
	if add["type"] != "string" {
		t.Fatalf("digests %#v", digests)
	}
	known := defs["KnownEvent"].(map[string]any)
	found := false
	for _, branch := range known["oneOf"].([]any) {
		props := branch.(map[string]any)["properties"].(map[string]any)
		if props["type"].(map[string]any)["const"] == "user.message" {
			found = true
		}
	}
	if !found {
		t.Fatal("KnownEvent missing user.message")
	}
	env := defs["Envelope"].(map[string]any)
	if env["additionalProperties"] != true {
		t.Fatal("Envelope rejects unknown fields")
	}
}

func TestSchemaGoldenRoundTrip(t *testing.T) {
	files, err := emitFiles()
	if err != nil {
		t.Fatal(err)
	}
	var golden []byte
	for _, f := range files {
		if f.rel == goldenRelPath {
			golden = f.body
		}
	}
	lines := bytes.Split(bytes.TrimSuffix(golden, []byte("\n")), []byte("\n"))
	if len(lines) != 6 {
		t.Fatalf("golden lines = %d", len(lines))
	}
	if !bytes.Contains(lines[0], []byte(`"futureField":1`)) {
		t.Fatalf("missing unknown field: %s", lines[0])
	}
	var env Envelope
	if err := json.Unmarshal(lines[0], &env); err != nil {
		t.Fatal(err)
	}
	ev, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	um, ok := ev.(UserMessage)
	if !ok || um.Text != "hello" {
		t.Fatalf("user message %#v", ev)
	}
	if err := json.Unmarshal(lines[5], &env); err != nil {
		t.Fatal(err)
	}
	ev, err = env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	unk, ok := ev.(UnknownEvent)
	if !ok || unk.Type != "future.extension" {
		t.Fatalf("unknown %#v", ev)
	}
	if !bytes.Contains(unk.Data, []byte(`"ok":true`)) {
		t.Fatalf("unknown data %s", unk.Data)
	}
}

func TestSchemaCatalogMatchesCodec(t *testing.T) {
	files := parseNonTestFiles(t)
	consts := constStringValues(files)
	fixed, dynamic := funcTypeSwitch(t, files, "eventType", consts)
	if len(dynamic) != 1 || dynamic[0] != "UnknownEvent" {
		t.Fatalf("dynamic event cases = %v", dynamic)
	}
	got := map[string]string{}
	for _, ev := range eventPrototypes() {
		name := reflect.TypeOf(ev).Name()
		got[name] = eventType(ev)
	}
	if !reflect.DeepEqual(fixed, got) {
		t.Fatalf("event catalog drift\ncodec %v\ncatalog %v", fixed, got)
	}
	decodeEvents := methodStringCases(t, files, "Envelope", "Decode", consts)
	if !sameStrings(decodeEvents, valuesOf(fixed)) {
		t.Fatalf("Decode cases != eventType\n%v\n%v", decodeEvents, valuesOf(fixed))
	}

	opFixed, opDynamic := funcTypeSwitch(t, files, "opType", consts)
	if len(opDynamic) != 0 {
		t.Fatalf("dynamic op cases = %v", opDynamic)
	}
	gotOps := map[string]string{}
	for _, op := range opPrototypes() {
		name := reflect.TypeOf(op).Name()
		gotOps[name] = opType(op)
	}
	if !reflect.DeepEqual(opFixed, gotOps) {
		t.Fatalf("op catalog drift\ncodec %v\ncatalog %v", opFixed, gotOps)
	}
	decodeOps := methodStringCases(t, files, "OpEnvelope", "Decode", consts)
	if !sameStrings(decodeOps, valuesOf(opFixed)) {
		t.Fatalf("op Decode cases != opType\n%v\n%v", decodeOps, valuesOf(opFixed))
	}
}

func parseNonTestFiles(t *testing.T) []*ast.File {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "pkg", "protocol")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
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
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

func constStringValues(files []*ast.File) map[string]string {
	raw := map[string]ast.Expr{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						raw[name.Name] = vs.Values[i]
					}
				}
			}
		}
	}
	out := map[string]string{}
	var resolve func(string, int) (string, bool)
	resolve = func(name string, depth int) (string, bool) {
		if depth > 8 {
			return "", false
		}
		if s, ok := out[name]; ok {
			return s, true
		}
		expr, ok := raw[name]
		if !ok {
			return "", false
		}
		switch v := expr.(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(v.Value)
			if err != nil {
				return "", false
			}
			out[name] = s
			return s, true
		case *ast.Ident:
			s, ok := resolve(v.Name, depth+1)
			if ok {
				out[name] = s
			}
			return s, ok
		default:
			return "", false
		}
	}
	for name := range raw {
		resolve(name, 0)
	}
	return out
}

func funcTypeSwitch(t *testing.T, files []*ast.File, fn string, consts map[string]string) (map[string]string, []string) {
	t.Helper()
	decl := findFunc(files, fn, "")
	if decl == nil {
		t.Fatalf("missing func %s", fn)
	}
	fixed := map[string]string{}
	var dynamic []string
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok || clause.List == nil {
			return true
		}
		wire, dyn := caseReturn(clause, consts)
		for _, expr := range clause.List {
			id, ok := expr.(*ast.Ident)
			if !ok {
				t.Fatalf("%s case %T", fn, expr)
			}
			if dyn {
				dynamic = append(dynamic, id.Name)
				continue
			}
			fixed[id.Name] = wire
		}
		return true
	})
	return fixed, dynamic
}

func caseReturn(clause *ast.CaseClause, consts map[string]string) (string, bool) {
	for _, stmt := range clause.Body {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		switch v := ret.Results[0].(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return "", true
			}
			s, err := strconv.Unquote(v.Value)
			if err != nil {
				return "", true
			}
			return s, false
		case *ast.Ident:
			if s, ok := consts[v.Name]; ok {
				return s, false
			}
			return "", true
		default:
			return "", true
		}
	}
	return "", true
}

func methodStringCases(t *testing.T, files []*ast.File, recv, method string, consts map[string]string) []string {
	t.Helper()
	decl := findFunc(files, method, recv)
	if decl == nil {
		t.Fatalf("missing method %s.%s", recv, method)
	}
	var cases []string
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range clause.List {
			switch v := expr.(type) {
			case *ast.BasicLit:
				if v.Kind != token.STRING {
					t.Fatalf("%s.%s case lit %s", recv, method, v.Kind)
				}
				s, err := strconv.Unquote(v.Value)
				if err != nil {
					t.Fatal(err)
				}
				cases = append(cases, s)
			case *ast.Ident:
				s, ok := consts[v.Name]
				if !ok {
					t.Fatalf("%s.%s case ident %s is not a string const", recv, method, v.Name)
				}
				cases = append(cases, s)
			default:
				t.Fatalf("%s.%s case %T", recv, method, expr)
			}
		}
		return true
	})
	return cases
}

func findFunc(files []*ast.File, name, recv string) *ast.FuncDecl {
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name {
				continue
			}
			got := receiverType(fn)
			if recv == "" && got == "" {
				return fn
			}
			if recv != "" && got == recv {
				return fn
			}
		}
	}
	return nil
}

func receiverType(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	id, ok := t.(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name
}

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return reflect.DeepEqual(aa, bb)
}
