package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/persist/plan"
)

// fakePlans is an in-memory host.Plans for API tests.
type fakePlans struct {
	mu     sync.Mutex
	plans  map[string]host.Plan
	nextID int
}

func newFakePlans(seed ...host.Plan) *fakePlans {
	f := &fakePlans{plans: map[string]host.Plan{}, nextID: 1}
	for _, p := range seed {
		cp := p
		if cp.Sections == nil {
			cp.Sections = []host.PlanSection{}
		}
		f.plans[cp.ID] = cp
	}
	return f
}

func (f *fakePlans) List() ([]host.PlanMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]host.PlanMeta, 0, len(f.plans))
	for _, p := range f.plans {
		out = append(out, host.PlanMeta{
			ID: p.ID, OwnerRoot: p.OwnerRoot, Title: p.Title, Status: p.Status,
			Version: p.Version, SectionCount: len(p.Sections),
			CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		})
	}
	return out, nil
}

func (f *fakePlans) Get(id string) (host.Plan, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.plans[id]
	if !ok {
		return host.Plan{}, false, nil
	}
	return cloneHostPlan(p), true, nil
}

func (f *fakePlans) Create(ownerRoot, title string, sections []host.PlanSection) (host.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ownerRoot == "" {
		return host.Plan{}, plan.ErrNotFound // reuse sentinel only for missing; empty owner is validation
	}
	if strings.TrimSpace(title) == "" {
		return host.Plan{}, errPlanTitle
	}
	id := "plan-" + itoa(f.nextID)
	f.nextID++
	now := time.Now().UTC()
	secs := make([]host.PlanSection, len(sections))
	for i, s := range sections {
		secs[i] = host.PlanSection{ID: "s" + itoa(i+1), Title: s.Title, Body: s.Body}
	}
	p := host.Plan{
		ID: id, OwnerRoot: ownerRoot, Title: title, Status: plan.StatusDraft,
		Sections: secs, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	f.plans[id] = p
	return cloneHostPlan(p), nil
}

var errPlanTitle = errString("plan: title is required")

type errString string

func (e errString) Error() string { return string(e) }

func (f *fakePlans) check(id, owner string, ver int) (host.Plan, error) {
	p, ok := f.plans[id]
	if !ok {
		return host.Plan{}, plan.ErrNotFound
	}
	if p.OwnerRoot != owner {
		return host.Plan{}, plan.ErrNotOwner
	}
	if p.Version != ver {
		return host.Plan{}, plan.ErrConflict
	}
	if p.Status == plan.StatusClosed {
		return host.Plan{}, plan.ErrClosedPlan
	}
	return p, nil
}

func (f *fakePlans) UpdateTitle(id, ownerRoot, title string, expectedVersion int) (host.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(id, ownerRoot, expectedVersion)
	if err != nil {
		return host.Plan{}, err
	}
	p.Title = title
	p.Version++
	p.UpdatedAt = time.Now().UTC()
	f.plans[id] = p
	return cloneHostPlan(p), nil
}

func (f *fakePlans) UpdateSection(id, ownerRoot, sectionID string, title, body *string, expectedVersion int) (host.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(id, ownerRoot, expectedVersion)
	if err != nil {
		return host.Plan{}, err
	}
	found := false
	secs := make([]host.PlanSection, len(p.Sections))
	copy(secs, p.Sections)
	for i := range secs {
		if secs[i].ID == sectionID {
			if title != nil {
				secs[i].Title = *title
			}
			if body != nil {
				secs[i].Body = *body
			}
			found = true
			break
		}
	}
	if !found {
		return host.Plan{}, plan.ErrNotFound
	}
	p.Sections = secs
	p.Version++
	p.UpdatedAt = time.Now().UTC()
	f.plans[id] = p
	return cloneHostPlan(p), nil
}

func (f *fakePlans) AddSection(id, ownerRoot, title, body string, expectedVersion int) (host.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(id, ownerRoot, expectedVersion)
	if err != nil {
		return host.Plan{}, err
	}
	sec := host.PlanSection{ID: "s" + itoa(len(p.Sections)+1), Title: title, Body: body}
	p.Sections = append(append([]host.PlanSection{}, p.Sections...), sec)
	p.Version++
	p.UpdatedAt = time.Now().UTC()
	f.plans[id] = p
	return cloneHostPlan(p), nil
}

func (f *fakePlans) SetStatus(id, ownerRoot, status string, expectedVersion int) (host.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.plans[id]
	if !ok {
		return host.Plan{}, plan.ErrNotFound
	}
	if p.OwnerRoot != ownerRoot {
		return host.Plan{}, plan.ErrNotOwner
	}
	if p.Version != expectedVersion {
		return host.Plan{}, plan.ErrConflict
	}
	if !plan.ValidStatus(status) {
		return host.Plan{}, plan.ErrInvalidStatus
	}
	if p.Status == plan.StatusClosed {
		return host.Plan{}, plan.ErrInvalidStatus
	}
	p.Status = status
	p.Version++
	p.UpdatedAt = time.Now().UTC()
	f.plans[id] = p
	return cloneHostPlan(p), nil
}

func (f *fakePlans) Reopen(id, ownerRoot string, expectedVersion int) (host.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.plans[id]
	if !ok {
		return host.Plan{}, plan.ErrNotFound
	}
	if p.OwnerRoot != ownerRoot {
		return host.Plan{}, plan.ErrNotOwner
	}
	if p.Version != expectedVersion {
		return host.Plan{}, plan.ErrConflict
	}
	if p.Status != plan.StatusClosed {
		return host.Plan{}, plan.ErrInvalidStatus
	}
	p.Status = plan.StatusDraft
	p.Version++
	p.UpdatedAt = time.Now().UTC()
	f.plans[id] = p
	return cloneHostPlan(p), nil
}

func cloneHostPlan(p host.Plan) host.Plan {
	out := p
	if p.Sections != nil {
		out.Sections = append([]host.PlanSection{}, p.Sections...)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func plansSrv(t *testing.T, plans host.Plans) *Server {
	t.Helper()
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Plans: plans}})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestPlansCapabilityAbsent(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Bootstrap: plans false when Services.Plans nil
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d", boot.Code)
	}
	if !strings.Contains(boot.Body.String(), `"plans":false`) {
		t.Fatalf("bootstrap plans capability want false: %s", boot.Body.String())
	}
	for _, path := range []string{"/v1/plans", "/v1/plans/x"} {
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "plans capability unavailable") {
			t.Errorf("GET %s = %d %s", path, res.Code, res.Body.String())
		}
	}
}

