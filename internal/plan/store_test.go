package plan

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOpenCreateGetList(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	p, err := s.Create("root-1", "Ship plans", []SectionInput{
		{Title: "Research", Body: "read code"},
		{Title: "Implement", Body: "write store"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.OwnerRoot != "root-1" || p.Title != "Ship plans" {
		t.Fatalf("create = %#v", p)
	}
	if p.Status != StatusDraft || p.Version != 1 {
		t.Fatalf("status/version = %s/%d", p.Status, p.Version)
	}
	if len(p.Sections) != 2 || p.Sections[0].ID != "s1" || p.Sections[1].ID != "s2" {
		t.Fatalf("sections = %#v", p.Sections)
	}
	if p.Sections[0].Title != "Research" || p.Sections[0].Body != "read code" {
		t.Fatalf("section0 = %#v", p.Sections[0])
	}

	got, ok, err := s.Get(p.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Title != p.Title || len(got.Sections) != 2 {
		t.Fatalf("got = %#v", got)
	}

	// Deep copy: mutating Get result must not affect store.
	got.Sections[0].Body = "mutated"
	got.Title = "mutated"
	again, ok, err := s.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if again.Title != "Ship plans" || again.Sections[0].Body != "read code" {
		t.Fatalf("store mutated via Get copy: %#v", again)
	}

	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %#v err=%v", list, err)
	}
	if list[0].ID != p.ID || list[0].SectionCount != 2 || list[0].OwnerRoot != "root-1" {
		t.Fatalf("meta = %#v", list[0])
	}
}

func TestOwnershipEnforced(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Create("owner", "T", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpdateTitle(p.ID, "other", "Nope", p.Version); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("other root title: %v", err)
	}
	title := "x"
	if _, err := s.UpdateSection(p.ID, "other", "s1", &title, nil, p.Version); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("other root section: %v", err)
	}
	if _, err := s.AddSection(p.ID, "other", "S", "", p.Version); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("other root add: %v", err)
	}
	if _, err := s.SetStatus(p.ID, "other", StatusApproved, p.Version); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("other root status: %v", err)
	}

	// Owner succeeds; other roots still see index metadata.
	updated, err := s.UpdateTitle(p.ID, "owner", "Owned", p.Version)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Owned" || updated.Version != 2 {
		t.Fatalf("updated = %#v", updated)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].Title != "Owned" {
		t.Fatalf("list meta = %#v err=%v", list, err)
	}
}

func TestCASConflict(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Create("r", "T", []SectionInput{{Title: "A", Body: "1"}})
	if err != nil {
		t.Fatal(err)
	}

	// First writer wins.
	p2, err := s.UpdateTitle(p.ID, "r", "V2", p.Version)
	if err != nil {
		t.Fatal(err)
	}
	// Stale expected version must not overwrite.
	if _, err := s.UpdateTitle(p.ID, "r", "stale", p.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale title: %v", err)
	}
	body := "stale-body"
	if _, err := s.UpdateSection(p.ID, "r", "s1", nil, &body, p.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale section: %v", err)
	}
	got, ok, err := s.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Title != "V2" || got.Version != p2.Version || got.Sections[0].Body != "1" {
		t.Fatalf("corrupted after stale writes: %#v", got)
	}

	// Fresh version succeeds.
	body2 := "ok"
	p3, err := s.UpdateSection(p.ID, "r", "s1", nil, &body2, p2.Version)
	if err != nil {
		t.Fatal(err)
	}
	if p3.Sections[0].Body != "ok" || p3.Version != p2.Version+1 {
		t.Fatalf("section update = %#v", p3)
	}
}

