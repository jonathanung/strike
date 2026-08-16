package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func parseStatus(t *testing.T, res Result) statusPayload {
	t.Helper()
	var p statusPayload
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatalf("unmarshal status: %v\n%s", err, res.Output)
	}
	return p
}

func execStatus(t *testing.T, tc *Context) (Result, error) {
	t.Helper()
	return NewStatus().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
}

func TestStatusNilSafeEmpty(t *testing.T) {
	tc := allowAll(t.TempDir())
	res, err := execStatus(t, tc)
	if err != nil {
		t.Fatal(err)
	}
	p := parseStatus(t, res)
	if !p.OK || p.Count != 0 || p.Files == nil || len(p.Files) != 0 {
		t.Fatalf("empty payload = %+v", p)
	}
	if res.Title != "0 files" {
		t.Fatalf("title = %q", res.Title)
	}

	var nilFiles *FileState
	var nilDiff *TurnDiff
	tc.Files = nilFiles
	tc.TurnDiff = nilDiff
	res, err = execStatus(t, tc)
	if err != nil {
		t.Fatal(err)
	}
	p = parseStatus(t, res)
	if !p.OK || p.Count != 0 || len(p.Files) != 0 {
		t.Fatalf("nil Files/TurnDiff payload = %+v", p)
	}
}

func TestStatusCreateUpdateDeleteMatchSnapshot(t *testing.T) {
	dir := t.TempDir()
	td := &TurnDiff{}
	fs := &FileState{}
	tc := allowAll(dir)
	tc.TurnDiff = td
	tc.Files = fs

	created := []byte("created\n")
	if _, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "new.txt",
		"content":  string(created),
	}), tc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "old.txt",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "old.txt",
		"oldString": "one",
		"newString": "two",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "gone.txt",
	}), tc); err != nil {
		t.Fatal(err)
	}

	res, err := execStatus(t, tc)
	if err != nil {
		t.Fatal(err)
	}
	p := parseStatus(t, res)
	if !p.OK {
		t.Fatalf("payload = %+v", p)
	}

	snap := td.Snapshot()
	if p.Count != len(snap) || len(p.Files) != len(snap) {
		t.Fatalf("count = %d files=%d snap=%d (%#v)", p.Count, len(p.Files), len(snap), p.Files)
	}
	gotKinds := make([]FileChange, 0, len(p.Files))
	for _, f := range p.Files {
		gotKinds = append(gotKinds, FileChange{Path: f.Path, Kind: f.Kind})
	}
	if !reflect.DeepEqual(gotKinds, snap) {
		t.Fatalf("status kinds = %#v, snapshot = %#v", gotKinds, snap)
	}

	byPath := map[string]statusEntry{}
	for _, f := range p.Files {
		byPath[f.Path] = f
	}
	if byPath["new.txt"].Kind != ChangeCreate {
		t.Fatalf("new.txt = %+v", byPath["new.txt"])
	}
	if byPath["old.txt"].Kind != ChangeUpdate {
		t.Fatalf("old.txt = %+v", byPath["old.txt"])
	}
	if byPath["gone.txt"].Kind != ChangeDelete {
		t.Fatalf("gone.txt = %+v", byPath["gone.txt"])
	}
	if byPath["gone.txt"].Hash != "" {
		t.Fatalf("delete should omit hash: %+v", byPath["gone.txt"])
	}
	if byPath["new.txt"].Hash != ContentHash(created) {
		t.Fatalf("create hash = %q, want %q", byPath["new.txt"].Hash, ContentHash(created))
	}
	if byPath["old.txt"].Hash != ContentHash([]byte("two")) {
		t.Fatalf("update hash = %q, want %q", byPath["old.txt"].Hash, ContentHash([]byte("two")))
	}
}

func TestStatusHashesMatchBaseHash(t *testing.T) {
	dir := t.TempDir()
	td := &TurnDiff{}
	fs := &FileState{}
	tc := allowAll(dir)
	tc.TurnDiff = td
	tc.Files = fs

	content := []byte("payload for hash\n")
	if _, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "h.txt",
		"content":  string(content),
	}), tc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "h.txt",
	}), tc); err != nil {
		t.Fatal(err)
	}

	res, err := execStatus(t, tc)
	if err != nil {
		t.Fatal(err)
	}
	p := parseStatus(t, res)
	if len(p.Files) != 1 || p.Files[0].Path != "h.txt" {
		t.Fatalf("files = %+v", p.Files)
	}
	want := ContentHash(content)
	if p.Files[0].Hash != want {
		t.Fatalf("hash = %q, want %q", p.Files[0].Hash, want)
	}
	if err := CheckBaseHash(filepath.Join(dir, "h.txt"), p.Files[0].Hash, "h.txt"); err != nil {
		t.Fatalf("baseHash check: %v", err)
	}
	if _, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "h.txt",
		"oldString": "payload",
		"newString": "updated",
		"baseHash":  p.Files[0].Hash,
	}), tc); err != nil {
		t.Fatalf("edit with status hash as baseHash: %v", err)
	}
}

func TestStatusResetAtTurnBoundary(t *testing.T) {
	dir := t.TempDir()
	td := &TurnDiff{}
	tc := allowAll(dir)
	tc.TurnDiff = td
	tc.Files = &FileState{}

	if _, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.txt",
		"content":  "x",
	}), tc); err != nil {
		t.Fatal(err)
	}
	res, err := execStatus(t, tc)
	if err != nil {
		t.Fatal(err)
	}
	if p := parseStatus(t, res); p.Count != 1 {
		t.Fatalf("before reset: %+v", p)
	}

	td.Reset()
	res, err = execStatus(t, tc)
	if err != nil {
		t.Fatal(err)
	}
	p := parseStatus(t, res)
	if !p.OK || p.Count != 0 || len(p.Files) != 0 {
		t.Fatalf("after reset: %+v", p)
	}
}

func TestStatusPermissionDenied(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Ask = func(context.Context, AskRequest) error { return errors.New("denied-by-test") }
	_, err := execStatus(t, tc)
	if err == nil {
		t.Fatal("expected permission error")
	}
	if err.Error() != "denied-by-test" {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusNameSchemaContract(t *testing.T) {
	tl := NewStatus()
	if tl.Name() != "status" {
		t.Fatalf("name = %q", tl.Name())
	}
	if len(tl.Schema()) == 0 || tl.Description() == "" {
		t.Fatal("missing schema/description")
	}
	c := LookupContract(tl)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.SideEffect != SideEffectNone || c.Idempotency != IdempotencySafeRetry {
		t.Fatalf("contract = %+v", c)
	}
}
