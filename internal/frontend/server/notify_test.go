package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
)

const (
	notifySecret = "sk-ant-api03-ABCDEFGHIJKLMNOP"
	notifyMarker = "UNIQUE_COMMAND_OUTPUT_9f3a"
)

func TestProjectLineAllowlistDropsSecretsAndOutput(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	long := notifySecret + " " + strings.Repeat("A", 4000) + notifyMarker
	cases := []struct {
		name     string
		parent   string
		line     string
		wantOK   bool
		wantKind string
		wantBody string
		wantSess string
		wantSub  string
		wantReq  string
		log      string
	}{
		{
			name:   "approval keeps the verb only",
			line:   `{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_2","permission":"bash","patterns":["echo ` + notifySecret + ` ` + notifyMarker + `"],"metadata":{"token":"` + notifySecret + `"}}}`,
			wantOK: true, wantKind: noteApproval, wantBody: "bash is waiting",
			wantSess: "root1", wantSub: "root1", wantReq: "root1:perm_2",
		},
		{
			name:   "permission field that looks like a command is not copied",
			line:   `{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_9","permission":"echo ` + notifySecret + `"}}`,
			wantOK: true, wantKind: noteApproval, wantBody: "tool is waiting",
			wantSess: "root1", wantReq: "root1:perm_9",
		},
		{
			name: "tool output is not a notification",
			line: `{"type":"tool.end","time":"2026-10-26T12:00:00Z","data":{"name":"bash","output":"` + notifyMarker + ` ` + notifySecret + `"}}`,
		},
		{
			name: "process output is not a notification",
			line: `{"type":"process.output","time":"2026-10-26T12:00:00Z","data":{"data":"` + notifyMarker + `"}}`,
		},
		{
			name:   "blocked child scrubs and clamps the summary",
			line:   `{"type":"child.completed","time":"2026-10-26T12:00:00Z","data":{"sessionId":"child-1","parentSessionId":"parent-1","status":"blocked","name":"scout","summary":"` + long + `","handoff":{"summary":"` + notifyMarker + `","filesChanged":["SECRET_FILE_PATH_ZZZ"]}}}`,
			wantOK: true, wantKind: noteChildBlocked, wantSess: "parent-1", wantSub: "child-1",
		},
		{
			name:   "finished child",
			line:   `{"type":"child.completed","time":"2026-10-26T12:00:00Z","data":{"sessionId":"child-2","parentSessionId":"parent-1","status":"completed","name":"scout"}}`,
			wantOK: true, wantKind: noteChildFinished, wantBody: "Finished",
			wantSess: "parent-1", wantSub: "child-2",
		},
		{
			name:   "delegation blocked matches the child id",
			log:    "parent-1",
			line:   `{"type":"delegation.changed","time":"2026-10-26T12:00:00Z","data":{"id":"d1","state":"blocked","sessionId":"child-1","name":"scout","reason":"` + notifySecret + `"}}`,
			wantOK: true, wantKind: noteChildBlocked, wantSess: "parent-1", wantSub: "child-1",
		},
		{
			name:   "root turn finished ignores files",
			line:   `{"type":"turn.completed","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","turnId":"turn-1","stopReason":"end_turn","files":[{"path":"` + notifyMarker + `","kind":"update"}]}}`,
			wantOK: true, wantKind: noteSessionFinished, wantBody: "Stopped: end_turn",
			wantSess: "root1",
		},
		{
			name:   "child turn is not a second session-finished",
			parent: "parent-1",
			line:   `{"type":"turn.completed","time":"2026-10-26T12:00:00Z","data":{"sessionId":"child-1","parentSessionId":"parent-1","turnId":"turn-9","stopReason":"end_turn"}}`,
		},
		{
			name:   "unsafe stop reason is dropped",
			line:   `{"type":"turn.completed","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","turnId":"turn-2","stopReason":"` + notifySecret + `"}}`,
			wantOK: true, wantKind: noteSessionFinished, wantBody: "The turn finished",
			wantSess: "root1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logSession := tc.log
			if logSession == "" {
				logSession = "root1"
				if strings.Contains(tc.line, "parent-1") && tc.wantSess == "parent-1" {
					logSession = "parent-1"
				}
			}
			got, ok := projectLine(logSession, tc.parent, []byte(tc.line), now)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return
			}
			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if tc.wantBody != "" && got.Body != tc.wantBody {
				t.Fatalf("body = %q, want %q", got.Body, tc.wantBody)
			}
			if tc.wantSess != "" && got.SessionID != tc.wantSess {
				t.Fatalf("session = %q, want %q", got.SessionID, tc.wantSess)
			}
			if tc.wantSub != "" && got.SubjectID != tc.wantSub {
				t.Fatalf("subject = %q, want %q", got.SubjectID, tc.wantSub)
			}
			if tc.wantReq != "" && got.RequestID != tc.wantReq {
				t.Fatalf("request = %q, want %q", got.RequestID, tc.wantReq)
			}
			if strings.Contains(string(raw), notifyMarker) || strings.Contains(string(raw), notifySecret) || strings.Contains(string(raw), "SECRET_FILE_PATH_ZZZ") {
				t.Fatalf("projected record leaked: %s", raw)
			}
			if len([]rune(got.Body)) > notifyBodyRunes || len([]rune(got.Title)) > notifyTitleRunes {
				t.Fatalf("title/body too long: %q %q", got.Title, got.Body)
			}
		})
	}

	blocked, ok := projectLine("parent-1", "", []byte(`{"type":"child.completed","time":"2026-10-26T12:00:00Z","data":{"sessionId":"child-1","parentSessionId":"parent-1","status":"blocked","name":"scout"}}`), now)
	if !ok {
		t.Fatal("blocked child")
	}
	again, ok := projectLine("parent-1", "", []byte(`{"type":"delegation.changed","time":"2026-10-26T12:04:00Z","data":{"state":"blocked","sessionId":"child-1","name":"scout"}}`), now)
	if !ok || again.ID != blocked.ID {
		t.Fatalf("dedupe id = %q and %q", blocked.ID, again.ID)
	}
}

