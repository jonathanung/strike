package local

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/product/config"
)

func TestWorkflowDraftsReviewAndSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	svc := NewWorkflowDrafts(work)

	valid := `{
  "schemaVersion": 1,
  "name": "host-draft",
  "description": "via host",
  "phases": [
    {
      "name": "one",
      "agent": "build",
      "permissions": [{"permission": "bash", "pattern": "*", "action": "allow"}],
      "exit": {"type": "check", "command": "make test"}
    }
  ]
}`
	rev := svc.Review(valid)
	if !rev.Valid {
		t.Fatalf("valid=false: %s", rev.ValidationError)
	}
	if !rev.HasChecks {
		t.Fatal("expected HasChecks")
	}
	if rev.CanonicalJSON == "" || !strings.Contains(rev.CanonicalJSON, "host-draft") {
		t.Fatalf("canonical = %q", rev.CanonicalJSON)
	}
	if len(rev.Phases) != 1 || !rev.Phases[0].CheckHighlighted {
		t.Fatalf("phases = %+v", rev.Phases)
	}

	// Rejection: no confirm.
	if _, err := svc.Save(valid, "project", false, false); !errors.Is(err, config.ErrSaveNotConfirmed) {
		t.Fatalf("err = %v", err)
	}
	// Invalid stays unsaved.
	bad := `{"name":"x","phases":[]}`
	brev := svc.Review(bad)
	if brev.Valid {
		t.Fatal("expected invalid review")
	}
	if _, err := svc.Save(bad, "project", true, false); !errors.Is(err, config.ErrDraftInvalid) {
		t.Fatalf("err = %v", err)
	}

	// Accepted save.
	res, err := svc.Save(valid, "project", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Activated {
		t.Fatal("save must not activate")
	}
	want := filepath.Join(work, ".strike", "workflows", "host-draft.json")
	if res.Path != want {
		t.Fatalf("path = %q want %q", res.Path, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
	// Overwrite requires force.
	if _, err := svc.Save(valid, "project", true, false); !errors.Is(err, config.ErrWorkflowExists) {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.Save(valid, "project", true, true); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowDraftsReviewWidening(t *testing.T) {
	// Defaults use ask for bash; allow is a widening.
	svc := NewWorkflowDrafts(t.TempDir())
	doc := `{
  "schemaVersion": 1,
  "name": "widen",
  "phases": [{
    "name": "go",
    "permissions": [{"permission": "bash", "pattern": "*", "action": "allow"}],
    "exit": {"type": "agent"}
  }]
}`
	rev := svc.Review(doc)
	if !rev.Valid {
		t.Fatalf("%s", rev.ValidationError)
	}
	if !rev.HasWidening {
		t.Fatal("expected widening vs defaults")
	}
	if len(rev.Phases[0].Widening) == 0 {
		t.Fatal("expected widening rules on phase")
	}
}
