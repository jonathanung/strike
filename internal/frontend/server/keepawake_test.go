package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/daemon/keepawake"
	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/protocol"
)

// fakeInhibitor is an in-memory keepawake.Process so tests never touch host
// power state.
type fakeInhibitor struct {
	exit   chan error
	killed chan struct{}
	once   sync.Once
}

func (p *fakeInhibitor) Wait() error { return <-p.exit }
func (p *fakeInhibitor) Kill() error {
	p.once.Do(func() { close(p.killed) })
	return nil
}

type fakeInhibitStarter struct {
	mu    sync.Mutex
	procs []*fakeInhibitor
}

func (f *fakeInhibitStarter) start(string, []string) (keepawake.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &fakeInhibitor{exit: make(chan error, 1), killed: make(chan struct{})}
	f.procs = append(f.procs, p)
	return p, nil
}

func (f *fakeInhibitStarter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.procs)
}

// keepAwakeSettings is a stateful host.Settings whose Defaults reflect the
// last SaveKeepAwake, mirroring the config store the handler re-reads.
type keepAwakeSettings struct {
	testSettings
	mu   *sync.Mutex
	mode *string
}

func newKeepAwakeSettings() keepAwakeSettings {
	return keepAwakeSettings{mu: &sync.Mutex{}, mode: new(string)}
}

func (k keepAwakeSettings) Defaults() host.UserDefaults {
	k.mu.Lock()
	defer k.mu.Unlock()
	return host.UserDefaults{KeepAwake: *k.mode}
}

func (k keepAwakeSettings) SaveKeepAwake(mode string) error {
	if mode != "on" && mode != "off" {
		return errors.New("unknown keepAwake " + mode)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	*k.mode = mode
	return nil
}

// SaveConfigDials rejects notify "bogus" so a mixed patch can fail after
// keepAwake would otherwise have been written.
func (k keepAwakeSettings) SaveConfigDials(_, notify, _, _, _, _ string) error {
	if notify == "bogus" {
		return errors.New("unknown notify")
	}
	return nil
}

func newKeepAwakeService(fs *fakeInhibitStarter, lookPath func(string) (string, error)) *keepawake.Service {
	return keepawake.New(keepawake.Options{
		Backend:  &keepawake.Backend{Name: "fake-inhibit", Detail: "fake", Args: func(int) []string { return nil }},
		Start:    fs.start,
		LookPath: lookPath,
		PID:      1,
		Grace:    20 * time.Millisecond,
	})
}

// newKeepAwakeServer builds a live (mutable) server so PATCH /v1/settings is
// not refused as read-only attach mode.
func newKeepAwakeServer(t *testing.T, svc *keepawake.Service) *Server {
	t.Helper()
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Settings: newKeepAwakeSettings()},
		Live:       live,
		KeepAwake:  svc,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func gatewayBody(t *testing.T, srv *Server) string {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/gateway", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("gateway = %d %s", res.Code, res.Body.String())
	}
	return res.Body.String()
}

func patchSettings(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v1/settings", strings.NewReader(body)))
	return res
}

func TestSettingsKeepAwakeAppliesLiveAndReportsOnGateway(t *testing.T) {
	fs := &fakeInhibitStarter{}
	svc := newKeepAwakeService(fs, func(string) (string, error) { return "/bin/fake-inhibit", nil })
	t.Cleanup(svc.Close)
	srv := newKeepAwakeServer(t, svc)

	if body := gatewayBody(t, srv); !strings.Contains(body, `"keepAwake":{"enabled":false,"supported":true,"state":"off","backend":"fake-inhibit"}`) {
		t.Fatalf("initial gateway = %s", body)
	}

	res := patchSettings(t, srv, `{"keepAwake":"on"}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"keepAwake":"on"`) {
		t.Fatalf("patch on = %d %s", res.Code, res.Body.String())
	}
	if st := svc.Status(); st.State != keepawake.StateOn || fs.count() != 1 {
		t.Fatalf("service after on = %+v starts=%d", st, fs.count())
	}
	body := gatewayBody(t, srv)
	for _, want := range []string{`"enabled":true`, `"state":"on"`, `"reason":"fake"`, `"since":"`} {
		if !strings.Contains(body, want) {
			t.Errorf("gateway after on %s missing %s", body, want)
		}
	}

	// Unknown value: 400 and the held assertion is untouched.
	if res := patchSettings(t, srv, `{"keepAwake":"sometimes"}`); res.Code != http.StatusBadRequest {
		t.Fatalf("patch unknown = %d %s", res.Code, res.Body.String())
	}
	if st := svc.Status(); st.State != keepawake.StateOn {
		t.Fatalf("unknown value changed state: %+v", st)
	}

	res = patchSettings(t, srv, `{"keepAwake":"off"}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"keepAwake":"off"`) {
		t.Fatalf("patch off = %d %s", res.Code, res.Body.String())
	}
	select {
	case <-fs.procs[0].killed:
	default:
		t.Fatal("inhibitor not released on off")
	}
	if body := gatewayBody(t, srv); !strings.Contains(body, `"state":"off"`) {
		t.Fatalf("gateway after off = %s", body)
	}

	// Unrelated settings patches leave the service alone.
	if res := patchSettings(t, srv, `{"theme":"dracula"}`); res.Code != http.StatusOK {
		t.Fatalf("patch theme = %d %s", res.Code, res.Body.String())
	}
	if fs.count() != 1 {
		t.Fatalf("theme patch touched keep-awake: starts=%d", fs.count())
	}

	// A mixed patch that fails on another dial must not persist keepAwake,
	// or config and the live daemon would diverge until the next restart.
	if res := patchSettings(t, srv, `{"keepAwake":"on","notify":"bogus"}`); res.Code != http.StatusBadRequest {
		t.Fatalf("mixed patch = %d %s", res.Code, res.Body.String())
	}
	if got := srv.opts.Services.Settings.Defaults().KeepAwake; got != "off" {
		t.Fatalf("failed mixed patch persisted keepAwake=%q", got)
	}
	if st := svc.Status(); st.State != keepawake.StateOff || fs.count() != 1 {
		t.Fatalf("failed mixed patch touched service: %+v starts=%d", st, fs.count())
	}
}

func TestGatewayKeepAwakeUnsupportedIsHonest(t *testing.T) {
	fs := &fakeInhibitStarter{}
	svc := newKeepAwakeService(fs, func(string) (string, error) { return "", errors.New("not found") })
	srv := newKeepAwakeServer(t, svc)
	// The preference persists, but the effective state never claims "on".
	if res := patchSettings(t, srv, `{"keepAwake":"on"}`); res.Code != http.StatusOK {
		t.Fatalf("patch on = %d %s", res.Code, res.Body.String())
	}
	body := gatewayBody(t, srv)
	for _, want := range []string{`"enabled":true`, `"supported":false`, `"state":"unsupported"`, `not found in PATH`} {
		if !strings.Contains(body, want) {
			t.Errorf("gateway %s missing %s", body, want)
		}
	}
	if fs.count() != 0 {
		t.Fatalf("unsupported host started an inhibitor: %d", fs.count())
	}
}

func TestGatewayOmitsKeepAwakeWithoutService(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if body := gatewayBody(t, srv); strings.Contains(body, "keepAwake") {
		t.Fatalf("attach-only gateway advertises keepAwake: %s", body)
	}
}