func TestQuietHoursWindow(t *testing.T) {
	q := QuietHours{Enabled: true, Start: "22:00", End: "07:00"}
	local := func(h, m int) time.Time {
		return time.Date(2026, 10, 26, h, m, 0, 0, time.Local)
	}
	if !inQuiet(local(23, 30), q) || !inQuiet(local(22, 0), q) || !inQuiet(local(0, 15), q) {
		t.Fatal("overnight window should be quiet")
	}
	if inQuiet(local(7, 0), q) || inQuiet(local(12, 0), q) {
		t.Fatal("daytime should not be quiet")
	}
	if inQuiet(local(23, 30), QuietHours{Enabled: false, Start: "22:00", End: "07:00"}) {
		t.Fatal("disabled quiet hours should not suppress")
	}
	day := QuietHours{Enabled: true, Start: "09:00", End: "17:00"}
	if !inQuiet(local(9, 0), day) || inQuiet(local(17, 0), day) || inQuiet(local(8, 59), day) {
		t.Fatal("same-day window bounds")
	}
}

func TestNotificationReplaySkipsHistoryAndKeepsSecretsOut(t *testing.T) {
	dir := t.TempDir()
	historical := `{"type":"tool.end","time":"2026-10-26T11:00:00Z","data":{"output":"` + notifyMarker + ` ` + notifySecret + `"}}` + "\n"
	if err := os.WriteFile(session.LogPath(dir, "root1"), []byte(historical), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := mustServer(t, Options{SessionDir: dir, PollInterval: 20 * time.Millisecond})

	first := notifyCall(t, srv, http.MethodGet, "/v1/notifications", "")
	mustNotLeak(t, first.raw)
	if first.code != http.StatusOK {
		t.Fatalf("first status = %d", first.code)
	}
	var opened notificationsResponse
	if err := json.Unmarshal([]byte(first.raw), &opened); err != nil {
		t.Fatal(err)
	}
	if len(opened.Notifications) != 0 {
		t.Fatalf("history was projected: %+v", opened.Notifications)
	}
	if !opened.Prefs.Enabled || opened.Prefs.Quiet.Start != "22:00" {
		t.Fatalf("default prefs = %+v", opened.Prefs)
	}

	appendSessionLine(t, dir, "root1", `{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_2","permission":"bash","patterns":["echo `+notifySecret+` `+notifyMarker+`"],"metadata":{"token":"`+notifySecret+`"}}}`)
	second := notifyCall(t, srv, http.MethodGet, "/v1/notifications", "")
	mustNotLeak(t, second.raw)
	var got notificationsResponse
	if err := json.Unmarshal([]byte(second.raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Notifications) != 1 {
		t.Fatalf("notifications = %+v", got.Notifications)
	}
	item := got.Notifications[0]
	if item.Kind != noteApproval || item.Body != "bash is waiting" || item.RequestID != "root1:perm_2" {
		t.Fatalf("item = %+v", item)
	}
	if item.Href != "/#session=root1&approval=root1:perm_2" {
		t.Fatalf("href = %q", item.Href)
	}
	if !item.Banner {
		t.Fatal("banner should be on by default")
	}
	keys := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(second.raw), &keys); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"patterns"`, `"metadata"`, `"output"`, `"argv"`} {
		if strings.Contains(second.raw, banned) {
			t.Fatalf("response contains %s: %s", banned, second.raw)
		}
	}

	again := notifyCall(t, srv, http.MethodGet, "/v1/notifications", "")
	var dup notificationsResponse
	if err := json.Unmarshal([]byte(again.raw), &dup); err != nil {
		t.Fatal(err)
	}
	if len(dup.Notifications) != 1 {
		t.Fatalf("rescan duplicated: %+v", dup.Notifications)
	}
	since := notifyCall(t, srv, http.MethodGet, "/v1/notifications?since=1", "")
	var tail notificationsResponse
	if err := json.Unmarshal([]byte(since.raw), &tail); err != nil {
		t.Fatal(err)
	}
	if len(tail.Notifications) != 0 || tail.Latest != 1 {
		t.Fatalf("since = %+v", tail)
	}

	// A session file created after the first read is replayed from the start.
	appendSessionLine(t, dir, "root2", `{"type":"turn.completed","time":"2026-10-26T12:05:00Z","data":{"sessionId":"root2","turnId":"turn-1","stopReason":"end_turn"}}`)
	caught := notifyCall(t, srv, http.MethodGet, "/v1/notifications?since=1", "")
	var up notificationsResponse
	if err := json.Unmarshal([]byte(caught.raw), &up); err != nil {
		t.Fatal(err)
	}
	if len(up.Notifications) != 1 || up.Notifications[0].Kind != noteSessionFinished || up.Notifications[0].SessionID != "root2" {
		t.Fatalf("catch-up = %+v", up.Notifications)
	}

	st, err := os.Stat(filepath.Join(dir, "notifications", "prefs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("prefs mode = %o", st.Mode().Perm())
	}
	dst, err := os.Stat(filepath.Join(dir, "notifications"))
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o", dst.Mode().Perm())
	}

	// Restart keeps the cursor: no duplicate, and a later line is still delivered.
	srv2 := mustServer(t, Options{SessionDir: dir, PollInterval: 20 * time.Millisecond})
	restart := notifyCall(t, srv2, http.MethodGet, "/v1/notifications", "")
	var kept notificationsResponse
	if err := json.Unmarshal([]byte(restart.raw), &kept); err != nil {
		t.Fatal(err)
	}
	if len(kept.Notifications) != 2 {
		t.Fatalf("restart replay = %+v", kept.Notifications)
	}
}

func TestNotificationPrefsMuteAndQuiet(t *testing.T) {
	dir := t.TempDir()
	srv := mustServer(t, Options{SessionDir: dir})
	if got := notifyCall(t, srv, http.MethodGet, "/v1/notifications", ""); got.code != http.StatusOK {
		t.Fatalf("init = %d", got.code)
	}
	appendSessionLine(t, dir, "root1", `{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_1","permission":"edit"}}`)

	log := srv.notifications()
	log.mu.Lock()
	log.now = func() time.Time { return time.Date(2026, 10, 26, 23, 30, 0, 0, time.Local) }
	log.mu.Unlock()

	put := notifyCall(t, srv, http.MethodPut, "/v1/notifications/prefs", `{"enabled":true,"quiet":{"enabled":true,"start":"22:00","end":"07:00"},"mutedSessions":[]}`)
	if put.code != http.StatusOK {
		t.Fatalf("put = %d %s", put.code, put.raw)
	}
	body := notifyBody(t, srv)
	if len(body.Notifications) != 1 || body.Notifications[0].Banner {
		t.Fatalf("quiet banner = %+v", body.Notifications)
	}
	if body.Notifications[0].Kind != noteApproval {
		t.Fatalf("record dropped during quiet hours: %+v", body.Notifications)
	}

	log.mu.Lock()
	log.now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local) }
	log.mu.Unlock()
	day := notifyBody(t, srv)
	if len(day.Notifications) != 1 || !day.Notifications[0].Banner {
		t.Fatalf("day banner = %+v", day.Notifications)
	}

	muted := notifyCall(t, srv, http.MethodPut, "/v1/notifications/prefs", `{"enabled":true,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":["root1"]}`)
	if muted.code != http.StatusOK {
		t.Fatalf("mute = %d %s", muted.code, muted.raw)
	}
	off := notifyBody(t, srv)
	if off.Notifications[0].Banner {
		t.Fatal("muted session still banners")
	}

	bad := notifyCall(t, srv, http.MethodPut, "/v1/notifications/prefs", `{"enabled":true,"quiet":{"enabled":true,"start":"25:99","end":"07:00"},"mutedSessions":[]}`)
	if bad.code != http.StatusBadRequest {
		t.Fatalf("bad hours = %d", bad.code)
	}
	leaked := notifyCall(t, srv, http.MethodPut, "/v1/notifications/prefs", `{"enabled":true,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":["`+notifySecret+`"]}`)
	if leaked.code != http.StatusBadRequest {
		t.Fatalf("bad id = %d", leaked.code)
	}
	mustNotLeak(t, leaked.raw)
	stored, err := os.ReadFile(filepath.Join(dir, "notifications", "prefs.json"))
	if err != nil {
		t.Fatal(err)
	}
	mustNotLeak(t, string(stored))
	if strings.Contains(string(stored), notifySecret) {
		t.Fatalf("prefs stored the rejected id: %s", stored)
	}

	neg := notifyCall(t, srv, http.MethodGet, "/v1/notifications?since=-1", "")
	if neg.code != http.StatusBadRequest {
		t.Fatalf("since = %d", neg.code)
	}
}

