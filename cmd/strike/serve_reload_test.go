package main

import (
	"bytes"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

func TestFinishServe(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name       string
		err        error
		reexecErr  error
		wantCode   int
		wantReexec bool
		wantStderr string
	}{
		{name: "clean exit", err: nil, wantCode: 0},
		{name: "server closed", err: http.ErrServerClosed, wantCode: 0},
		{name: "failure", err: boom, wantCode: 1, wantStderr: "strike: boom"},
		{name: "reload re-execs", err: errServeReload, wantCode: 0, wantReexec: true, wantStderr: "restarting strike serve"},
		{name: "reload exec fails", err: errServeReload, reexecErr: boom, wantCode: 1, wantReexec: true, wantStderr: "restart failed"},
		// A gated reload (cleanup incomplete) must not re-exec.
		{name: "aborted reload exits", err: reloadGate(errServeReload, boom), wantCode: 1, wantStderr: "reload aborted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			called := false
			code := finishServe(tc.err, func() error {
				called = true
				return tc.reexecErr
			}, &stderr)
			if code != tc.wantCode {
				t.Errorf("code = %d, want %d", code, tc.wantCode)
			}
			if called != tc.wantReexec {
				t.Errorf("reexec called = %v, want %v", called, tc.wantReexec)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

func TestReloadGate(t *testing.T) {
	boom := errors.New("sessions did not stop")
	other := errors.New("listen failed")
	cases := []struct {
		name       string
		result     error
		cleanupErr error
		wantReload bool
		wantErr    error
	}{
		{name: "reload after clean shutdown", result: errServeReload, wantReload: true},
		{name: "reload blocked by incomplete shutdown", result: errServeReload, cleanupErr: boom, wantErr: boom},
		{name: "plain exit ignores cleanup error", result: nil, cleanupErr: boom},
		{name: "other error passes through", result: other, cleanupErr: boom, wantErr: other},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reloadGate(tc.result, tc.cleanupErr)
			if errors.Is(got, errServeReload) != tc.wantReload {
				t.Fatalf("reloadGate = %v, reload=%v want %v", got, errors.Is(got, errServeReload), tc.wantReload)
			}
			if tc.wantErr != nil && !errors.Is(got, tc.wantErr) {
				t.Fatalf("reloadGate = %v, want wrapping %v", got, tc.wantErr)
			}
			if tc.wantErr == nil && !tc.wantReload && got != nil {
				t.Fatalf("reloadGate = %v, want nil", got)
			}
		})
	}
}

func TestServeReloadable(t *testing.T) {
	if !serveReexecSupported {
		if serveReloadable(serveOptions{addr: "127.0.0.1:8787"}) {
			t.Fatal("reloadable without exec support")
		}
		t.Skip("no exec in place on this OS")
	}
	cases := []struct {
		name string
		opts serveOptions
		want bool
	}{
		{name: "no auth fixed port", opts: serveOptions{addr: "127.0.0.1:8787"}, want: true},
		{name: "token flag", opts: serveOptions{addr: "127.0.0.1:8787", auth: true, token: "t"}, want: true},
		{name: "token file", opts: serveOptions{addr: "127.0.0.1:8787", auth: true, tokenFile: "/x/token"}, want: true},
		{name: "in-memory minted token", opts: serveOptions{addr: "127.0.0.1:8787", auth: true}, want: false},
		{name: "random port", opts: serveOptions{addr: "127.0.0.1:0"}, want: false},
		{name: "unparseable addr", opts: serveOptions{addr: "127.0.0.1"}, want: false},
		{name: "attach-only", opts: serveOptions{addr: "127.0.0.1:8787", attachOnly: true}, want: false},
		{name: "read-only", opts: serveOptions{addr: "127.0.0.1:8787", readOnly: true}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveReloadable(tc.opts); got != tc.want {
				t.Fatalf("serveReloadable = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestReloadedAssemblyFollowsStoredTrust walks untrusted → granted → revoked
// the way a reload does: each step re-opens the trust file from disk, as the
// re-exec'd assemble() does, and loads config through the resulting scope.
// After the revoke no project MCP server is configured, so the reloaded
// daemon starts none.
func TestReloadedAssemblyFollowsStoredTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	repo := t.TempDir()
	writeRepoConfig(t, repo)
	storePath := workspacetrust.DefaultPath(filepath.Join(home, ".strike"))

	reassemble := func() (workspaceTrust, config.Config) {
		t.Helper()
		store, err := workspacetrust.Open(storePath)
		if err != nil {
			t.Fatal(err)
		}
		wt := resolveWorkspaceTrust(store, repo, repo)
		cfg, err := config.Load(wt.scope(repo))
		if err != nil {
			t.Fatal(err)
		}
		return wt, cfg
	}
	mutate := func(grant bool) {
		t.Helper()
		store, err := workspacetrust.Open(storePath)
		if err != nil {
			t.Fatal(err)
		}
		if grant {
			err = store.Trust(repo)
		} else {
			err = store.Revoke(repo)
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	steps := []struct {
		name    string
		grant   *bool
		trusted bool
		mcp     int
	}{
		{name: "untrusted start", trusted: false, mcp: 0},
		{name: "after grant", grant: boolPtr(true), trusted: true, mcp: 1},
		{name: "after revoke", grant: boolPtr(false), trusted: false, mcp: 0},
	}
	for _, st := range steps {
		if st.grant != nil {
			mutate(*st.grant)
		}
		wt, cfg := reassemble()
		if wt.trusted != st.trusted {
			t.Fatalf("%s: trusted = %v, want %v", st.name, wt.trusted, st.trusted)
		}
		if len(cfg.MCP.Servers) != st.mcp {
			t.Fatalf("%s: project MCP servers = %d, want %d", st.name, len(cfg.MCP.Servers), st.mcp)
		}
		if !st.trusted && len(cfg.Hooks) != 0 {
			t.Fatalf("%s: repo hooks loaded while untrusted: %+v", st.name, cfg.Hooks)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
