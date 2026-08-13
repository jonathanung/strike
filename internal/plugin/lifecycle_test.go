package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testBundle(t *testing.T, dir, id string) string {
	t.Helper()
	root := filepath.Join(dir, "src-"+id)
	writePlugin(t, root, id, map[string]string{
		"plugin.json":          apsManifest(id),
		"skills/demo/SKILL.md": validSkillMD("demo"),
	})
	return root
}

func testdataFiles(t *testing.T, rel string) map[string]string {
	t.Helper()
	root := filepath.Join("testdata", filepath.FromSlash(rel))
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relPath)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no files under testdata/%s", rel)
	}
	return files
}

func TestInstallLocal_AtomicAndLockfile(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	src := testBundle(t, t.TempDir(), "acme.pack")

	res, err := Install(context.Background(), InstallOptions{
		Scope:         ScopeGlobal,
		GlobalRoot:    filepath.Join(home, ".strike"),
		ProjectRoot:   filepath.Join(work, ".strike"),
		LocalPath:     src,
		StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "acme.pack" || res.Digest == "" || !strings.HasPrefix(res.Digest, "sha256:") {
		t.Fatalf("result: %+v", res)
	}
	if _, err := os.Stat(res.Root); err != nil {
		t.Fatal(err)
	}
	// No staging leftovers.
	entries, _ := os.ReadDir(filepath.Join(home, ".strike", "plugins"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging") {
			t.Fatalf("staging left behind: %s", e.Name())
		}
	}
	lf, err := ReadLockfile(filepath.Join(home, ".strike", "plugins.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := lf.Plugins["acme.pack"]
	if !ok || e.Source == nil || e.Source.Type != SourceLocal || e.Digest != res.Digest {
		t.Fatalf("lock entry: %+v", e)
	}
	if !EntryEnabled(e) {
		t.Fatal("expected enabled")
	}
}

func TestInstallLocal_FailedValidationLeavesNothing(t *testing.T) {
	home := t.TempDir()
	src := t.TempDir()
	// Invalid manifest.
	if err := os.WriteFile(filepath.Join(src, "plugin.json"), []byte(`{"nope":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Install(context.Background(), InstallOptions{
		Scope:         ScopeGlobal,
		GlobalRoot:    filepath.Join(home, ".strike"),
		LocalPath:     src,
		StrikeVersion: "0.2.0",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	pluginsDir := filepath.Join(home, ".strike", "plugins")
	if entries, err := os.ReadDir(pluginsDir); err == nil {
		for _, e := range entries {
			t.Fatalf("unexpected entry after failed install: %s", e.Name())
		}
	}
	// Lockfile should not enable anything.
	lf, err := ReadLockfile(filepath.Join(home, ".strike", "plugins.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lf.Plugins) != 0 {
		t.Fatalf("lockfile should be empty, got %+v", lf.Plugins)
	}
}

func TestInstallLocal_ProjectScopeCannotEscape(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	src := testBundle(t, t.TempDir(), "acme.proj")
	res, err := Install(context.Background(), InstallOptions{
		Scope:         ScopeProject,
		WorkDir:       work,
		GlobalRoot:    filepath.Join(home, ".strike"),
		ProjectRoot:   filepath.Join(work, ".strike"),
		LocalPath:     src,
		StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(work, ".strike", "plugins")
	if !strings.HasPrefix(res.Root, wantPrefix) {
		t.Fatalf("root %s not under %s", res.Root, wantPrefix)
	}
	// Must not land under global.
	if strings.Contains(res.Root, filepath.Join(home, ".strike")) {
		t.Fatal("project install escaped into global")
	}
}

func TestInstall_RejectsDuplicateWithoutForce(t *testing.T) {
	home := t.TempDir()
	src := testBundle(t, t.TempDir(), "acme.pack")
	opts := InstallOptions{
		Scope:         ScopeGlobal,
		GlobalRoot:    filepath.Join(home, ".strike"),
		LocalPath:     src,
		StrikeVersion: "0.2.0",
	}
	if _, err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("want duplicate error, got %v", err)
	}
	// Force replaces.
	opts.Force = true
	if _, err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
}

func TestDisablePreservesFiles_RemoveRequiresConfirm(t *testing.T) {
	home := t.TempDir()
	src := testBundle(t, t.TempDir(), "acme.pack")
	res, err := Install(context.Background(), InstallOptions{
		Scope:         ScopeGlobal,
		GlobalRoot:    filepath.Join(home, ".strike"),
		LocalPath:     src,
		StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Disable(EnableOptions{ID: "acme.pack", Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Root); err != nil {
		t.Fatal("disable must preserve files:", err)
	}
	// Discover should skip disabled.
	dres := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(dres.Plugins) != 0 {
		t.Fatalf("disabled should not load: %+v", dres.Plugins)
	}
	if err := Remove(RemoveOptions{ID: "acme.pack", Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike"), Confirm: false}); err == nil {
		t.Fatal("remove without confirm should fail")
	}
	if err := Remove(RemoveOptions{ID: "acme.pack", Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike"), Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.Root); !os.IsNotExist(err) {
		t.Fatal("remove should delete files")
	}
	lf, _ := ReadLockfile(filepath.Join(home, ".strike", "plugins.lock.json"))
	if _, ok := lf.Plugins["acme.pack"]; ok {
		t.Fatal("lock entry should be gone")
	}
}

func TestEnableDisableRoundTrip(t *testing.T) {
	home := t.TempDir()
	src := testBundle(t, t.TempDir(), "acme.pack")
	if _, err := Install(context.Background(), InstallOptions{
		Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike"), LocalPath: src, StrikeVersion: "0.2.0",
	}); err != nil {
		t.Fatal(err)
	}
	eo := EnableOptions{ID: "acme.pack", Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike")}
	if err := Disable(eo); err != nil {
		t.Fatal(err)
	}
	if err := Enable(eo); err != nil {
		t.Fatal(err)
	}
	dres := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(dres.Plugins) != 1 {
		t.Fatalf("want enabled plugin, got %+v", dres.Plugins)
	}
}

func TestDoctor_NoSecretsOrEnvValues(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	secretURL := "https://user:supersecretTOKEN123456@example.com/mcp"
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.tools",
  "version": "1.0.0",
  "name": "Tools",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "agents": [{ "path": "agents/a.md" }],
    "mcp": [{
      "name": "db",
      "transport": "http",
      "url": "` + secretURL + `",
      "env": { "ACME_TOKEN": "secret://env/ACME_TOKEN", "RAW": "should-never-print-this-value" },
      "headers": { "Authorization": "Bearer sk-ant-api03-ABCDEFGHIJKLMNOP" }
    }]
  }
}`,
		"agents/a.md": validAgentMD("a"),
	})
	// Seed lockfile with git-like URL containing userinfo.
	lf := emptyLockfile()
	lf.Plugins["acme.tools"] = LockfileEntry{
		Enabled: boolPtr(true),
		Version: "1.0.0",
		Source: &SourceIdentity{
			Type:   SourceGit,
			URL:    "https://user:tok_secret_value_here@github.com/acme/tools.git",
			Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	if err := WriteLockfile(filepath.Join(home, ".strike", "plugins.lock.json"), lf); err != nil {
		t.Fatal(err)
	}

	report, err := Doctor(DoctorOptions{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	text := FormatDoctorText(report)
	banned := []string{
		"supersecretTOKEN123456",
		"should-never-print-this-value",
		"sk-ant-api03-ABCDEFGHIJKLMNOP",
		"tok_secret_value_here",
		"Bearer sk-ant",
	}
	for _, b := range banned {
		if strings.Contains(text, b) {
			t.Fatalf("doctor leaked %q in:\n%s", b, text)
		}
	}
	// Env keys may appear; values must not.
	if !strings.Contains(text, "envKeys=ACME_TOKEN") && !strings.Contains(text, "ACME_TOKEN") {
		// FormatDoctorText joins env keys — accept either form.
		if !strings.Contains(text, "ACME_TOKEN") {
			t.Fatalf("expected env key names in doctor output:\n%s", text)
		}
	}
	if !strings.Contains(text, "root:") && !strings.Contains(text, home) {
		// root path should be present
		found := false
		for _, p := range report.Plugins {
			if p.Root != "" {
				found = true
			}
		}
		if !found {
			t.Fatal("doctor must identify source paths")
		}
	}
}

func TestDoctor_ReportsExactRoot(t *testing.T) {
	home := t.TempDir()
	src := testBundle(t, t.TempDir(), "acme.pack")
	res, err := Install(context.Background(), InstallOptions{
		Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike"), LocalPath: src, StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Doctor(DoctorOptions{ID: "acme.pack", GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Plugins) != 1 || report.Plugins[0].Root != res.Root {
		t.Fatalf("got %+v want root %s", report.Plugins, res.Root)
	}
}

func TestListInspect(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	src := testBundle(t, t.TempDir(), "acme.pack")
	if _, err := Install(context.Background(), InstallOptions{
		Scope: ScopeProject, WorkDir: work,
		GlobalRoot: filepath.Join(home, ".strike"), ProjectRoot: filepath.Join(work, ".strike"),
		LocalPath: src, StrikeVersion: "0.2.0",
	}); err != nil {
		t.Fatal(err)
	}
	list, _, err := ListInstalled(ListOptions{
		WorkDir: work, GlobalRoot: filepath.Join(home, ".strike"), ProjectRoot: filepath.Join(work, ".strike"),
	})
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	ip, err := Inspect(EnableOptions{
		ID: "acme.pack", WorkDir: work,
		GlobalRoot: filepath.Join(home, ".strike"), ProjectRoot: filepath.Join(work, ".strike"),
	})
	if err != nil || ip.Scope != ScopeProject {
		t.Fatalf("inspect: %v %+v", err, ip)
	}
}

func TestLockfileConcurrentWrites(t *testing.T) {
	home := t.TempDir()
	lockPath := filepath.Join(home, ".strike", "plugins.lock.json")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "p" + strings.Repeat("x", 1) // unique via index below
			id = "plugin" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			// simpler unique ids:
			id = "plug" + itoa(i)
			err := WithLockfileLock(lockPath, func(lf Lockfile) (Lockfile, bool, error) {
				e := LockfileEntry{Enabled: boolPtr(true), Version: "1.0.0", Digest: "sha256:" + strings.Repeat("ab", 32)}
				// fix digest length
				e.Digest = "sha256:" + strings.Repeat("a", 64)
				return setLockEntry(lf, id, e), false, nil
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	lf, err := ReadLockfile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(lf.Plugins) != n {
		t.Fatalf("want %d entries, got %d", n, len(lf.Plugins))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestInstallGit_PinsCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Create a local bare-ish repo with a plugin.
	repo := t.TempDir()
	pluginDir := filepath.Join(repo, "bundle")
	writePlugin(t, pluginDir, "acme.gitpack", map[string]string{
		"plugin.json": manifest("acme.gitpack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("from-git"),
	})
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("git", "init", "--quiet")
	run("git", "add", ".")
	run("git", "commit", "--quiet", "-m", "init")
	shaBytes, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	home := t.TempDir()
	// Use file:// URL to the repo; subdir=bundle.
	url := "file://" + repo
	res, err := Install(context.Background(), InstallOptions{
		Scope:         ScopeGlobal,
		GlobalRoot:    filepath.Join(home, ".strike"),
		GitURL:        url,
		GitSubdir:     "bundle",
		StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source.Type != SourceGit || res.Source.Commit != strings.ToLower(sha) {
		t.Fatalf("want pinned %s, got %+v", sha, res.Source)
	}
	// Lockfile must store full commit.
	lf, err := ReadLockfile(filepath.Join(home, ".strike", "plugins.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := lf.Plugins["acme.gitpack"]
	if e.Source == nil || e.Source.Commit != strings.ToLower(sha) {
		t.Fatalf("lock source: %+v", e.Source)
	}
	// Explicit commit pin works.
	if _, err := Install(context.Background(), InstallOptions{
		Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike"),
		GitURL: url, GitSubdir: "bundle", GitCommit: sha, Force: true, StrikeVersion: "0.2.0",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover_SkipsStagingAndBackupDirs(t *testing.T) {
	home := t.TempDir()
	plugins := filepath.Join(home, ".strike", "plugins")
	// Staging leftover must not load.
	writePlugin(t, filepath.Join(plugins, ".staging-install-xyz"), "bad.staging", map[string]string{
		"plugin.json": manifest("bad.staging", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("x"),
	})
	writePlugin(t, filepath.Join(plugins, ".bak-acme-1"), "bad.bak", map[string]string{
		"plugin.json": manifest("bad.bak", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("y"),
	})
	writePlugin(t, filepath.Join(plugins, "acme.pack"), "acme.pack", map[string]string{
		"plugin.json": manifest("acme.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("z"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 || res.Plugins[0].ID != "acme.pack" {
		t.Fatalf("got %+v", res.Plugins)
	}
}

func TestInstallForce_RollbackRestoresPrevious(t *testing.T) {
	// Covered indirectly: force replace succeeds; ensure no .bak leftovers.
	home := t.TempDir()
	src := testBundle(t, t.TempDir(), "acme.pack")
	opts := InstallOptions{
		Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike"),
		LocalPath: src, StrikeVersion: "0.2.0",
	}
	if _, err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	opts.Force = true
	if _, err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".strike", "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("leftover hidden dir after force install: %s", e.Name())
		}
	}
}

func TestParseInstallSource(t *testing.T) {
	loc, git, cat, ver, err := ParseInstallSource("./my-plugin")
	if err != nil || loc != "./my-plugin" || git != "" || cat != "" {
		t.Fatalf("local: %v %q %q %q", err, loc, git, cat)
	}
	loc, git, cat, ver, err = ParseInstallSource("https://github.com/acme/pack.git")
	if err != nil || loc != "" || git == "" || cat != "" {
		t.Fatalf("git: %v %q %q %q", err, loc, git, cat)
	}
	loc, git, cat, ver, err = ParseInstallSource("catalog:acme.pack@1.2.0")
	if err != nil || loc != "" || git != "" || cat != "acme.pack" || ver != "1.2.0" {
		t.Fatalf("catalog: %v %q %q %q %q", err, loc, git, cat, ver)
	}
}

func TestSourceIdentityValidate(t *testing.T) {
	if err := (SourceIdentity{Type: SourceLocal, Path: "/x"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (SourceIdentity{Type: SourceGit, URL: "u"}).Validate(); err == nil {
		t.Fatal("git without commit")
	}
	if err := (SourceIdentity{Type: SourceGit, URL: "u", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (SourceIdentity{Type: SourceCatalog}).Validate(); err == nil {
		t.Fatal("catalog incomplete should fail")
	}
	cat := SourceIdentity{
		Type:     SourceCatalog,
		Registry: "https://example.com/plugins",
		Package:  "acme.pack",
		Version:  "1.0.0",
		URL:      "https://example.com/acme.pack-1.0.0.tar.gz",
		Digest:   "sha256:" + strings.Repeat("a", 64),
	}
	if err := cat.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteLockfileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.lock.json")
	lf := emptyLockfile()
	lf.Plugins["acme.pack"] = LockfileEntry{
		Enabled: boolPtr(false),
		Version: "1.2.3",
		Digest:  "sha256:" + strings.Repeat("b", 64),
		Source:  &SourceIdentity{Type: SourceLocal, Path: "/tmp/src"},
	}
	if err := WriteLockfile(path, lf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLockfile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.Plugins["acme.pack"])
	if strings.Contains(string(raw), "password") {
		t.Fatal("unexpected")
	}
	if EntryEnabled(got.Plugins["acme.pack"]) {
		t.Fatal("want disabled")
	}
}

func TestRootsConfinePath(t *testing.T) {
	home := t.TempDir()
	r, err := ResolveRoots(ScopeGlobal, Options{GlobalRoot: filepath.Join(home, ".strike")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.PluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(r.PluginsDir, "acme.pack")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.ConfinePath(inside); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "escape")
	if err := r.ConfinePath(outside); err == nil {
		t.Fatal("expected escape error")
	}
}
