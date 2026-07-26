package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseEditMetadata(t *testing.T) {
	tests := []struct {
		name    string
		raw     json.RawMessage
		wantOK  bool
		wantOld string
		wantNew string
		wantCnt int
	}{
		{
			name:    "valid full JSON with count",
			raw:     json.RawMessage(`{"oldString":"foo","newString":"bar","count":3}`),
			wantOK:  true,
			wantOld: "foo",
			wantNew: "bar",
			wantCnt: 3,
		},
		{
			name:    "valid without count",
			raw:     json.RawMessage(`{"oldString":"a","newString":"b"}`),
			wantOK:  true,
			wantOld: "a",
			wantNew: "b",
			wantCnt: 0,
		},
		{
			name:    "empty strings for old and new still ok",
			raw:     json.RawMessage(`{"oldString":"","newString":""}`),
			wantOK:  true,
			wantOld: "",
			wantNew: "",
		},
		{
			name:   "empty raw",
			raw:    nil,
			wantOK: false,
		},
		{
			name:   "empty byte slice",
			raw:    json.RawMessage{},
			wantOK: false,
		},
		{
			name:   "invalid JSON",
			raw:    json.RawMessage(`{not json`),
			wantOK: false,
		},
		{
			name:   "write-shaped metadata",
			raw:    json.RawMessage(`{"exists":true,"oldSize":1,"newSize":2}`),
			wantOK: false,
		},
		{
			name:   "missing oldString",
			raw:    json.RawMessage(`{"newString":"bar"}`),
			wantOK: false,
		},
		{
			name:   "missing newString",
			raw:    json.RawMessage(`{"oldString":"foo"}`),
			wantOK: false,
		},
		{
			name:   "wrong type oldString number",
			raw:    json.RawMessage(`{"oldString":1,"newString":"bar"}`),
			wantOK: false,
		},
		{
			name:   "wrong type newString number",
			raw:    json.RawMessage(`{"oldString":"foo","newString":2}`),
			wantOK: false,
		},
		{
			name:   "null",
			raw:    json.RawMessage(`null`),
			wantOK: false,
		},
		{
			name:   "null oldString",
			raw:    json.RawMessage(`{"oldString":null,"newString":"bar"}`),
			wantOK: false,
		},
		{
			name:   "null newString",
			raw:    json.RawMessage(`{"oldString":"foo","newString":null}`),
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseEditMetadata(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (meta=%+v)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.OldString != tt.wantOld || got.NewString != tt.wantNew || got.Count != tt.wantCnt {
				t.Errorf("meta = %+v, want old=%q new=%q count=%d", got, tt.wantOld, tt.wantNew, tt.wantCnt)
			}
		})
	}
}

func TestFirstChangedLine(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		file string
		want int
	}{
		{
			name: "simple replace mid-file",
			old:  "old",
			new:  "new",
			file: "line1\nnew\nline3\n",
			want: 2,
		},
		{
			name: "shared prefix lines skip to first change",
			old:  "keep\nold mid\ntail",
			new:  "keep\nnew mid\ntail",
			file: "hdr\nkeep\nnew mid\ntail\n",
			want: 3,
		},
		{
			name: "empty new falls back to 1",
			old:  "gone",
			new:  "",
			file: "other\n",
			want: 1,
		},
		{
			name: "new not in file falls back to 1",
			old:  "a",
			new:  "missing",
			file: "nope\n",
			want: 1,
		},
		{
			name: "replace at start",
			old:  "a",
			new:  "b",
			file: "b\nc\n",
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstChangedLine(tt.old, tt.new, tt.file); got != tt.want {
				t.Fatalf("firstChangedLine = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestToolTouchedPath(t *testing.T) {
	tests := []struct {
		name string
		cell *toolCell
		want string
	}{
		{
			name: "edit uses title",
			cell: &toolCell{name: "edit", title: "pkg/a.go", done: true},
			want: "pkg/a.go",
		},
		{
			name: "edit falls back to args",
			cell: &toolCell{
				name: "edit",
				done: true,
				args: json.RawMessage(`{"filePath":"from/args.go","oldString":"a","newString":"b"}`),
			},
			want: "from/args.go",
		},
		{
			name: "write title",
			cell: &toolCell{name: "write", title: "new.txt", done: true},
			want: "new.txt",
		},
		{
			name: "apply_patch first path",
			cell: &toolCell{
				name:     "apply_patch",
				done:     true,
				metadata: json.RawMessage(`[{"type":"update","path":"one.go"},{"type":"add","path":"two.go"}]`),
			},
			want: "one.go",
		},
		{
			name: "apply_patch move uses moveTo",
			cell: &toolCell{
				name:     "apply_patch",
				done:     true,
				metadata: json.RawMessage(`[{"type":"move","path":"old.go","moveTo":"new.go"}]`),
			},
			want: "new.go",
		},
		{
			name: "bash not reviewable path",
			cell: &toolCell{name: "bash", title: "echo hi", done: true},
			want: "",
		},
		{
			name: "error edit skipped by reviewable",
			cell: &toolCell{name: "edit", title: "x.go", done: true, isError: true},
			want: "x.go", // path still extracted; reviewable() gates isError
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolTouchedPath(tt.cell); got != tt.want {
				t.Fatalf("toolTouchedPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReviewTargetUsesFirstHunkLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	content := "package main\n\nfunc a() {}\nfunc b() {}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate post-edit file where old "func a() {}" became "func a() { return }".
	updated := "package main\n\nfunc a() { return }\nfunc b() {}\n"
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{
		"oldString": "func a() {}",
		"newString": "func a() { return }",
		"count":     1,
	})
	tc := &toolCell{
		name:     "edit",
		title:    "sample.go",
		done:     true,
		metadata: meta,
	}
	if !tc.reviewable() {
		t.Fatal("edit cell should be reviewable")
	}
	gotPath, line, ok := tc.reviewTarget(dir)
	if !ok {
		t.Fatal("reviewTarget ok=false")
	}
	if gotPath != "sample.go" {
		t.Errorf("path = %q, want sample.go", gotPath)
	}
	if line != 3 {
		t.Errorf("line = %d, want 3 (first changed hunk)", line)
	}
}

func TestReviewableRequiresDoneSuccess(t *testing.T) {
	base := &toolCell{name: "edit", title: "a.go", done: true}
	if !base.reviewable() {
		t.Fatal("want reviewable")
	}
	running := &toolCell{name: "edit", title: "a.go", done: false}
	if running.reviewable() {
		t.Fatal("running edit must not be reviewable")
	}
	errCell := &toolCell{name: "edit", title: "a.go", done: true, isError: true}
	if errCell.reviewable() {
		t.Fatal("error edit must not be reviewable")
	}
	bash := &toolCell{name: "bash", title: "ls", done: true, output: "x\ny\nz\n1\n2\n3\n4\n5\n6\n"}
	if bash.reviewable() {
		t.Fatal("bash must not be reviewable")
	}
	if !bash.collapsible() {
		t.Fatal("long bash should still be collapsible")
	}
}
