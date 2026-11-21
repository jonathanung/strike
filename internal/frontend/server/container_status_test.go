package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike/internal/integrate/container"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// fakeContainerEngine scripts the docker CLI the route shells out to.
type fakeContainerEngine struct {
	found     bool
	infoCode  int
	inspect   string // "" → no such container
	labels    string
	mu        sync.Mutex
	binaries  []string
	inspected bool
	targets   []string
}

func (f *fakeContainerEngine) runtime(binary string) *container.CLI {
	f.mu.Lock()
	f.binaries = append(f.binaries, binary)
	f.mu.Unlock()
	cli := container.NewCLI(binary)
	cli.LookPath = func(name string) (string, error) {
		if !f.found {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + filepath.Base(name), nil
	}
	cli.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
		if len(args) == 0 {
			return "", "", 1, nil
		}
		switch args[0] {
		case "info":
			if f.infoCode != 0 {
				return "", "Cannot connect to the Docker daemon", f.infoCode, nil
			}
			return "27.0.1\n", "", 0, nil
		case "inspect":
			f.mu.Lock()
			f.inspected = true
			f.targets = append(f.targets, args[len(args)-1])
			f.mu.Unlock()
			if f.inspect == "" {
				return "", "Error: No such object: x", 1, nil
			}
			if len(args) > 2 && strings.Contains(args[2], ".Config.Labels") {
				return f.labels, "", 0, nil
			}
			return f.inspect, "", 0, nil
		}
		return "", "unexpected", 1, nil
	}
	return cli
}

