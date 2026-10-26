package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/persist/plan"
	"github.com/jonathanung/strike/internal/persist/taskrun"
)

func taskFixture(t *testing.T) (root string, ps *plan.Store, runs *taskrun.Store, created plan.Plan) {
	t.Helper()
	root = t.TempDir()
	var err error
	ps, err = plan.Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	runs, err = taskrun.Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	created, err = ps.Create("root-a", "Ship", []plan.SectionInput{
		{Title: "Write", Body: "the note"},
		{Title: "Publish", Body: "after the note"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, ps, runs, created
}

func taskServer(t *testing.T, ps *plan.Store, runs *taskrun.Store, spawn func(context.Context, string, string, string) (string, error)) *Server {
	t.Helper()
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Plans: local.NewPlans(ps)},
		TaskRuns:   runs,
		TaskSpawn:  spawn,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func postRun(srv *Server, id, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/plans/"+id+"/run", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func getRun(srv *Server, id string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/plans/"+id+"/run", nil)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeRun(t *testing.T, rec *httptest.ResponseRecorder) taskRunResponse {
	t.Helper()
	var out taskRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return out
}

func statusByID(run taskRunDTO) map[string]string {
	out := map[string]string{}
	for _, sec := range run.Sections {
		out[sec.ID] = sec.Status
	}
	return out
}

func TestTaskRunHTTPRestartBlocksDependants(t *testing.T) {
	root, ps, runs, created := taskFixture(t)
	var mu sync.Mutex
	var objectives []string
	spawn := func(_ context.Context, owner, name, objective string) (string, error) {
		if owner != "root-a" {
			t.Errorf("owner = %s", owner)
		}
		if name == "" || !strings.Contains(objective, "schema:handoff") {
			t.Errorf("spawn name=%q objective=%s", name, objective)
		}
		mu.Lock()
		objectives = append(objectives, objective)
		n := len(objectives)
		mu.Unlock()
		return "child-" + itoa(n), nil
	}
	srv := taskServer(t, ps, runs, spawn)

	opened := postRun(srv, created.ID, `{"action":"open","ownerRoot":"root-a"}`)
	if opened.Code != http.StatusOK {
		t.Fatalf("open = %d %s", opened.Code, opened.Body.String())
	}
	board := decodeRun(t, opened)
	if len(board.Run.Sections) != 2 || !board.Run.Sections[0].Ready || board.Run.Sections[1].Ready {
		t.Fatalf("open board = %#v", board.Run.Sections)
	}

	started := postRun(srv, created.ID, `{"action":"start","ownerRoot":"root-a","expectedVersion":`+itoa(board.Run.Version)+`}`)
	if started.Code != http.StatusOK {
		t.Fatalf("start = %d %s", started.Code, started.Body.String())
	}
	running := decodeRun(t, started)
	if len(running.Spawned) != 1 || running.Spawned[0].SectionID != "s1" {
		t.Fatalf("spawned = %#v", running.Spawned)
	}
	if statusByID(running.Run)["s1"] != taskrun.StatusRunning || statusByID(running.Run)["s2"] != taskrun.StatusPending {
		t.Fatalf("after start = %#v", statusByID(running.Run))
	}
	if _, err := runs.ApplyChild(created.ID, "s1", taskrun.Outcome{
		ChildID:       "child-1",
		ChildStatus:   "blocked",
		Incomplete:    true,
		HasVerify:     true,
		VerifyPassed:  false,
		VerifySummary: "schema:handoff failed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := runs.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := taskrun.Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	srv2 := taskServer(t, ps, reopened, spawn)
	got := getRun(srv2, created.ID)
	if got.Code != http.StatusOK {
		t.Fatalf("get = %d %s", got.Code, got.Body.String())
	}
	board = decodeRun(t, got)
	st := statusByID(board.Run)
	if st["s1"] != taskrun.StatusFailed || st["s2"] != taskrun.StatusBlocked {
		t.Fatalf("restart statuses = %#v", st)
	}
	if board.Run.Sections[0].Checkpoint == nil || board.Run.Sections[0].Checkpoint.VerifySummary != "schema:handoff failed" {
		t.Fatalf("checkpoint = %#v", board.Run.Sections[0].Checkpoint)
	}
	before := len(objectives)
	again := postRun(srv2, created.ID, `{"action":"start","ownerRoot":"root-a","expectedVersion":`+itoa(board.Run.Version)+`}`)
	if again.Code != http.StatusOK {
		t.Fatalf("start again = %d %s", again.Code, again.Body.String())
	}
	againBody := decodeRun(t, again)
	if len(againBody.Spawned) != 0 {
		t.Fatalf("spawned dependant = %#v", againBody.Spawned)
	}
	if len(objectives) != before {
		t.Fatalf("spawner calls %d -> %d", before, len(objectives))
	}
	if !strings.Contains(againBody.Note, "blocks") {
		t.Fatalf("note = %q", againBody.Note)
	}
	if statusByID(againBody.Run)["s2"] != taskrun.StatusBlocked {
		t.Fatalf("still blocked = %#v", statusByID(againBody.Run))
	}
}

func TestTaskRunApproveThenStartNext(t *testing.T) {
	_, ps, runs, created := taskFixture(t)
	var got []string
	srv := taskServer(t, ps, runs, func(_ context.Context, _, _, objective string) (string, error) {
		got = append(got, objective)
		return "child-" + itoa(len(got)), nil
	})
	opened := decodeRun(t, postRun(srv, created.ID, `{"action":"open","ownerRoot":"root-a"}`))
	started := postRun(srv, created.ID, `{"action":"start","ownerRoot":"root-a","expectedVersion":`+itoa(opened.Run.Version)+`}`)
	if started.Code != http.StatusOK {
		t.Fatal(started.Body.String())
	}
	if _, err := runs.ApplyChild(created.ID, "s1", taskrun.Outcome{
		ChildID: "child-1", ChildStatus: "completed", Summary: "wrote it", HasVerify: true, VerifyPassed: true,
	}); err != nil {
		t.Fatal(err)
	}
	current, _, err := runs.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready := taskrun.ReadyIDs(current); len(ready) != 0 {
		t.Fatalf("ready before approve = %#v", ready)
	}
	approved := postRun(srv, created.ID, `{"action":"approve","ownerRoot":"root-a","sectionId":"s1","expectedVersion":`+itoa(current.Version)+`}`)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", approved.Code, approved.Body.String())
	}
	board := decodeRun(t, approved)
	if statusByID(board.Run)["s1"] != taskrun.StatusApproved || !board.Run.Sections[1].Ready {
		t.Fatalf("after approve = %#v", board.Run.Sections)
	}
	next := postRun(srv, created.ID, `{"action":"start","ownerRoot":"root-a","expectedVersion":`+itoa(board.Run.Version)+`}`)
	if next.Code != http.StatusOK {
		t.Fatal(next.Body.String())
	}
	spawned := decodeRun(t, next)
	if len(spawned.Spawned) != 1 || spawned.Spawned[0].SectionID != "s2" {
		t.Fatalf("second spawn = %#v", spawned.Spawned)
	}
	if len(got) != 2 || !strings.Contains(got[1], "after the note") {
		t.Fatalf("objectives = %#v", got)
	}
}

func TestTaskRunWrongOwnerAndMissingCapability(t *testing.T) {
	_, ps, runs, created := taskFixture(t)
	srv := taskServer(t, ps, runs, nil)
	deny := postRun(srv, created.ID, `{"action":"open","ownerRoot":"root-b"}`)
	if deny.Code != http.StatusForbidden {
		t.Fatalf("owner = %d %s", deny.Code, deny.Body.String())
	}
	bare, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Plans: local.NewPlans(ps)}})
	if err != nil {
		t.Fatal(err)
	}
	miss := postRun(bare, created.ID, `{"action":"open","ownerRoot":"root-a"}`)
	if miss.Code != http.StatusNotImplemented {
		t.Fatalf("capability = %d %s", miss.Code, miss.Body.String())
	}
}

func TestTaskRunStartWithoutLiveReleasesClaim(t *testing.T) {
	_, ps, runs, created := taskFixture(t)
	srv := taskServer(t, ps, runs, nil)
	opened := decodeRun(t, postRun(srv, created.ID, `{"action":"open","ownerRoot":"root-a"}`))
	started := postRun(srv, created.ID, `{"action":"start","ownerRoot":"root-a","expectedVersion":`+itoa(opened.Run.Version)+`}`)
	if started.Code != http.StatusConflict || !strings.Contains(started.Body.String(), "not live") {
		t.Fatalf("start = %d %s", started.Code, started.Body.String())
	}
	got, _, err := runs.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sections[0].Status != taskrun.StatusPending {
		t.Fatalf("status after failed spawn = %s", got.Sections[0].Status)
	}
}
