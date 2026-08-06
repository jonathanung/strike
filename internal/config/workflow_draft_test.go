package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
)

func validDraftJSON(name string) string {
	return `{
  "schemaVersion": 1,
  "name": "` + name + `",
  "description": "test flow",
  "phases": [
    {
      "name": "review",
      "agent": "reviewer",
      "context": "Read-only review",
      "permissions": [
        {"permission": "write", "pattern": "*", "action": "deny"},
        {"permission": "edit", "pattern": "*", "action": "deny"}
      ],
      "exit": {"type": "user"}
    },
    {
      "name": "fix",
      "agent": "build",
      "permissions": [
        {"permission": "bash", "pattern": "*", "action": "allow"}
      ],
      "exit": {"type": "check", "command": "make test"}
    }
  ]
}`
}

func TestDraftFromJSONValid(t *testing.T) {
	d := DraftFromJSON([]byte(validDraftJSON("ship-it")), "model")
	if !d.Valid() {
		t.Fatalf("want valid, diagnostics=%v", d.Diagnostics)
	}
	if d.Workflow.Name != "ship-it" || len(d.Workflow.Phases) != 2 {
		t.Fatalf("workflow = %#v", d.Workflow)
	}
}

func TestDraftFromJSONInvalidRemainsEditable(t *testing.T) {
	// Missing phases — invalid but Raw preserved for correction.
	raw := `{"schemaVersion":1,"name":"broken","phases":[]}`
	d := DraftFromJSON([]byte(raw), "model")
	if d.Valid() {
		t.Fatal("expected invalid")
	}
	if len(d.Diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	if string(d.Raw) != raw {
		t.Fatalf("Raw not preserved: %q", d.Raw)
	}
	if d.Workflow.Name != "broken" {
		t.Fatalf("partial name = %q", d.Workflow.Name)
	}
	// Correction path.
	d.ApplyJSON([]byte(validDraftJSON("broken")), map[string]struct{}{
		"reviewer": {}, "build": {},
	})
	if !d.Valid() {
		t.Fatalf("after correction: %v", d.Diagnostics)
	}
	if d.SourceLabel != "edit" {
		t.Fatalf("source after edit = %q", d.SourceLabel)
	}
}

func TestDraftFromModelTextFencedAndBare(t *testing.T) {
	inner := validDraftJSON("fenced")
	fenced := "Here you go:\n```json\n" + inner + "\n```\n"
	d := DraftFromModelText(fenced, "model")
	if !d.Valid() {
		t.Fatalf("fenced: %v", d.Diagnostics)
	}
	bare := "Sure.\n" + inner + "\nThanks."
	d2 := DraftFromModelText(bare, "model")
	if !d2.Valid() {
		t.Fatalf("bare: %v", d2.Diagnostics)
	}
	d3 := DraftFromModelText("no json here", "model")
	if d3.Valid() || len(d3.Diagnostics) == 0 {
		t.Fatalf("want extract error, got valid=%v diag=%v", d3.Valid(), d3.Diagnostics)
	}
}

func TestReviewHighlightsChecksAndWidening(t *testing.T) {
	d := DraftFromJSON([]byte(validDraftJSON("rev")), "model")
	d.Revalidate(map[string]struct{}{"reviewer": {}, "build": {}})
	// Baseline denies bash so phase allow is a widening.
	baseline := []permission.Ruleset{{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
		{Permission: "write", Pattern: "*", Action: permission.Ask},
		{Permission: "edit", Pattern: "*", Action: permission.Ask},
	}}
	rev := ReviewWorkflowDraft(d, DraftReviewOpts{Baseline: baseline})
	if !rev.Valid {
		t.Fatalf("review valid=false: %v", rev.Diagnostics)
	}
	if !rev.HasChecks {
		t.Fatal("expected HasChecks")
	}
	if !rev.HasWidening {
		t.Fatal("expected HasWidening")
	}
	if len(rev.Phases) != 2 {
		t.Fatalf("phases = %d", len(rev.Phases))
	}
	if !rev.Phases[1].CheckHighlighted || rev.Phases[1].GateCommand != "make test" {
		t.Fatalf("check phase = %#v", rev.Phases[1])
	}
	if len(rev.Phases[1].Widening) == 0 {
		t.Fatal("expected bash widening on fix phase")
	}
	text := FormatDraftReview(rev)
	for _, want := range []string{
		"EXECUTABLE CHECK GATES",
		"EFFECTIVE PERMISSION WIDENING",
		"make test",
		"[CHECK]",
		"[WIDEN]",
		"never activated",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("review text missing %q:\n%s", want, text)
		}
	}
}

