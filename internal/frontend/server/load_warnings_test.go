package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeLoadFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func bootstrapLoadWarnings(t *testing.T, work string) []loadWarning {
	t.Helper()
	srv, err := New(Options{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d %s", rr.Code, rr.Body.String())
	}
	var boot bootstrapResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	return boot.LoadWarnings
}

func TestBootstrapLoadWarningsNameBrokenFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()
	skill := filepath.Join(work, ".strike", "skills", "nodesc.md")
	writeLoadFixture(t, skill, "---\nname: nodesc\n---\nBody.\n")
	writeLoadFixture(t, filepath.Join(work, ".claude", "agents", "fast.md"), "---\neffort: warp\n---\nPrompt.\n")

	got := bootstrapLoadWarnings(t, work)
	byFile := map[string]loadWarning{}
	for _, w := range got {
		byFile[w.File] = w
	}
	s, ok := byFile["nodesc.md"]
	if !ok || s.Kind != "skill" || s.Code != "missing_description" || s.Fingerprint == "" || s.Message == "" {
		t.Fatalf("skill warning = %+v (all %+v)", s, got)
	}
	a, ok := byFile["fast.md"]
	if !ok || a.Kind != "agent" || a.Code != "skipped" || a.Fatal {
		t.Fatalf("agent warning = %+v (all %+v)", a, got)
	}

	// Fixing the files clears the warnings without a restart.
	writeLoadFixture(t, skill, "---\nname: nodesc\ndescription: now described\n---\nBody.\n")
	if err := os.Remove(filepath.Join(work, ".claude", "agents", "fast.md")); err != nil {
		t.Fatal(err)
	}
	if again := bootstrapLoadWarnings(t, work); len(again) != 0 {
		t.Fatalf("warnings after fix = %+v", again)
	}
}

func TestBootstrapLoadWarningsReportStrictFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()
	writeLoadFixture(t, filepath.Join(work, ".strike", "skills", "bad.md"), "---\nname: has space\ndescription: x\n---\nBody.\n")

	got := bootstrapLoadWarnings(t, work)
	if len(got) != 1 || got[0].Code != "invalid" || !got[0].Fatal || got[0].Severity != "error" {
		t.Fatalf("warnings = %+v", got)
	}
}

func TestWarningFileNamesDirectoryLayout(t *testing.T) {
	cases := map[string]string{
		"/w/.claude/skills/deploy/SKILL.md": "deploy/SKILL.md",
		"/w/.strike/skills/note.md":         "note.md",
		"/w/.opencode/skills/x/skill.md":    "x/skill.md",
	}
	for in, want := range cases {
		if got := warningFile(in); got != want {
			t.Errorf("warningFile(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadWarningsPreferLiveCWD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	daemonDir, rootDir := t.TempDir(), t.TempDir()
	writeLoadFixture(t, filepath.Join(rootDir, ".strike", "skills", "nodesc.md"), "---\nname: nodesc\n---\nBody.\n")
	srv, err := New(Options{WorkDir: daemonDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := srv.loadWarnings(""); len(got) != 0 {
		t.Fatalf("daemon dir warnings = %+v", got)
	}
	got := srv.loadWarnings(rootDir)
	if len(got) != 1 || got[0].File != "nodesc.md" {
		t.Fatalf("root cwd warnings = %+v", got)
	}
	if statusCWD(nil) != "" || statusCWD(&StatusSnapshot{CWD: rootDir}) != rootDir {
		t.Fatal("statusCWD")
	}
}
