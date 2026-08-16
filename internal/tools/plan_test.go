package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonathanung/strike-cli/internal/tool"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/plan"
)

func openPlan(t *testing.T) *plan.Store {
	t.Helper()
	s, err := plan.Open(t.TempDir(), "test-project")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func rootTC(t *testing.T, rootID string) *tool.Context {
	t.Helper()
	tc := allowAll(t.TempDir())
	tc.SessionID = rootID
	tc.RootSessionID = rootID
	return tc
}

func childTC(t *testing.T, rootID, childID string) *tool.Context {
	t.Helper()
	tc := allowAll(t.TempDir())
	tc.SessionID = childID
	tc.RootSessionID = rootID
	return tc
}

func TestPlanWriteCreateAndRead(t *testing.T) {
	store := openPlan(t)
	tc := rootTC(t, "root-a")
	tw := NewPlanWrite(store)
	tr := NewPlanRead(store)

	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
		"title":  "Ship plans",
		"sections": []map[string]any{
			{"title": "Research", "body": "read store"},
			{"title": "Implement", "body": "tools"},
		},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "created") {
		t.Errorf("title = %q", res.Title)
	}
	var created planView
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.OwnerRoot != "root-a" || created.Version != 1 {
		t.Fatalf("created = %+v", created)
	}
	if len(created.Sections) != 2 || created.Sections[0].ID != "s1" {
		t.Fatalf("sections = %+v", created.Sections)
	}
	// Metadata must carry identity for handoff.
	var meta planView
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ID != created.ID || meta.Version != 1 {
		t.Fatalf("metadata = %+v", meta)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{"id": created.ID}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Ship plans") || !strings.Contains(res.Output, "read store") {
		t.Errorf("read output = %s", res.Output)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "1 plans" {
		t.Errorf("list title = %q", res.Title)
	}
	if strings.Contains(res.Output, "read store") {
		t.Error("list must not include section bodies")
	}
}

func TestPlanWriteUpdateSectionAndCAS(t *testing.T) {
	store := openPlan(t)
	tc := rootTC(t, "root-a")
	tw := NewPlanWrite(store)

	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
		"title":  "CAS plan",
		"sections": []map[string]any{
			{"title": "One", "body": "v1"},
		},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var created planView
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil {
		t.Fatal(err)
	}

	body := "v2 body"
	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "update_section",
		"id":               created.ID,
		"section_id":       "s1",
		"body":             body,
		"expected_version": created.Version,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var updated planView
	if err := json.Unmarshal([]byte(res.Output), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Sections[0].Body != body {
		t.Fatalf("updated = %+v", updated)
	}

	// Stale CAS: preserve newer edit, return conflict.
	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "update_section",
		"id":               created.ID,
		"section_id":       "s1",
		"body":             "stale",
		"expected_version": created.Version, // stale
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var conflict planView
	if err := json.Unmarshal([]byte(res.Output), &conflict); err != nil {
		t.Fatal(err)
	}
	if !conflict.Conflict {
		t.Fatalf("expected conflict flag: %s", res.Output)
	}
	if conflict.Version != 2 || conflict.Sections[0].Body != body {
		t.Fatalf("conflict must preserve newer: %+v", conflict)
	}
	if !strings.Contains(res.Title, "conflict") {
		t.Errorf("title = %q", res.Title)
	}

	// Current version still intact in store.
	got, ok, err := store.Get(created.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Version != 2 || got.Sections[0].Body != body {
		t.Fatalf("store after conflict = %+v", got)
	}
}

func TestPlanWriteChildCannotMutate(t *testing.T) {
	store := openPlan(t)
	root := rootTC(t, "root-a")
	child := childTC(t, "root-a", "child-1")
	other := rootTC(t, "root-b")
	tw := NewPlanWrite(store)

	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
		"title":  "Owned",
		"sections": []map[string]any{
			{"title": "S", "body": "b"},
		},
	}), root)
	if err != nil {
		t.Fatal(err)
	}
	var created planView
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil {
		t.Fatal(err)
	}

	// Child of owning root cannot create.
	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
		"title":  "Hijack create",
	}), child)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "only the owning root") && !strings.Contains(res.Output, "not owner") {
		t.Errorf("child create = %s", res.Output)
	}

	// Child cannot update.
	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "update_title",
		"id":               created.ID,
		"title":            "nope",
		"expected_version": created.Version,
	}), child)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(res.Title+res.Output), "owner") {
		t.Errorf("child update = title=%q out=%s", res.Title, res.Output)
	}

	// Unrelated root cannot mutate.
	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "update_title",
		"id":               created.ID,
		"title":            "nope",
		"expected_version": created.Version,
	}), other)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(res.Title+res.Output), "owner") {
		t.Errorf("other root update = title=%q out=%s", res.Title, res.Output)
	}

	// Child may still read.
	tr := NewPlanRead(store)
	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{"id": created.ID}), child)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Owned") {
		t.Errorf("child read = %s", res.Output)
	}

	// Store unchanged.
	got, ok, _ := store.Get(created.ID)
	if !ok || got.Title != "Owned" || got.Version != 1 {
		t.Fatalf("store after denied mutates = %+v", got)
	}
}

