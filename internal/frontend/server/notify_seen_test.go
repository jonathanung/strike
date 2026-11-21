package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedApprovals primes the log and appends n approval asks, returning the
// centre rows in seq order.
func seedApprovals(t *testing.T, srv *Server, dir string, n int) []Notification {
	t.Helper()
	if got := notifyCall(t, srv, http.MethodGet, "/v1/notifications", ""); got.code != http.StatusOK {
		t.Fatalf("init = %d", got.code)
	}
	for i := 1; i <= n; i++ {
		appendSessionLine(t, dir, "root1", fmt.Sprintf(`{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_%d","permission":"bash"}}`, i))
	}
	body := notifyBody(t, srv)
	if len(body.Notifications) != n {
		t.Fatalf("seeded %d rows, want %d", len(body.Notifications), n)
	}
	return body.Notifications
}

func postSeen(t *testing.T, srv *Server, body string) (NotifySeen, notifyResult) {
	t.Helper()
	got := notifyCall(t, srv, http.MethodPost, "/v1/notifications/seen", body)
	var out NotifySeen
	if got.code == http.StatusOK {
		if err := json.Unmarshal([]byte(got.raw), &out); err != nil {
			t.Fatalf("decode %s: %v", got.raw, err)
		}
	}
	return out, got
}

func TestNotificationSeenSharedAcrossClientsAndPersisted(t *testing.T) {
	dir := t.TempDir()
	srv := mustServer(t, Options{SessionDir: dir})
	notes := seedApprovals(t, srv, dir, 3)

	if fresh := notifyBody(t, srv); fresh.Seen.UpTo != 0 || len(fresh.Seen.IDs) != 0 {
		t.Fatalf("fresh seen = %+v", fresh.Seen)
	}

	// Device A opens the middle row; device B (a second GET) sees it.
	got, res := postSeen(t, srv, fmt.Sprintf(`{"ids":[%q]}`, notes[1].ID))
	if res.code != http.StatusOK {
		t.Fatalf("mark one = %d %s", res.code, res.raw)
	}
	if got.UpTo != 0 || len(got.IDs) != 1 || got.IDs[0] != notes[1].ID {
		t.Fatalf("mark one = %+v", got)
	}
	other := notifyBody(t, srv)
	if len(other.Seen.IDs) != 1 || other.Seen.IDs[0] != notes[1].ID {
		t.Fatalf("second client seen = %+v", other.Seen)
	}

	// Mark all up to the first row: ids at or below upTo fold into it.
	got, res = postSeen(t, srv, fmt.Sprintf(`{"upTo":%d}`, notes[1].Seq))
	if res.code != http.StatusOK {
		t.Fatalf("mark all = %d %s", res.code, res.raw)
	}
	if got.UpTo != notes[1].Seq || len(got.IDs) != 0 {
		t.Fatalf("watermark should cover the id: %+v", got)
	}

	// A restart reads seen.json back.
	reopened := mustServer(t, Options{SessionDir: dir})
	after := notifyBody(t, reopened)
	if after.Seen.UpTo != notes[1].Seq {
		t.Fatalf("persisted seen = %+v", after.Seen)
	}
	info, err := os.Stat(filepath.Join(dir, "notifications", "seen.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("seen.json mode = %o", perm)
	}
}

func TestNotificationSeenMergeRules(t *testing.T) {
	dir := t.TempDir()
	srv := mustServer(t, Options{SessionDir: dir})
	notes := seedApprovals(t, srv, dir, 3)
	last := notes[len(notes)-1].Seq

	cases := []struct {
		name     string
		body     string
		wantUpTo int64
		wantIDs  []string
	}{
		{
			name:     "unknown and page-local ids are not stored",
			body:     fmt.Sprintf(`{"ids":["local-gateway-1","deadbeef",%q]}`, notes[2].ID),
			wantUpTo: 0,
			wantIDs:  []string{notes[2].ID},
		},
		{
			name:     "duplicate ids collapse",
			body:     fmt.Sprintf(`{"ids":[%q,%q]}`, notes[2].ID, notes[2].ID),
			wantUpTo: 0,
			wantIDs:  []string{notes[2].ID},
		},
		{
			name:     "watermark past the newest row is clamped",
			body:     `{"upTo":999999}`,
			wantUpTo: last,
			wantIDs:  []string{},
		},
		{
			name:     "watermark never moves backwards",
			body:     `{"upTo":1}`,
			wantUpTo: last,
			wantIDs:  []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, res := postSeen(t, srv, tc.body)
			if res.code != http.StatusOK {
				t.Fatalf("code = %d %s", res.code, res.raw)
			}
			if got.UpTo != tc.wantUpTo || strings.Join(got.IDs, ",") != strings.Join(tc.wantIDs, ",") {
				t.Fatalf("seen = %+v, want upTo=%d ids=%v", got, tc.wantUpTo, tc.wantIDs)
			}
		})
	}
	stored, err := os.ReadFile(filepath.Join(dir, "notifications", "seen.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "local-gateway-1") || strings.Contains(string(stored), "deadbeef") {
		t.Fatalf("unknown ids persisted: %s", stored)
	}
}

func TestNotificationSeenRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	srv := mustServer(t, Options{SessionDir: dir})
	seedApprovals(t, srv, dir, 1)
	many := make([]string, notifyMaxItems+1)
	for i := range many {
		many[i] = fmt.Sprintf("%q", fmt.Sprintf("id%d", i))
	}
	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"nothing to mark", `{}`},
		{"negative watermark", `{"upTo":-1}`},
		{"unknown field", `{"upTo":1,"all":true}`},
		{"trailing data", `{"upTo":1}{"upTo":2}`},
		{"path-like id", `{"ids":["../prefs"]}`},
		{"credential-shaped id", `{"ids":["` + notifySecret + `"]}`},
		{"too many ids", `{"ids":[` + strings.Join(many, ",") + `]}`},
		{"oversized body", `{"ids":["` + strings.Repeat("a", notifySeenLimit) + `"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, res := postSeen(t, srv, tc.body)
			if res.code != http.StatusBadRequest {
				t.Fatalf("code = %d %s", res.code, res.raw)
			}
			mustNotLeak(t, res.raw)
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "notifications", "seen.json")); !os.IsNotExist(err) {
		t.Fatalf("rejected requests wrote seen.json: %v", err)
	}
}

func TestNotificationSeenClampsAfterLogReset(t *testing.T) {
	dir := t.TempDir()
	srv := mustServer(t, Options{SessionDir: dir})
	notes := seedApprovals(t, srv, dir, 2)
	if _, res := postSeen(t, srv, fmt.Sprintf(`{"upTo":%d}`, notes[1].Seq)); res.code != http.StatusOK {
		t.Fatalf("mark all = %d", res.code)
	}
	// The centre log and its seq counter are wiped; seen.json survives.
	for _, name := range []string{"log.jsonl", "state.json"} {
		if err := os.Remove(filepath.Join(dir, "notifications", name)); err != nil {
			t.Fatal(err)
		}
	}
	reopened := mustServer(t, Options{SessionDir: dir})
	if got := notifyBody(t, reopened); got.Seen.UpTo != 0 {
		t.Fatalf("stale watermark survived a seq reset: %+v", got.Seen)
	}
}

func TestNotificationSeenRequiresAuth(t *testing.T) {
	srv := mustServer(t, Options{Auth: true, Token: "t", SessionDir: t.TempDir()})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	for _, tc := range []struct {
		name   string
		header string
		want   int
	}{
		{"missing token", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"right token, nothing to mark", "Bearer t", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/notifications/seen", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("code = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}
