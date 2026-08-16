package artifact

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenCreateGetList(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	a, err := s.Create(CreateInput{
		Type:         TypeFindings,
		Title:        "Auth review",
		Content:      `["missing rate limit"]`,
		OwnerSession: "sess-1",
		OwnerRoot:    "root-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.Type != TypeFindings || a.Version != 1 {
		t.Fatalf("create = %#v", a)
	}
	if a.Scope != ScopeProject || a.Access != AccessTeam {
		t.Fatalf("defaults scope/access = %s/%s", a.Scope, a.Access)
	}
	if a.OwnerSession != "sess-1" || a.OwnerRoot != "root-1" {
		t.Fatalf("owners = %#v", a)
	}

	got, ok, err := s.Get(a.ID, "child-2", "root-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Content != a.Content || got.Title != "Auth review" {
		t.Fatalf("got = %#v", got)
	}

	// Deep copy isolation.
	got.Content = "mutated"
	again, ok, err := s.Get(a.ID, "sess-1", "root-1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if again.Content != `["missing rate limit"]` {
		t.Fatalf("store mutated via Get copy: %#v", again)
	}

	list, err := s.List("child-2", "root-1", ListFilter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %#v err=%v", list, err)
	}
	if list[0].ID != a.ID || list[0].Type != TypeFindings {
		t.Fatalf("meta = %#v", list[0])
	}
}

func TestTypeValidation(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.Create(CreateInput{
		Type:         "not_a_type",
		Content:      "x",
		OwnerSession: "s",
		OwnerRoot:    "r",
	})
	if !errors.Is(err, errInvalidType) {
		t.Fatalf("unknown type: %v", err)
	}

	for _, typ := range []string{TypePlan, TypeContract, TypeFindings, TypePatch, TypeTestReport} {
		a, err := s.Create(CreateInput{
			Type:         typ,
			Content:      "body-" + typ,
			OwnerSession: "s",
			OwnerRoot:    "r",
		})
		if err != nil {
			t.Fatalf("type %s: %v", typ, err)
		}
		if a.Type != typ {
			t.Fatalf("type = %q", a.Type)
		}
	}

	list, err := s.List("s", "r", ListFilter{Type: TypePatch})
	if err != nil || len(list) != 1 || list[0].Type != TypePatch {
		t.Fatalf("filter patch = %#v err=%v", list, err)
	}
}

func TestCASConflict(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.Create(CreateInput{
		Type:         TypePatch,
		Content:      "v1",
		OwnerSession: "s",
		OwnerRoot:    "r",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := "v2"
	a2, err := s.Update(a.ID, "s", "r", a.Version, UpdateInput{Content: &content})
	if err != nil {
		t.Fatal(err)
	}
	if a2.Version != 2 || a2.Content != "v2" {
		t.Fatalf("update = %#v", a2)
	}

	stale := "stale"
	if _, err := s.Update(a.ID, "s", "r", a.Version, UpdateInput{Content: &stale}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	got, ok, err := s.Get(a.ID, "s", "r")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Content != "v2" || got.Version != 2 {
		t.Fatalf("corrupted after stale: %#v", got)
	}
}

func TestOwnerVsTeamPermissions(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Team access: peer under same root can read/write.
	team, err := s.Create(CreateInput{
		Type:         TypeFindings,
		Content:      "team-body",
		Access:       AccessTeam,
		OwnerSession: "owner",
		OwnerRoot:    "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(team.ID, "peer", "root"); err != nil || !ok {
		t.Fatalf("team peer get: ok=%v err=%v", ok, err)
	}
	c := "peer-edit"
	if _, err := s.Update(team.ID, "peer", "root", team.Version, UpdateInput{Content: &c}); err != nil {
		t.Fatalf("team peer write: %v", err)
	}
	// Foreign root denied.
	if _, _, err := s.Get(team.ID, "x", "other-root"); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign get: %v", err)
	}
	if _, err := s.Update(team.ID, "x", "other-root", 2, UpdateInput{Content: &c}); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign write: %v", err)
	}

	// Owner-only: peer cannot read.
	own, err := s.Create(CreateInput{
		Type:         TypeContract,
		Content:      "secret",
		Access:       AccessOwner,
		OwnerSession: "owner",
		OwnerRoot:    "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(own.ID, "peer", "root"); !errors.Is(err, ErrDenied) {
		t.Fatalf("owner-only peer get: %v", err)
	}
	if _, ok, err := s.Get(own.ID, "owner", "root"); err != nil || !ok {
		t.Fatalf("owner get: ok=%v err=%v", ok, err)
	}
	// Peer cannot appear in list either.
	list, err := s.List("peer", "root", ListFilter{Type: TypeContract})
	if err != nil || len(list) != 0 {
		t.Fatalf("owner-only list for peer = %#v", list)
	}
}

func TestAccessChangeOwnerOnly(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.Create(CreateInput{
		Type:         TypeFindings,
		Content:      "x",
		Access:       AccessTeam,
		OwnerSession: "owner",
		OwnerRoot:    "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	acc := AccessOwner
	if _, err := s.Update(a.ID, "peer", "root", a.Version, UpdateInput{Access: &acc}); !errors.Is(err, ErrDenied) {
		t.Fatalf("peer access change: %v", err)
	}
	if _, err := s.Update(a.ID, "owner", "root", a.Version, UpdateInput{Access: &acc}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionScopeDurable(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(CreateInput{
		Type:         TypeTestReport,
		Content:      `{"passed":true}`,
		Scope:        ScopeSession,
		SessionID:    "sess-abc",
		OwnerSession: "sess-abc",
		OwnerRoot:    "root-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Scope != ScopeSession || a.SessionID != "sess-abc" {
		t.Fatalf("session fields = %#v", a)
	}
	path := s.Path()
	_ = s.Close()

	// Re-open (simulates process restart / session resume).
	s2, err := Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if s2.Path() != path {
		t.Fatalf("path drift: %s vs %s", s2.Path(), path)
	}
	got, ok, err := s2.Get(a.ID, "sess-abc", "root-1")
	if err != nil || !ok {
		t.Fatalf("resume get: ok=%v err=%v", ok, err)
	}
	if got.Content != `{"passed":true}` || got.Version != 1 {
		t.Fatalf("resume content = %#v", got)
	}
	list, err := s2.List("peer", "root-1", ListFilter{Scope: ScopeSession, SessionID: "sess-abc"})
	if err != nil || len(list) != 1 {
		t.Fatalf("session list = %#v err=%v", list, err)
	}

	// File mode is private.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("artifact file mode = %o, want 0600-ish", fi.Mode().Perm())
	}
}

func TestTTLExpiry(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var offset atomic.Int64
	s.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }

	a, err := s.Create(CreateInput{
		Type:         TypeFindings,
		Content:      "temp",
		OwnerSession: "s",
		OwnerRoot:    "r",
		TTL:          time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.ExpiresAt == nil {
		t.Fatal("expected expires_at")
	}
	if _, ok, err := s.Get(a.ID, "s", "r"); err != nil || !ok {
		t.Fatalf("before expiry: ok=%v err=%v", ok, err)
	}

	offset.Store(int64(2 * time.Hour))
	if _, ok, err := s.Get(a.ID, "s", "r"); err != nil || ok {
		t.Fatalf("after expiry: ok=%v err=%v", ok, err)
	}
	list, err := s.List("s", "r", ListFilter{})
	if err != nil || len(list) != 0 {
		t.Fatalf("list after expiry = %#v", list)
	}
	c := "nope"
	if _, err := s.Update(a.ID, "s", "r", a.Version, UpdateInput{Content: &c}); !errors.Is(err, ErrExpired) {
		t.Fatalf("update expired: %v", err)
	}
}

func TestGetVersion(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.Create(CreateInput{
		Type: TypePatch, Content: "1", OwnerSession: "s", OwnerRoot: "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := "2"
	a2, err := s.Update(a.ID, "s", "r", 1, UpdateInput{Content: &c})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetVersion(a.ID, 1, "s", "r"); err != nil || ok {
		t.Fatalf("old version should miss: ok=%v err=%v", ok, err)
	}
	got, ok, err := s.GetVersion(a.ID, a2.Version, "s", "r")
	if err != nil || !ok || got.Content != "2" {
		t.Fatalf("current version: ok=%v got=%#v err=%v", ok, got, err)
	}
}

func TestConcurrentCAS(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.Create(CreateInput{
		Type: TypeFindings, Content: "0", OwnerSession: "s", OwnerRoot: "r",
	})
	if err != nil {
		t.Fatal(err)
	}

	const n = 20
	var wg sync.WaitGroup
	var wins atomic.Int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			body := string(rune('a' + i%26))
			_, err := s.Update(a.ID, "s", "r", a.Version, UpdateInput{Content: &body})
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("unexpected: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("wins = %d, want 1", wins.Load())
	}
	got, ok, err := s.Get(a.ID, "s", "r")
	if err != nil || !ok || got.Version != 2 {
		t.Fatalf("after race: %#v ok=%v err=%v", got, ok, err)
	}
}

func TestPersistReload(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "k")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(CreateInput{
		Type: TypeContract, Title: "API", Content: "{}", OwnerSession: "s", OwnerRoot: "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	// Corrupt-proof: directory exists.
	if _, err := os.Stat(filepath.Dir(s.Path())); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root, "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, ok, err := s2.Get(a.ID, "s", "r")
	if err != nil || !ok || got.Title != "API" {
		t.Fatalf("reload = %#v ok=%v err=%v", got, ok, err)
	}
}