func TestPlanWriteAddSectionStatusReopen(t *testing.T) {
	store := openPlan(t)
	tc := rootTC(t, "root-a")
	tw := NewPlanWrite(store)

	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
		"title":  "Lifecycle",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var p planView
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatal(err)
	}

	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "add_section",
		"id":               p.ID,
		"title":            "Step 1",
		"body":             "do it",
		"expected_version": p.Version,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Sections) != 1 || p.Sections[0].ID != "s1" {
		t.Fatalf("sections = %+v", p.Sections)
	}

	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "set_status",
		"id":               p.ID,
		"status":           "approved",
		"expected_version": p.Version,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != "approved" {
		t.Fatalf("status = %q", p.Status)
	}

	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "set_status",
		"id":               p.ID,
		"status":           "closed",
		"expected_version": p.Version,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatal(err)
	}

	// Content mutation on closed → soft reject.
	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "add_section",
		"id":               p.ID,
		"title":            "nope",
		"expected_version": p.Version,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "closed") {
		t.Errorf("closed mutate = %s", res.Output)
	}

	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "reopen",
		"id":               p.ID,
		"expected_version": p.Version,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != "draft" {
		t.Fatalf("reopen status = %q", p.Status)
	}
}

func TestPlanWriteValidationAndPermission(t *testing.T) {
	store := openPlan(t)
	tw := NewPlanWrite(store)
	tc := rootTC(t, "root-a")

	_, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "action") {
		t.Fatalf("missing action err = %v", err)
	}
	_, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("missing title err = %v", err)
	}
	_, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "update_section",
		"id":     "x",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "section_id") {
		t.Fatalf("missing section_id err = %v", err)
	}
	_, err = tw.Execute(context.Background(), json.RawMessage(`{`), tc)
	if err == nil {
		t.Fatal("expected invalid JSON")
	}

	deny := errors.New("denied")
	denied := &tool.Context{
		WorkDir:       t.TempDir(),
		SessionID:     "root-a",
		RootSessionID: "root-a",
		Ask:           func(context.Context, tool.AskRequest) error { return deny },
	}
	_, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create", "title": "x",
	}), denied)
	if !errors.Is(err, deny) {
		t.Fatalf("write deny = %v", err)
	}
	_, err = NewPlanRead(store).Execute(context.Background(), mustJSON(t, map[string]any{}), denied)
	if !errors.Is(err, deny) {
		t.Fatalf("read deny = %v", err)
	}
}

func TestPlanReadMissAndEmptyList(t *testing.T) {
	store := openPlan(t)
	tc := rootTC(t, "root-a")
	tr := NewPlanRead(store)

	res, err := tr.Execute(context.Background(), mustJSON(t, map[string]any{"id": "missing"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "no plan") {
		t.Errorf("miss = %s", res.Output)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "0 plans" || strings.TrimSpace(res.Output) != "[]" {
		t.Errorf("empty list title=%q out=%q", res.Title, res.Output)
	}
}

func TestPlanWriteWorkspaceMutationStillSeparate(t *testing.T) {
	// plan_write permission is independent of write/edit — plan agent can
	// revise plans while workspace mutation stays denied at the permission layer.
	store := openPlan(t)
	var asked []string
	tc := &tool.Context{
		WorkDir:       t.TempDir(),
		SessionID:     "root-a",
		RootSessionID: "root-a",
		Ask: func(_ context.Context, req tool.AskRequest) error {
			asked = append(asked, req.Permission)
			if req.Permission == "write" || req.Permission == "edit" {
				return errors.New("workspace denied")
			}
			return nil
		},
	}
	res, err := NewPlanWrite(store).Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
		"title":  "While write denied",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "created") {
		t.Errorf("title = %q", res.Title)
	}
	if len(asked) != 1 || asked[0] != "plan_write" {
		t.Fatalf("asked = %v", asked)
	}
}

func TestPlanToolsDeferredUntilDiscover(t *testing.T) {
	// Plan tools are deferred under the minimal core surface (#988); workflow
	// activation (#991) or toolsearch/direct call promotes them.
	if tool.IsCoreTool("plan_write") || tool.IsCoreTool("plan_read") || tool.IsCoreTool("plan_delegate") {
		t.Fatal("plan_write/plan_read/plan_delegate should not be core")
	}
	if !tool.IsDeferredTool("plan_write") || !tool.IsDeferredTool("plan_read") || !tool.IsDeferredTool("plan_delegate") {
		t.Fatal("plan tools must be deferred")
	}
	store := openPlan(t)
	reg := tool.NewRegistry(tool.NewRead(), NewPlanWrite(store), NewPlanRead(store), NewPlanDelegate(store))
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)
	names := map[string]bool{}
	for _, s := range reg.SchemasForProvider() {
		names[s.Name] = true
	}
	if names["plan_write"] || names["plan_read"] || names["plan_delegate"] {
		t.Fatalf("plan tools should be omitted under defer: %v", names)
	}
	if !names["read"] || !names["toolsearch"] {
		t.Fatalf("core missing under defer: %v", names)
	}
	reg.Discover("plan_write", "plan_read")
	names = map[string]bool{}
	for _, s := range reg.SchemasForProvider() {
		names[s.Name] = true
	}
	if !names["plan_write"] || !names["plan_read"] {
		t.Fatalf("plan tools missing after Discover: %v", names)
	}
	if names["plan_delegate"] {
		t.Fatalf("undiscovered plan_delegate leaked: %v", names)
	}
}

func TestRootActorRequiresRootSession(t *testing.T) {
	_, err := rootActor(nil)
	if err == nil {
		t.Fatal("nil context")
	}
	_, err = rootActor(&tool.Context{SessionID: "c", RootSessionID: "r"})
	if !errors.Is(err, plan.ErrNotOwner) {
		t.Fatalf("child err = %v", err)
	}
	id, err := rootActor(&tool.Context{SessionID: "r", RootSessionID: "r"})
	if err != nil || id != "r" {
		t.Fatalf("root = %q err=%v", id, err)
	}
	id, err = rootActor(&tool.Context{SessionID: "solo"})
	if err != nil || id != "solo" {
		t.Fatalf("fallback = %q err=%v", id, err)
	}
}
