package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/rootstart"
	"github.com/jonathanung/strike/internal/schedule"
)

func TestHubStarterSetsPlanBeforePromptAndDoesNotStealActive(t *testing.T) {
	opsExisting := make(chan protocol.Op, 4)
	existing := NewLive("root-existing", t.TempDir(), nil, opsExisting)
	opsNew := make(chan protocol.Op, 4)
	var hub *LiveHub
	interactive := 0
	hub = NewLiveHub(func(ctx context.Context) (string, error) {
		interactive++
		return "", fmt.Errorf("interactive spawn")
	}, nil)
	hub.SetUnattendedSpawn(func(ctx context.Context) (string, error) {
		live := NewLive("root-new", t.TempDir(), nil, opsNew)
		hub.Add("root-new", live)
		return "root-new", nil
	})
	hub.Add("root-existing", existing)
	if err := hub.Activate("root-existing"); err != nil {
		t.Fatal(err)
	}
	starter := NewHubStarter(hub)
	res, err := starter.StartRoot(context.Background(), rootstart.Request{
		Source:     rootstart.SourceSchedule,
		SourceID:   "sch_1",
		Title:      "Nightly",
		Prompt:     "look around",
		Workflow:   "plan-implement",
		Unattended: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "root-new" {
		t.Fatalf("session %s", res.SessionID)
	}
	if hub.ActiveID() != "root-existing" {
		t.Fatalf("active root became %s", hub.ActiveID())
	}
	if interactive != 0 {
		t.Fatalf("unattended start used the interactive spawn %d times", interactive)
	}
	if hub.LiveFor("root-new").Status().SessionID != "root-new" {
		t.Fatal("new root missing")
	}
	select {
	case op := <-opsNew:
		mode, ok := op.(protocol.SetPermissionMode)
		if !ok || mode.Mode != protocol.PermissionModePlan {
			t.Fatalf("first op %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("missing permission mode")
	}
	select {
	case op := <-opsNew:
		wf, ok := op.(protocol.StartWorkflow)
		if !ok || wf.Name != "plan-implement" {
			t.Fatalf("workflow op %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("missing workflow start")
	}
	select {
	case op := <-opsNew:
		in, ok := op.(protocol.UserInput)
		if !ok || in.Text != "look around" {
			t.Fatalf("prompt op %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("missing prompt")
	}
	var titled string
	for _, sum := range hub.List() {
		if sum.ID == "root-new" {
			titled = sum.Title
		}
	}
	if titled != "Nightly" {
		t.Fatalf("title %q", titled)
	}
}

func TestSchedulesHTTPCreateRunAndReadOnly(t *testing.T) {
	start := &recordingStarter{id: "sess-http"}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	svc, err := schedule.Open(t.TempDir(), schedule.Hooks{
		Now:     func() time.Time { return now },
		Starter: start,
		WorkspaceMode: func(id string) (string, bool) {
			if id == "ws" {
				return "yolo", true
			}
			return "", false
		},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{SessionDir: t.TempDir(), Schedules: svc})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"name":"Morning","kind":"cron","cron":"0 9 * * 1-5","timezone":"UTC","source":"prompt","prompt":"review CI","workspaceId":"ws"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/schedules", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var created scheduleDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.PermissionMode != "" || created.Overlap != "skip" || created.Missed != "skip" {
		t.Fatalf("dto %+v", created)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/schedules/"+created.ID+"/run", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("run %d %s", rec.Code, rec.Body.String())
	}
	var run runDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.SessionID != "sess-http" || run.PermissionMode != "yolo" {
		t.Fatalf("run %+v", run)
	}
	if start.reqs[0].PermissionMode != "yolo" || start.reqs[0].WorkspaceID != "ws" {
		t.Fatalf("starter %+v", start.reqs[0])
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/schedules/"+created.ID+"/history", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "sess-http") {
		t.Fatalf("history %d %s", rec.Code, rec.Body.String())
	}

	bad := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/schedules", strings.NewReader(`{"name":"x","kind":"cron","cron":"not cron","source":"prompt","prompt":"x"}`))
	srv.Handler().ServeHTTP(bad, req)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "cron") {
		t.Fatalf("bad cron %d %s", bad.Code, bad.Body.String())
	}

	ro, err := New(Options{SessionDir: t.TempDir(), Schedules: svc, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	denied := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/schedules", strings.NewReader(body))
	ro.Handler().ServeHTTP(denied, req)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("read-only %d", denied.Code)
	}
	listed := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/schedules", nil)
	ro.Handler().ServeHTTP(listed, req)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "Morning") {
		t.Fatalf("list %d %s", listed.Code, listed.Body.String())
	}
}

func TestSchedulesMissingCapability(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/schedules", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
}

// recordingStarter is a rootstart.Starter for HTTP tests.
type recordingStarter struct {
	id   string
	reqs []rootstart.Request
}

func (r *recordingStarter) StartRoot(ctx context.Context, req rootstart.Request) (rootstart.Result, error) {
	r.reqs = append(r.reqs, req)
	return rootstart.Result{SessionID: r.id}, nil
}

func TestSchedulePauseResumeDelete(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	svc, err := schedule.Open(t.TempDir(), schedule.Hooks{Now: func() time.Time { return now }}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{SessionDir: t.TempDir(), Schedules: svc})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/schedules", strings.NewReader(`{"name":"Hourly","kind":"interval","every":"1h","source":"prompt","prompt":"ping"}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var created scheduleDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/schedules/"+created.ID+"/pause", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("pause %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/v1/schedules/"+created.ID, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/schedules/"+created.ID, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete %d", rec.Code)
	}
}
