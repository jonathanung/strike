package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
)

// Regression tests for #1399: DELETE /v1/sessions/{id} used to leave the
// session's live root registered in LiveHub, so GET /v1/roots kept listing
// the deleted session (with liveId set) and root-scoped routes still
// resolved it until daemon restart.

// sessionDeleteFixture wires a real session Manager (via the local host
// adapter) to a hub holding one Live per given session id.
type sessionDeleteFixture struct {
	srv   *Server
	hub   *LiveHub
	mgr   *session.Manager
	lives map[string]*Live
}

func newSessionDeleteFixture(t *testing.T, ids ...string) *sessionDeleteFixture {
	t.Helper()
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	f := &sessionDeleteFixture{hub: NewLiveHub(nil, nil), mgr: mgr, lives: map[string]*Live{}}
	for _, id := range ids {
		if _, err := mgr.Create(session.CreateOptions{ID: id}); err != nil {
			t.Fatal(err)
		}
		live := NewLive(id, dir, nil, make(chan protocol.Op, 4))
		f.lives[id] = live
		f.hub.Add(id, live)
	}
	srv, err := New(Options{
		SessionDir: dir,
		LiveHub:    f.hub,
		Services:   &host.Services{Sessions: local.NewSessions(mgr, "")},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.srv = srv
	return f
}

func (f *sessionDeleteFixture) do(t *testing.T, method, path string) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code, rec.Body.Bytes()
}

func (f *sessionDeleteFixture) roots(t *testing.T) rootsResponse {
	t.Helper()
	code, body := f.do(t, http.MethodGet, "/v1/roots")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/roots = %d %s", code, body)
	}
	var rr rootsResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		t.Fatal(err)
	}
	return rr
}