func TestPlansCapabilityPresent(t *testing.T) {
	srv := plansSrv(t, newFakePlans())
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"plans":true`) {
		t.Fatalf("bootstrap plans capability want true: %s", boot.Body.String())
	}
}

func TestPlansListCreateGet(t *testing.T) {
	fp := newFakePlans()
	srv := plansSrv(t, fp)

	// empty list
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/plans", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"plans":[]`) {
		t.Fatalf("empty list = %d %s", list.Code, list.Body.String())
	}

	// create
	create := httptest.NewRecorder()
	body := `{"ownerRoot":"root-a","title":"Ship web plans","sections":[{"title":"API","body":"REST"}]}`
	srv.Handler().ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/plans", strings.NewReader(body)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.Code, create.Body.String())
	}
	var created host.Plan
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Title != "Ship web plans" || created.OwnerRoot != "root-a" || created.Status != "draft" || created.Version != 1 {
		t.Fatalf("created = %#v", created)
	}
	if len(created.Sections) != 1 || created.Sections[0].Title != "API" {
		t.Fatalf("sections = %#v", created.Sections)
	}

	// list
	list2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list2, httptest.NewRequest(http.MethodGet, "/v1/plans", nil))
	if list2.Code != http.StatusOK || !strings.Contains(list2.Body.String(), created.ID) {
		t.Fatalf("list = %d %s", list2.Code, list2.Body.String())
	}

	// get
	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/plans/"+created.ID, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "REST") {
		t.Fatalf("get = %d %s", get.Code, get.Body.String())
	}

	// missing
	miss := httptest.NewRecorder()
	srv.Handler().ServeHTTP(miss, httptest.NewRequest(http.MethodGet, "/v1/plans/nope", nil))
	if miss.Code != http.StatusNotFound {
		t.Fatalf("missing = %d %s", miss.Code, miss.Body.String())
	}
}

