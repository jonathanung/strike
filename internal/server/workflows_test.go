package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// --- test doubles ---

type testWorkflows struct {
	items   []host.WorkflowSummary
	docs    map[string]host.WorkflowDocument
	saved   []workflowSaveCall
	saveErr error
}

type workflowSaveCall struct {
	Doc   host.WorkflowDocument
	Scope string
	Force bool
}

func (t *testWorkflows) List() []host.WorkflowSummary {
	if len(t.items) == 0 {
		return nil
	}
	out := make([]host.WorkflowSummary, len(t.items))
	copy(out, t.items)
	return out
}

func (t *testWorkflows) Get(name string) (host.WorkflowSummary, bool) {
	for _, w := range t.items {
		if w.Name == name {
			return w, true
		}
	}
	return host.WorkflowSummary{}, false
}

func (t *testWorkflows) Document(name string) (host.WorkflowDocument, bool) {
	if t.docs == nil {
		return host.WorkflowDocument{}, false
	}
	doc, ok := t.docs[name]
	return doc, ok
}

func (t *testWorkflows) Scaffold(name string) (host.WorkflowDocument, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return host.WorkflowDocument{}, errors.New("name required")
	}
	return host.WorkflowDocument{
		SchemaVersion: 1,
		Name:          name,
		Description:   "TODO",
		Phases: []host.WorkflowPhaseDocument{
			{Name: "phase-1", Gate: "agent"},
		},
	}, nil
}

func (t *testWorkflows) Validate(doc host.WorkflowDocument) error {
	if strings.TrimSpace(doc.Name) == "" {
		return errors.New("name required")
	}
	if len(doc.Phases) == 0 {
		return errors.New("at least one phase required")
	}
	return nil
}

func (t *testWorkflows) Format(doc host.WorkflowDocument) (string, error) {
	if err := t.Validate(doc); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (t *testWorkflows) PhaseGrants(doc host.WorkflowDocument, phaseIndex int) []host.WorkflowPermission {
	if phaseIndex < 0 || phaseIndex >= len(doc.Phases) {
		return nil
	}
	return append([]host.WorkflowPermission(nil), doc.Phases[phaseIndex].Permissions...)
}

func (t *testWorkflows) Save(doc host.WorkflowDocument, scope string, force bool) (string, error) {
	if t.saveErr != nil {
		return "", t.saveErr
	}
	if err := t.Validate(doc); err != nil {
		return "", errors.Join(host.ErrWorkflowInvalid, err)
	}
	t.saved = append(t.saved, workflowSaveCall{Doc: doc, Scope: scope, Force: force})
	// upsert catalog
	sum := host.WorkflowSummary{
		Name:   doc.Name,
		Source: scope,
		Valid:  true,
		Phases: make([]host.WorkflowPhaseSummary, 0, len(doc.Phases)),
	}
	for _, p := range doc.Phases {
		sum.Phases = append(sum.Phases, host.WorkflowPhaseSummary{
			Name: p.Name, Agent: p.Agent, Gate: p.Gate, GateCommand: p.GateCommand,
			Permissions: p.Permissions,
		})
	}
	found := false
	for i, item := range t.items {
		if item.Name == doc.Name {
			t.items[i] = sum
			found = true
			break
		}
	}
	if !found {
		t.items = append(t.items, sum)
	}
	if t.docs == nil {
		t.docs = map[string]host.WorkflowDocument{}
	}
	t.docs[doc.Name] = doc
	return "/tmp/" + doc.Name + ".json", nil
}

type testWorkflowDrafts struct {
	lastReview string
	lastSave   string
	saveErr    error
}

func (t *testWorkflowDrafts) Review(jsonDocument string) host.WorkflowDraftReview {
	t.lastReview = jsonDocument
	var doc host.WorkflowDocument
	_ = json.Unmarshal([]byte(jsonDocument), &doc)
	valid := doc.Name != "" && len(doc.Phases) > 0
	rev := host.WorkflowDraftReview{
		Name:          doc.Name,
		Description:   doc.Description,
		SourceLabel:   "draft",
		Valid:         valid,
		HasWidening:   true,
		CanonicalJSON: jsonDocument,
		Phases: []host.WorkflowPhaseDraftReview{
			{
				Name:             "p0",
				Gate:             "check",
				GateCommand:      "make test",
				CheckHighlighted: true,
				Widening: []host.WorkflowPermission{
					{Permission: "bash", Pattern: "*", Action: "allow"},
				},
			},
		},
	}
	if !valid {
		rev.ValidationError = "invalid draft"
	}
	return rev
}

func (t *testWorkflowDrafts) Save(jsonDocument, scope string, confirm, force bool) (host.WorkflowDraftSave, error) {
	t.lastSave = jsonDocument
	if t.saveErr != nil {
		return host.WorkflowDraftSave{}, t.saveErr
	}
	if !confirm {
		return host.WorkflowDraftSave{}, errors.New("workflow save requires explicit confirmation")
	}
	rev := t.Review(jsonDocument)
	if !rev.Valid {
		return host.WorkflowDraftSave{}, host.ErrWorkflowInvalid
	}
	_ = scope
	_ = force
	return host.WorkflowDraftSave{Path: "/tmp/draft-" + rev.Name + ".json", Activated: false}, nil
}

func sampleCatalog() *testWorkflows {
	return &testWorkflows{
		items: []host.WorkflowSummary{
			{
				Name:   "plan-implement",
				Source: host.WorkflowSourceBuiltin,
				Valid:  true,
				Phases: []host.WorkflowPhaseSummary{
					{
						Name: "plan", Gate: "user",
						Permissions: []host.WorkflowPermission{
							{Permission: "write", Pattern: "*", Action: "deny"},
						},
					},
					{Name: "implement", Gate: "agent"},
				},
			},
			{
				Name:            "broken",
				Source:          host.WorkflowSourceProject,
				Valid:           false,
				ValidationError: "no phases",
			},
		},
		docs: map[string]host.WorkflowDocument{
			"plan-implement": {
				SchemaVersion: 1,
				Name:          "plan-implement",
				Phases: []host.WorkflowPhaseDocument{
					{
						Name: "plan", Gate: "user", Context: "plan it",
						Permissions: []host.WorkflowPermission{
							{Permission: "write", Pattern: "*", Action: "deny"},
						},
					},
					{Name: "implement", Gate: "agent"},
				},
			},
		},
	}
}

func TestWorkflowAPIsUnavailableWithoutHost(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/workflows"},
		{http.MethodGet, "/v1/workflows/x"},
		{http.MethodGet, "/v1/workflows/x/document"},
		{http.MethodPost, "/v1/workflows/scaffold"},
		{http.MethodPost, "/v1/workflows/validate"},
		{http.MethodPost, "/v1/workflows/format"},
		{http.MethodPost, "/v1/workflows/phase-grants"},
		{http.MethodPost, "/v1/workflows/save"},
		{http.MethodPost, "/v1/workflows/stop"},
		{http.MethodPost, "/v1/workflows/x/start"},
		{http.MethodPost, "/v1/workflow-drafts/review"},
		{http.MethodPost, "/v1/workflow-drafts/save"},
	}
	for _, p := range paths {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(p.method, p.path, strings.NewReader(`{}`))
		if p.method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		srv.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "capability unavailable") {
			t.Errorf("%s %s = %d %q", p.method, p.path, res.Code, res.Body.String())
		}
	}
}

