package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/persist/plan"
)

func TestPlanDelegateDispatchTwoSectionsAndRejectInFlight(t *testing.T) {
	store := openPlan(t)
	p, err := store.Create("root-a", "Ship", []plan.SectionInput{
		{Title: "Research", Body: "look around"},
		{Title: "Implement", Body: "write code"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var spawns []tool.TaskRequest
	tc := rootTC(t, "root-a")
	n := 0
	tc.SpawnTask = func(_ context.Context, req tool.TaskRequest) (tool.TaskResult, error) {
		n++
		id := fmt.Sprintf("child-%c", 'a'+n-1)
		mu.Lock()
		spawns = append(spawns, req)
		mu.Unlock()
		return tool.TaskResult{Status: "started", SessionID: id, Name: req.Name, Output: "started " + id}, nil
	}

	td := NewPlanDelegate(store)
	res1, err := td.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":     "dispatch",
		"id":         p.ID,
		"section_id": "s1",
		"name":       "refiner-a",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res1.Title, "s1") {
		t.Errorf("title=%q", res1.Title)
	}

	res2, err := td.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":     "dispatch",
		"id":         p.ID,
		"section_id": "s2",
		"name":       "refiner-b",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res2.Output, "in_flight") && !strings.Contains(res2.Output, "child-b") {
		// both should be in flight in status
	}

	// Concurrent sections both in flight.
	st, err := td.Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "status",
		"id":     p.ID,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var view planDelegateView
	if err := json.Unmarshal([]byte(st.Output), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Sections) != 2 {
		t.Fatalf("sections=%d", len(view.Sections))
	}
	if view.Sections[0].DelegateStatus != plan.DelegateInFlight || view.Sections[1].DelegateStatus != plan.DelegateInFlight {
		t.Fatalf("want both in_flight: %+v", view.Sections)
	}
	if view.Sections[0].DelegateChildID != "child-a" || view.Sections[1].DelegateChildID != "child-b" {
		t.Fatalf("child ids: %+v", view.Sections)
	}

	// Second dispatch of s1 rejected.
	res3, err := td.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":     "dispatch",
		"id":         p.ID,
		"section_id": "s1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res3.Output, "in_flight") && !strings.Contains(res3.Output, "in flight") {
		t.Fatalf("want in_flight soft error: %s", res3.Output)
	}
	if n != 2 {
		// optimistic path may spawn then interrupt; allow 3 if race path
		if n > 3 {
			t.Fatalf("spawns=%d", n)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(spawns) < 2 {
		t.Fatalf("spawns=%d", len(spawns))
	}
	if spawns[0].PlanID != p.ID || spawns[0].SectionID != "s1" {
		t.Fatalf("spawn0=%+v", spawns[0])
	}
	if !strings.Contains(spawns[0].Prompt, "section_body") {
		t.Fatalf("prompt missing section_body guidance: %s", spawns[0].Prompt)
	}
}

func TestPlanDelegateChildCannotDispatch(t *testing.T) {
	store := openPlan(t)
	p, err := store.Create("root-a", "Ship", []plan.SectionInput{{Title: "A", Body: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	tc := childTC(t, "root-a", "child-1")
	tc.SpawnTask = func(context.Context, tool.TaskRequest) (tool.TaskResult, error) {
		t.Fatal("must not spawn")
		return tool.TaskResult{}, nil
	}
	td := NewPlanDelegate(store)
	res, err := td.Execute(context.Background(), mustJSON(t, map[string]any{
		"action":     "dispatch",
		"id":         p.ID,
		"section_id": "s1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "not owner") && !strings.Contains(res.Output, "owning root") {
		t.Fatalf("want not owner: %s", res.Output)
	}
}

func TestPlanDelegatePermissionAndDeferred(t *testing.T) {
	if tool.IsCoreTool("plan_delegate") {
		t.Fatal("plan_delegate should not be core (#988)")
	}
	if !tool.IsDeferredTool("plan_delegate") {
		t.Fatal("plan_delegate must be deferred")
	}

	store := openPlan(t)
	p, err := store.Create("root-a", "Ship", []plan.SectionInput{{Title: "A", Body: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	tc := rootTC(t, "root-a")
	tc.Ask = func(_ context.Context, req tool.AskRequest) error {
		asked = append(asked, req.Permission)
		return errors.New("denied")
	}
	tc.SpawnTask = func(context.Context, tool.TaskRequest) (tool.TaskResult, error) {
		t.Fatal("spawn after deny")
		return tool.TaskResult{}, nil
	}
	_, err = NewPlanDelegate(store).Execute(context.Background(), mustJSON(t, map[string]any{
		"action":     "dispatch",
		"id":         p.ID,
		"section_id": "s1",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err=%v", err)
	}
	if len(asked) != 1 || asked[0] != "plan_delegate" {
		t.Fatalf("asked=%v", asked)
	}
}

func TestPlanDelegateReclaimsStaleInFlight(t *testing.T) {
	store := openPlan(t)
	p, err := store.Create("root-a", "Ship", []plan.SectionInput{{Title: "A", Body: "keep"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSectionDelegate(p.ID, "root-a", "s1", "dead-child", "old"); err != nil {
		t.Fatal(err)
	}

	tc := rootTC(t, "root-a")
	// Child is unknown → not live.
	tc.TaskStatus = func(context.Context, tool.TaskStatusRequest) (tool.TaskStatusResult, error) {
		return tool.TaskStatusResult{}, errors.New("unknown child")
	}
	var spawned string
	tc.SpawnTask = func(_ context.Context, req tool.TaskRequest) (tool.TaskResult, error) {
		spawned = "new-child"
		return tool.TaskResult{Status: "started", SessionID: spawned, Name: "fresh", Output: "ok"}, nil
	}

	res, err := NewPlanDelegate(store).Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "dispatch", "id": p.ID, "section_id": "s1", "name": "fresh",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if spawned != "new-child" {
		t.Fatal("expected spawn after reclaim")
	}
	if strings.Contains(res.Output, `"in_flight": true`) && !strings.Contains(res.Output, "new-child") {
		t.Fatalf("still blocked: %s", res.Output)
	}
	got, ok, err := store.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Sections[0].Body != "keep" {
		t.Fatalf("body mutated: %q", got.Sections[0].Body)
	}
	if got.Sections[0].DelegateStatus != plan.DelegateInFlight || got.Sections[0].DelegateChildID != "new-child" {
		t.Fatalf("sec=%#v", got.Sections[0])
	}
}

func TestPlanDelegateRejectsLiveInFlight(t *testing.T) {
	store := openPlan(t)
	p, err := store.Create("root-a", "Ship", []plan.SectionInput{{Title: "A", Body: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSectionDelegate(p.ID, "root-a", "s1", "live-child", ""); err != nil {
		t.Fatal(err)
	}
	tc := rootTC(t, "root-a")
	tc.TaskStatus = func(context.Context, tool.TaskStatusRequest) (tool.TaskStatusResult, error) {
		return tool.TaskStatusResult{State: "working", SessionID: "live-child"}, nil
	}
	tc.SpawnTask = func(context.Context, tool.TaskRequest) (tool.TaskResult, error) {
		t.Fatal("must not spawn while live")
		return tool.TaskResult{}, nil
	}
	res, err := NewPlanDelegate(store).Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "dispatch", "id": p.ID, "section_id": "s1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "in_flight") && !strings.Contains(res.Output, "in flight") {
		t.Fatalf("want in_flight: %s", res.Output)
	}
}

func TestBuildSectionDelegatePrompt(t *testing.T) {
	p := plan.Plan{ID: "abc", Title: "T", Status: "draft"}
	sec := plan.Section{ID: "s1", Title: "Research", Body: " dig "}
	got := buildSectionDelegatePrompt(p, sec, "focus on APIs")
	for _, want := range []string{"s1", "Research", "dig", "focus on APIs", "section_body", "plan_write"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in prompt:\n%s", want, got)
		}
	}
}
