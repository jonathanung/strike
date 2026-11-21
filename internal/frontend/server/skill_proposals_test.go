package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/pkg/protocol"
)

// seedRepeatedWorkflow writes n root sessions for projectKey that open with
// the same request and run the same tools in their first turn.
func seedRepeatedWorkflow(t *testing.T, dir, projectKey string, n int) {
	t.Helper()
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		id := "wf" + string(rune('a'+i))
		c := protocol.Correlation{SessionID: id, TurnID: "t1"}
		events := []protocol.Event{
			protocol.UserMessage{Correlation: c, Text: "Bump the version, update the changelog and tag the release"},
			protocol.ToolCallBegin{Correlation: c, CallID: "1", Name: "bash", Args: json.RawMessage(`{}`)},
			protocol.ToolCallBegin{Correlation: c, CallID: "2", Name: "edit", Args: json.RawMessage(`{}`)},
			protocol.TurnCompleted{Correlation: c},
		}
		var lines []string
		for j, ev := range events {
			env, err := protocol.Wrap(ev)
			if err != nil {
				t.Fatal(err)
			}
			env.Time = now.Add(-time.Duration(n-i) * time.Hour).Add(time.Duration(j) * time.Second)
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(raw))
		}
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := session.WriteMeta(dir, id, session.Meta{ProjectKey: projectKey, Title: "Release"}); err != nil {
			t.Fatal(err)
		}
	}
}

type proposalsFixture struct {
	srv  *Server
	work string
}

func newProposalsFixture(t *testing.T, readOnly bool) proposalsFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	sessions := t.TempDir()
	id, err := project.Resolve(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	seedRepeatedWorkflow(t, sessions, id.Key, 3)
	srv, err := New(Options{SessionDir: sessions, WorkDir: work, ReadOnly: readOnly})
	if err != nil {
		t.Fatal(err)
	}
	return proposalsFixture{srv: srv, work: work}
}

func (f proposalsFixture) call(t *testing.T, method, path, body string) (int, skillProposalsResponse, string) {
	t.Helper()
	res := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	f.srv.Handler().ServeHTTP(res, req)
	var out skillProposalsResponse
	_ = json.Unmarshal(res.Body.Bytes(), &out)
	return res.Code, out, res.Body.String()
}

func TestSkillProposalsOptInScanAccept(t *testing.T) {
	f := newProposalsFixture(t, false)

	code, got, raw := f.call(t, http.MethodGet, "/v1/skill-proposals", "")
	if code != http.StatusOK || got.Enabled || got.Threshold != 3 || got.WindowDays != 28 || len(got.Pending) != 0 {
		t.Fatalf("default = %d %s", code, raw)
	}
	if code, _, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/scan", ""); code != http.StatusConflict {
		t.Fatalf("scan while off = %d %s", code, raw)
	}
	if code, got, raw := f.call(t, http.MethodPatch, "/v1/skill-proposals", `{"enabled":true}`); code != http.StatusOK || !got.Enabled {
		t.Fatalf("enable = %d %s", code, raw)
	}
	if code, _, raw := f.call(t, http.MethodPatch, "/v1/skill-proposals", `{"threshold":1}`); code != http.StatusBadRequest {
		t.Fatalf("bad threshold = %d %s", code, raw)
	}
	code, got, raw = f.call(t, http.MethodPost, "/v1/skill-proposals/scan", "")
	if code != http.StatusOK || len(got.Pending) != 1 || got.LastScan == nil {
		t.Fatalf("scan = %d %s", code, raw)
	}
	p := got.Pending[0]
	if p.Count != 3 || len(p.Sessions) != 3 || p.Name != "bump-version-update" {
		t.Fatalf("proposal = %+v", p)
	}

	code, _, raw = f.call(t, http.MethodPost, "/v1/skill-proposals/accept", `{"id":"`+p.ID+`"}`)
	if code != http.StatusOK || !strings.Contains(raw, `"wrote":true`) {
		t.Fatalf("accept = %d %s", code, raw)
	}
	res := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/customize", nil))
	if !strings.Contains(res.Body.String(), `"name":"bump-version-update"`) {
		t.Fatalf("accepted skill not in the catalog: %s", res.Body.String())
	}
	if _, got, raw := f.call(t, http.MethodGet, "/v1/skill-proposals", ""); len(got.Pending) != 0 {
		t.Fatalf("accepted draft still pending: %s", raw)
	}
	// The installed name is not proposed again.
	if _, got, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/scan", ""); len(got.Pending) != 0 {
		t.Fatalf("rescan re-proposed an installed skill: %s", raw)
	}
	if code, _, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/accept", `{"id":"`+p.ID+`"}`); code != http.StatusNotFound {
		t.Fatalf("accept twice = %d %s", code, raw)
	}
}

func TestSkillProposalsDismissSuppresses(t *testing.T) {
	f := newProposalsFixture(t, false)
	f.call(t, http.MethodPatch, "/v1/skill-proposals", `{"enabled":true}`)
	_, got, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/scan", "")
	if len(got.Pending) != 1 {
		t.Fatalf("scan = %s", raw)
	}
	code, got, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/dismiss", `{"id":"`+got.Pending[0].ID+`"}`)
	if code != http.StatusOK || len(got.Pending) != 0 {
		t.Fatalf("dismiss = %d %s", code, raw)
	}
	if _, got, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/scan", ""); len(got.Pending) != 0 {
		t.Fatalf("dismissed pattern reappeared: %s", raw)
	}
	if code, _, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/dismiss", `{"id":"nope"}`); code != http.StatusNotFound {
		t.Fatalf("unknown id = %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(f.work, ".strike", "skills")); err == nil {
		t.Fatal("dismiss must not install anything")
	}
}

func TestSkillProposalsReadOnlyRefusesMutations(t *testing.T) {
	f := newProposalsFixture(t, true)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPatch, "/v1/skill-proposals", `{"enabled":true}`},
		{http.MethodPost, "/v1/skill-proposals/scan", ""},
		{http.MethodPost, "/v1/skill-proposals/dismiss", `{"id":"x"}`},
		{http.MethodPost, "/v1/skill-proposals/accept", `{"id":"x"}`},
	} {
		if code, _, raw := f.call(t, tc.method, tc.path, tc.body); code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s", tc.method, tc.path, code, raw)
		}
	}
	if code, _, _ := f.call(t, http.MethodGet, "/v1/skill-proposals", ""); code != http.StatusOK {
		t.Fatalf("read-only GET = %d", code)
	}
}