func containerRequest(t *testing.T, srv *Server, path string) (int, containerStatusResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var out containerStatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func newContainerServer(t *testing.T, work string, eng *fakeContainerEngine, sandboxMode string) *Server {
	t.Helper()
	live := NewLive("sess-container", work, nil, make(chan protocol.Op))
	t.Cleanup(live.Close)
	opts := Options{SessionDir: t.TempDir(), Live: live, ContainerRuntime: eng.runtime}
	if sandboxMode != "" {
		opts.Sandbox = &SandboxSnapshot{Mode: sandboxMode}
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func activeRungs(ladder []containerRung) []string {
	var out []string
	for _, r := range ladder {
		if r.Active {
			out = append(out, r.Label)
		}
	}
	return out
}

func TestContainerStatusStates(t *testing.T) {
	cases := []struct {
		name           string
		eng            *fakeContainerEngine
		wantFound      bool
		wantAvailable  bool
		wantStatus     string
		wantRunning    bool
		wantRuntimeErr bool
	}{
		{name: "no runtime installed", eng: &fakeContainerEngine{found: false}, wantStatus: "absent", wantRuntimeErr: true},
		{name: "engine daemon down", eng: &fakeContainerEngine{found: true, infoCode: 1}, wantFound: true, wantStatus: "absent", wantRuntimeErr: true},
		{name: "runtime up, no container", eng: &fakeContainerEngine{found: true}, wantFound: true, wantAvailable: true, wantStatus: "absent"},
		{
			name:          "runtime up, container running",
			eng:           &fakeContainerEngine{found: true, inspect: "abc123|/strike-repo|true|running|strike/img:1|0|false|", labels: `{"com.strike.managed":"true","com.strike.config-hash":"deadbeef"}`},
			wantFound:     true,
			wantAvailable: true,
			wantStatus:    "running",
			wantRunning:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv(protocol.IsolationEnvKey, "")
			work := t.TempDir()
			eng := tc.eng
			srv := newContainerServer(t, work, eng, "off")
			code, got, raw := containerRequest(t, srv, "/v1/container/status")
			if code != http.StatusOK {
				t.Fatalf("GET = %d %s", code, raw)
			}
			if got.Runtime.Found != tc.wantFound || got.Runtime.Available != tc.wantAvailable {
				t.Fatalf("runtime = %+v, want found=%v available=%v", got.Runtime, tc.wantFound, tc.wantAvailable)
			}
			if (got.Runtime.Error != "") != tc.wantRuntimeErr {
				t.Fatalf("runtime error = %q, want error=%v", got.Runtime.Error, tc.wantRuntimeErr)
			}
			if got.Container.Status != tc.wantStatus || got.Container.Running != tc.wantRunning {
				t.Fatalf("container = %+v, want status=%s running=%v", got.Container, tc.wantStatus, tc.wantRunning)
			}
			if got.Container.Name == "" || got.Container.SessionModel == "" {
				t.Fatalf("container name/session model empty: %+v", got.Container)
			}
			if !tc.wantAvailable && eng.inspected {
				t.Fatal("inspected a container with no available engine")
			}
			if tc.wantRunning && (got.Container.ID != "abc123" || got.Container.Compatible == nil) {
				t.Fatalf("running container detail = %+v", got.Container)
			}
			// Host mode is a first-class posture, never an error (#1290).
			if got.Posture != protocol.IsolationHostDefault || got.PostureSource != "dials" {
				t.Fatalf("posture = %q (%s), want host+default from dials", got.Posture, got.PostureSource)
			}
			if len(got.Ladder) != 5 {
				t.Fatalf("ladder = %d rungs, want 5", len(got.Ladder))
			}
			if a := activeRungs(got.Ladder); len(a) != 1 || a[0] != protocol.IsolationHostDefault {
				t.Fatalf("active rungs = %v", a)
			}
			if got.Drift.State != "not-ejected" || got.Drift.Path != container.DefaultEjectName {
				t.Fatalf("drift = %+v, want not-ejected", got.Drift)
			}
			if got.ConfigScope != "global" {
				t.Fatalf("configScope = %q, want global for an untrusted workspace", got.ConfigScope)
			}
		})
	}
}

func TestContainerStatusPostureFromEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(protocol.IsolationEnvKey, "container+no-network")
	srv := newContainerServer(t, t.TempDir(), &fakeContainerEngine{}, "workspace-write")
	_, got, raw := containerRequest(t, srv, "/v1/container/status")
	if got.Posture != protocol.IsolationContainerNoNet || got.PostureSource != "env" {
		t.Fatalf("posture = %q (%s): %s", got.Posture, got.PostureSource, raw)
	}
	if a := activeRungs(got.Ladder); len(a) != 1 || a[0] != protocol.IsolationContainerNoNet {
		t.Fatalf("active rungs = %v", a)
	}
}

func TestContainerStatusSandboxDialIsHostSandbox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(protocol.IsolationEnvKey, "")
	srv := newContainerServer(t, t.TempDir(), &fakeContainerEngine{}, "workspace-write")
	_, got, _ := containerRequest(t, srv, "/v1/container/status")
	if got.Posture != protocol.IsolationHostSandbox || got.SandboxMode != "workspace-write" {
		t.Fatalf("posture = %q sandbox=%q, want host+sandbox", got.Posture, got.SandboxMode)
	}
}

