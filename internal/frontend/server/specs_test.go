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

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/persist/plan"
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

func version(t *testing.T, v map[string]any) int {
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
	v := version(t, created)

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
	v = version(t, saved)

	step := func(method, path, body string) map[string]any {
		t.Helper()
		code, out := specDo(t, srv, method, path, body)
		if code != http.StatusOK {
			t.Fatalf("%s %s = %d %v", method, path, code, out)
		}
		v = version(t, out)
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
