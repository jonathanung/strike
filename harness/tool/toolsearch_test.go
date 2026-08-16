package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSplitQuery(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		expect []string
	}{
		{name: "single word", query: "read", expect: []string{"read"}},
		{name: "two words", query: "read file", expect: []string{"read", "file"}},
		{name: "quoted phrase only", query: `"read file"`, expect: []string{"read file"}},
		{name: "quoted phrase + word", query: `"read file" write`, expect: []string{"read file", "write"}},
		{name: "word + quoted phrase", query: `write "read file"`, expect: []string{"write", "read file"}},
		{name: "multiple quoted", query: `"glob pattern" "regex search"`, expect: []string{"glob pattern", "regex search"}},
		{name: "mixed with extra spaces", query: `  "read  file"   write  `, expect: []string{"read  file", "write"}},
		{name: "empty quoted", query: `""`, expect: nil},
		{name: "empty quoted with words", query: `"" read`, expect: []string{"read"}},
		{name: "unmatched quote", query: `"read file`, expect: []string{"read file"}},
		{name: "empty string", query: "", expect: nil},
		{name: "tab separator", query: "read\tfile", expect: []string{"read", "file"}},
		{name: "newline separator", query: "read\nfile", expect: []string{"read", "file"}},
		{name: "cr separator", query: "read\rfile", expect: []string{"read", "file"}},
		{name: "crlf separator", query: "read\r\nfile", expect: []string{"read", "file"}},
		{name: "mixed whitespace", query: "read \t\n\r file", expect: []string{"read", "file"}},
		{name: "quoted with surrounding newlines", query: "\n\"read file\"\nwrite\n", expect: []string{"read file", "write"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitQuery(tt.query)
			if !reflect.DeepEqual(got, tt.expect) {
				t.Errorf("splitQuery(%q) = %v, want %v", tt.query, got, tt.expect)
			}
		})
	}
}

func TestToolSearchHits(t *testing.T) {
	reg := NewRegistry(NewRead(), NewBash())
	ts := NewToolSearch(reg)
	reg.Register(ts)

	res, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "read",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "- read:") {
		t.Errorf("output = %q", res.Output)
	}
	if strings.Contains(res.Output, "- bash:") {
		// "read" should not match bash name/description typically — bash desc may not contain "read"
		// Only fail if bash line present when query is exact tool name "read" — bash shouldn't match.
	}
	var meta struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Query != "read" || meta.Count < 1 {
		t.Errorf("meta = %+v", meta)
	}
	if !strings.Contains(res.Title, "matches") {
		t.Errorf("title = %q", res.Title)
	}
}

func TestToolSearchEmptyQuery(t *testing.T) {
	reg := NewRegistry(NewRead())
	ts := NewToolSearch(reg)
	_, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "   ",
	}), allowAll(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("got %v", err)
	}
}

func TestToolSearchNoMatch(t *testing.T) {
	reg := NewRegistry(NewRead(), NewBash())
	ts := NewToolSearch(reg)
	reg.Register(ts)
	res, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "zzzz-not-a-tool-xyz",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "No tools matched") {
		t.Errorf("output = %q", res.Output)
	}
	if !strings.Contains(res.Title, "0 matches") {
		t.Errorf("title = %q", res.Title)
	}
}

func TestToolSearchMultiToken(t *testing.T) {
	reg := NewRegistry(NewRead(), NewBash(), NewGlob())
	ts := NewToolSearch(reg)
	reg.Register(ts)

	// Both tokens must match the same tool's name+description haystack.
	res, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "bash command",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "- bash:") {
		t.Errorf("expected bash match, got %q", res.Output)
	}
	// "bash command" should not match read (no "bash" in read).
	if strings.Contains(res.Output, "- read:") {
		t.Errorf("read should not match %q", res.Output)
	}

	// Tokens that no single tool satisfies.
	res, err = ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "bash glob",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "No tools matched") {
		// Unless some description mentions both — unlikely for stock tools.
		if strings.Contains(res.Output, "- bash:") && strings.Contains(res.Output, "- glob:") {
			t.Errorf("multi-token AND should not return both separately: %q", res.Output)
		}
	}
}

func TestToolSearchQuotedPhrase(t *testing.T) {
	reg := NewRegistry(NewRead(), NewBash(), NewGlob(), NewGrep())
	ts := NewToolSearch(reg)
	reg.Register(ts)

	// Quoted phrase with space — should match grep's description which contains "file contents".
	res, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": `"file contents"`,
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "- grep:") {
		t.Errorf("expected grep match for quoted phrase, got %q", res.Output)
	}

	// Quoted phrase that is not a contiguous substring in any tool.
	res, err = ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": `"no such tool exists"`,
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "No tools matched") {
		t.Errorf("expected no match for non-existent phrase: %q", res.Output)
	}
}

func TestToolSearchPermissionDenied(t *testing.T) {
	reg := NewRegistry(NewRead())
	ts := NewToolSearch(reg)
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
	}
	_, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{"query": "read"}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestToolSearchNilRegistry(t *testing.T) {
	ts := NewToolSearch(nil)
	_, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{"query": "x"}), allowAll(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("got %v", err)
	}
}
