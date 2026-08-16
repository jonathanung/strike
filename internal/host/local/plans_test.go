package local

import (
	"errors"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/persist/plan"
)

func TestPlansNilStore(t *testing.T) {
	if NewPlans(nil) != nil {
		t.Fatal("nil store should yield nil host.Plans")
	}
	svc := New(nil, nil, nil, nil, nil, nil, nil, "")
	if svc.Plans != nil {
		t.Fatal("Plans should be nil until NewPlans")
	}
}

func TestPlansWiredThrough(t *testing.T) {
	store, err := plan.Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := New(nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	svc.Plans = NewPlans(store)
	if svc.Plans == nil {
		t.Fatal("NewPlans returned nil")
	}

	created, err := svc.Plans.Create("root-a", "My plan", []host.PlanSection{
		{Title: "One", Body: "body-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.OwnerRoot != "root-a" || created.Status != "draft" || created.Version != 1 {
		t.Fatalf("created = %#v", created)
	}
	if len(created.Sections) != 1 || created.Sections[0].ID != "s1" {
		t.Fatalf("sections = %#v", created.Sections)
	}

	// Non-owner cannot mutate; index still lists metadata.
	if _, err := svc.Plans.UpdateTitle(created.ID, "root-b", "hijack", created.Version); !errors.Is(err, plan.ErrNotOwner) {
		t.Fatalf("non-owner: %v", err)
	}
	list, err := svc.Plans.List()
	if err != nil || len(list) != 1 || list[0].Title != "My plan" || list[0].SectionCount != 1 {
		t.Fatalf("list = %#v err=%v", list, err)
	}

	// Owner CAS update.
	updated, err := svc.Plans.UpdateTitle(created.ID, "root-a", "Renamed", created.Version)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Renamed" || updated.Version != 2 {
		t.Fatalf("updated = %#v", updated)
	}
	// Stale version fails.
	if _, err := svc.Plans.UpdateTitle(created.ID, "root-a", "stale", created.Version); !errors.Is(err, plan.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}

	body := "new-body"
	sec, err := svc.Plans.UpdateSection(created.ID, "root-a", "s1", nil, &body, updated.Version)
	if err != nil {
		t.Fatal(err)
	}
	if sec.Sections[0].Body != "new-body" {
		t.Fatalf("section = %#v", sec.Sections[0])
	}

	sec, err = svc.Plans.AddSection(created.ID, "root-a", "Two", "b2", sec.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(sec.Sections) != 2 || sec.Sections[1].ID != "s2" {
		t.Fatalf("add = %#v", sec.Sections)
	}

	sec, err = svc.Plans.SetStatus(created.ID, "root-a", "approved", sec.Version)
	if err != nil || sec.Status != "approved" {
		t.Fatalf("approve = %#v err=%v", sec, err)
	}
	sec, err = svc.Plans.SetStatus(created.ID, "root-a", "closed", sec.Version)
	if err != nil || sec.Status != "closed" {
		t.Fatalf("close = %#v err=%v", sec, err)
	}
	sec, err = svc.Plans.Reopen(created.ID, "root-a", sec.Version)
	if err != nil || sec.Status != "draft" {
		t.Fatalf("reopen = %#v err=%v", sec, err)
	}

	got, ok, err := svc.Plans.Get(created.ID)
	if err != nil || !ok || got.Title != "Renamed" {
		t.Fatalf("get = %#v ok=%v err=%v", got, ok, err)
	}
	// Deep copy isolation through host adapter.
	got.Sections[0].Body = "mutated"
	again, _, _ := svc.Plans.Get(created.ID)
	if again.Sections[0].Body == "mutated" {
		t.Fatal("host Get leaked mutable section body")
	}
}