func TestPlansOwnerAndCASEnforced(t *testing.T) {
	seed := host.Plan{
		ID: "p1", OwnerRoot: "root-a", Title: "Owned", Status: plan.StatusDraft,
		Version: 2, Sections: []host.PlanSection{{ID: "s1", Title: "One", Body: "body"}},
	}
	srv := plansSrv(t, newFakePlans(seed))

	// non-owner cannot update title
	deny := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deny, httptest.NewRequest(http.MethodPatch, "/v1/plans/p1",
		strings.NewReader(`{"ownerRoot":"root-b","title":"Hijack","expectedVersion":2}`)))
	if deny.Code != http.StatusForbidden || !strings.Contains(deny.Body.String(), "owning root") {
		t.Fatalf("non-owner = %d %s", deny.Code, deny.Body.String())
	}

	// stale version
	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, httptest.NewRequest(http.MethodPatch, "/v1/plans/p1",
		strings.NewReader(`{"ownerRoot":"root-a","title":"Stale","expectedVersion":1}`)))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale = %d %s", stale.Code, stale.Body.String())
	}

	// owner CAS success
	ok := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ok, httptest.NewRequest(http.MethodPatch, "/v1/plans/p1",
		strings.NewReader(`{"ownerRoot":"root-a","title":"Renamed","expectedVersion":2}`)))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), "Renamed") {
		t.Fatalf("owner update = %d %s", ok.Code, ok.Body.String())
	}
	var updated host.Plan
	if err := json.Unmarshal(ok.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Version != 3 {
		t.Fatalf("version = %d, want 3", updated.Version)
	}

	// section update
	sec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(sec, httptest.NewRequest(http.MethodPatch, "/v1/plans/p1/sections/s1",
		strings.NewReader(`{"ownerRoot":"root-a","body":"new body","expectedVersion":3}`)))
	if sec.Code != http.StatusOK || !strings.Contains(sec.Body.String(), "new body") {
		t.Fatalf("section = %d %s", sec.Code, sec.Body.String())
	}

	// non-owner section
	secDeny := httptest.NewRecorder()
	srv.Handler().ServeHTTP(secDeny, httptest.NewRequest(http.MethodPatch, "/v1/plans/p1/sections/s1",
		strings.NewReader(`{"ownerRoot":"root-b","body":"nope","expectedVersion":4}`)))
	if secDeny.Code != http.StatusForbidden {
		t.Fatalf("section non-owner = %d %s", secDeny.Code, secDeny.Body.String())
	}

	// add section
	add := httptest.NewRecorder()
	srv.Handler().ServeHTTP(add, httptest.NewRequest(http.MethodPost, "/v1/plans/p1/sections",
		strings.NewReader(`{"ownerRoot":"root-a","title":"Two","body":"b2","expectedVersion":4}`)))
	if add.Code != http.StatusOK || !strings.Contains(add.Body.String(), "Two") {
		t.Fatalf("add section = %d %s", add.Code, add.Body.String())
	}

	// approve
	approve := httptest.NewRecorder()
	srv.Handler().ServeHTTP(approve, httptest.NewRequest(http.MethodPost, "/v1/plans/p1/status",
		strings.NewReader(`{"ownerRoot":"root-a","status":"approved","expectedVersion":5}`)))
	if approve.Code != http.StatusOK || !strings.Contains(approve.Body.String(), `"Status":"approved"`) {
		t.Fatalf("approve = %d %s", approve.Code, approve.Body.String())
	}

	// close
	closeRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(closeRes, httptest.NewRequest(http.MethodPost, "/v1/plans/p1/status",
		strings.NewReader(`{"ownerRoot":"root-a","status":"closed","expectedVersion":6}`)))
	if closeRes.Code != http.StatusOK || !strings.Contains(closeRes.Body.String(), `"Status":"closed"`) {
		t.Fatalf("close = %d %s", closeRes.Code, closeRes.Body.String())
	}

	// reopen
	reopen := httptest.NewRecorder()
	srv.Handler().ServeHTTP(reopen, httptest.NewRequest(http.MethodPost, "/v1/plans/p1/reopen",
		strings.NewReader(`{"ownerRoot":"root-a","expectedVersion":7}`)))
	if reopen.Code != http.StatusOK || !strings.Contains(reopen.Body.String(), `"Status":"draft"`) {
		t.Fatalf("reopen = %d %s", reopen.Code, reopen.Body.String())
	}

	// owner via ?root= query
	viaRoot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(viaRoot, httptest.NewRequest(http.MethodPatch, "/v1/plans/p1?root=root-a",
		strings.NewReader(`{"title":"Via root","expectedVersion":8}`)))
	if viaRoot.Code != http.StatusOK || !strings.Contains(viaRoot.Body.String(), "Via root") {
		t.Fatalf("root query = %d %s", viaRoot.Code, viaRoot.Body.String())
	}
}

func TestPlansCreateValidation(t *testing.T) {
	srv := plansSrv(t, newFakePlans())
	for name, body := range map[string]string{
		"no owner": `{"title":"T"}`,
		"no title": `{"ownerRoot":"r"}`,
	} {
		t.Run(name, func(t *testing.T) {
			res := httptest.NewRecorder()
			srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/plans", strings.NewReader(body)))
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d %s", res.Code, res.Body.String())
			}
		})
	}
}
