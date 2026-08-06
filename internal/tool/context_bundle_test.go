package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeContextBundle(t *testing.T) {
	t.Parallel()
	got, err := NormalizeContextBundle(ContextBundle{
		Goal:          " ship it ",
		Acceptance:    []string{" tests pass ", ""},
		AllowedPaths:  []string{"internal/foo", "internal/foo"},
		RequiredPaths: []string{"docs/spec.md"},
		Artifacts:     []BundleArtifactRef{{ID: " ab12 ", Version: 2, Type: "findings"}},
		Constraints:   []string{"no network"},
		FilePins:      []ContextFilePin{{Path: "README.md", Hash: "abc", Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal != "ship it" {
		t.Fatalf("goal = %q", got.Goal)
	}
	if len(got.Acceptance) != 1 || got.Acceptance[0] != "tests pass" {
		t.Fatalf("acceptance = %#v", got.Acceptance)
	}
	if len(got.AllowedPaths) != 1 || got.AllowedPaths[0] != "internal/foo" {
		t.Fatalf("allowed = %#v", got.AllowedPaths)
	}
	// Synthetic items for provenance.
	ids := map[string]bool{}
	for _, it := range got.Items {
		ids[it.ID] = true
	}
	for _, want := range []string{"goal", "acceptance-1", "allowed-path-1", "required-path-1", "artifact-1", "constraint-1", "file-pin-1"} {
		if !ids[want] {
			t.Fatalf("missing synthetic item %q in %#v", want, got.Items)
		}
	}
}

func TestNormalizeContextBundleRejectsAbsPath(t *testing.T) {
	t.Parallel()
	_, err := NormalizeContextBundle(ContextBundle{AllowedPaths: []string{"/etc/passwd"}})
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = NormalizeContextBundle(ContextBundle{RequiredPaths: []string{"../escape"}})
	if err == nil {
		t.Fatal("expected escape error")
	}
}

func TestNormalizeContextBundleDuplicateItemID(t *testing.T) {
	t.Parallel()
	_, err := NormalizeContextBundle(ContextBundle{
		Items: []ContextBundleItem{{ID: "a"}, {ID: "a"}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v", err)
	}
}

func TestNormalizeContextBundleEmpty(t *testing.T) {
	t.Parallel()
	got, err := NormalizeContextBundle(ContextBundle{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Empty() {
		t.Fatalf("got = %#v", got)
	}
}

func TestContextBundleToolGetAndItem(t *testing.T) {
	t.Parallel()
	bundle, err := NormalizeContextBundle(ContextBundle{
		Goal: "do work",
		Items: []ContextBundleItem{
			{ID: "custom", Kind: "note", Text: "secret-ish"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tc := &Context{
		ContextBundle: &bundle,
		Ask:           func(context.Context, AskRequest) error { return nil },
	}
	tool := NewContextBundle()

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"get"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"attached": true`) && !strings.Contains(res.Output, `"attached":true`) {
		t.Fatalf("output = %s", res.Output)
	}
	if !strings.Contains(res.Output, "do work") {
		t.Fatalf("missing goal: %s", res.Output)
	}

	res, err = tool.Execute(context.Background(), json.RawMessage(`{"action":"item","id":"custom"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "secret-ish") {
		t.Fatalf("item = %s", res.Output)
	}

	_, err = tool.Execute(context.Background(), json.RawMessage(`{"action":"item","id":"nope"}`), tc)
	if err == nil {
		t.Fatal("expected missing item error")
	}
}

func TestContextBundleToolEmpty(t *testing.T) {
	t.Parallel()
	tc := &Context{Ask: func(context.Context, AskRequest) error { return nil }}
	res, err := NewContextBundle().Execute(context.Background(), nil, tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"attached":false`) && !strings.Contains(res.Output, `"attached": false`) {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestTaskPassesContextBundle(t *testing.T) {
	t.Parallel()
	var got TaskRequest
	tc := &Context{
		Ask: func(context.Context, AskRequest) error { return nil },
		SpawnTask: func(_ context.Context, req TaskRequest) (TaskResult, error) {
			got = req
			return TaskResult{Status: "started", SessionID: "c1"}, nil
		},
	}
	args := `{
		"prompt": "go",
		"context_bundle": {
			"goal": "fix bug",
			"allowed_paths": ["internal/x"],
			"artifacts": [{"id": "a1", "type": "findings"}]
		}
	}`
	_, err := NewTask().Execute(context.Background(), json.RawMessage(args), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContextBundle.Goal != "fix bug" {
		t.Fatalf("bundle = %#v", got.ContextBundle)
	}
	if len(got.ContextBundle.AllowedPaths) != 1 || got.ContextBundle.AllowedPaths[0] != "internal/x" {
		t.Fatalf("paths = %#v", got.ContextBundle.AllowedPaths)
	}
	if len(got.ContextBundle.Artifacts) != 1 || got.ContextBundle.Artifacts[0].ID != "a1" {
		t.Fatalf("artifacts = %#v", got.ContextBundle.Artifacts)
	}
}

func TestTaskRejectsBadBundle(t *testing.T) {
	t.Parallel()
	tc := &Context{
		Ask: func(context.Context, AskRequest) error { return nil },
		SpawnTask: func(context.Context, TaskRequest) (TaskResult, error) {
			t.Fatal("must not spawn")
			return TaskResult{}, nil
		},
	}
	args := `{"prompt":"go","context_bundle":{"allowed_paths":["/abs"]}}`
	_, err := NewTask().Execute(context.Background(), json.RawMessage(args), tc)
	if err == nil {
		t.Fatal("expected error")
	}
}
