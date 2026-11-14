package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/product/update"
)

// fakeReleases is a GitHub Releases stand-in that counts every request.
type fakeReleases struct {
	srv    *httptest.Server
	hits   atomic.Int32
	other  atomic.Int32 // any non-latest path (asset/checksum downloads)
	status int
	tag    string
}

func newFakeReleases(t *testing.T, status int, tag string) *fakeReleases {
	t.Helper()
	f := &fakeReleases{status: status, tag: tag}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			f.other.Add(1)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(f.status)
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newUpdateServer(t *testing.T, dial string, gh *fakeReleases, canReplace bool) *Server {
	t.Helper()
	probe := NewUpdateProbe(update.ProbeOptions{
		Options:   update.Options{APIBase: gh.srv.URL, HTTPClient: gh.srv.Client(), Current: "v1.0.0"},
		CacheFile: filepath.Join(t.TempDir(), "update-check-web.json"),
		Install:   &update.InstallInfo{Kind: update.InstallWritable, CanReplace: canReplace},
	})
	services := &host.Services{Settings: testSettings{defaults: host.UserDefaults{Autoupdate: dial}}}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services, UpdateProbe: probe})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func getUpdate(t *testing.T, srv *Server) updateCheckResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/update", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/update = %d %s", rec.Code, rec.Body.String())
	}
	var out updateCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getSettings(t *testing.T, srv *Server) settingsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/settings = %d %s", rec.Code, rec.Body.String())
	}
	var out settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUpdateCheckOffNeverProbes(t *testing.T) {
	gh := newFakeReleases(t, http.StatusOK, "v9.0.0")
	srv := newUpdateServer(t, "off", gh, false)
	got := getUpdate(t, srv)
	if got.State != updateStateOff || got.Mode != "off" || got.Available {
		t.Fatalf("off = %+v", got)
	}
	if n := gh.hits.Load(); n != 0 {
		t.Fatalf("dial off made %d network requests", n)
	}
	if s := getSettings(t, srv); s.AutoupdateEffective != "off" || s.UpdateCheck == nil || s.UpdateCheck.State != updateStateOff {
		t.Fatalf("settings = %+v / %+v", s, s.UpdateCheck)
	}
}

func TestUpdateCheckNotifyReportsAvailableAndCaches(t *testing.T) {
	gh := newFakeReleases(t, http.StatusOK, "v1.2.0")
	// Empty dial is effective notify.
	srv := newUpdateServer(t, "", gh, true)
	if s := getSettings(t, srv); s.AutoupdateEffective != "notify" || s.UpdateCheck != nil {
		t.Fatalf("settings before check = %+v (reading settings must not probe)", s)
	}
	if n := gh.hits.Load(); n != 0 {
		t.Fatalf("GET /v1/settings probed (%d hits)", n)
	}
	got := getUpdate(t, srv)
	if got.State != updateStateAvailable || !got.Available || got.Latest != "v1.2.0" || got.Current != "v1.0.0" || got.Mode != "notify" {
		t.Fatalf("available = %+v", got)
	}
	if got.Message == "" || got.CheckedAt == "" {
		t.Fatalf("message/checkedAt missing: %+v", got)
	}
	// Second call inside the probe interval is served from cache.
	again := getUpdate(t, srv)
	if again.State != updateStateAvailable || again.Latest != "v1.2.0" || again.Message == "" {
		t.Fatalf("cached = %+v", again)
	}
	if n := gh.hits.Load(); n != 1 {
		t.Fatalf("network hits = %d, want 1 (cache interval)", n)
	}
	if s := getSettings(t, srv); s.UpdateCheck == nil || s.UpdateCheck.State != updateStateAvailable {
		t.Fatalf("settings last check = %+v", s.UpdateCheck)
	}
}

func TestUpdateCheckUpToDate(t *testing.T) {
	gh := newFakeReleases(t, http.StatusOK, "v1.0.0")
	srv := newUpdateServer(t, "notify", gh, false)
	got := getUpdate(t, srv)
	if got.State != updateStateCurrent || got.Available || got.Latest != "v1.0.0" {
		t.Fatalf("current = %+v", got)
	}
	if again := getUpdate(t, srv); again.State != updateStateCurrent {
		t.Fatalf("cached current = %+v", again)
	}
}

func TestUpdateCheckFailureIsNeverUpToDate(t *testing.T) {
	gh := newFakeReleases(t, http.StatusInternalServerError, "")
	srv := newUpdateServer(t, "notify", gh, false)
	got := getUpdate(t, srv)
	if got.State != updateStateFailed || got.Available || got.Error == "" {
		t.Fatalf("failed = %+v", got)
	}
	// Probe error from the hook itself is also failed, not current.
	services := &host.Services{Settings: testSettings{defaults: host.UserDefaults{Autoupdate: "notify"}}}
	srv2, err := New(Options{SessionDir: t.TempDir(), Services: services, UpdateProbe: func(context.Context) (UpdateProbeResult, error) {
		return UpdateProbeResult{}, errors.New("boom")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := getUpdate(t, srv2); got.State != updateStateFailed || got.Error != "boom" {
		t.Fatalf("hook error = %+v", got)
	}
}

func TestUpdateCheckAutoNeverInstalls(t *testing.T) {
	gh := newFakeReleases(t, http.StatusOK, "v2.0.0")
	srv := newUpdateServer(t, "auto", gh, true)
	got := getUpdate(t, srv)
	if got.State != updateStateAvailable || got.Mode != "auto" {
		t.Fatalf("auto = %+v", got)
	}
	if n := gh.other.Load(); n != 0 {
		t.Fatalf("auto dial fetched %d install assets from the web route", n)
	}
}

func TestUpdateCheckCapability(t *testing.T) {
	// No probe wired → capability off and route 501.
	services := &host.Services{Settings: testSettings{}}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/update", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unwired = %d %s", rec.Code, rec.Body.String())
	}
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"updateCheck":false`) {
		t.Fatalf("bootstrap unwired = %s", boot.Body.String())
	}

	gh := newFakeReleases(t, http.StatusOK, "v1.0.0")
	wired := newUpdateServer(t, "notify", gh, false)
	boot = httptest.NewRecorder()
	wired.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"updateCheck":true`) {
		t.Fatalf("bootstrap wired = %s", boot.Body.String())
	}
	if n := gh.hits.Load(); n != 0 {
		t.Fatalf("bootstrap probed (%d hits)", n)
	}
}

func TestSettingsDropsUpdateCheckForChangedDial(t *testing.T) {
	gh := newFakeReleases(t, http.StatusOK, "v1.0.0")
	srv := newUpdateServer(t, "notify", gh, false)
	srv.updateLast = &updateCheckResponse{Mode: "off", State: updateStateOff}
	if s := getSettings(t, srv); s.UpdateCheck != nil {
		t.Fatalf("stale off result surfaced under notify: %+v", s.UpdateCheck)
	}
	srv.updateLast = &updateCheckResponse{Mode: "notify", State: updateStateCurrent}
	if s := getSettings(t, srv); s.UpdateCheck == nil || s.UpdateCheck.State != updateStateCurrent {
		t.Fatalf("matching result dropped: %+v", s.UpdateCheck)
	}
}
