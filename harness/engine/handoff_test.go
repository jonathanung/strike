package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestParseCompletionHandoffWholeJSON(t *testing.T) {
	raw := `{
		"summary": "fixed auth",
		"files_changed": ["a.go", "b.go"],
		"verification": "go test ./internal/product/auth",
		"findings": ["token refresh race"],
		"blockers": [],
		"recommended_next_action": "merge after CI"
	}`
	h, ok := parseCompletionHandoff(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if h.Summary != "fixed auth" {
		t.Fatalf("summary = %q", h.Summary)
	}
	if len(h.FilesChanged) != 2 || h.FilesChanged[0] != "a.go" {
		t.Fatalf("files = %#v", h.FilesChanged)
	}
	if h.Verification != "go test ./internal/product/auth" {
		t.Fatalf("verification = %q", h.Verification)
	}
	if len(h.Findings) != 1 || h.Findings[0] != "token refresh race" {
		t.Fatalf("findings = %#v", h.Findings)
	}
	if h.RecommendedNextAction != "merge after CI" {
		t.Fatalf("next = %q", h.RecommendedNextAction)
	}
}

func TestParseCompletionHandoffCamelCase(t *testing.T) {
	raw := `{"summary":"ok","filesChanged":["x.go"],"recommendedNextAction":"ship"}`
	h, ok := parseCompletionHandoff(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(h.FilesChanged) != 1 || h.FilesChanged[0] != "x.go" {
		t.Fatalf("files = %#v", h.FilesChanged)
	}
	if h.RecommendedNextAction != "ship" {
		t.Fatalf("next = %q", h.RecommendedNextAction)
	}
}

func TestParseCompletionHandoffFenced(t *testing.T) {
	text := "Here is the result.\n\n```json\n{\"summary\":\"done\",\"files_changed\":[],\"blockers\":[]}\n```\n"
	h, ok := parseCompletionHandoff(text)
	if !ok {
		t.Fatal("expected fenced parse")
	}
	if h.Summary != "done" {
		t.Fatalf("summary = %q", h.Summary)
	}
}

func TestParseCompletionHandoffTrailing(t *testing.T) {
	text := "Work finished.\n\n{\"summary\":\"shipped\",\"files_changed\":[\"p.go\"],\"findings\":[],\"blockers\":[]}"
	h, ok := parseCompletionHandoff(text)
	if !ok {
		t.Fatal("expected trailing parse")
	}
	if h.Summary != "shipped" || len(h.FilesChanged) != 1 {
		t.Fatalf("handoff = %#v", h)
	}
}

func TestParseCompletionHandoffRejectsArbitraryJSON(t *testing.T) {
	if _, ok := parseCompletionHandoff(`{"foo":1,"bar":2}`); ok {
		t.Fatal("arbitrary JSON should not parse as handoff")
	}
	if _, ok := parseCompletionHandoff("just prose, no json"); ok {
		t.Fatal("prose should not parse")
	}
}

func TestParseCompletionHandoffMissingContextAndProvenance(t *testing.T) {
	raw := `{
		"summary": "blocked: need more",
		"files_changed": [],
		"findings": [],
		"blockers": ["need spec"],
		"missing_context": [
			{"kind": "path", "path": "docs/spec.md", "detail": "not attached"},
			{"kind": "question", "question": "Which API?"},
			{"artifact_id": "ab12", "detail": "infer kind"}
		],
		"provenance": ["goal", "constraint-1"]
	}`
	h, ok := parseCompletionHandoff(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(h.MissingContext) != 3 {
		t.Fatalf("missing = %#v", h.MissingContext)
	}
	if h.MissingContext[0].Kind != "path" || h.MissingContext[0].Path != "docs/spec.md" {
		t.Fatalf("mc0 = %#v", h.MissingContext[0])
	}
	if h.MissingContext[2].Kind != "artifact" || h.MissingContext[2].ArtifactID != "ab12" {
		t.Fatalf("mc2 inferred = %#v", h.MissingContext[2])
	}
	if len(h.Provenance) != 2 || h.Provenance[0] != "goal" {
		t.Fatalf("provenance = %#v", h.Provenance)
	}

	// camelCase keys
	h2, ok := parseCompletionHandoff(`{"summary":"x","missingContext":[{"kind":"item","itemId":"c1"}],"provenance":["c1"]}`)
	if !ok {
		t.Fatal("camelCase parse")
	}
	if len(h2.MissingContext) != 1 || h2.MissingContext[0].ItemID != "c1" {
		t.Fatalf("camel = %#v", h2.MissingContext)
	}
}

func TestApplyMissingContextStatus(t *testing.T) {
	h := protocol.CompletionHandoff{
		Summary:        "task completed",
		MissingContext: []protocol.MissingContextEntry{{Kind: "path", Path: "a.go"}},
	}
	st := applyMissingContextStatus(protocol.ChildStatusCompleted, &h)
	if st != protocol.ChildStatusBlocked {
		t.Fatalf("status = %q", st)
	}
	if h.Summary != "blocked: missing context" {
		t.Fatalf("summary = %q", h.Summary)
	}
	// failed stays failed
	h2 := protocol.CompletionHandoff{MissingContext: []protocol.MissingContextEntry{{Kind: "other"}}}
	if got := applyMissingContextStatus(protocol.ChildStatusFailed, &h2); got != protocol.ChildStatusFailed {
		t.Fatalf("failed → %q", got)
	}
}

func TestBundlePathScopeRules(t *testing.T) {
	rules := bundlePathScopeRules([]string{"internal/foo"})
	if len(rules) == 0 {
		t.Fatal("expected rules")
	}
	// Outside scope denied
	if act := permission.Evaluate("read", "other/x.go", rules); act != permission.Deny {
		t.Fatalf("outside read = %v", act)
	}
	// Inside allowed
	if act := permission.Evaluate("read", "internal/foo/bar.go", rules); act != permission.Allow {
		t.Fatalf("inside read = %v", act)
	}
	if act := permission.Evaluate("edit", "internal/foo/bar.go", rules); act != permission.Allow {
		t.Fatalf("inside edit = %v", act)
	}
	if act := permission.Evaluate("write", "secret.txt", rules); act != permission.Deny {
		t.Fatalf("outside write = %v", act)
	}
}

func TestParseCompletionHandoffArtifactRefs(t *testing.T) {
	raw := `{
		"summary": "done",
		"files_changed": [],
		"artifact_refs": [
			{"id": "abc123", "version": 3, "type": "findings"},
			{"id": "def456", "type": "patch"}
		]
	}`
	h, ok := parseCompletionHandoff(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(h.ArtifactRefs) != 2 {
		t.Fatalf("refs = %#v", h.ArtifactRefs)
	}
	if h.ArtifactRefs[0].ID != "abc123" || h.ArtifactRefs[0].Version != 3 || h.ArtifactRefs[0].Type != "findings" {
		t.Fatalf("ref0 = %#v", h.ArtifactRefs[0])
	}
	// camelCase key + bare string ids
	raw2 := `{"summary":"ok","artifactRefs":["id1","id2"]}`
	h2, ok := parseCompletionHandoff(raw2)
	if !ok {
		t.Fatal("expected camelCase parse")
	}
	if len(h2.ArtifactRefs) != 2 || h2.ArtifactRefs[0].ID != "id1" {
		t.Fatalf("string refs = %#v", h2.ArtifactRefs)
	}
	// Model view includes snake_case artifact_refs.
	js := marshalHandoffModelJSON(h)
	if !strings.Contains(js, `"artifact_refs"`) || !strings.Contains(js, "abc123") {
		t.Fatalf("model json = %s", js)
	}
}

func TestBuildCompletionHandoffSuccessIncomplete(t *testing.T) {
	h := buildCompletionHandoff(protocol.ChildStatusCompleted, "child finished work", []string{"a.go"})
	if !h.Incomplete {
		t.Fatal("want incomplete when no structured JSON")
	}
	if h.Summary != "child finished work" {
		t.Fatalf("summary = %q", h.Summary)
	}
	if len(h.FilesChanged) != 1 || h.FilesChanged[0] != "a.go" {
		t.Fatalf("files = %#v", h.FilesChanged)
	}
	if h.FilesChanged == nil || h.Findings == nil || h.Blockers == nil {
		t.Fatal("want non-nil slices")
	}
}

func TestBuildCompletionHandoffMergesTrackedAndModelFiles(t *testing.T) {
	text := `{"summary":"ok","files_changed":["model.go","a.go"],"verification":"make test","findings":[],"blockers":[]}`
	h := buildCompletionHandoff(protocol.ChildStatusCompleted, text, []string{"a.go", "engine.go"})
	if h.Incomplete {
		t.Fatal("structured JSON should clear incomplete")
	}
	// Sorted unique merge.
	want := []string{"a.go", "engine.go", "model.go"}
	if len(h.FilesChanged) != len(want) {
		t.Fatalf("files = %#v, want %#v", h.FilesChanged, want)
	}
	for i := range want {
		if h.FilesChanged[i] != want[i] {
			t.Fatalf("files = %#v, want %#v", h.FilesChanged, want)
		}
	}
	if h.Verification != "make test" {
		t.Fatalf("verification = %q", h.Verification)
	}
}

func TestBuildCompletionHandoffFailureReduced(t *testing.T) {
	h := buildCompletionHandoff(protocol.ChildStatusFailed, "boom\n\nError: no provider", nil)
	if h.Summary == "" {
		t.Fatal("want summary")
	}
	if len(h.Blockers) == 0 {
		t.Fatal("want default blockers on incomplete failure")
	}
	if h.FilesChanged == nil {
		t.Fatal("want empty files slice")
	}
}

func TestBuildCompletionHandoffCanceled(t *testing.T) {
	h := buildCompletionHandoff(protocol.ChildStatusCanceled, "", nil)
	if h.Summary != "task canceled" {
		t.Fatalf("summary = %q", h.Summary)
	}
	if len(h.Blockers) != 1 || h.Blockers[0] != "task canceled" {
		t.Fatalf("blockers = %#v", h.Blockers)
	}
}

func TestFormatChildCompletedNoticeIncludesHandoffJSON(t *testing.T) {
	got := formatChildCompletedNotice(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "abcdef12xyz"},
		Status:      protocol.ChildStatusCompleted,
		Summary:     "done",
		Name:        "explorer",
		Handoff: protocol.CompletionHandoff{
			Summary:      "done",
			FilesChanged: []string{"pkg/x.go"},
			Verification: "go test",
			Findings:     []string{},
			Blockers:     []string{},
		},
	})
	if !strings.Contains(got, "name=explorer") {
		t.Fatalf("notice missing name: %q", got)
	}
	if !strings.Contains(got, "handoff: ") {
		t.Fatalf("notice missing handoff prefix: %q", got)
	}
	if !strings.Contains(got, `"files_changed"`) || !strings.Contains(got, "pkg/x.go") {
		t.Fatalf("notice missing files_changed: %q", got)
	}
	// Ensure model-facing JSON is parseable after the prefix.
	idx := strings.Index(got, "handoff: ")
	rest := got[idx+len("handoff: "):]
	line, _, _ := strings.Cut(rest, "\n")
	var view handoffModelView
	if err := json.Unmarshal([]byte(line), &view); err != nil {
		t.Fatalf("handoff JSON: %v\nline=%q", err, line)
	}
	if view.Summary != "done" || len(view.FilesChanged) != 1 {
		t.Fatalf("view = %#v", view)
	}
}

func TestFormatChildCompletedNoticeFlagsIncomplete(t *testing.T) {
	got := formatChildCompletedNotice(protocol.ChildCompleted{
		Status:  protocol.ChildStatusCompleted,
		Summary: "prose only",
		Handoff: protocol.CompletionHandoff{
			Summary:      "prose only",
			FilesChanged: []string{},
			Findings:     []string{},
			Blockers:     []string{},
			Incomplete:   true,
		},
	})
	if !strings.Contains(got, "incomplete=true") {
		t.Fatalf("want incomplete flag note: %q", got)
	}
}

func TestNoteMutatedPathRelative(t *testing.T) {
	dir := t.TempDir()
	eng := New(Options{WorkDir: dir, SessionID: "s1"})
	abs := filepath.Join(dir, "internal", "foo.go")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng.noteMutatedPath(abs)
	eng.noteMutatedPath(abs) // dedupe
	got := eng.mutatedPathsSnapshot()
	if len(got) != 1 || got[0] != "internal/foo.go" {
		t.Fatalf("paths = %#v", got)
	}
}

func TestNoteMutatedPathViaFileSyncOnlyOnSuccess(t *testing.T) {
	// Mirrors turn wiring: FileSync records paths; Checkpoint alone must not.
	dir := t.TempDir()
	var outer []string
	eng := New(Options{
		WorkDir:   dir,
		SessionID: "s-fsync",
		FileSync: func(abs string, _ string, _ bool) {
			outer = append(outer, abs)
		},
	})
	abs := filepath.Join(dir, "ok.txt")
	// Simulate tool Context FileSync wrapper used in turn.go.
	sync := func(absPath string, content string, deleted bool) {
		eng.noteMutatedPath(absPath)
		if eng.opts.FileSync != nil {
			eng.opts.FileSync(absPath, content, deleted)
		}
	}
	// Checkpoint-only (failed write path) — no handoff entry.
	eng.checkpoints.BeginTurn("t1")
	eng.checkpoints.Snapshot(abs)
	if got := eng.mutatedPathsSnapshot(); len(got) != 0 {
		t.Fatalf("checkpoint alone should not track: %#v", got)
	}
	// Successful mutation notifies FileSync.
	sync(abs, "hi", false)
	got := eng.mutatedPathsSnapshot()
	if len(got) != 1 || got[0] != "ok.txt" {
		t.Fatalf("paths = %#v", got)
	}
	if len(outer) != 1 {
		t.Fatalf("outer FileSync calls = %d", len(outer))
	}
}

func TestMergeUniquePaths(t *testing.T) {
	got := mergeUniquePaths([]string{"b.go", "a.go"}, []string{"a.go", " c.go "})
	want := []string{"a.go", "b.go", "c.go"}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
}