func TestNotificationStreamAndAuth(t *testing.T) {
	dir := t.TempDir()
	srv := mustServer(t, Options{Auth: true, Token: "t", SessionDir: dir, PollInterval: 20 * time.Millisecond})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	unauth, err := http.Get(ts.URL + "/v1/notifications")
	if err != nil {
		t.Fatal(err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token = %d", unauth.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/notifications", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("authed = %d", res.StatusCode)
	}

	appendSessionLine(t, dir, "root1", `{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_3","permission":"bash","patterns":["`+notifySecret+`"]}}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sreq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/notifications/stream?since=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	sreq.Header.Set("Authorization", "Bearer t")
	sres, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Fatal(err)
	}
	defer sres.Body.Close()
	if sres.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", sres.StatusCode)
	}
	got := readSSEData(t, sres.Body, 1, 2*time.Second)
	if got[0]["kind"] != noteApproval || got[0]["body"] != "bash is waiting" {
		t.Fatalf("stream event = %+v", got[0])
	}
	blob, _ := json.Marshal(got[0])
	mustNotLeak(t, string(blob))
	cancel()

	boot := httptest.NewRecorder()
	breq := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	breq.Header.Set("Authorization", "Bearer t")
	srv.Handler().ServeHTTP(boot, breq)
	if !strings.Contains(boot.Body.String(), `"notifications":true`) {
		t.Fatalf("bootstrap missing notifications: %s", boot.Body.String())
	}
}

func TestNormalizePrefsRejectsGarbage(t *testing.T) {
	if _, err := normalizePrefs(NotifyPrefs{Quiet: QuietHours{Start: "ab", End: "07:00"}}); err == nil {
		t.Fatal("expected invalid hours")
	}
	ok, err := normalizePrefs(NotifyPrefs{Enabled: true, Muted: []string{"root1", "root1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.Muted) != 1 || ok.Muted[0] != "root1" || ok.Quiet.Start != "22:00" {
		t.Fatalf("normalized = %+v", ok)
	}
	sessionID := "20261026T120000.000000000Z-abcdefghijklmnopqrstuvwxyz"
	if got, good := safeID(sessionID); !good || got != sessionID {
		t.Fatalf("real session id rejected: %q %v", got, good)
	}
	if _, good := safeID(sessionID + ":perm_1"); !good {
		t.Fatal("permission request id rejected")
	}
}

type notifyResult struct {
	code int
	raw  string
}

func notifyCall(t *testing.T, srv *Server, method, target, body string) notifyResult {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	return notifyResult{code: res.Code, raw: res.Body.String()}
}

func notifyBody(t *testing.T, srv *Server) notificationsResponse {
	t.Helper()
	got := notifyCall(t, srv, http.MethodGet, "/v1/notifications", "")
	if got.code != http.StatusOK {
		t.Fatalf("GET = %d %s", got.code, got.raw)
	}
	mustNotLeak(t, got.raw)
	var out notificationsResponse
	if err := json.Unmarshal([]byte(got.raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func appendSessionLine(t *testing.T, dir, id, line string) {
	t.Helper()
	f, err := os.OpenFile(session.LogPath(dir, id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func mustNotLeak(t *testing.T, raw string) {
	t.Helper()
	for _, banned := range []string{notifySecret, notifyMarker, "SECRET_FILE_PATH_ZZZ"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("leaked %q in %s", banned, raw)
		}
	}
}