func rootIDs(rr rootsResponse) []string {
	out := make([]string, 0, len(rr.Roots))
	for _, r := range rr.Roots {
		out = append(out, r.ID)
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// (a) After a successful session DELETE, the live root is gone from
// LiveHub and root-scoped lookups fail exactly as for a never-registered
// root. A failed (409) delete must not deregister anything.
func TestSessionDeleteDeregistersLiveRoot(t *testing.T) {
	f := newSessionDeleteFixture(t, "del-a", "del-b")

	// Non-force delete of the open session fails (409) and the root stays.
	if code, body := f.do(t, http.MethodDelete, "/v1/sessions/del-a"); code != http.StatusConflict {
		t.Fatalf("DELETE open session without force = %d %s, want 409", code, body)
	}
	if f.hub.LiveFor("del-a") == nil {
		t.Fatal("root del-a deregistered by a failed (409) session delete")
	}

	// Force delete succeeds and deregisters the root in the same call.
	if code, body := f.do(t, http.MethodDelete, "/v1/sessions/del-a?force=true"); code != http.StatusNoContent {
		t.Fatalf("DELETE force = %d %s, want 204", code, body)
	}
	if got := f.hub.LiveFor("del-a"); got != nil {
		t.Fatalf("LiveFor(del-a) after session delete = %v, want nil", got)
	}
	rr := f.roots(t)
	if ids := rootIDs(rr); len(ids) != 1 || ids[0] != "del-b" {
		t.Fatalf("roots after delete = %v, want [del-b]", ids)
	}
	if rr.ActiveID != "del-b" {
		t.Fatalf("activeId after delete = %q, want del-b", rr.ActiveID)
	}

	// The deleted root's Live bridge is closed, like DELETE /v1/roots does.
	if err := f.lives["del-a"].Submit(context.Background(), protocol.UserInput{Text: "x"}); err == nil {
		t.Fatal("deleted root's Live still accepts ops after session delete")
	}

	// Root-scoped lookup parity: the deleted root fails exactly like a
	// root that was never registered.
	codeDeleted, _ := f.do(t, http.MethodGet, "/v1/status?root=del-a")
	codeNever, _ := f.do(t, http.MethodGet, "/v1/status?root=never-registered")
	if codeDeleted != http.StatusServiceUnavailable || codeDeleted != codeNever {
		t.Fatalf("status for deleted root = %d, never-registered = %d, want both 503", codeDeleted, codeNever)
	}
	if code, _ := f.do(t, http.MethodGet, "/v1/status?root=del-b"); code != http.StatusOK {
		t.Fatalf("status for surviving root = %d, want 200", code)
	}

	// Sessions list no longer shows the deleted session either.
	_, body := f.do(t, http.MethodGet, "/v1/sessions")
	var sess sessionsResponse
	if err := json.Unmarshal(body, &sess); err != nil {
		t.Fatal(err)
	}
	for _, s := range sess.Sessions {
		if s.ID == "del-a" {
			t.Fatal("deleted session still in GET /v1/sessions")
		}
	}
	if sess.LiveID == "del-a" {
		t.Fatalf("liveId still %q after delete", sess.LiveID)
	}

	// Deleting a durable session that was never live is a hub no-op.
	if _, err := f.mgr.Create(session.CreateOptions{ID: "del-c"}); err != nil {
		t.Fatal(err)
	}
	if err := f.mgr.Close("del-c"); err != nil {
		t.Fatal(err)
	}
	if code, body := f.do(t, http.MethodDelete, "/v1/sessions/del-c"); code != http.StatusNoContent {
		t.Fatalf("DELETE non-live session = %d %s, want 204", code, body)
	}
	if ids := rootIDs(f.roots(t)); len(ids) != 1 || ids[0] != "del-b" {
		t.Fatalf("roots after non-live delete = %v, want [del-b]", ids)
	}
}

// (b) Two sessions sharing one hub: deleting one leaves the other's root
// registered and usable. And when two hub entries share the same Live
// bridge for a session, deleting that session leaves the registration in
// place — a root another live entry still uses is never deregistered.
func TestSessionDeleteSharedRootStaysRegistered(t *testing.T) {
	t.Run("other root unaffected", func(t *testing.T) {
		f := newSessionDeleteFixture(t, "keep-a", "keep-b")
		if code, body := f.do(t, http.MethodDelete, "/v1/sessions/keep-a?force=true"); code != http.StatusNoContent {
			t.Fatalf("DELETE force = %d %s, want 204", code, body)
		}
		if got := f.hub.LiveFor("keep-b"); got == nil {
			t.Fatal("surviving root keep-b deregistered by another session's delete")
		}
		if err := f.lives["keep-b"].Submit(context.Background(), protocol.UserInput{Text: "x"}); err != nil {
			t.Fatalf("surviving root's Live broken by another session's delete: %v", err)
		}
		if ids := rootIDs(f.roots(t)); len(ids) != 1 || ids[0] != "keep-b" {
			t.Fatalf("roots = %v, want [keep-b]", ids)
		}
	})

	t.Run("shared live bridge kept", func(t *testing.T) {
		dir := t.TempDir()
		mgr := session.NewManager(dir)
		for _, id := range []string{"shared-a", "shared-b"} {
			if _, err := mgr.Create(session.CreateOptions{ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		shared := NewLive("shared-a", dir, nil, make(chan protocol.Op, 4))
		hub := NewLiveHub(nil, nil)
		hub.Add("shared-a", shared)
		hub.Add("shared-b", shared) // second live entry referencing the same root bridge
		srv, err := New(Options{
			SessionDir: dir,
			LiveHub:    hub,
			Services:   &host.Services{Sessions: local.NewSessions(mgr, "")},
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/sessions/shared-a?force=true", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE force = %d %s, want 204", rec.Code, rec.Body.String())
		}
		if got := hub.LiveFor("shared-b"); got == nil {
			t.Fatal("root deregistered while shared-b still references it")
		}
		if err := shared.Submit(context.Background(), protocol.UserInput{Text: "x"}); err != nil {
			t.Fatalf("shared Live closed by session delete: %v", err)
		}
		if ids := rootIDs(mustRoots(t, srv)); !contains(ids, "shared-b") {
			t.Fatalf("roots = %v, want shared-b still registered", ids)
		}
	})
}

func mustRoots(t *testing.T, srv *Server) rootsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/roots", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/roots = %d %s", rec.Code, rec.Body.String())
	}
	var rr rootsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rr); err != nil {
		t.Fatal(err)
	}
	return rr
}

// (c) The cockpit's composed flow — session DELETE followed by
// DELETE /v1/roots/{id} — still succeeds: the follow-up root delete is a
// harmless 404 for the already-deregistered root, and root DELETE still
// works for a root that is still live.
func TestSessionDeleteThenRootDeleteComposedFlow(t *testing.T) {
	f := newSessionDeleteFixture(t, "comp-a", "comp-b")

	if code, body := f.do(t, http.MethodDelete, "/v1/sessions/comp-a?force=true"); code != http.StatusNoContent {
		t.Fatalf("session DELETE = %d %s, want 204", code, body)
	}
	// Cockpit follow-up for a live root: already gone, so a plain 404 and
	// no other effect (the pre-existing DELETE /v1/roots semantics).
	if code, body := f.do(t, http.MethodDelete, "/v1/roots/comp-a"); code != http.StatusNotFound {
		t.Fatalf("composed root DELETE = %d %s, want 404", code, body)
	}
	if ids := rootIDs(f.roots(t)); len(ids) != 1 || ids[0] != "comp-b" {
		t.Fatalf("roots after composed flow = %v, want [comp-b]", ids)
	}
	// Root DELETE on a still-live root is unchanged.
	if code, body := f.do(t, http.MethodDelete, "/v1/roots/comp-b"); code != http.StatusOK {
		t.Fatalf("root DELETE live root = %d %s, want 200", code, body)
	}
	if ids := rootIDs(f.roots(t)); len(ids) != 0 {
		t.Fatalf("roots after root delete = %v, want []", ids)
	}
}

// Hub-level semantics of RemoveForSession.
func TestLiveHubRemoveForSession(t *testing.T) {
	t.Run("unknown session is a no-op", func(t *testing.T) {
		hub := NewLiveHub(nil, nil)
		live := NewLive("r1", "/a", nil, make(chan protocol.Op, 1))
		defer live.Close()
		hub.Add("r1", live)
		if hub.RemoveForSession("missing") {
			t.Fatal("RemoveForSession(missing) = true, want false")
		}
		if len(hub.List()) != 1 {
			t.Fatal("hub changed by unknown RemoveForSession")
		}
	})

	t.Run("root serving a different session is kept", func(t *testing.T) {
		hub := NewLiveHub(nil, nil)
		live := NewLive("someone-else", "/a", nil, make(chan protocol.Op, 1))
		defer live.Close()
		hub.Add("alias-key", live)
		if hub.RemoveForSession("alias-key") {
			t.Fatal("RemoveForSession removed a root whose Live serves a different session")
		}
		if hub.LiveFor("alias-key") == nil {
			t.Fatal("mismatched root deregistered")
		}
	})

	t.Run("unique aliased entry is removed by session id", func(t *testing.T) {
		hub := NewLiveHub(nil, nil)
		live := NewLive("sess-x", "/a", nil, make(chan protocol.Op, 1))
		hub.Add("alias-key", live)
		if !hub.RemoveForSession("sess-x") {
			t.Fatal("RemoveForSession(sess-x) = false, want true for the unique aliased entry")
		}
		if hub.LiveFor("alias-key") != nil || len(hub.List()) != 0 {
			t.Fatal("aliased entry still registered after RemoveForSession")
		}
	})

	t.Run("nil hub", func(t *testing.T) {
		var hub *LiveHub
		if hub.RemoveForSession("r1") {
			t.Fatal("nil hub RemoveForSession = true, want false")
		}
	})
}
