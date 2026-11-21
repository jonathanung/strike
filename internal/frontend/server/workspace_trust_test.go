package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// writeTrustRepo lays out a repo shipping a project config, an MCP server
// (with an env secret that must never reach the browser) and steering.
func writeTrustRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	files := map[string]string{
		".strike/config":    `{"systemPrompt": "repo", "hooks": [{"event": "pre_tool_use", "command": "echo hook"}]}`,
		".strike/mcp.jsonc": `{"evil": {"command": "npx", "args": ["-y", "evil"], "env": {"SECRET": "hunter2"}}}`,
		"AGENTS.md":         "repo steering",
	}
	for rel, body := range files {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

type trustFixture struct {
	srv       *Server
	repo      string
	storePath string
}

func newTrustFixture(t *testing.T, readOnly bool, assembled func(repo string) *WorkspaceTrustAssembly) trustFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := writeTrustRepo(t)
	storePath := filepath.Join(t.TempDir(), "workspace-trust.json")
	live := NewLive("sess-trust", repo, nil, make(chan protocol.Op))
	t.Cleanup(live.Close)
	var asm *WorkspaceTrustAssembly
	if assembled != nil {
		asm = assembled(repo)
	}
	srv, err := New(Options{
		SessionDir:              t.TempDir(),
		Live:                    live,
		ReadOnly:                readOnly,
		WorkspaceTrustPath:      storePath,
		WorkspaceTrustAssembled: asm,
	})
	if err != nil {
		t.Fatal(err)
	}
	return trustFixture{srv: srv, repo: repo, storePath: storePath}
}

func trustRequest(t *testing.T, srv *Server, method, path, body string) (int, workspaceTrustResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out workspaceTrustResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func trustCanonical(t *testing.T, dir string) string {
	t.Helper()
	c, err := workspacetrust.CanonicalRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWorkspaceTrustGetListsSurfacesUntrusted(t *testing.T) {
	f := newTrustFixture(t, false, nil)
	code, got, raw := trustRequest(t, f.srv, http.MethodGet, "/v1/workspace/trust", "")
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, raw)
	}
	if got.Trusted || got.TrustedAt != "" {
		t.Fatalf("trusted = %v %q, want untrusted", got.Trusted, got.TrustedAt)
	}
	if got.Root != trustCanonical(t, f.repo) {
		t.Fatalf("root = %q, want %q", got.Root, trustCanonical(t, f.repo))
	}
	if !got.Mutable {
		t.Fatal("mutable = false, want true with live root")
	}
	if len(got.Surfaces.MCP) != 1 || got.Surfaces.MCP[0].Name != "evil" || got.Surfaces.MCP[0].Command != "npx" {
		t.Fatalf("mcp surfaces = %+v", got.Surfaces.MCP)
	}
	if len(got.Surfaces.Hooks) != 1 || len(got.Surfaces.Steering) != 1 {
		t.Fatalf("hooks/steering = %+v / %+v", got.Surfaces.Hooks, got.Surfaces.Steering)
	}
	if !strings.Contains(got.Summary, "MCP server") {
		t.Fatalf("summary = %q", got.Summary)
	}
	if strings.Contains(raw, "hunter2") || strings.Contains(raw, "SECRET") {
		t.Fatalf("response leaks MCP env: %s", raw)
	}
	if _, err := os.Stat(f.storePath); !os.IsNotExist(err) {
		t.Fatalf("GET wrote the trust store: %v", err)
	}
}