func TestWorkflowCatalogAndAuthoringAPIs(t *testing.T) {
	cat := sampleCatalog()
	drafts := &testWorkflowDrafts{}
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Workflows: cat, WorkflowDrafts: drafts},
	})
	if err != nil {
		t.Fatal(err)
	}

	// bootstrap capabilities
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d", boot.Code)
	}
	body := boot.Body.String()
	for _, want := range []string{`"workflows":true`, `"workflowDrafts":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("bootstrap missing %s: %s", want, body)
		}
	}

	// list
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/workflows", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "plan-implement") {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `"valid":false`) {
		t.Errorf("list should include invalid entry: %s", list.Body.String())
	}

	// get + document
	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/workflows/plan-implement", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"name":"plan-implement"`) {
		t.Fatalf("get = %d %s", get.Code, get.Body.String())
	}
	docRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(docRes, httptest.NewRequest(http.MethodGet, "/v1/workflows/plan-implement/document", nil))
	if docRes.Code != http.StatusOK || !strings.Contains(docRes.Body.String(), `"context":"plan it"`) {
		t.Fatalf("document = %d %s", docRes.Code, docRes.Body.String())
	}

	// scaffold
	sc := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/workflows/scaffold", strings.NewReader(`{"name":"demo"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(sc, req)
	if sc.Code != http.StatusOK || !strings.Contains(sc.Body.String(), `"name":"demo"`) {
		t.Fatalf("scaffold = %d %s", sc.Code, sc.Body.String())
	}

	// validate ok / fail
	valOK := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/validate", strings.NewReader(`{"document":{"name":"x","phases":[{"name":"p","gate":"agent"}]}}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(valOK, req)
	if valOK.Code != http.StatusOK || !strings.Contains(valOK.Body.String(), `"ok":true`) {
		t.Fatalf("validate ok = %d %s", valOK.Code, valOK.Body.String())
	}
	valBad := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/validate", strings.NewReader(`{"document":{"name":"","phases":[]}}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(valBad, req)
	if valBad.Code != http.StatusOK || !strings.Contains(valBad.Body.String(), `"ok":false`) {
		t.Fatalf("validate bad = %d %s", valBad.Code, valBad.Body.String())
	}

	// format + phase grants
	fmtRes := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/format", strings.NewReader(`{"document":{"name":"x","phases":[{"name":"p","gate":"agent"}]}}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(fmtRes, req)
	if fmtRes.Code != http.StatusOK || !strings.Contains(fmtRes.Body.String(), `"json"`) {
		t.Fatalf("format = %d %s", fmtRes.Code, fmtRes.Body.String())
	}
	grants := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/phase-grants", strings.NewReader(`{"phaseIndex":0,"document":{"name":"x","phases":[{"name":"p","permissions":[{"permission":"bash","pattern":"*","action":"allow"}]}]}}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(grants, req)
	if grants.Code != http.StatusOK || !strings.Contains(grants.Body.String(), `"bash"`) {
		t.Fatalf("grants = %d %s", grants.Code, grants.Body.String())
	}

	// save refuses invalid; accepts valid without activating
	saveBad := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/save", strings.NewReader(`{"scope":"project","document":{"name":"","phases":[]}}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(saveBad, req)
	if saveBad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("save invalid = %d %s", saveBad.Code, saveBad.Body.String())
	}
	saveOK := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/save", strings.NewReader(`{"scope":"project","force":false,"document":{"name":"web-flow","phases":[{"name":"one","gate":"agent","permissions":[{"permission":"bash","pattern":"*","action":"allow"}]}]}}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(saveOK, req)
	if saveOK.Code != http.StatusOK {
		t.Fatalf("save = %d %s", saveOK.Code, saveOK.Body.String())
	}
	if !strings.Contains(saveOK.Body.String(), `"activated":false`) {
		t.Errorf("save must report activated=false: %s", saveOK.Body.String())
	}
	if len(cat.saved) != 1 || cat.saved[0].Doc.Name != "web-flow" {
		t.Fatalf("saved calls = %+v", cat.saved)
	}

	// draft review surfaces checks + widening
	rev := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflow-drafts/review", strings.NewReader(`{"json":"{\"name\":\"d\",\"phases\":[{\"name\":\"p\"}]}"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rev, req)
	if rev.Code != http.StatusOK {
		t.Fatalf("review = %d %s", rev.Code, rev.Body.String())
	}
	for _, want := range []string{`"hasWidening":true`, `"checkHighlighted":true`, `"make test"`, `"bash"`} {
		if !strings.Contains(rev.Body.String(), want) {
			t.Errorf("review missing %s: %s", want, rev.Body.String())
		}
	}

	// draft save requires confirm
	ds := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflow-drafts/save", strings.NewReader(`{"scope":"project","confirm":false,"json":"{\"name\":\"d\",\"phases\":[{\"name\":\"p\"}]}"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(ds, req)
	if ds.Code != http.StatusBadRequest {
		t.Fatalf("draft save no confirm = %d %s", ds.Code, ds.Body.String())
	}
}

