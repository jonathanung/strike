package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/persist/session"
)

// Regression: the daemon mints dotted session ids via
// persist/session.NewID (e.g. "20261027T015110.714741099Z-<suffix>"), so
// the shared cleanSessionID validator must accept them — otherwise the
// seen/unseen endpoints 400 for every real session.
func TestCleanSessionIDAcceptsDaemonNewID(t *testing.T) {
	for _, id := range []string{
		session.NewID(),
		"20261027T015110.714741099Z-KLTIMWG3SW22Q65XPBYUQ6L5P6",
		"root-1",
		"a_b.c-d",
	} {
		got, ok := cleanSessionID(id)
		if !ok || got != id {
			t.Errorf("cleanSessionID(%q) = %q, %v; want the id itself, true", id, got, ok)
		}
	}
	for _, id := range []string{
		"",
		"..",
		"a..b",
		"../etc",
		"a/b",
		`a\b`,
		"a b",
		"a:b",
		strings.Repeat("a", 81),
	} {
		if got, ok := cleanSessionID(id); ok {
			t.Errorf("cleanSessionID(%q) = %q, true; want rejection", id, got)
		}
	}
}

func TestSessionSeenUnseenAcceptDaemonNewID(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(Options{SessionDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	id := session.NewID()
	seenReq, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions/"+id+"/seen", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	seenReq.Header.Set("Content-Type", "application/json")
	seenRes, err := http.DefaultClient.Do(seenReq)
	if err != nil {
		t.Fatal(err)
	}
	seenRes.Body.Close()
	if seenRes.StatusCode != http.StatusOK {
		t.Fatalf("seen status for NewID() id %q = %d, want 200", id, seenRes.StatusCode)
	}

	unseenReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/v1/sessions/"+id+"/seen", nil)
	if err != nil {
		t.Fatal(err)
	}
	unseenRes, err := http.DefaultClient.Do(unseenReq)
	if err != nil {
		t.Fatal(err)
	}
	unseenRes.Body.Close()
	if unseenRes.StatusCode != http.StatusOK {
		t.Fatalf("unseen status for NewID() id %q = %d, want 200", id, unseenRes.StatusCode)
	}

	badReq, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions/no..pe/seen", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	badReq.Header.Set("Content-Type", "application/json")
	badRes, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	badRes.Body.Close()
	if badRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("seen status for '..' id = %d, want 400", badRes.StatusCode)
	}
}
