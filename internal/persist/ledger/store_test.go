package ledger

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAppendGetList(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	e, err := s.Append(AppendInput{
		Kind:          KindAssumption,
		Statement:     "API X is the only auth path",
		Confidence:    ConfidenceHigh,
		EvidenceRefs:  []string{"artifact:ab12"},
		ScopePaths:    []string{"internal/auth"},
		AuthorSession: "sess-1",
		AuthorAgent:   "orchestrator",
		AuthorRoot:    "root-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.Status != StatusActive || e.Kind != KindAssumption {
		t.Fatalf("append = %#v", e)
	}
	if e.Confidence != ConfidenceHigh || e.AuthorSession != "sess-1" {
		t.Fatalf("fields = %#v", e)
	}

	got, ok, err := s.Get(e.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Statement != e.Statement {
		t.Fatalf("got = %#v", got)
	}
	got.Statement = "mutated"
	again, ok, err := s.Get(e.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if again.Statement != "API X is the only auth path" {
		t.Fatalf("store mutated via Get copy: %#v", again)
	}

	list, err := s.List(ListFilter{Status: StatusActive})
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %#v err=%v", list, err)
	}
}

func TestInvalidatePreservesHistory(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	e, err := s.Append(AppendInput{
		Kind:          KindAssumption,
		Statement:     "tests are flaky so skip",
		AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}

	inv, err := s.Invalidate(e.ID, InvalidateInput{
		Reason:   "CI is green with retries disabled",
		Evidence: []string{"run:123", "file:ci.yml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Status != StatusInvalidated {
		t.Fatalf("status = %s", inv.Status)
	}
	if inv.InvalidateReason == "" || inv.InvalidatedAt == nil {
		t.Fatalf("missing invalidate meta: %#v", inv)
	}
	if len(inv.InvalidateEvidence) != 2 {
		t.Fatalf("evidence = %#v", inv.InvalidateEvidence)
	}
	// Original statement preserved.
	if inv.Statement != e.Statement {
		t.Fatalf("statement changed: %q", inv.Statement)
	}

	// Still listable in full history.
	all, err := s.List(ListFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("history = %#v err=%v", all, err)
	}
	active, err := s.ActiveSlice("", "")
	if err != nil || len(active) != 0 {
		t.Fatalf("active after invalidate = %#v", active)
	}

	// Cannot invalidate twice.
	if _, err := s.Invalidate(e.ID, InvalidateInput{Reason: "again"}); !errors.Is(err, errNotActive) {
		t.Fatalf("second invalidate: %v", err)
	}
}

func TestSupersede(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	old, err := s.Append(AppendInput{
		Kind:          KindDecision,
		Statement:     "use library A",
		AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	neu, err := s.Supersede(old.ID, AppendInput{
		Kind:          KindDecision,
		Statement:     "use library B instead",
		AuthorSession: "s",
		EvidenceRefs:  []string{"review:#9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if neu.Status != StatusActive || neu.Supersedes != old.ID {
		t.Fatalf("new = %#v", neu)
	}
	prior, ok, err := s.Get(old.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if prior.Status != StatusSuperseded || prior.SupersededBy != neu.ID {
		t.Fatalf("prior = %#v", prior)
	}
	active, err := s.ActiveSlice("", "")
	if err != nil || len(active) != 1 || active[0].ID != neu.ID {
		t.Fatalf("active = %#v", active)
	}
}

func TestActiveSliceScope(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.Append(AppendInput{
		Kind: KindConstraint, Statement: "global only", AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Append(AppendInput{
		Kind: KindAssumption, Statement: "auth scoped",
		ScopePaths: []string{"internal/auth"}, AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Append(AppendInput{
		Kind: KindDecision, Statement: "task scoped",
		ScopeTaskIDs: []string{"del-9"}, AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Path filter: global + matching path.
	byPath, err := s.ActiveSlice("internal/auth/oauth.go", "")
	if err != nil || len(byPath) != 2 {
		t.Fatalf("by path = %#v err=%v", byPath, err)
	}
	// Task filter: global + matching task.
	byTask, err := s.ActiveSlice("", "del-9")
	if err != nil || len(byTask) != 2 {
		t.Fatalf("by task = %#v err=%v", byTask, err)
	}
	// Unrelated path: only global.
	other, err := s.ActiveSlice("internal/tool/x.go", "")
	if err != nil || len(other) != 1 || other[0].Statement != "global only" {
		t.Fatalf("other path = %#v", other)
	}
}

func TestKindConfidenceValidation(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.Append(AppendInput{Kind: "nope", Statement: "x", AuthorSession: "s"}); !errors.Is(err, errInvalidKind) {
		t.Fatalf("kind: %v", err)
	}
	if _, err := s.Append(AppendInput{Kind: KindDecision, Statement: "x", Confidence: "maybe", AuthorSession: "s"}); !errors.Is(err, errInvalidConfidence) {
		t.Fatalf("conf: %v", err)
	}
	if _, err := s.Append(AppendInput{Kind: KindDecision, Statement: "", AuthorSession: "s"}); !errors.Is(err, errEmptyStatement) {
		t.Fatalf("empty: %v", err)
	}
	// Default confidence medium.
	e, err := s.Append(AppendInput{Kind: KindDecision, Statement: "ok", AuthorSession: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Confidence != ConfidenceMedium {
		t.Fatalf("default conf = %s", e.Confidence)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	root := t.TempDir()
	s1, err := Open(root, "proj-key")
	if err != nil {
		t.Fatal(err)
	}
	e, err := s1.Append(AppendInput{
		Kind: KindConstraint, Statement: "no force push", AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := s1.Path()
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root, "proj-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if s2.Path() != path {
		t.Fatalf("path drift %s vs %s", s2.Path(), path)
	}
	got, ok, err := s2.Get(e.ID)
	if err != nil || !ok || got.Statement != "no force push" {
		t.Fatalf("reload = %#v ok=%v err=%v", got, ok, err)
	}
	// File mode is private.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("perms = %v", fi.Mode())
	}
}

func TestConcurrentAppendSafety(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	const n = 40
	var wg sync.WaitGroup
	var fails atomic.Int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, err := s.Append(AppendInput{
				Kind:          KindAssumption,
				Statement:     "concurrent-entry-" + strconv.Itoa(i),
				AuthorSession: "s",
			})
			if err != nil {
				fails.Add(1)
			}
		}()
	}
	wg.Wait()
	if fails.Load() != 0 {
		t.Fatalf("concurrent failures: %d", fails.Load())
	}
	list, err := s.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != n {
		t.Fatalf("got %d entries, want %d", len(list), n)
	}
	data, err := os.ReadFile(s.Path())
	if err != nil || len(data) == 0 {
		t.Fatalf("disk: %v len=%d", err, len(data))
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
	if _, err := s.Append(AppendInput{Kind: KindDecision, Statement: "x", AuthorSession: "s"}); !errors.Is(err, errClosed) {
		t.Fatalf("append closed: %v", err)
	}
	if _, _, err := s.Get("x"); !errors.Is(err, errClosed) {
		t.Fatalf("get closed: %v", err)
	}
}

func TestAutoLoadLayer(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Append(AppendInput{
		Kind: KindAssumption, Statement: "we use module M", AuthorSession: "s",
		ScopePaths: []string{"pkg/m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, omitted, err := AutoLoadLayer(s, "pkg/m/x.go", "", "")
	if err != nil || omitted != 0 {
		t.Fatalf("autoload err=%v omitted=%d", err, omitted)
	}
	if text == "" || !strings.Contains(text, "module M") || !strings.Contains(text, "ledger_write") {
		t.Fatalf("layer = %q", text)
	}
	// Invalidate removes from active slice / autoload.
	list, _ := s.List(ListFilter{Status: StatusActive})
	_, err = s.Invalidate(list[0].ID, InvalidateInput{Reason: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	text2, _, err := AutoLoadLayer(s, "", "", "")
	if err != nil || text2 != "" {
		t.Fatalf("after invalidate layer=%q err=%v", text2, err)
	}
}

func TestClockOverride(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	fixed := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }
	e, err := s.Append(AppendInput{Kind: KindDecision, Statement: "t", AuthorSession: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !e.CreatedAt.Equal(fixed) {
		t.Fatalf("created = %v", e.CreatedAt)
	}
}
