package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/schedule"
)

const (
	scriptCmdSentinel = "CMD_SENTINEL_7c1e"
	scriptOutSentinel = "OUT_SENTINEL_d42b"
)

// scriptClock wires a schedule.Service whose runner returns result to srv's
// notification centre, the way strike serve does.
func scriptClock(t *testing.T, srv *Server, result schedule.ScriptResult) (*schedule.Service, schedule.Schedule, func(time.Time)) {
	t.Helper()
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	now := t0
	runner := func(ctx context.Context, req schedule.ScriptRequest, started func(schedule.ScriptStart)) schedule.ScriptResult {
		if result.Status == schedule.StatusFinished || result.Status == schedule.StatusFailed {
			started(schedule.ScriptStart{StartedAt: t0})
		}
		return result
	}
	dir := t.TempDir()
	svc, err := schedule.Open(dir, schedule.Hooks{
		Now:    func() time.Time { return now },
		Script: runner,
		Notify: func(n schedule.ScriptNotice) {
			srv.NotifyScriptRun(n.ScheduleID, n.RunID, n.Name, n.ExitCode, n.Failed)
		},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := svc.Create(schedule.Spec{
		Name: "nightly", Kind: schedule.KindInterval, Every: "1h", Source: schedule.SourceScript,
		Command: "echo " + scriptCmdSentinel + " && cat " + notifySecret, ArmedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, sch, func(at time.Time) { now = at }
}

func TestScriptRunNotificationNeverCarriesCommandOrOutput(t *testing.T) {
	zero, two := 0, 2
	cases := []struct {
		name     string
		result   schedule.ScriptResult
		wantKind string
		wantBody string
	}{
		{
			name:     "finished",
			result:   schedule.ScriptResult{Status: schedule.StatusFinished, ExitCode: &zero, Output: []byte(scriptOutSentinel + " " + notifyMarker + "\n")},
			wantKind: noteScriptFinished, wantBody: "nightly: exit 0",
		},
		{
			name:     "failed",
			result:   schedule.ScriptResult{Status: schedule.StatusFailed, ExitCode: &two, Output: []byte("FAIL " + scriptOutSentinel + "\n"), Error: "exit code 2 " + scriptOutSentinel},
			wantKind: noteScriptFailed, wantBody: "nightly: exit 2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := mustServer(t, Options{SessionDir: t.TempDir()})
			svc, sch, setNow := scriptClock(t, srv, tc.result)
			setNow(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
			svc.Tick(context.Background(), false)
			svc.WaitScripts()

			body := notifyBody(t, srv)
			if len(body.Notifications) != 1 {
				t.Fatalf("notifications = %+v", body.Notifications)
			}
			note := body.Notifications[0]
			for _, banned := range []string{scriptCmdSentinel, scriptOutSentinel, notifyMarker, notifySecret} {
				if strings.Contains(note.Title, banned) || strings.Contains(note.Body, banned) {
					t.Fatalf("notification carries %q: %+v", banned, note)
				}
			}
			raw, _ := json.Marshal(note)
			if strings.Contains(string(raw), scriptCmdSentinel) || strings.Contains(string(raw), scriptOutSentinel) {
				t.Fatalf("notification record carries command or output: %s", raw)
			}
			got, _ := svc.Get(sch.ID)
			if note.Kind != tc.wantKind || note.Body != tc.wantBody || note.Title != fallbackTitle(tc.wantKind) ||
				note.SessionID != "" || note.SubjectID != sch.ID || note.Href != "/#schedule="+sch.ID+"&run="+got.Runs[0].ID || !note.Banner {
				t.Fatalf("notification = %+v", note)
			}
		})
	}
}

func TestScriptRunNotificationOnlyForFinishedOrFailed(t *testing.T) {
	cases := []struct {
		name   string
		result schedule.ScriptResult
	}{
		{name: "denied", result: schedule.ScriptResult{Status: schedule.StatusDenied, Decision: schedule.DecisionDeny, Error: "denied by permission rules"}},
		{name: "error", result: schedule.ScriptResult{Status: schedule.StatusError, Error: "start: boom"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := mustServer(t, Options{SessionDir: t.TempDir()})
			svc, _, setNow := scriptClock(t, srv, tc.result)
			setNow(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
			svc.Tick(context.Background(), false)
			svc.WaitScripts()
			if body := notifyBody(t, srv); len(body.Notifications) != 0 {
				t.Fatalf("notifications = %+v", body.Notifications)
			}
		})
	}
	t.Run("missed on startup", func(t *testing.T) {
		zero := 0
		srv := mustServer(t, Options{SessionDir: t.TempDir()})
		svc, sch, setNow := scriptClock(t, srv, schedule.ScriptResult{Status: schedule.StatusFinished, ExitCode: &zero})
		setNow(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
		svc.Tick(context.Background(), true)
		svc.WaitScripts()
		if got, _ := svc.Get(sch.ID); got.LastStatus != schedule.StatusMissed {
			t.Fatalf("last status %q", got.LastStatus)
		}
		if body := notifyBody(t, srv); len(body.Notifications) != 0 {
			t.Fatalf("notifications = %+v", body.Notifications)
		}
	})
}

func TestScriptRunNotificationRespectsPrefs(t *testing.T) {
	zero := 0
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	if got := notifyCall(t, srv, http.MethodGet, "/v1/notifications", ""); got.code != http.StatusOK {
		t.Fatalf("init = %d", got.code)
	}
	svc, sch, setNow := scriptClock(t, srv, schedule.ScriptResult{Status: schedule.StatusFinished, ExitCode: &zero})
	setNow(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
	svc.Tick(context.Background(), false)
	svc.WaitScripts()

	if day := notifyBody(t, srv); len(day.Notifications) != 1 || !day.Notifications[0].Banner {
		t.Fatalf("day = %+v", day.Notifications)
	}
	muted := notifyCall(t, srv, http.MethodPut, "/v1/notifications/prefs",
		`{"enabled":true,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":["`+sch.ID+`"]}`)
	if muted.code != http.StatusOK {
		t.Fatalf("mute = %d %s", muted.code, muted.raw)
	}
	if off := notifyBody(t, srv); len(off.Notifications) != 1 || off.Notifications[0].Banner {
		t.Fatalf("muted schedule = %+v", off.Notifications)
	}
	disabled := notifyCall(t, srv, http.MethodPut, "/v1/notifications/prefs",
		`{"enabled":false,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":[]}`)
	if disabled.code != http.StatusOK {
		t.Fatalf("disable = %d %s", disabled.code, disabled.raw)
	}
	if off := notifyBody(t, srv); len(off.Notifications) != 1 || off.Notifications[0].Banner {
		t.Fatalf("disabled banners = %+v", off.Notifications)
	}
}

func TestScriptRunNoteConstructor(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	one := 1
	cases := []struct {
		name     string
		schID    string
		runID    string
		label    string
		code     *int
		failed   bool
		wantOK   bool
		wantBody string
	}{
		{name: "failed with exit code", schID: "sch_ab12", runID: "run_cd34", label: "nightly", code: &one, failed: true, wantOK: true, wantBody: "nightly: exit 1"},
		{name: "no exit code", schID: "sch_ab12", runID: "run_cd34", label: "nightly", failed: true, wantOK: true, wantBody: "nightly: no exit code"},
		{name: "empty name falls back", schID: "sch_ab12", runID: "run_cd34", label: "   ", code: &one, wantOK: true, wantBody: "Script schedule: exit 1"},
		{name: "secret in the name is scrubbed", schID: "sch_ab12", runID: "run_cd34", label: "deploy " + notifySecret, code: &one, wantOK: true},
		{name: "unsafe schedule id", schID: "../x", runID: "run_cd34", label: "nightly", code: &one},
		{name: "unsafe run id", schID: "sch_ab12", runID: "run/1", label: "nightly", code: &one},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note, ok := ScriptRunNote(tc.schID, tc.runID, tc.label, tc.code, tc.failed, at)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				return
			}
			if tc.wantBody != "" && note.Body != tc.wantBody {
				t.Fatalf("body %q", note.Body)
			}
			if strings.Contains(note.Body, notifySecret) || strings.Contains(note.Title, notifySecret) {
				t.Fatalf("secret leaked: %+v", note)
			}
			wantKind := noteScriptFinished
			if tc.failed {
				wantKind = noteScriptFailed
			}
			if note.Kind != wantKind || note.Title != fallbackTitle(wantKind) || note.ID == "" || !note.At.Equal(at) {
				t.Fatalf("note %+v", note)
			}
		})
	}

	// The constructor's inputs are ids, name, exit code, outcome, and time:
	// a command or output parameter would have to change this signature.
	fn := reflect.TypeOf(ScriptRunNote)
	want := []reflect.Type{
		reflect.TypeOf(""), reflect.TypeOf(""), reflect.TypeOf(""),
		reflect.TypeOf((*int)(nil)), reflect.TypeOf(false), reflect.TypeOf(time.Time{}),
	}
	if fn.NumIn() != len(want) {
		t.Fatalf("ScriptRunNote takes %d parameters", fn.NumIn())
	}
	for i, w := range want {
		if fn.In(i) != w {
			t.Fatalf("parameter %d is %s, want %s", i, fn.In(i), w)
		}
	}
	var fields []string
	notice := reflect.TypeOf(schedule.ScriptNotice{})
	for i := 0; i < notice.NumField(); i++ {
		fields = append(fields, notice.Field(i).Name)
	}
	if strings.Join(fields, ",") != "ScheduleID,RunID,Name,ExitCode,Failed" {
		t.Fatalf("ScriptNotice fields %v: command and output must not be added", fields)
	}
}