func TestWorkflowStartRequiresConfirmAndRejectsInvalid(t *testing.T) {
	cat := sampleCatalog()
	ops := make(chan protocol.Op, 4)
	live := NewLive("live", t.TempDir(), nil, ops)
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Workflows: cat},
	})
	if err != nil {
		t.Fatal(err)
	}

	// no confirm
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/workflows/plan-implement/start", strings.NewReader(`{"confirm":false}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "confirm") {
		t.Fatalf("no confirm = %d %s", res.Code, res.Body.String())
	}
	select {
	case op := <-ops:
		t.Fatalf("unexpected op without confirm: %#v", op)
	default:
	}

	// invalid cannot activate
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/broken/start", strings.NewReader(`{"confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid start = %d %s", res.Code, res.Body.String())
	}

	// valid + confirm → StartWorkflow op
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/plan-implement/start", strings.NewReader(`{"confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("start = %d %s", res.Code, res.Body.String())
	}
	select {
	case op := <-ops:
		sw, ok := op.(protocol.StartWorkflow)
		if !ok || sw.Name != "plan-implement" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("expected StartWorkflow")
	}

	// stop
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/workflows/stop", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("stop = %d %s", res.Code, res.Body.String())
	}
	select {
	case op := <-ops:
		if _, ok := op.(protocol.StopWorkflow); !ok {
			t.Fatalf("op = %#v, want StopWorkflow", op)
		}
	case <-time.After(time.Second):
		t.Fatal("expected StopWorkflow")
	}
}

func TestBootstrapListsWorkflowProtocolOpsWhenLive(t *testing.T) {
	ops := make(chan protocol.Op)
	live := NewLive("live", t.TempDir(), nil, ops)
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	body := res.Body.String()
	for _, want := range []string{`"workflow.start"`, `"workflow.stop"`} {
		if !strings.Contains(body, want) {
			t.Errorf("bootstrap missing %s: %s", want, body)
		}
	}
}
