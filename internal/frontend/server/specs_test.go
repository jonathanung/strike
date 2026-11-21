package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/persist/plan"
	"github.com/jonathanung/strike/internal/persist/spec"
	"github.com/jonathanung/strike/internal/protocol"
)

func specServer(t *testing.T) (*Server, string, *plan.Store) {
	t.Helper()
	work := t.TempDir()
	ps, err := plan.Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		WorkDir:    work,
		Services:   &host.Services{Plans: local.NewPlans(ps)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, work, ps
}

func specDo(t *testing.T, srv *Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	srv.Handler().ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func specVersion(t *testing.T, v map[string]any) int {
	t.Helper()
	n, ok := v["version"].(float64)
	if !ok {
		t.Fatalf("no version in %v", v)
	}
	return int(n)
}

func TestSpecsUnavailableWithoutWorkDir(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := specDo(t, srv, http.MethodGet, "/v1/specs", "")
	if code == http.StatusOK {
		t.Fatalf("specs served without a workspace")
	}
}

func TestSpecLifecycleOverHTTP(t *testing.T) {
	srv, work, ps := specServer(t)

	code, created := specDo(t, srv, http.MethodPost, "/v1/specs", `{"name":"export","title":"Data export"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, created)
	}
	if _, err := os.Stat(filepath.Join(work, ".strike", "specs", "export", "requirements.md")); err != nil {
		t.Fatalf("spec files not in workspace: %v", err)
	}
	if code, _ := specDo(t, srv, http.MethodPost, "/v1/specs", `{"name":"export"}`); code != http.StatusConflict {
		t.Fatalf("duplicate create = %d", code)
	}
	v := specVersion(t, created)

	// Design approval before requirements is refused with the reason.
	code, refused := specDo(t, srv, http.MethodPost, "/v1/specs/export/phases/design", `{"status":"approved","expectedVersion":1}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(refused["error"].(string), "approve requirements first") {
		t.Fatalf("design-first = %d %v", code, refused)
	}

	code, saved := specDo(t, srv, http.MethodPut, "/v1/specs/export/requirements",
		`{"expectedVersion":1,"requirements":[{"story":"export csv","criteria":["WHEN I click export THE SYSTEM SHALL download a csv"]},{"story":"export json","criteria":["exports json"]}]}`)
	if code != http.StatusOK {
		t.Fatalf("save requirements = %d %v", code, saved)
	}
	lint, _ := saved["lint"].(map[string]any)
	if _, ok := lint["R2#0"]; !ok {
		t.Fatalf("EARS warning missing for R2: %v", lint)
	}
	v = specVersion(t, saved)

	step := func(method, path, body string) map[string]any {
		t.Helper()
		code, out := specDo(t, srv, method, path, body)
		if code != http.StatusOK {
			t.Fatalf("%s %s = %d %v", method, path, code, out)
		}
		v = specVersion(t, out)
		return out
	}
	vs := func() string { return strconv.Itoa(v) }
	step(http.MethodPost, "/v1/specs/export/phases/requirements", `{"status":"approved","expectedVersion":`+vs()+`}`)
	step(http.MethodPut, "/v1/specs/export/design", `{"design":"# Design","expectedVersion":`+vs()+`}`)
	step(http.MethodPost, "/v1/specs/export/phases/design", `{"status":"approved","expectedVersion":`+vs()+`}`)
	tasks := step(http.MethodPut, "/v1/specs/export/tasks", `{"tasks":[{"title":"csv writer","requirements":["R1"]}],"expectedVersion":`+vs()+`}`)
	cov, _ := tasks["coverage"].(map[string]any)
	if cov["covered"].(float64) != 1 || cov["total"].(float64) != 2 {
		t.Fatalf("coverage = %v", cov)
	}
	step(http.MethodPost, "/v1/specs/export/phases/tasks", `{"status":"approved","expectedVersion":`+vs()+`}`)

	// Hand-off with an uncovered requirement is refused and creates no plan.
	code, out := specDo(t, srv, http.MethodPost, "/v1/specs/export/handoff", `{"ownerRoot":"root-a","expectedVersion":`+vs()+`}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(out["error"].(string), "R2") {
		t.Fatalf("uncovered hand-off = %d %v", code, out)
	}
	if list, _ := ps.List(); len(list) != 0 {
		t.Fatalf("refused hand-off created a plan: %v", list)
	}

	step(http.MethodPut, "/v1/specs/export/tasks", `{"tasks":[{"title":"csv writer","requirements":["R1"]},{"title":"json writer","requirements":["R2"]}],"expectedVersion":`+vs()+`}`)
	step(http.MethodPost, "/v1/specs/export/phases/tasks", `{"status":"approved","expectedVersion":`+vs()+`}`)
	done := step(http.MethodPost, "/v1/specs/export/handoff", `{"ownerRoot":"root-a","expectedVersion":`+vs()+`}`)
	planID, _ := done["planId"].(string)
	if planID == "" {
		t.Fatalf("hand-off did not record a plan: %v", done)
	}
	p, ok, err := ps.Get(planID)
	if err != nil || !ok {
		t.Fatalf("plan %s not stored: %v", planID, err)
	}
	if p.Title != "Data export" || len(p.Sections) != 2 || !strings.Contains(p.Sections[1].Body, "R2: export json") {
		t.Fatalf("plan = %+v", p)
	}
	// Read-only after hand-off.
	if code, _ := specDo(t, srv, http.MethodPut, "/v1/specs/export/design", `{"design":"x","expectedVersion":`+vs()+`}`); code != http.StatusConflict {
		t.Fatalf("edit after hand-off = %d", code)
	}
	if code, _ := specDo(t, srv, http.MethodGet, "/v1/specs/missing", ""); code != http.StatusNotFound {
		t.Fatalf("missing spec = %d", code)
	}
	code, list := specDo(t, srv, http.MethodGet, "/v1/specs", "")
	if code != http.StatusOK || len(list["specs"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, list)
	}
}

// TestSpecStoreSharedAcrossRequests pins that every request for one work
// dir gets the same Store, so its lock serialises CAS and hand-off.
func TestSpecStoreSharedAcrossRequests(t *testing.T) {
	srv, _, _ := specServer(t)
	a, rootA := srv.specStore()
	b, rootB := srv.specStore()
	if a == nil || b == nil {
		t.Fatal("specStore returned nil")
	}
	if a != b || rootA != rootB {
		t.Fatalf("specStore returned distinct stores %p %p for %q / %q", a, b, rootA, rootB)
	}
	other, _, _ := specServer(t)
	c, _ := other.specStore()
	if c == a {
		t.Fatal("different work dirs must not share a store")
	}
}

func TestSpecPlannerFindMatchesOwnerTitleAndTime(t *testing.T) {
	ps, err := plan.Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	plans := local.NewPlans(ps)
	since := time.Now().Add(-time.Minute)
	mine := specPlanner{plans: plans, owner: "root-a"}
	want, err := mine.Create("Data export", []spec.PlanSection{{Title: "T1", Body: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (specPlanner{plans: plans, owner: "root-b"}).Create("Data export", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := mine.Create("Other", nil); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		title string
		since time.Time
		want  string
	}{
		{"own plan after intent", "Data export", since, want},
		{"plan older than intent", "Data export", time.Now().Add(time.Hour), ""},
		{"no plan with title", "Missing", since, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mine.Find(tc.title, tc.since)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Find = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSpecDraftRequirementsFillsUnsavedDraft(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("sess-draft", "/work/repo", nil, ops)
	defer live.Close()
	model := &sharpenModel{}
	srv, err := New(Options{SessionDir: t.TempDir(), WorkDir: t.TempDir(), Live: live, SideChat: model})
	if err != nil {
		t.Fatal(err)
	}
	code, created := specDo(t, srv, http.MethodPost, "/v1/specs", `{"name":"export","title":"Data export","description":"Users download their data."}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, created)
	}
	v := specVersion(t, created)

	tests := []struct {
		name      string
		body      string
		modelText string
		wantCode  int
		wantRows  int
		wantInQ   string
	}{
		{
			name:      "explicit description",
			body:      `{"description":"Export CSV and JSON."}`,
			modelText: "```json\n[{\"story\":\"As a user I export CSV\",\"criteria\":[\"WHEN I click export THE SYSTEM SHALL download a csv\"]},{\"story\":\"As a user I export JSON\",\"criteria\":[]}]\n```",
			wantCode:  http.StatusOK,
			wantRows:  2,
			wantInQ:   "Export CSV and JSON.",
		},
		{
			name:      "falls back to the spec description",
			body:      `{}`,
			modelText: `[{"story":"s","criteria":["THE SYSTEM SHALL x"]}]`,
			wantCode:  http.StatusOK,
			wantRows:  1,
			wantInQ:   "Users download their data.",
		},
		{
			name:      "unparseable model output",
			body:      `{"description":"x"}`,
			modelText: "Sorry, I can't.",
			wantCode:  http.StatusBadGateway,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model.out = SideChatCompletion{Text: tc.modelText}
			code, out := specDo(t, srv, http.MethodPost, "/v1/specs/export/requirements/draft", tc.body)
			if code != tc.wantCode {
				t.Fatalf("draft = %d %v", code, out)
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			rows, _ := out["requirements"].([]any)
			if len(rows) != tc.wantRows {
				t.Fatalf("rows = %v", out)
			}
			if !strings.Contains(model.got.Question, tc.wantInQ) || !strings.Contains(model.got.Question, "Data export") {
				t.Fatalf("question = %q", model.got.Question)
			}
			if !strings.Contains(model.got.System, "EARS") {
				t.Fatalf("system prompt lacks EARS guidance")
			}
		})
	}
	// Drafting never saves or approves: the spec is untouched.
	code, got := specDo(t, srv, http.MethodGet, "/v1/specs/export", "")
	if code != http.StatusOK || specVersion(t, got) != v || len(got["requirements"].([]any)) != 0 {
		t.Fatalf("draft mutated the spec: %d %v", code, got)
	}
	if st, _ := got["status"].(map[string]any); st["requirements"] != "draft" {
		t.Fatalf("draft changed phase status: %v", st)
	}
}

func TestSpecDraftRequirementsNeedsDescription(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("sess-draft", "/work/repo", nil, ops)
	defer live.Close()
	model := &sharpenModel{}
	srv, err := New(Options{SessionDir: t.TempDir(), WorkDir: t.TempDir(), Live: live, SideChat: model})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := specDo(t, srv, http.MethodPost, "/v1/specs", `{"name":"bare"}`); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if code, _ := specDo(t, srv, http.MethodPost, "/v1/specs/bare/requirements/draft", `{}`); code != http.StatusBadRequest {
		t.Fatalf("no description = %d", code)
	}
	if code, _ := specDo(t, srv, http.MethodPost, "/v1/specs/missing/requirements/draft", `{"description":"x"}`); code != http.StatusNotFound {
		t.Fatalf("missing spec = %d", code)
	}
	if model.called {
		t.Fatal("model called without a usable description")
	}
}