func TestWorkspaceTrustGrantThenRevoke(t *testing.T) {
	f := newTrustFixture(t, false, nil)
	code, got, raw := trustRequest(t, f.srv, http.MethodPost, "/v1/workspace/trust", `{"action":"grant"}`)
	if code != http.StatusOK || !got.Trusted || got.TrustedAt == "" {
		t.Fatalf("grant = %d %s", code, raw)
	}
	store, err := workspacetrust.Open(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Trusted(f.repo) {
		t.Fatal("store does not record the grant")
	}
	code, got, raw = trustRequest(t, f.srv, http.MethodGet, "/v1/workspace/trust", "")
	if code != http.StatusOK || !got.Trusted {
		t.Fatalf("GET after grant = %d %s", code, raw)
	}

	code, got, raw = trustRequest(t, f.srv, http.MethodPost, "/v1/workspace/trust", `{"action":"revoke"}`)
	if code != http.StatusOK || got.Trusted {
		t.Fatalf("revoke = %d %s", code, raw)
	}
	store, err = workspacetrust.Open(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	if store.Trusted(f.repo) {
		t.Fatal("store still trusts after revoke")
	}
}

func TestWorkspaceTrustRestartRequired(t *testing.T) {
	f := newTrustFixture(t, false, func(repo string) *WorkspaceTrustAssembly {
		return &WorkspaceTrustAssembly{Dir: repo, Root: trustCanonical(t, repo), Trusted: false}
	})

	_, got, _ := trustRequest(t, f.srv, http.MethodGet, "/v1/workspace/trust", "")
	if got.RestartRequired {
		t.Fatal("restartRequired before any change")
	}
	_, got, _ = trustRequest(t, f.srv, http.MethodPost, "/v1/workspace/trust", `{"action":"grant"}`)
	if !got.RestartRequired {
		t.Fatal("restartRequired = false after grant on an untrusted assembly")
	}
	_, got, _ = trustRequest(t, f.srv, http.MethodPost, "/v1/workspace/trust", `{"action":"revoke"}`)
	if got.RestartRequired {
		t.Fatal("restartRequired = true after reverting to the assembled state")
	}
}

// A session bound to a strike worktree has a CWD outside the launch repo,
// but it runs on the daemon's one assembly: trust must key on the
// assembled workspace, not the worktree path.
func TestWorkspaceTrustUsesAssembledWorkspaceOverRootCWD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	launch := writeTrustRepo(t)
	worktree := t.TempDir()
	live := NewLive("sess-wt", worktree, nil, make(chan protocol.Op))
	defer live.Close()
	storePath := filepath.Join(t.TempDir(), "workspace-trust.json")
	srv, err := New(Options{
		SessionDir:              t.TempDir(),
		Live:                    live,
		WorkspaceTrustPath:      storePath,
		WorkspaceTrustAssembled: &WorkspaceTrustAssembly{Dir: launch, Root: trustCanonical(t, launch)},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, got, raw := trustRequest(t, srv, http.MethodPost, "/v1/workspace/trust", `{"action":"grant"}`)
	if code != http.StatusOK || got.Root != trustCanonical(t, launch) || len(got.Surfaces.MCP) != 1 {
		t.Fatalf("grant = %d %s, want the launch workspace", code, raw)
	}
	store, err := workspacetrust.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Trusted(launch) || store.Trusted(worktree) {
		t.Fatalf("trusted roots = %v, want only %s", store.Roots(), launch)
	}
}

func TestWorkspaceTrustPostRefusals(t *testing.T) {
	cases := []struct {
		name     string
		readOnly bool
		body     string
		want     int
	}{
		{name: "read-only daemon", readOnly: true, body: `{"action":"grant"}`, want: http.StatusForbidden},
		{name: "unknown action", body: `{"action":"allow-all"}`, want: http.StatusBadRequest},
		{name: "empty action", body: `{}`, want: http.StatusBadRequest},
		{name: "path in body is rejected", body: `{"action":"grant","root":"/"}`, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTrustFixture(t, tc.readOnly, nil)
			code, _, raw := trustRequest(t, f.srv, http.MethodPost, "/v1/workspace/trust", tc.body)
			if code != tc.want {
				t.Fatalf("POST = %d %s, want %d", code, raw, tc.want)
			}
			if _, err := os.Stat(f.storePath); !os.IsNotExist(err) {
				t.Fatalf("refused POST wrote the trust store: %v", err)
			}
		})
	}
}

func TestWorkspaceTrustPostAttachOnlyRefused(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir(), WorkDir: t.TempDir(), WorkspaceTrustPath: filepath.Join(t.TempDir(), "workspace-trust.json")})
	if err != nil {
		t.Fatal(err)
	}
	code, _, raw := trustRequest(t, srv, http.MethodPost, "/v1/workspace/trust", `{"action":"grant"}`)
	if code != http.StatusForbidden {
		t.Fatalf("attach-only POST = %d %s, want 403", code, raw)
	}
	code, got, raw := trustRequest(t, srv, http.MethodGet, "/v1/workspace/trust", "")
	if code != http.StatusOK || got.Mutable {
		t.Fatalf("attach-only GET = %d %s, want 200 mutable=false", code, raw)
	}
}

func TestWorkspaceTrustHomeIsUntrustable(t *testing.T) {
	f := newTrustFixture(t, false, nil)
	t.Setenv("HOME", f.repo)
	code, got, raw := trustRequest(t, f.srv, http.MethodPost, "/v1/workspace/trust", `{"action":"grant"}`)
	if code != http.StatusUnprocessableEntity || got.Trusted || !got.Untrustable || got.Error == "" {
		t.Fatalf("grant home = %d %s, want 422 untrustable", code, raw)
	}
}

func TestWorkspaceTrustUnwired(t *testing.T) {
	live := NewLive("sess-trust-off", t.TempDir(), nil, make(chan protocol.Op))
	defer live.Close()
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		code, _, raw := trustRequest(t, srv, method, "/v1/workspace/trust", `{"action":"grant"}`)
		if code != http.StatusNotImplemented {
			t.Fatalf("%s unwired = %d %s, want 501", method, code, raw)
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	var boot struct {
		Capabilities capabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if boot.Capabilities.WorkspaceTrust {
		t.Fatal("bootstrap workspaceTrust = true without a store path")
	}
}

func TestWorkspaceTrustRootScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo1, repo2 := writeTrustRepo(t), t.TempDir()
	live1 := NewLive("sess-t1", repo1, nil, make(chan protocol.Op))
	live2 := NewLive("sess-t2", repo2, nil, make(chan protocol.Op))
	defer live1.Close()
	defer live2.Close()
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live1)
	hub.Add("r2", live2)
	storePath := filepath.Join(t.TempDir(), "workspace-trust.json")
	srv, err := New(Options{SessionDir: t.TempDir(), LiveHub: hub, WorkspaceTrustPath: storePath})
	if err != nil {
		t.Fatal(err)
	}
	code, got, raw := trustRequest(t, srv, http.MethodPost, "/v1/workspace/trust?root=r2", `{"action":"grant"}`)
	if code != http.StatusOK || got.Root != trustCanonical(t, repo2) {
		t.Fatalf("grant r2 = %d %s", code, raw)
	}
	_, got, _ = trustRequest(t, srv, http.MethodGet, "/v1/workspace/trust?root=r1", "")
	if got.Trusted || got.Root != trustCanonical(t, repo1) {
		t.Fatalf("r1 = %+v, want untrusted repo1 (grant was for r2)", got)
	}
	code, _, _ = trustRequest(t, srv, http.MethodGet, "/v1/workspace/trust?root=nope", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown root = %d, want 404", code)
	}
}