func TestSaveWorkflowDraftRequiresConfirmAndValidity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	d := DraftFromJSON([]byte(validDraftJSON("save-me")), "model")
	d.Revalidate(map[string]struct{}{"reviewer": {}, "build": {}})

	// No confirm.
	if _, err := SaveWorkflowDraft(d, SaveDraftOpts{Scope: "project", WorkDir: work}); !errors.Is(err, ErrSaveNotConfirmed) {
		t.Fatalf("err = %v", err)
	}
	// Confirm but invalid.
	bad := DraftFromJSON([]byte(`{"name":"x","phases":[]}`), "model")
	if _, err := SaveWorkflowDraft(bad, SaveDraftOpts{Scope: "project", WorkDir: work, Confirm: true}); !errors.Is(err, ErrDraftInvalid) {
		t.Fatalf("err = %v", err)
	}
	// Accepted save.
	path, err := SaveWorkflowDraft(d, SaveDraftOpts{Scope: "project", WorkDir: work, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(work, ".strike", "workflows", "save-me.json")
	if path != wantPath {
		t.Fatalf("path = %q want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"active"`) {
		t.Fatal("save must not mark active")
	}
	// Overwrite without force.
	if _, err := SaveWorkflowDraft(d, SaveDraftOpts{Scope: "project", WorkDir: work, Confirm: true}); !errors.Is(err, ErrWorkflowExists) {
		t.Fatalf("err = %v", err)
	}
	// Prior content preserved after refused overwrite.
	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data2) != string(data) {
		t.Fatal("prior file changed after refused overwrite")
	}
	// Force overwrite.
	d.Workflow.Description = "updated-desc"
	path2, err := SaveWorkflowDraft(d, SaveDraftOpts{Scope: "project", WorkDir: work, Confirm: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if path2 != path {
		t.Fatalf("path2 = %q", path2)
	}
	data3, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data3), "updated-desc") {
		t.Fatalf("content = %s", data3)
	}
}

func TestSaveWorkflowDraftPreservesPriorOnInvalidForceAttempt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	d := DraftFromJSON([]byte(validDraftJSON("keep")), "model")
	d.Revalidate(map[string]struct{}{"reviewer": {}, "build": {}})
	path, err := SaveWorkflowDraft(d, SaveDraftOpts{Scope: "project", WorkDir: work, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	prior, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Invalid draft with force+confirm must not clobber.
	bad := DraftFromJSON([]byte(`{"name":"keep","phases":[]}`), "edit")
	if _, err := SaveWorkflowDraft(bad, SaveDraftOpts{
		Scope: "project", WorkDir: work, Confirm: true, Force: true,
	}); !errors.Is(err, ErrDraftInvalid) {
		t.Fatalf("err = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(prior) {
		t.Fatalf("prior file corrupted on invalid save")
	}
}

func TestGenerateWorkflowDraftDeterministic(t *testing.T) {
	jsonOut := validDraftJSON("gen-flow")
	complete := func(ctx context.Context, system, user string) (string, error) {
		if !strings.Contains(system, "schemaVersion") {
			t.Fatal("system prompt missing schema")
		}
		if !strings.Contains(user, "ship features safely") {
			t.Fatalf("user = %q", user)
		}
		if !strings.Contains(system, "reviewer") {
			t.Fatal("agents not in system prompt")
		}
		return "```json\n" + jsonOut + "\n```", nil
	}
	d, err := GenerateWorkflowDraft(context.Background(), complete, GenerateDraftOpts{
		Intent: "ship features safely",
		Agents: []string{"reviewer", "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Valid() {
		t.Fatalf("diagnostics = %v", d.Diagnostics)
	}
	if d.Workflow.Name != "gen-flow" {
		t.Fatalf("name = %q", d.Workflow.Name)
	}
}

func TestGenerateWorkflowDraftInvalidOutput(t *testing.T) {
	complete := func(ctx context.Context, system, user string) (string, error) {
		return `{"name":"bad","phases":[]}`, nil
	}
	d, err := GenerateWorkflowDraft(context.Background(), complete, GenerateDraftOpts{
		Intent: "whatever",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Valid() {
		t.Fatal("expected invalid draft")
	}
	if len(d.Diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	// Still no save side effects — Generate never writes.
}

func TestGenerateWorkflowDraftRejectionOfEmpty(t *testing.T) {
	_, err := GenerateWorkflowDraft(context.Background(), func(context.Context, string, string) (string, error) {
		return "", nil
	}, GenerateDraftOpts{Intent: ""})
	if err == nil || !strings.Contains(err.Error(), "empty intent") {
		t.Fatalf("err = %v", err)
	}
	_, err = GenerateWorkflowDraft(context.Background(), nil, GenerateDraftOpts{Intent: "x"})
	if err == nil || !strings.Contains(err.Error(), "nil completer") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerateThenCorrectThenSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	// Model returns invalid (unknown agent + empty phases recovered via correction).
	complete := func(context.Context, string, string) (string, error) {
		return `{"schemaVersion":1,"name":"loop","phases":[{"name":"a","agent":"nope","exit":{"type":"agent"}}]}`, nil
	}
	d, err := GenerateWorkflowDraft(context.Background(), complete, GenerateDraftOpts{
		Intent:      "loop",
		KnownAgents: map[string]struct{}{"build": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Valid() {
		t.Fatal("expected invalid from unknown agent")
	}
	// User rejects save (no Confirm).
	if _, err := SaveWorkflowDraft(d, SaveDraftOpts{Scope: "project", WorkDir: work}); !errors.Is(err, ErrSaveNotConfirmed) {
		t.Fatalf("err = %v", err)
	}
	// Correction.
	fixed := `{
  "schemaVersion": 1,
  "name": "loop",
  "phases": [{"name": "a", "agent": "build", "exit": {"type": "agent"}}]
}`
	d.ApplyJSON([]byte(fixed), map[string]struct{}{"build": {}})
	if !d.Valid() {
		t.Fatalf("after fix: %v", d.Diagnostics)
	}
	path, err := SaveWorkflowDraft(d, SaveDraftOpts{Scope: "project", WorkDir: work, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestWriteWorkflowFilePreserveUsesErrWorkflowExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.json")
	w, err := ScaffoldWorkflow("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkflowFilePreserve(path, w, false); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkflowFilePreserve(path, w, false); !errors.Is(err, ErrWorkflowExists) {
		t.Fatalf("err = %v", err)
	}
}
