package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/protocol"
)

func initRequest(t *testing.T, srv *Server, method, path string) (int, projectInitResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("{}")))
	var body projectInitResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func bootstrapProjectInit(t *testing.T, srv *Server) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	var boot struct {
		Capabilities capabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatalf("bootstrap: %v (%s)", err, rec.Body.String())
	}
	return boot.Capabilities.ProjectInit
}

func TestProjectInitCreatesThenRefusesOverwrite(t *testing.T) {
	work := t.TempDir()
	live := NewLive("sess-init-1", work, nil, make(chan protocol.Op))
	defer live.Close()
	srv, err := New(Options{
		SessionDir:     t.TempDir(),
		Live:           live,
		Services:       &host.Services{Init: local.NewProjectInit(t.TempDir())},
		ProjectInitFor: local.NewProjectInit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrapProjectInit(t, srv) {
		t.Fatal("bootstrap projectInit = false, want true with service + live")
	}
	want := filepath.Join(work, "AGENTS.md")

	code, got := initRequest(t, srv, http.MethodGet, "/v1/init")
	if code != http.StatusOK || got.Exists || got.Path != want {
		t.Fatalf("GET before = %d %+v, want 200 exists=false path=%s", code, got, want)
	}

	code, got = initRequest(t, srv, http.MethodPost, "/v1/init")
	if code != http.StatusCreated || !got.Created || got.Path != want {
		t.Fatalf("POST = %d %+v, want 201 created path=%s", code, got, want)
	}
	data, err := os.ReadFile(want)
	if err != nil || len(data) == 0 {
		t.Fatalf("AGENTS.md not written in live root: %v (%d bytes)", err, len(data))
	}

	code, got = initRequest(t, srv, http.MethodGet, "/v1/init")
	if code != http.StatusOK || !got.Exists {
		t.Fatalf("GET after = %d %+v, want exists=true", code, got)
	}

	// Second POST refuses and leaves the file untouched.
	if err := os.WriteFile(want, []byte("# hand edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, got = initRequest(t, srv, http.MethodPost, "/v1/init")
	if code != http.StatusConflict || !got.Exists || got.Path != want || got.Error == "" {
		t.Fatalf("POST again = %d %+v, want 409 exists", code, got)
	}
	if after, _ := os.ReadFile(want); string(after) != "# hand edited\n" {
		t.Fatalf("existing AGENTS.md clobbered: %q", after)
	}
}

func TestProjectInitFallsBackToDaemonService(t *testing.T) {
	daemonDir := t.TempDir()
	live := NewLive("sess-init-2", t.TempDir(), nil, make(chan protocol.Op))
	defer live.Close()
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Init: local.NewProjectInit(daemonDir)},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, got := initRequest(t, srv, http.MethodPost, "/v1/init")
	if code != http.StatusCreated || got.Path != filepath.Join(daemonDir, "AGENTS.md") {
		t.Fatalf("POST = %d %+v, want daemon dir target", code, got)
	}
}

func TestProjectInitRootScoped(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	live1 := NewLive("sess-r1", dir1, nil, make(chan protocol.Op))
	live2 := NewLive("sess-r2", dir2, nil, make(chan protocol.Op))
	defer live1.Close()
	defer live2.Close()
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live1)
	hub.Add("r2", live2)
	srv, err := New(Options{
		SessionDir:     t.TempDir(),
		LiveHub:        hub,
		Services:       &host.Services{Init: local.NewProjectInit(t.TempDir())},
		ProjectInitFor: local.NewProjectInit,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, got := initRequest(t, srv, http.MethodPost, "/v1/init?root=r2")
	if code != http.StatusCreated || got.Path != filepath.Join(dir2, "AGENTS.md") {
		t.Fatalf("POST root=r2 = %d %+v", code, got)
	}
	if _, err := os.Stat(filepath.Join(dir1, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root r1 must be untouched, stat err = %v", err)
	}
	code, _ = initRequest(t, srv, http.MethodPost, "/v1/init?root=nope")
	if code != http.StatusNotFound {
		t.Fatalf("POST unknown root = %d, want 404", code)
	}
	code, _ = initRequest(t, srv, http.MethodGet, "/v1/init?root=nope")
	if code != http.StatusNotFound {
		t.Fatalf("GET unknown root = %d, want 404", code)
	}
}

func TestProjectInitUnavailable(t *testing.T) {
	work := t.TempDir()
	newLive := func() *Live {
		l := NewLive("sess-init-x", work, nil, make(chan protocol.Op))
		t.Cleanup(l.Close)
		return l
	}
	cases := []struct {
		name     string
		opts     Options
		getCode  int
		postCode int
	}{
		{
			name:     "no services",
			opts:     Options{Live: newLive()},
			getCode:  http.StatusNotImplemented,
			postCode: http.StatusNotImplemented,
		},
		{
			name:     "service absent",
			opts:     Options{Live: newLive(), Services: &host.Services{}},
			getCode:  http.StatusNotImplemented,
			postCode: http.StatusNotImplemented,
		},
		{
			name:     "attach-only",
			opts:     Options{Services: &host.Services{Init: local.NewProjectInit(work)}},
			getCode:  http.StatusOK,
			postCode: http.StatusForbidden,
		},
		{
			name:     "read-only",
			opts:     Options{Live: newLive(), ReadOnly: true, Services: &host.Services{Init: local.NewProjectInit(work)}},
			getCode:  http.StatusOK,
			postCode: http.StatusForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.SessionDir = t.TempDir()
			srv, err := New(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if bootstrapProjectInit(t, srv) {
				t.Fatal("bootstrap projectInit = true, want false")
			}
			if code, _ := initRequest(t, srv, http.MethodGet, "/v1/init"); code != tc.getCode {
				t.Fatalf("GET = %d, want %d", code, tc.getCode)
			}
			if code, _ := initRequest(t, srv, http.MethodPost, "/v1/init"); code != tc.postCode {
				t.Fatalf("POST = %d, want %d", code, tc.postCode)
			}
			if _, err := os.Stat(filepath.Join(work, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("AGENTS.md written despite refusal: %v", err)
			}
		})
	}
}
