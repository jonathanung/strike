package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPatchCollabNilHandler(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewPatchCollab().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "list",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestPatchCollabValidation(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.PatchCollab = func(context.Context, PatchCollabRequest) (PatchCollabResult, error) {
		t.Fatal("handler should not run")
		return PatchCollabResult{}, nil
	}
	cases := []map[string]any{
		{"action": "submit"},
		{"action": "preview"},
		{"action": "reject", "id": "p1"},
		{"action": "reject", "id": "p1", "reason": ""},
		{"action": "apply"},
		{"action": "nope"},
	}
	for _, args := range cases {
		if _, err := NewPatchCollab().Execute(context.Background(), mustJSON(t, args), tc); err == nil {
			t.Errorf("expected error for %#v", args)
		}
	}
}

func TestPatchCollabSubmitAndList(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	var got PatchCollabRequest
	tc.PatchCollab = func(_ context.Context, req PatchCollabRequest) (PatchCollabResult, error) {
		got = req
		if req.Action == "submit" {
			return PatchCollabResult{
				LeadID: "L",
				Action: "submit",
				Patch: &PatchCollabItem{
					ID:      "p1",
					Status:  "pending",
					Files:   []string{"n.txt"},
					Version: 1,
					Patch:   req.Patch,
				},
				Files:   []string{"n.txt"},
				Preview: &PatchPreview{Valid: true, Files: []string{"n.txt"}},
			}, nil
		}
		return PatchCollabResult{LeadID: "L", Action: "list", Patches: []PatchCollabItem{}}, nil
	}
	patch := sampleAddPatch("n.txt", "hi\n")
	res, err := NewPatchCollab().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "submit",
		"patch":  patch,
		"title":  "child work",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "submit" || got.Title != "child work" || !strings.Contains(got.Patch, "n.txt") {
		t.Fatalf("req = %#v", got)
	}
	if !strings.Contains(res.Output, `"p1"`) {
		t.Fatalf("output = %s", res.Output)
	}
	var parsed PatchCollabResult
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Patch == nil || parsed.Patch.ID != "p1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestPatchCollabRejectPath(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.PatchCollab = func(_ context.Context, req PatchCollabRequest) (PatchCollabResult, error) {
		return PatchCollabResult{
			Action: "reject",
			Patch: &PatchCollabItem{
				ID:           "p1",
				Status:       "rejected",
				RejectReason: req.Reason,
				Version:      2,
			},
			Detail: req.Reason,
		}, nil
	}
	res, err := NewPatchCollab().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "reject",
		"id":     "p1",
		"reason": "tests fail",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "rejected") || !strings.Contains(res.Output, "tests fail") {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestPatchCollabApplyUsesEditPermission(t *testing.T) {
	var perms []string
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(_ context.Context, req AskRequest) error {
			perms = append(perms, req.Permission)
			return nil
		},
		PatchCollab: func(context.Context, PatchCollabRequest) (PatchCollabResult, error) {
			return PatchCollabResult{Action: "apply", Files: []string{"a.go"}, Summary: "ok"}, nil
		},
	}
	if _, err := NewPatchCollab().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "apply",
		"id":     "p1",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if len(perms) != 1 || perms[0] != "edit" {
		t.Fatalf("perms = %v, want [edit]", perms)
	}
}

func TestPatchCollabPermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(context.Context, AskRequest) error {
			return ErrPermissionDenied("denied")
		},
		PatchCollab: func(context.Context, PatchCollabRequest) (PatchCollabResult, error) {
			t.Fatal("should not run")
			return PatchCollabResult{}, nil
		},
	}
	if _, err := NewPatchCollab().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "list",
	}), tc); err == nil {
		t.Fatal("expected permission error")
	}
}

func TestPatchCollabDescriptionMentionsApplyPatch(t *testing.T) {
	d := NewPatchCollab().Description()
	for _, s := range []string{"apply_patch", "preview", "reject", "submit"} {
		if !strings.Contains(d, s) {
			t.Errorf("description missing %q", s)
		}
	}
}
