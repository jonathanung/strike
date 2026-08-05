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
		"cp a.txt b.txt",
		"tee out.txt",
		"truncate -s 0 a.txt",
		"ln -s a.txt link.txt",
		"install a.txt dest.txt",
		"dd if=a.txt of=b.txt",
		"sed -i s/a/b/ a.txt",
		"perl -i -pe s/a/b/ a.txt",
		"rsync --delete ./src/ ./dst/",
		"echo hi > out.txt",
		"echo hi >> out.txt",
		"echo hi 2> err.txt",
		"bash -c 'rm a.txt'",
		"sh -c \"rm ./build\"",
		"env rm a.txt",
		"nohup rm a.txt",
		"nice rm a.txt",
		"timeout 5 rm a.txt",
		"xargs rm a.txt",
		"find . -name '*.o' -delete",
		"find build -exec rm {} ;",
		"echo $(echo hi)",
		"echo `echo hi`",
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
	// the tool layer; the static path guard still runs before execution for
	// known destructive forms (incomplete — not a security boundary).
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
		t.Fatalf("outside marker removed despite path guard: %v", statErr)
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

func TestCheckBashGuardRedirections(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outFile := filepath.Join(outside, "out.txt")

	block := []string{
		"echo hi > " + outFile,
		"echo hi >> " + outFile,
		"echo hi 2> " + outFile,
		"echo hi 2>> " + outFile,
		"echo hi >" + outFile,
		"echo hi &> " + outFile,
		"echo hi &>> " + outFile,
		"echo hi 1>" + outFile,
		// redirection alone
		"> " + outFile,
	}
	for _, c := range block {
		if err := checkBashWorkspaceBoundary(c, root); err == nil {
			t.Fatalf("check(%q) = nil, want error", c)
		}
	}

	// Fd-to-fd dup is not a path write.
	if err := checkBashWorkspaceBoundary("echo hi 2>&1", root); err != nil {
		t.Fatalf("2>&1 should be allowed: %v", err)
	}
	if err := checkBashWorkspaceBoundary("echo hi > out.txt 2>&1", root); err != nil {
		t.Fatalf("in-workspace redir + fd dup: %v", err)
	}
}

func TestCheckBashGuardNewDestructiveCmds(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")

	block := []string{
		"cp a.txt " + secret,
		"cp " + secret + " ./in.txt",
		"dd if=a.txt of=" + secret,
		"truncate -s 0 " + secret,
		"tee " + secret,
		"install a.txt " + secret,
		"ln -s a.txt " + filepath.Join(outside, "link"),
		"ln " + secret + " ./link",
		"sed -i s/a/b/ " + secret,
		"sed -i.bak s/a/b/ " + secret,
		"perl -i -pe s/a/b/ " + secret,
		"perl -pi -e s/a/b/ " + secret,
		"rsync --delete ./src/ " + outside + "/",
		"rsync --delete-after " + outside + "/ ./dst/",
	}
	for _, c := range block {
		if err := checkBashWorkspaceBoundary(c, root); err == nil {
			t.Fatalf("check(%q) = nil, want error", c)
		}
	}

	// rsync without --delete is not treated as destructive by this guard.
	if err := checkBashWorkspaceBoundary("rsync ./src/ "+outside+"/", root); err != nil {
		t.Fatalf("rsync without --delete should pass guard: %v", err)
	}
	// sed without -i is not in-place destructive.
	if err := checkBashWorkspaceBoundary("sed s/a/b/ "+secret, root); err != nil {
		t.Fatalf("sed without -i should pass guard: %v", err)
	}
}

func TestCheckBashGuardWrappers(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")

	block := []string{
		"bash -c 'rm -rf " + secret + "'",
		`bash -c "rm -rf ` + secret + `"`,
		"sh -c 'rm " + secret + "'",
		"zsh -c 'rm " + secret + "'",
		"env rm " + secret,
		"env -i PATH=/bin rm " + secret,
		"eval rm " + secret,
		`eval "rm ` + secret + `"`,
		"nohup rm " + secret,
		"nice rm " + secret,
		"nice -n 10 rm " + secret,
		"timeout 5 rm " + secret,
		"timeout --signal=KILL 1s rm " + secret,
		"xargs rm " + secret,
		"xargs -n 1 rm " + secret,
	}
	for _, c := range block {
		if err := checkBashWorkspaceBoundary(c, root); err == nil {
			t.Fatalf("check(%q) = nil, want error", c)
		}
	}

	// Nested wrapper chain.
	if err := checkBashWorkspaceBoundary("timeout 2 env bash -c 'rm "+secret+"'", root); err == nil {
		t.Fatal("nested wrappers: want error")
	}
}

