package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/artifact"
)

// CreateDashboard lets the shared fakeArtifacts satisfy host.DashboardWriter.
// It validates like the real store so a template that drifted out of the
// spec fails here.
func (f *fakeArtifacts) CreateDashboard(ownerSession, ownerRoot, title, content string) (host.Artifact, error) {
	if _, err := artifact.ParseDashboard(content); err != nil {
		return host.Artifact{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.arts == nil {
		f.arts = map[string]host.Artifact{}
	}
	now := time.Now().UTC()
	a := host.Artifact{
		ID: fmt.Sprintf("dash-%d", len(f.arts)+1), Type: artifact.TypeDashboard, Title: title, Content: content,
		Version: 1, Scope: "project", Access: "team",
		OwnerSession: ownerSession, OwnerRoot: ownerRoot, CreatedAt: now, UpdatedAt: now, Versions: []int{1},
	}
	f.arts[a.ID] = a
	return a, nil
}

func dashCall(t *testing.T, srv *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decodePage(t *testing.T, rec *httptest.ResponseRecorder) dashboardPageDTO {
	t.Helper()
	var page dashboardPageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return page
}

func findBlock(page dashboardPageDTO, metric string) *dashboardBlockDTO {
	for i := range page.Blocks {
		if page.Blocks[i].Metric == metric {
			return &page.Blocks[i]
		}
	}
	return nil
}

func TestDashboardTemplatesAreValidSpecs(t *testing.T) {
	for _, name := range []string{"project_report", "standup", "kanban"} {
		title, spec, ok := dashboardTemplate(name)
		if !ok || title == "" {
			t.Fatalf("%s: missing", name)
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.ParseDashboard(string(raw)); err != nil {
			t.Fatalf("%s: template is not a valid spec: %v", name, err)
		}
	}
	if _, _, ok := dashboardTemplate("roadmap"); ok {
		t.Fatal("unknown template accepted")
	}
}

func TestDashboardCreateMeasuresFromSessionRecords(t *testing.T) {
	dir := t.TempDir()
	appendSessionLine(t, dir, "sess-a", `{"type":"session.started","time":"2026-11-20T12:00:00Z","data":{"sessionId":"sess-a"}}`)
	appendSessionLine(t, dir, "sess-b", `{"type":"session.started","time":"2026-11-20T12:00:00Z","data":{"sessionId":"sess-b"}}`)
	arts := &fakeArtifacts{arts: map[string]host.Artifact{}}
	srv, err := New(Options{SessionDir: dir, Services: &host.Services{Artifacts: arts}})
	if err != nil {
		t.Fatal(err)
	}
	rec := dashCall(t, srv, http.MethodPost, "/v1/dashboards?actorRoot=root-1", `{"template":"project_report"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	page := decodePage(t, rec)
	if page.Type != artifact.TypeDashboard || page.Title != "Project report" || page.Template != "project_report" {
		t.Fatalf("page = %+v", page.artifactMetaDTO)
	}
	total := findBlock(page, "sessions.total")
	if total == nil || !total.Measured || total.Value == nil || *total.Value != 2 || total.Provenance != "measured" {
		t.Fatalf("sessions.total = %+v", total)
	}
	cost := findBlock(page, "cost.usd")
	if cost == nil || cost.Measured || cost.Value != nil || cost.Note == "" {
		t.Fatalf("cost.usd must be unmeasured (dash), got %+v", cost)
	}
	approvals := findBlock(page, "approvals.pending")
	if approvals == nil || approvals.Measured || approvals.Value != nil {
		t.Fatalf("approvals without a live hub must be unmeasured, got %+v", approvals)
	}
	var kanban, summary *dashboardBlockDTO
	for i := range page.Blocks {
		switch page.Blocks[i].Kind {
		case artifact.BlockSessionsKanban:
			kanban = &page.Blocks[i]
		case artifact.BlockText:
			summary = &page.Blocks[i]
		}
	}
	if kanban == nil || !kanban.Measured || len(kanban.Columns) != 3 {
		t.Fatalf("sessions kanban = %+v", kanban)
	}
	cards := 0
	for _, c := range kanban.Columns {
		cards += len(c.Cards)
	}
	if cards != 2 {
		t.Fatalf("kanban cards = %d, want 2", cards)
	}
	if summary == nil || summary.Provenance != "agent" || summary.Measured {
		t.Fatalf("summary = %+v", summary)
	}

	// The index lists it for the same root, not for another.
	list := dashCall(t, srv, http.MethodGet, "/v1/dashboards?actorRoot=root-1", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), page.ID) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	other := dashCall(t, srv, http.MethodGet, "/v1/dashboards?actorRoot=root-2", "")
	if strings.Contains(other.Body.String(), page.ID) {
		t.Fatalf("another root sees the page: %s", other.Body.String())
	}

	// Reopen re-measures: a new session shows up in the same page.
	appendSessionLine(t, dir, "sess-c", `{"type":"session.started","time":"2026-11-20T12:00:00Z","data":{"sessionId":"sess-c"}}`)
	got := dashCall(t, srv, http.MethodGet, "/v1/dashboards/"+page.ID+"?actorRoot=root-1", "")
	if got.Code != http.StatusOK {
		t.Fatalf("get = %d %s", got.Code, got.Body.String())
	}
	again := findBlock(decodePage(t, got), "sessions.total")
	if again == nil || again.Value == nil || *again.Value != 3 {
		t.Fatalf("re-measured total = %+v", again)
	}
}

func TestDashboardAgentBlocksPassThroughAndNeverCarryValues(t *testing.T) {
	spec := `{"version":1,"blocks":[
	 {"kind":"metric","title":"Bogus","metric":"made.up"},
	 {"kind":"list","title":"Today","items":["ship it"]},
	 {"kind":"kanban","title":"Work","columns":[{"title":"Doing","cards":["parser"]}]}
	]}`
	arts := &fakeArtifacts{arts: map[string]host.Artifact{
		"d1":  {ID: "d1", Type: artifact.TypeDashboard, Title: "Mine", Content: spec, Version: 3, Scope: "project", Access: "team", OwnerSession: "s1", OwnerRoot: "root-1"},
		"f1":  {ID: "f1", Type: artifact.TypeFindings, Content: "x", Version: 1, Scope: "project", Access: "team", OwnerSession: "s1", OwnerRoot: "root-1"},
		"bad": {ID: "bad", Type: artifact.TypeDashboard, Content: "not json", Version: 1, Scope: "project", Access: "team", OwnerSession: "s1", OwnerRoot: "root-1"},
	}}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Artifacts: arts}})
	if err != nil {
		t.Fatal(err)
	}
	rec := dashCall(t, srv, http.MethodGet, "/v1/dashboards/d1?actorRoot=root-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	page := decodePage(t, rec)
	if len(page.Blocks) != 3 {
		t.Fatalf("blocks = %+v", page.Blocks)
	}
	bogus := page.Blocks[0]
	if bogus.Measured || bogus.Value != nil || bogus.Note != "unknown metric" {
		t.Fatalf("unknown metric = %+v", bogus)
	}
	if page.Blocks[1].Provenance != "agent" || len(page.Blocks[1].Items) != 1 {
		t.Fatalf("list = %+v", page.Blocks[1])
	}
	if page.Blocks[2].Provenance != "agent" || page.Blocks[2].Columns[0].Cards[0] != "parser" {
		t.Fatalf("kanban = %+v", page.Blocks[2])
	}
	if strings.Contains(rec.Body.String(), `"value"`) {
		t.Fatalf("no block on this page is measured, so no value may be sent: %s", rec.Body.String())
	}
	cases := []struct {
		name, target string
		code         int
	}{
		{"non-dashboard artifact", "/v1/dashboards/f1?actorRoot=root-1", http.StatusNotFound},
		{"other root", "/v1/dashboards/d1?actorRoot=root-2", http.StatusNotFound},
		{"missing", "/v1/dashboards/nope?actorRoot=root-1", http.StatusNotFound},
		{"corrupt spec", "/v1/dashboards/bad?actorRoot=root-1", http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		if got := dashCall(t, srv, http.MethodGet, tc.target, ""); got.Code != tc.code {
			t.Fatalf("%s: %d %s, want %d", tc.name, got.Code, got.Body.String(), tc.code)
		}
	}
}

func TestDashboardCreateRejectsBadRequests(t *testing.T) {
	arts := &fakeArtifacts{arts: map[string]host.Artifact{}}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Artifacts: arts}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, target, body string
	}{
		{"no template", "/v1/dashboards?actorRoot=r", `{}`},
		{"empty body", "/v1/dashboards?actorRoot=r", ""},
		{"unknown template", "/v1/dashboards?actorRoot=r", `{"template":"roadmap"}`},
		{"unknown field", "/v1/dashboards?actorRoot=r", `{"template":"standup","blocks":[]}`},
		{"long title", "/v1/dashboards?actorRoot=r", `{"template":"standup","title":"` + strings.Repeat("t", 121) + `"}`},
	}
	for _, tc := range cases {
		if got := dashCall(t, srv, http.MethodPost, tc.target, tc.body); got.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", tc.name, got.Code, got.Body.String())
		}
	}
	if len(arts.arts) != 0 {
		t.Fatalf("bad requests created pages: %+v", arts.arts)
	}
}