func TestLifecycleReopen(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Create("r", "T", []SectionInput{{Title: "A"}})
	if err != nil {
		t.Fatal(err)
	}

	p, err = s.SetStatus(p.ID, "r", StatusApproved, p.Version)
	if err != nil || p.Status != StatusApproved {
		t.Fatalf("approve: %#v err=%v", p, err)
	}
	p, err = s.SetStatus(p.ID, "r", StatusClosed, p.Version)
	if err != nil || p.Status != StatusClosed {
		t.Fatalf("close: %#v err=%v", p, err)
	}

	// Content mutations blocked while closed.
	if _, err := s.UpdateTitle(p.ID, "r", "nope", p.Version); !errors.Is(err, ErrClosedPlan) {
		t.Fatalf("title on closed: %v", err)
	}
	// Cannot SetStatus out of closed.
	if _, err := s.SetStatus(p.ID, "r", StatusDraft, p.Version); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("setstatus out of closed: %v", err)
	}

	p, err = s.Reopen(p.ID, "r", p.Version)
	if err != nil || p.Status != StatusDraft {
		t.Fatalf("reopen: %#v err=%v", p, err)
	}
	// After reopen, content edits work again.
	p, err = s.UpdateTitle(p.ID, "r", "again", p.Version)
	if err != nil || p.Title != "again" {
		t.Fatalf("post-reopen title: %#v err=%v", p, err)
	}

	// Reopen non-closed fails.
	if _, err := s.Reopen(p.ID, "r", p.Version); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("reopen draft: %v", err)
	}
}

