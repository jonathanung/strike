package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// --- fake host.Goals ---

type testGoals struct {
	mu     sync.Mutex
	seq    int
	items  map[string]host.Goal
	logs   map[string][]host.GoalIteration
	runErr error
}

func newTestGoals() *testGoals {
	return &testGoals{
		items: make(map[string]host.Goal),
		logs:  make(map[string][]host.GoalIteration),
	}
}

func (t *testGoals) Set(description string, criteria []string, opts host.GoalSetOptions) (host.Goal, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.TrimSpace(description) == "" {
		return host.Goal{}, errors.New("description required")
	}
	if len(criteria) == 0 {
		return host.Goal{}, errors.New("at least one criterion required")
	}
	t.seq++
	id := fmt.Sprintf("g%03d", t.seq)
	crit := make([]host.GoalCriterion, len(criteria))
	for i, c := range criteria {
		crit[i] = host.GoalCriterion{Description: c, Check: c}
	}
	maxIter := opts.MaxIterations
	if maxIter <= 0 {
		maxIter = 25
	}
	g := host.Goal{
		ID:            id,
		Description:   description,
		Criteria:      crit,
		Status:        "pending",
		MaxIterations: maxIter,
		MaxCostUSD:    opts.MaxCostUSD,
		AllowedTools:  append([]string(nil), opts.AllowedTools...),
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
	}
	t.items[id] = g
	return g, nil
}

func (t *testGoals) List() ([]host.Goal, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]host.Goal, 0, len(t.items))
	for _, g := range t.items {
		out = append(out, g)
	}
	return out, nil
}

func (t *testGoals) Get(id string) (host.Goal, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, ok := t.items[id]
	return g, ok, nil
}

func (t *testGoals) Run(ctx context.Context, id string) (host.Goal, error) {
	if err := ctx.Err(); err != nil {
		return host.Goal{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runErr != nil {
		return host.Goal{}, t.runErr
	}
	g, ok := t.items[id]
	if !ok {
		return host.Goal{}, errors.New("goal: not found")
	}
	if g.Status == "done" || g.Status == "failed" || g.Status == "aborted" {
		return host.Goal{}, errors.New("goal: invalid status transition: run requires pending or paused")
	}
	g.Status = "done"
	g.LastIteration = 1
	for i := range g.Criteria {
		g.Criteria[i].Satisfied = true
	}
	t.items[id] = g
	t.logs[id] = append(t.logs[id], host.GoalIteration{
		N: 1, Plan: "", StateHash: "abc", CostUSD: 0, Summary: "iter 1 [OK:cmd: true]",
	})
	return g, nil
}

func (t *testGoals) Pause(id string) (host.Goal, error) {
	return t.setStatus(id, "paused", func(g host.Goal) error {
		if g.Status != "active" {
			return errors.New("goal: invalid status transition: pause requires active")
		}
		return nil
	})
}

func (t *testGoals) Resume(id string) (host.Goal, error) {
	return t.setStatus(id, "active", func(g host.Goal) error {
		if g.Status != "pending" && g.Status != "paused" {
			return errors.New("goal: invalid status transition: resume/run requires pending or paused")
		}
		return nil
	})
}

func (t *testGoals) Abort(id string) (host.Goal, error) {
	return t.setStatus(id, "aborted", func(g host.Goal) error {
		if g.Status == "done" || g.Status == "failed" || g.Status == "aborted" {
			return errors.New("goal: cannot abort terminal goal")
		}
		return nil
	})
}

func (t *testGoals) setStatus(id, status string, check func(host.Goal) error) (host.Goal, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, ok := t.items[id]
	if !ok {
		return host.Goal{}, errors.New("goal: not found")
	}
	if err := check(g); err != nil {
		return host.Goal{}, err
	}
	g.Status = status
	if status == "aborted" {
		g.FailReason = "aborted by user"
	}
	t.items[id] = g
	return g, nil
}

func (t *testGoals) Log(id string, iter int) ([]host.GoalIteration, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.items[id]; !ok {
		return nil, errors.New("goal: not found")
	}
	recs := t.logs[id]
	if iter <= 0 {
		out := make([]host.GoalIteration, len(recs))
		copy(out, recs)
		return out, nil
	}
	var out []host.GoalIteration
	for _, r := range recs {
		if r.N == iter {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestGoalsCapabilityUnavailable(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/goals"},
		{http.MethodPost, "/v1/goals"},
		{http.MethodGet, "/v1/goals/x"},
		{http.MethodPost, "/v1/goals/x/run"},
		{http.MethodPost, "/v1/goals/x/pause"},
		{http.MethodPost, "/v1/goals/x/resume"},
		{http.MethodPost, "/v1/goals/x/abort"},
		{http.MethodGet, "/v1/goals/x/log"},
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

func TestGoalsLifecycleHappyPath(t *testing.T) {
	fake := newTestGoals()
	ops := make(chan protocol.Op, 4)
	live := NewLive("live-1", t.TempDir(), nil, ops)
	defer live.Close()

	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Goals: fake},
	})
	if err != nil {
		t.Fatal(err)
	}

	// bootstrap capability
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d", boot.Code)
	}
	if !strings.Contains(boot.Body.String(), `"goals":true`) {
		t.Fatalf("bootstrap missing goals: %s", boot.Body.String())
	}

	// set
	set := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/goals", strings.NewReader(`{
		"description":"pass check",
		"criteria":["cmd: true"],
		"maxIterations":3
	}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(set, req)
	if set.Code != http.StatusCreated {
		t.Fatalf("set = %d %s", set.Code, set.Body.String())
	}
	var created goalDTO
	if err := json.Unmarshal(set.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != "pending" || created.Description != "pass check" {
		t.Fatalf("created=%+v", created)
	}
	if len(created.Criteria) != 1 || created.Criteria[0].Check != "cmd: true" {
		t.Fatalf("criteria=%+v", created.Criteria)
	}
	// camelCase wire shape (no TUI leakage)
	if !strings.Contains(set.Body.String(), `"maxIterations":3`) {
		t.Errorf("expected camelCase maxIterations: %s", set.Body.String())
	}

	// list
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/goals", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), created.ID) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}

	// get
	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/goals/"+created.ID, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"status":"pending"`) {
		t.Fatalf("get = %d %s", get.Code, get.Body.String())
	}

	// resume (pending → active) then pause then resume
	for _, action := range []string{"resume", "pause", "resume"} {
		res := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/goals/"+created.ID+"/"+action, nil)
		srv.Handler().ServeHTTP(res, r)
		if res.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", action, res.Code, res.Body.String())
		}
	}

	// run → done
	run := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost, "/v1/goals/"+created.ID+"/run", nil)
	srv.Handler().ServeHTTP(run, runReq)
	if run.Code != http.StatusOK || !strings.Contains(run.Body.String(), `"status":"done"`) {
		t.Fatalf("run = %d %s", run.Code, run.Body.String())
	}

	// log
	logRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(logRes, httptest.NewRequest(http.MethodGet, "/v1/goals/"+created.ID+"/log", nil))
	if logRes.Code != http.StatusOK || !strings.Contains(logRes.Body.String(), `"n":1`) {
		t.Fatalf("log = %d %s", logRes.Code, logRes.Body.String())
	}
	if !strings.Contains(logRes.Body.String(), "iter 1") {
		t.Errorf("log missing summary: %s", logRes.Body.String())
	}

	// abort on terminal → conflict
	abort := httptest.NewRecorder()
	srv.Handler().ServeHTTP(abort, httptest.NewRequest(http.MethodPost, "/v1/goals/"+created.ID+"/abort", nil))
	if abort.Code != http.StatusConflict {
		t.Fatalf("abort terminal = %d %s", abort.Code, abort.Body.String())
	}
}

