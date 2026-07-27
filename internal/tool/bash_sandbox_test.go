package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckBashWorkspaceBoundaryAllowsInside(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds := []string{
		"rm a.txt",
		"rm -rf build",
		"rm -rf ./build/tmp",
		"rmdir empty",
		"mv a.txt b.txt",
		"chmod 644 a.txt",
		"chown user a.txt",
		"rm -- -weird",
		"ls -la", // non-destructive
		"echo hi && rm a.txt",
		"cd sub && rm file.txt",
	}
	for _, c := range cmds {
		if err := checkBashWorkspaceBoundary(c, root); err != nil {
			t.Fatalf("check(%q) = %v, want nil", c, err)
		}
	}
}

func TestCheckBashWorkspaceBoundaryBlocksOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmds := []string{
		"rm -rf " + outside,
		"rm -rf " + secret,
		"rm " + secret,
		"/bin/rm -rf " + outside,
		"rm -rf /tmp/outside-workspace-strike-test",
		"rm -rf /home",
		"rm -rf /",
		"rm -rf /*",
		"mv a.txt " + filepath.Join(outside, "out.txt"),
		"mv " + secret + " ./in.txt",
		"chmod 777 " + secret,
		"chown root " + secret,
		"rmdir " + outside,
		"unlink " + secret,
		"shred " + secret,
		"cd " + outside + " && rm -rf secret.txt",
		"cd /tmp && rm -rf outside-workspace-strike-test",
		"rm -rf ../" + filepath.Base(outside),
	}
	for _, c := range cmds {
		err := checkBashWorkspaceBoundary(c, root)
		if err == nil {
			t.Fatalf("check(%q) = nil, want error", c)
		}
		if !strings.Contains(err.Error(), "escapes") &&
			!strings.Contains(err.Error(), "critical") &&
			!strings.Contains(err.Error(), "not statically bound") {
			t.Fatalf("check(%q) = %v, want boundary/critical error", c, err)
		}
	}
}

func TestCheckBashWorkspaceBoundaryBlocksSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "leak")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	// Recursive rm through dir symlink would delete outside contents.
	err := checkBashWorkspaceBoundary("rm -rf leak/secret.txt", root)
	if err == nil {
		t.Fatal("expected symlink escape block")
	}
}

func TestCheckBashWorkspaceBoundaryBlocksVariables(t *testing.T) {
	root := t.TempDir()
	err := checkBashWorkspaceBoundary("rm -rf $HOME", root)
	if err == nil {
		t.Fatal("expected variable path block")
	}
	err = checkBashWorkspaceBoundary("rm -rf $(pwd)/../outside", root)
	if err == nil {
		t.Fatal("expected substitution path block")
	}
}

func TestCheckBashWorkspaceBoundaryYoloStillBlocked(t *testing.T) {
	// allowAll Ask is the yolo / --dangerously-skip-permissions analogue at
	// the tool layer; boundary check must run before execution regardless.
	root := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep-me")
	if err := os.WriteFile(marker, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(root)
	_, err := NewBash().Execute(t.Context(), mustJSON(t, map[string]any{
		"command": "rm -rf " + outside,
	}), tc)
	if err == nil {
		t.Fatal("yolo bash rm outside: want error")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("outside marker removed despite sandbox: %v", statErr)
	}
}

func TestBashRmInsideWorkspaceStillWorks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "doomed.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(root)
	res, err := NewBash().Execute(t.Context(), mustJSON(t, map[string]any{
		"command": "rm -f doomed.txt",
	}), tc)
	if err != nil {
		t.Fatalf("rm inside workspace: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("file still exists; output=%q", res.Output)
	}
}

func TestSplitBashStatements(t *testing.T) {
	got := splitBashStatements(`echo a && rm b; ls | wc || true`)
	// ; && || | are all separators → echo a, rm b, ls, wc, true
	if len(got) != 5 {
		t.Fatalf("parts = %#v", got)
	}
	// Quoted separator stays intact.
	got = splitBashStatements(`echo "a && b"; rm x`)
	if len(got) != 2 || !strings.Contains(got[0], "&&") {
		t.Fatalf("quoted = %#v", got)
	}
}

func TestIsDangerousRemovalPath(t *testing.T) {
	if !isDangerousRemovalPath("/") {
		t.Fatal("/ should be dangerous")
	}
	if !isDangerousRemovalPath("/tmp") {
		t.Fatal("/tmp should be dangerous")
	}
	if !isDangerousRemovalPath("/home") {
		t.Fatal("/home should be dangerous")
	}
	home, _ := os.UserHomeDir()
	if home != "" && !isDangerousRemovalPath(home) {
		t.Fatalf("home %q should be dangerous", home)
	}
	// Nested under /tmp is not a root child (parent is /tmp, not /).
	if isDangerousRemovalPath(filepath.Join("/tmp", "project-build")) {
		t.Fatal("nested path should not be critical-system")
	}
}