// The container config names the engine binary the route executes, so an
// untrusted repo's container.jsonc must not pick it (#1352).
func TestContainerStatusEngineFromRepoNeedsTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(protocol.IsolationEnvKey, "")
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".strike", "container.jsonc"), []byte(`{"engine": "/repo/evil-engine"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := &fakeContainerEngine{found: true}
	srv := newContainerServer(t, work, eng, "off")
	_, got, _ := containerRequest(t, srv, "/v1/container/status")
	if got.ConfigScope != "global" {
		t.Fatalf("untrusted configScope = %q", got.ConfigScope)
	}
	for _, b := range eng.binaries {
		if b == "/repo/evil-engine" {
			t.Fatal("untrusted repo chose the engine binary")
		}
	}

	id, err := project.Resolve(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	store, err := workspacetrust.Open(workspacetrust.DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Trust(id.Root); err != nil {
		t.Fatal(err)
	}
	eng2 := &fakeContainerEngine{found: true}
	srv2 := newContainerServer(t, work, eng2, "off")
	_, got, _ = containerRequest(t, srv2, "/v1/container/status")
	if got.ConfigScope != "project" {
		t.Fatalf("trusted configScope = %q, want project", got.ConfigScope)
	}
	if len(eng2.binaries) == 0 || eng2.binaries[len(eng2.binaries)-1] != "/repo/evil-engine" {
		t.Fatalf("trusted engine binaries = %v, want the repo's engine", eng2.binaries)
	}
}

func TestContainerStatusDrifted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(protocol.IsolationEnvKey, "")
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, container.DefaultEjectName), []byte("FROM scratch\n# strike-config-hash: 0000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newContainerServer(t, work, &fakeContainerEngine{}, "off")
	_, got, raw := containerRequest(t, srv, "/v1/container/status")
	if got.Drift.State != "drifted" || got.Drift.Want == "" {
		t.Fatalf("drift = %+v (%s), want drifted with a want hash", got.Drift, raw)
	}
}

func TestContainerStatusDetectsManifests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(protocol.IsolationEnvKey, "")
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newContainerServer(t, work, &fakeContainerEngine{}, "off")
	_, got, _ := containerRequest(t, srv, "/v1/container/status")
	if !got.Detect.Go || len(got.Detect.Markers) == 0 {
		t.Fatalf("detect = %+v, want go.mod detected", got.Detect)
	}
	if got.Workspace != work {
		t.Fatalf("workspace = %q, want %q", got.Workspace, work)
	}
}

func TestContainerStatusCapabilityAndRoots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := containerRequest(t, srv, "/v1/container/status")
	if code != http.StatusNotImplemented {
		t.Fatalf("no workspace = %d, want 501", code)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	var boot struct {
		Capabilities capabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if boot.Capabilities.Container {
		t.Fatal("bootstrap container = true with no workspace")
	}

	// Attach-only daemons still answer (read-only route) from WorkDir.
	attach, err := New(Options{SessionDir: t.TempDir(), WorkDir: t.TempDir(), ContainerRuntime: (&fakeContainerEngine{}).runtime})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, raw := containerRequest(t, attach, "/v1/container/status"); code != http.StatusOK {
		t.Fatalf("attach-only = %d %s, want 200", code, raw)
	}

	live := NewLive("sess-c1", t.TempDir(), nil, make(chan protocol.Op))
	defer live.Close()
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live)
	multi, err := New(Options{SessionDir: t.TempDir(), LiveHub: hub, ContainerRuntime: (&fakeContainerEngine{}).runtime})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := containerRequest(t, multi, "/v1/container/status?root=nope"); code != http.StatusNotFound {
		t.Fatalf("unknown root = %d, want 404", code)
	}
	if code, _, raw := containerRequest(t, multi, "/v1/container/status?root=r1"); code != http.StatusOK {
		t.Fatalf("root r1 = %d %s", code, raw)
	}
}

// The container id cache is inside the repo: an untrusted workspace must not
// redirect the panel to another container through it.
func TestContainerStatusUntrustedIgnoresRepoContainerCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(protocol.IsolationEnvKey, "")
	work := t.TempDir()
	cacheDir := filepath.Join(work, filepath.FromSlash(container.CacheDirName))
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "container_id"), []byte("victim-container\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := &fakeContainerEngine{found: true}
	srv := newContainerServer(t, work, eng, "off")
	if code, _, raw := containerRequest(t, srv, "/v1/container/status"); code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, raw)
	}
	want := container.ContainerName(work)
	if len(eng.targets) == 0 {
		t.Fatal("no inspect issued")
	}
	for _, target := range eng.targets {
		if target != want {
			t.Fatalf("inspected %q, want only %q", target, want)
		}
	}
}