func TestGoalsControlsRequireLive(t *testing.T) {
	fake := newTestGoals()
	g, err := fake.Set("x", []string{"cmd: true"}, host.GoalSetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Goals: fake},
		// no Live → attach-only
	})
	if err != nil {
		t.Fatal(err)
	}

	// list still works without live
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/goals", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list without live = %d", list.Code)
	}

	// set still works without live (store-only)
	set := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/goals", strings.NewReader(`{"description":"y","criteria":["cmd: true"]}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(set, req)
	if set.Code != http.StatusCreated {
		t.Fatalf("set without live = %d %s", set.Code, set.Body.String())
	}

	for _, action := range []string{"run", "pause", "resume", "abort"} {
		res := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/goals/"+g.ID+"/"+action, nil)
		srv.Handler().ServeHTTP(res, r)
		if res.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without live = %d %q", action, res.Code, res.Body.String())
		}
	}
}

func TestGoalsSetValidation(t *testing.T) {
	fake := newTestGoals()
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Goals: fake},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		body string
		code int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"description":"x"}`, http.StatusBadRequest},
		{`{"description":"","criteria":["cmd: true"]}`, http.StatusBadRequest},
		{`{"description":"ok","criteria":["cmd: true"],"extra":1}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/goals", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(res, req)
		if res.Code != tc.code {
			t.Errorf("body=%s → %d want %d (%s)", tc.body, res.Code, tc.code, res.Body.String())
		}
	}
}

func TestGoalsNotFound(t *testing.T) {
	fake := newTestGoals()
	ops := make(chan protocol.Op, 1)
	live := NewLive("live", t.TempDir(), nil, ops)
	defer live.Close()
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Goals: fake},
	})
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/goals/missing", nil))
	if get.Code != http.StatusNotFound {
		t.Fatalf("get missing = %d", get.Code)
	}
	run := httptest.NewRecorder()
	srv.Handler().ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/v1/goals/missing/run", nil))
	if run.Code != http.StatusNotFound {
		t.Fatalf("run missing = %d %s", run.Code, run.Body.String())
	}
}
