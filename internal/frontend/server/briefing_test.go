package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func briefingCall(t *testing.T, srv *Server, method, body string) (BriefingSeen, notifyResult) {
	t.Helper()
	got := notifyCall(t, srv, method, "/v1/briefing/seen", body)
	var out BriefingSeen
	if got.code == http.StatusOK {
		if err := json.Unmarshal([]byte(got.raw), &out); err != nil {
			t.Fatalf("decode %s: %v", got.raw, err)
		}
	}
	return out, got
}

func fixBriefingClock(srv *Server, at time.Time) {
	srv.briefing().now = func() time.Time { return at }
}

func TestBriefingSeenFreshIsZeroWithDaemonNow(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	now := time.UnixMilli(1_800_000_000_000)
	fixBriefingClock(srv, now)
	got, res := briefingCall(t, srv, http.MethodGet, "")
	if res.code != http.StatusOK {
		t.Fatalf("GET = %d %s", res.code, res.raw)
	}
	if got.SeenAt != 0 || got.Now != now.UnixMilli() {
		t.Fatalf("fresh = %+v", got)
	}
}

func TestBriefingSeenAdvancesMonotonicSharedAndPersisted(t *testing.T) {
	dir := t.TempDir()
	srv := mustServer(t, Options{SessionDir: dir})
	now := time.UnixMilli(1_800_000_000_000)
	fixBriefingClock(srv, now)

	steps := []struct {
		name string
		post int64
		want int64
	}{
		{"first mark", now.UnixMilli() - 60_000, now.UnixMilli() - 60_000},
		{"older is a no-op", now.UnixMilli() - 3_600_000, now.UnixMilli() - 60_000},
		{"equal is a no-op", now.UnixMilli() - 60_000, now.UnixMilli() - 60_000},
		{"future clamps to daemon now", now.UnixMilli() + 86_400_000, now.UnixMilli()},
	}
	for _, step := range steps {
		body, _ := json.Marshal(map[string]int64{"seenAt": step.post})
		got, res := briefingCall(t, srv, http.MethodPost, string(body))
		if res.code != http.StatusOK {
			t.Fatalf("%s: POST = %d %s", step.name, res.code, res.raw)
		}
		if got.SeenAt != step.want || got.Now != now.UnixMilli() {
			t.Fatalf("%s: got %+v want seenAt %d", step.name, got, step.want)
		}
		// A second client (plain GET) sees the same watermark.
		other, _ := briefingCall(t, srv, http.MethodGet, "")
		if other.SeenAt != step.want {
			t.Fatalf("%s: second client = %+v", step.name, other)
		}
	}

	path := filepath.Join(dir, "notifications", "briefing.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("briefing.json mode = %o, want 600", perm)
	}

	reopened := mustServer(t, Options{SessionDir: dir})
	got, _ := briefingCall(t, reopened, http.MethodGet, "")
	if got.SeenAt != now.UnixMilli() {
		t.Fatalf("after restart = %+v, want %d", got, now.UnixMilli())
	}
}

func TestBriefingSeenRejectsBadBodies(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"zero", `{"seenAt":0}`},
		{"negative", `{"seenAt":-5}`},
		{"unknown field", `{"seenAt":5,"extra":1}`},
		{"trailing data", `{"seenAt":5}{"seenAt":6}`},
		{"wrong type", `{"seenAt":"soon"}`},
		{"not json", `nope`},
	}
	for _, tc := range cases {
		_, res := briefingCall(t, srv, http.MethodPost, tc.body)
		if res.code != http.StatusBadRequest {
			t.Fatalf("%s: POST = %d %s, want 400", tc.name, res.code, res.raw)
		}
	}
	got, _ := briefingCall(t, srv, http.MethodGet, "")
	if got.SeenAt != 0 {
		t.Fatalf("rejected bodies moved the watermark: %+v", got)
	}
}

func TestBriefingSeenIgnoresCorruptFile(t *testing.T) {
	dir := t.TempDir()
	notes := filepath.Join(dir, "notifications")
	if err := os.MkdirAll(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"not json", `{"seenAt":-10}`} {
		if err := os.WriteFile(filepath.Join(notes, "briefing.json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		srv := mustServer(t, Options{SessionDir: dir})
		got, res := briefingCall(t, srv, http.MethodGet, "")
		if res.code != http.StatusOK || got.SeenAt != 0 {
			t.Fatalf("%q: GET = %d %+v", raw, res.code, got)
		}
	}
}
