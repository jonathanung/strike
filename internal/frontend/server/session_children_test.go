package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
)

// childSessions is a Sessions stub with one child. The other methods are unused.
type childSessions struct {
	items []host.Session
}

func (c childSessions) Get(string) (host.Session, bool, error) {
	return host.Session{}, false, nil
}
func (c childSessions) List(bool) ([]host.Session, error) { return nil, nil }
func (c childSessions) Children(string) ([]host.Session, error) {
	return c.items, nil
}
func (c childSessions) ReplayJSONL(string) ([]byte, error) { return nil, nil }
func (c childSessions) Fork(string) (host.Session, error) {
	return host.Session{}, errChildStub
}
func (c childSessions) ForkAt(string, int) (host.Session, error) {
	return host.Session{}, errChildStub
}
func (c childSessions) Rename(string, string) (host.Session, error) {
	return host.Session{}, errChildStub
}
func (c childSessions) Delete(string, bool) error { return errChildStub }

type stubErr string

func (e stubErr) Error() string { return string(e) }

const errChildStub stubErr = "not used"

func TestSessionChildrenUsesSummaryFields(t *testing.T) {
	when := time.Date(2026, 10, 26, 1, 2, 3, 0, time.UTC)
	dir := t.TempDir()
	srv, err := New(Options{
		SessionDir: dir,
		Services: &host.Services{
			Sessions: childSessions{items: []host.Session{{
				ID:        "child-1",
				ParentID:  "root-1",
				Title:     "scribe",
				UpdatedAt: when,
			}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/root-1/children", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("children: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 {
		t.Fatalf("sessions = %#v", body.Sessions)
	}
	row := body.Sessions[0]
	if row["id"] != "child-1" || row["parentId"] != "root-1" || row["title"] != "scribe" {
		t.Fatalf("summary fields = %#v", row)
	}
	if _, ok := row["ID"]; ok {
		t.Fatalf("exported Go field name leaked: %#v", row)
	}
}