func TestCheckBashGuardFind(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	block := []string{
		"find " + outside + " -delete",
		"find " + outside + " -name '*.txt' -delete",
		"find " + outside + " -exec rm {} ;",
		"find " + outside + " -exec rm -rf {} +",
		"find " + outside + " -ok unlink {} ;",
		"find . -exec rm " + filepath.Join(outside, "x") + " ;",
	}
	for _, c := range block {
		if err := checkBashWorkspaceBoundary(c, root); err == nil {
			t.Fatalf("check(%q) = nil, want error", c)
		}
	}

	// find without destructive action is allowed even outside (read-only scan).
	if err := checkBashWorkspaceBoundary("find "+outside+" -name '*.txt' -print", root); err != nil {
		t.Fatalf("find -print outside should pass: %v", err)
	}
	if err := checkBashWorkspaceBoundary("find . -name '*.o' -delete", root); err != nil {
		t.Fatalf("find -delete inside: %v", err)
	}
}

func TestCheckBashGuardInterpreterOneLiners(t *testing.T) {
	root := t.TempDir()
	block := []string{
		`python -c 'open("/tmp/x","w").write("x")'`,
		`python3 -c "print(1)"`,
		`node -e 'require("fs").rmSync("/tmp/x")'`,
		`node -p "1+1"`,
		`ruby -e 'File.delete("/tmp/x")'`,
		`perl -e 'unlink "/tmp/x"'`,
		`php -r 'unlink("/tmp/x");'`,
	}
	for _, c := range block {
		err := checkBashWorkspaceBoundary(c, root)
		if err == nil {
			t.Fatalf("check(%q) = nil, want error", c)
		}
		if !strings.Contains(err.Error(), "not statically bound") &&
			!strings.Contains(err.Error(), "one-liner") {
			t.Fatalf("check(%q) = %v, want unbounded one-liner error", c, err)
		}
	}

	// Running a script file is not a one-liner.
	if err := checkBashWorkspaceBoundary("python script.py", root); err != nil {
		t.Fatalf("python script.py should pass: %v", err)
	}
	if err := checkBashWorkspaceBoundary("node app.js", root); err != nil {
		t.Fatalf("node app.js should pass: %v", err)
	}
}

func TestCheckBashGuardCommandSubstitution(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")

	block := []string{
		"echo $(rm " + secret + ")",
		"echo `rm " + secret + "`",
		`echo "$(rm ` + secret + `)"`,
		"echo $(bash -c 'rm " + secret + "')",
		"true && echo $(rm " + secret + ")",
	}
	for _, c := range block {
		if err := checkBashWorkspaceBoundary(c, root); err == nil {
			t.Fatalf("check(%q) = nil, want error", c)
		}
	}

	// Substitution with safe inner command.
	if err := checkBashWorkspaceBoundary("echo $(echo hi)", root); err != nil {
		t.Fatalf("safe substitution: %v", err)
	}
}

func TestPeelRedirections(t *testing.T) {
	paths, rest := peelRedirections(`echo hi > /tmp/out`)
	if len(paths) != 1 || paths[0] != "/tmp/out" {
		t.Fatalf("paths=%v rest=%q", paths, rest)
	}
	if strings.Contains(rest, ">") || strings.Contains(rest, "/tmp/out") {
		t.Fatalf("rest still has redir: %q", rest)
	}
	words := shellWords(rest)
	if len(words) < 2 || words[0] != "echo" {
		t.Fatalf("words=%v", words)
	}

	paths, _ = peelRedirections(`cmd 2>&1`)
	if len(paths) != 0 {
		t.Fatalf("fd dup paths=%v", paths)
	}

	paths, rest = peelRedirections(`echo "a > b" >out`)
	if len(paths) != 1 || paths[0] != "out" {
		t.Fatalf("quoted paths=%v rest=%q", paths, rest)
	}
}

func TestExtractCommandSubstitutions(t *testing.T) {
	got := extractCommandSubstitutions(`echo $(rm x) and $(echo y)`)
	if len(got) != 2 || got[0] != "rm x" || got[1] != "echo y" {
		t.Fatalf("got=%v", got)
	}
	got = extractCommandSubstitutions("echo `rm x`")
	if len(got) != 1 || got[0] != "rm x" {
		t.Fatalf("backtick got=%v", got)
	}
	// Nested $()
	got = extractCommandSubstitutions(`echo $(echo $(rm x))`)
	if len(got) != 1 || got[0] != "echo $(rm x)" {
		t.Fatalf("nested outer=%v", got)
	}
	// Single-quoted $() is literal — not expanded by shell, so not extracted.
	got = extractCommandSubstitutions(`echo '$(rm x)'`)
	if len(got) != 0 {
		t.Fatalf("single-quoted sub should be ignored: %v", got)
	}
}