func TestAddSectionStableIDs(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Create("r", "T", []SectionInput{{Title: "A"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.AddSection(p.ID, "r", "B", "body-b", p.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sections) != 2 || p.Sections[1].ID != "s2" || p.Sections[1].Body != "body-b" {
		t.Fatalf("add = %#v", p.Sections)
	}
	// Order preserved; s1 unchanged.
	if p.Sections[0].ID != "s1" || p.Sections[0].Title != "A" {
		t.Fatalf("order broken: %#v", p.Sections)
	}
}

func TestSectionNotFound(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Create("r", "T", []SectionInput{{Title: "A"}})
	if err != nil {
		t.Fatal(err)
	}
	title := "x"
	if _, err := s.UpdateSection(p.ID, "r", "s99", &title, nil, p.Version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing section: %v", err)
	}
	if _, err := s.UpdateTitle("missing", "r", "t", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing plan: %v", err)
	}
}

func TestProjectIsolation(t *testing.T) {
	root := t.TempDir()
	a, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(root, "/proj/b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	if _, err := a.Create("r", "secret", nil); err != nil {
		t.Fatal(err)
	}
	list, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("project b saw a's plans: %#v", list)
	}
	if a.Path() == b.Path() {
		t.Fatal("projects share the same plans file path")
	}
}

func TestPersistAcrossOpen(t *testing.T) {
	root := t.TempDir()
	s1, err := Open(root, "key")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s1.Create("r", "keep", []SectionInput{{Title: "S", Body: "me"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s1.SetStatus(p.ID, "r", StatusApproved, p.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root, "key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, ok, err := s2.Get(p.ID)
	if err != nil || !ok {
		t.Fatalf("reload Get: ok=%v err=%v", ok, err)
	}
	if got.Title != "keep" || got.Status != StatusApproved || got.Version != 2 {
		t.Errorf("reloaded = %#v", got)
	}
	if len(got.Sections) != 1 || got.Sections[0].ID != "s1" || got.Sections[0].Body != "me" {
		t.Errorf("sections = %#v", got.Sections)
	}
	// next section seq survives.
	got, err = s2.AddSection(got.ID, "r", "Next", "", got.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sections[1].ID != "s2" {
		t.Fatalf("section id after reload = %q", got.Sections[1].ID)
	}
}

func TestClosedStore(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("r", "t", nil); !errors.Is(err, errClosed) {
		t.Fatalf("create on closed: %v", err)
	}
	if _, _, err := s.Get("x"); !errors.Is(err, errClosed) {
		t.Fatalf("get on closed: %v", err)
	}
	if _, err := s.List(); !errors.Is(err, errClosed) {
		t.Fatalf("list on closed: %v", err)
	}
}

func TestValidation(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Create("", "t", nil); !errors.Is(err, errEmptyOwner) {
		t.Fatalf("empty owner: %v", err)
	}
	if _, err := s.Create("r", "", nil); !errors.Is(err, errEmptyTitle) {
		t.Fatalf("empty title: %v", err)
	}
	if _, err := s.Create("r", "t", []SectionInput{{Title: ""}}); err == nil {
		t.Fatal("empty section title accepted")
	}
	if _, err := s.Create("r", "t\nx", nil); err == nil {
		t.Fatal("newline title accepted")
	}
	if _, err := Open("", "p"); err == nil {
		t.Fatal("empty global root accepted")
	}
	if _, err := Open(t.TempDir(), ""); err == nil {
		t.Fatal("empty project key accepted")
	}
	if _, err := s.SetStatus("nope", "r", "bogus", 1); !errors.Is(err, ErrInvalidStatus) && !errors.Is(err, ErrNotFound) {
		// invalid status checked before lookup when status string is bad
		t.Fatalf("bogus status: %v", err)
	}
	p, err := s.Create("r", "ok", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(p.ID, "r", "bogus", p.Version); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("bogus status on existing: %v", err)
	}
}

func TestConcurrentCASWriters(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Create("r", "T", []SectionInput{{Title: "A", Body: ""}})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	var accepted atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				cur, ok, err := s.Get(p.ID)
				if err != nil || !ok {
					t.Errorf("get: ok=%v err=%v", ok, err)
					return
				}
				body := cur.Sections[0].Body + "x"
				_, err = s.UpdateSection(p.ID, "r", "s1", nil, &body, cur.Version)
				if err == nil {
					accepted.Add(1)
					return
				}
				if !errors.Is(err, ErrConflict) {
					t.Errorf("unexpected: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if accepted.Load() != workers {
		t.Fatalf("accepted=%d want %d", accepted.Load(), workers)
	}
	got, ok, err := s.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	// Each accepted update appends one "x"; version = 1 + accepted.
	wantVer := int(1 + accepted.Load())
	if got.Version != wantVer {
		t.Fatalf("version=%d want %d", got.Version, wantVer)
	}
	if got.Sections[0].Body != string(makeXs(workers)) {
		t.Fatalf("body=%q want %d x's", got.Sections[0].Body, workers)
	}

	// Reload from disk — no corruption / lost updates.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	reloaded, ok, err := s2.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if reloaded.Version != wantVer || reloaded.Sections[0].Body != got.Sections[0].Body {
		t.Fatalf("reloaded=%#v", reloaded)
	}
}

func makeXs(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return b
}

func TestSectionDelegateConcurrentAndCAS(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Create("root", "Plan", []SectionInput{
		{Title: "A", Body: "body-a"},
		{Title: "B", Body: "body-b"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Two different sections can be in flight concurrently.
	p, err = s.BeginSectionDelegate(p.ID, "root", "s1", "child-a", "refiner-a")
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.BeginSectionDelegate(p.ID, "root", "s2", "child-b", "refiner-b")
	if err != nil {
		t.Fatal(err)
	}
	if p.Sections[0].DelegateStatus != DelegateInFlight || p.Sections[1].DelegateStatus != DelegateInFlight {
		t.Fatalf("want both in_flight: %#v", p.Sections)
	}
	if p.Sections[0].DelegateChildID != "child-a" || p.Sections[1].DelegateChildID != "child-b" {
		t.Fatalf("child ids: %#v", p.Sections)
	}

	// Second dispatch of an in-flight section is rejected.
	if _, err := s.BeginSectionDelegate(p.ID, "root", "s1", "child-x", ""); !errors.Is(err, ErrInFlight) {
		t.Fatalf("second dispatch: %v", err)
	}

	// Apply s1 successfully.
	body := "refined-a"
	p, err = s.FinishSectionDelegate(p.ID, "root", "s1", "child-a", DelegateOutcome{
		Status: DelegateApplied,
		Body:   &body,
		Detail: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Sections[0].Body != "refined-a" || p.Sections[0].DelegateStatus != DelegateApplied {
		t.Fatalf("applied s1: %#v", p.Sections[0])
	}
	// s2 still in flight and unchanged.
	if p.Sections[1].Body != "body-b" || p.Sections[1].DelegateStatus != DelegateInFlight {
		t.Fatalf("s2: %#v", p.Sections[1])
	}

	// Intervening user edit on s2, then child tries to apply → conflict, content preserved.
	edited := "user-edit-b"
	p, err = s.UpdateSection(p.ID, "root", "s2", nil, &edited, p.Version)
	if err != nil {
		t.Fatal(err)
	}
	childBody := "child-b-result"
	p, err = s.FinishSectionDelegate(p.ID, "root", "s2", "child-b", DelegateOutcome{
		Status: DelegateApplied,
		Body:   &childBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Sections[1].Body != "user-edit-b" {
		t.Fatalf("intervening edit overwritten: %q", p.Sections[1].Body)
	}
	if p.Sections[1].DelegateStatus != DelegateConflict {
		t.Fatalf("status=%q want conflict", p.Sections[1].DelegateStatus)
	}
	if p.Sections[1].DelegateDetail == "" {
		t.Fatal("want actionable conflict detail")
	}
}

func TestSectionDelegateFailedCanceledMalformed(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.Create("root", "Plan", []SectionInput{
		{Title: "A", Body: "keep-me"},
		{Title: "B", Body: "keep-b"},
		{Title: "C", Body: "keep-c"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		sec    string
		child  string
		status string
	}{
		{"s1", "c1", DelegateFailed},
		{"s2", "c2", DelegateCanceled},
		{"s3", "c3", DelegateMalformed},
	}
	for _, tc := range cases {
		if _, err := s.BeginSectionDelegate(p.ID, "root", tc.sec, tc.child, ""); err != nil {
			t.Fatalf("begin %s: %v", tc.sec, err)
		}
	}
	// Refresh version after begins.
	cur, ok, err := s.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	p = cur

	for _, tc := range cases {
		got, err := s.FinishSectionDelegate(p.ID, "root", tc.sec, tc.child, DelegateOutcome{
			Status: tc.status,
			Detail: "reason-" + tc.status,
		})
		if err != nil {
			t.Fatalf("finish %s: %v", tc.sec, err)
		}
		p = got
	}
	for i, tc := range cases {
		sec := p.Sections[i]
		if sec.DelegateStatus != tc.status {
			t.Errorf("%s status=%q want %q", tc.sec, sec.DelegateStatus, tc.status)
		}
		if !strings.Contains(sec.DelegateDetail, tc.status) && sec.DelegateDetail == "" {
			t.Errorf("%s detail empty", tc.sec)
		}
		// Prior content preserved.
		wantBody := map[string]string{"s1": "keep-me", "s2": "keep-b", "s3": "keep-c"}[tc.sec]
		if sec.Body != wantBody {
			t.Errorf("%s body=%q want %q", tc.sec, sec.Body, wantBody)
		}
	}
}

func TestSectionDelegateMismatchAndPersist(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Create("root", "Plan", []SectionInput{{Title: "A", Body: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.BeginSectionDelegate(p.ID, "root", "s1", "child-1", "alias")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishSectionDelegate(p.ID, "root", "s1", "other-child", DelegateOutcome{
		Status: DelegateApplied,
	}); !errors.Is(err, ErrDelegateMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	// Still in flight after mismatch.
	got, ok, err := s.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Sections[0].DelegateStatus != DelegateInFlight {
		t.Fatalf("status=%q", got.Sections[0].DelegateStatus)
	}

	body := "done"
	if _, err := s.FinishSectionDelegate(p.ID, "root", "s1", "child-1", DelegateOutcome{
		Status: DelegateApplied,
		Body:   &body,
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s2, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	reloaded, ok, err := s2.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	sec := reloaded.Sections[0]
	if sec.Body != "done" || sec.DelegateStatus != DelegateApplied || sec.DelegateChildID != "child-1" {
		t.Fatalf("persist: %#v", sec)
	}
	if sec.DelegateChildName != "alias" {
		t.Fatalf("name=%q", sec.DelegateChildName)
	}
}
