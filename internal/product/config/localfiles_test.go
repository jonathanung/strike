package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestServeReadsUsesLoaderPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	work := t.TempDir()

	files := ServeReads(work, work)
	got := map[string]LocalRead{}
	for _, f := range files {
		if _, dup := got[f.Path]; dup {
			t.Fatalf("duplicate path %s", f.Path)
		}
		got[f.Path] = f
		if strings.HasSuffix(f.Path, "onboarding.json") {
			t.Fatalf("serve does not read onboarding.json, listed %s", f.Path)
		}
	}

	want := []string{GlobalPath()}
	want = append(want, mcpFileCandidates(GlobalRoot())...)
	want = append(want, containerFileCandidates(GlobalRoot())...)
	want = append(want, providersFileCandidates(GlobalRoot())...)
	want = append(want, keybindsFileCandidates(GlobalRoot())...)
	want = append(want, globalInstructionCandidates()...)
	for _, src := range agentDiscoveryRoots(work) {
		want = append(want, src.dir)
	}
	for _, src := range skillDiscoveryRoots(work) {
		want = append(want, src.dir)
	}
	proj := projectRoot(work)
	want = append(want,
		filepath.Join(proj, "config"),
		filepath.Join(proj, "plugins"),
		filepath.Join(proj, "plugins.lock.json"),
		filepath.Join(proj, "themes"),
		filepath.Join(proj, "workflows"),
		filepath.Join(work, "AGENTS.md"),
		filepath.Join(work, "CLAUDE.md"),
	)
	want = append(want, managedFileCandidates(ManagedConfigPath())...)
	if dir := ManagedDropInDir(); dir != "" {
		want = append(want, dir)
	}
	for _, p := range want {
		if p == "" {
			continue
		}
		if _, ok := got[p]; !ok {
			t.Errorf("missing loader path %s", p)
		}
	}
	if _, ok := got[GlobalPath()]; !ok {
		t.Fatal("missing global config")
	}
	if got[GlobalPath()].Group != "config" {
		t.Fatalf("global config group %q", got[GlobalPath()].Group)
	}
}

func TestInstructionDirsStayInsideRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	dirs := instructionDirs(nested, root)
	if len(dirs) != 3 || dirs[0] != root || dirs[2] != nested {
		t.Fatalf("dirs %#v", dirs)
	}
	outside := filepath.Join(root, "..", "elsewhere")
	if got := instructionDirs(outside, root); len(got) != 0 {
		t.Fatalf("walk escaped root: %#v", got)
	}
}
