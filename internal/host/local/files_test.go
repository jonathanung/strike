package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
)

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink creation is unsupported: %v", err)
	}
}

func TestResolveUnderRootUsesPhysicalRootForRelativePath(t *testing.T) {
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "work")
	mustSymlink(t, realRoot, linkRoot)

	resolved, rel, err := resolveUnderRoot(linkRoot, "backup/data.json")
	if err != nil {
		t.Fatal(err)
	}
	physicalRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(physicalRoot, "backup", "data.json")
	if resolved != want || rel != "backup/data.json" {
		t.Fatalf("resolveUnderRoot() = (%q, %q), want (%q, %q)", resolved, rel, want, "backup/data.json")
	}
}

func TestNewFilesReadFile(t *testing.T) {
	work := t.TempDir()
	relContent := []byte("relative hello")
	if err := os.WriteFile(filepath.Join(work, "notes.md"), relContent, 0o644); err != nil {
		t.Fatal(err)
	}
	absDir := t.TempDir()
	absPath := filepath.Join(absDir, "abs.md")
	absContent := []byte("absolute hello")
	if err := os.WriteFile(absPath, absContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Nested file reachable via ".." from a path under work.
	if err := os.WriteFile(filepath.Join(work, "parent.md"), []byte("via dots"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(work, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	oversize := make([]byte, maxFileBytes+1)
	for i := range oversize {
		oversize[i] = 'x'
	}
	oversizePath := filepath.Join(work, "big.bin")
	if err := os.WriteFile(oversizePath, oversize, 0o644); err != nil {
		t.Fatal(err)
	}

	files := NewFiles(work)

	tests := []struct {
		name    string
		path    string
		want    []byte
		wantErr string
	}{
		{
			name: "relative path under workdir",
			path: "notes.md",
			want: relContent,
		},
		{
			name: "absolute path",
			path: absPath,
			want: absContent,
		},
		{
			name:    "missing file",
			path:    "missing.md",
			wantErr: "file not found",
		},
		{
			name:    "directory",
			path:    "subdir",
			wantErr: "not a regular file",
		},
		{
			name:    "empty path",
			path:    "",
			wantErr: "path is empty",
		},
		{
			name:    "whitespace-only path",
			path:    "   ",
			wantErr: "path is empty",
		},
		{
			name:    "oversize file",
			path:    "big.bin",
			wantErr: "1MB limit",
		},
		{
			name: "path with dots stays readable",
			path: filepath.Join("nested", "..", "parent.md"),
			want: []byte("via dots"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := files.ReadFile(tt.path)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ReadFile(%q) = %q, nil; want error containing %q", tt.path, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ReadFile(%q) error = %q, want substring %q", tt.path, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", tt.path, err)
			}
			if string(got) != string(tt.want) {
				t.Errorf("ReadFile(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestReadFileRejectsNonRegular(t *testing.T) {
	work := t.TempDir()
	fifo := filepath.Join(work, "pipe.fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	files := NewFiles(work)
	_, err := files.ReadFile("pipe.fifo")
	if err == nil {
		t.Fatal("ReadFile(fifo) = nil error, want not a regular file")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("ReadFile(fifo) error = %q, want substring %q", err, "not a regular file")
	}
}

func TestListDir(t *testing.T) {
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "Zed.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "alpha.go"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "pkg", "main.go"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	absOther := t.TempDir()
	if err := os.WriteFile(filepath.Join(absOther, "out.txt"), []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := NewFiles(work)

	root, err := files.ListDir("")
	if err != nil {
		t.Fatalf("ListDir(\"\") error = %v", err)
	}
	// dirs first, then files case-insensitive.
	wantNames := []string{"pkg", "alpha.go", "Zed.txt"}
	if len(root) != len(wantNames) {
		t.Fatalf("ListDir root = %#v, want names %v", root, wantNames)
	}
	for i, name := range wantNames {
		if root[i].Name != name {
			t.Errorf("root[%d].Name = %q, want %q", i, root[i].Name, name)
		}
	}
	if !root[0].IsDir || root[1].IsDir || root[2].IsDir {
		t.Errorf("IsDir flags = %#v", root)
	}

	nested, err := files.ListDir("pkg")
	if err != nil {
		t.Fatalf("ListDir(pkg) error = %v", err)
	}
	if len(nested) != 1 || nested[0].Name != "main.go" || nested[0].IsDir {
		t.Errorf("ListDir(pkg) = %#v", nested)
	}

	absEntries, err := files.ListDir(absOther)
	if err != nil {
		t.Fatalf("ListDir(abs) error = %v", err)
	}
	if len(absEntries) != 1 || absEntries[0].Name != "out.txt" {
		t.Errorf("ListDir(abs) = %#v", absEntries)
	}

	if _, err := files.ListDir("missing"); err == nil || !strings.Contains(err.Error(), "directory not found") {
		t.Errorf("ListDir(missing) err = %v, want directory not found", err)
	}
	if _, err := files.ListDir("alpha.go"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("ListDir(file) err = %v, want not a directory", err)
	}
}

func TestListDirEmptyWorkDirRequiresPath(t *testing.T) {
	files := NewFiles("")
	if _, err := files.ListDir(""); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Errorf("ListDir(\"\") err = %v, want path is empty", err)
	}
}

func TestSearchFilesRanksAndSkipsHeavyDirs(t *testing.T) {
	work := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha.go", "a")
	write("internal/tui/app.go", "app")
	write("internal/tui/completion.go", "c")
	write("node_modules/pkg/index.js", "skip")
	write(".git/config", "skip")
	write(".plan/features.md", "noise")

	files := NewFiles(work)
	got, err := files.SearchFiles("app", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0] != "internal/tui/app.go" {
		t.Fatalf("SearchFiles(app) = %v, want internal/tui/app.go first", got)
	}
	for _, p := range got {
		if strings.Contains(p, "node_modules") || strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, ".plan/") {
			t.Fatalf("SearchFiles leaked skipped path %q in %v", p, got)
		}
	}

	all, err := files.SearchFiles("", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if strings.Contains(p, "node_modules") || strings.Contains(p, ".git") || strings.Contains(p, ".plan") {
			t.Fatalf("index includes skipped %q: %v", p, all)
		}
	}
	// Files + parent dirs (internal/, internal/tui/).
	wantFiles := map[string]bool{
		"alpha.go": true, "internal/": true, "internal/tui/": true,
		"internal/tui/app.go": true, "internal/tui/completion.go": true,
	}
	if len(all) != len(wantFiles) {
		t.Fatalf("index = %v, want %d entries %v", all, len(wantFiles), wantFiles)
	}
	for _, p := range all {
		if !wantFiles[p] {
			t.Fatalf("unexpected index entry %q in %v", p, all)
		}
	}
}

func TestSearchFilesSkipsPlanUnlessQueried(t *testing.T) {
	work := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/tui/app.go", "app")
	write(".plan/features.md", "plan")
	write(".plan/notes.txt", "n")

	files := NewFiles(work)
	got, err := files.SearchFiles("fea", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if strings.Contains(p, ".plan") {
			t.Fatalf("SearchFiles(fea) included .plan noise: %v", got)
		}
	}

	// Exact path still resolves even when outside the fuzzy index.
	got, err = files.SearchFiles(".plan/features.md", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0] != ".plan/features.md" {
		t.Fatalf("exact .plan path = %v, want [.plan/features.md]", got)
	}
}

func TestSearchFilesExactPathHitNested(t *testing.T) {
	work := t.TempDir()
	p := filepath.Join(work, "internal", "tui", "app.go")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("package tui"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Flood index with other matches so exact path would miss top-k without boost.
	for i := 0; i < 40; i++ {
		name := filepath.Join(work, "noise", fmt.Sprintf("app_extra_%02d.go", i))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := NewFiles(work)
	got, err := files.SearchFiles("internal/tui/app.go", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0] != "internal/tui/app.go" {
		t.Fatalf("exact nested path = %v, want internal/tui/app.go first", got)
	}
}

func TestSearchFilesDirectories(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "pkg", "main.go"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)
	got, err := files.SearchFiles("pkg", 20)
	if err != nil {
		t.Fatal(err)
	}
	var sawDir, sawFile bool
	for _, p := range got {
		if p == "pkg/" {
			sawDir = true
		}
		if p == "pkg/main.go" {
			sawFile = true
		}
	}
	if !sawDir || !sawFile {
		t.Fatalf("SearchFiles(pkg) = %v, want pkg/ and pkg/main.go", got)
	}
}

func TestReadScopedDirectoryListing(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "pkg", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "pkg", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "pkg", "nested", "deep.go"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)
	fc, err := files.ReadScoped("pkg/")
	if err != nil {
		t.Fatal(err)
	}
	if fc.Skip {
		t.Fatalf("ReadScoped(pkg/) skip: %+v", fc)
	}
	if fc.Path != "pkg/" {
		t.Fatalf("Path = %q, want pkg/", fc.Path)
	}
	if !strings.Contains(fc.Content, "main.go") || !strings.Contains(fc.Content, "nested/") {
		t.Fatalf("listing missing children: %q", fc.Content)
	}
	if strings.Contains(fc.Content, "deep.go") {
		t.Fatalf("listing must be immediate children only: %q", fc.Content)
	}
	if !strings.Contains(fc.Content, "directory listing") {
		t.Fatalf("listing missing policy header: %q", fc.Content)
	}
}

func TestSearchFilesSkipsDirectorySymlinkEscape(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "ok.go"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, filepath.Join(work, "leak"))

	files := NewFiles(work)
	got, err := files.SearchFiles("", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if strings.Contains(p, "secret") || strings.HasPrefix(p, "leak/") {
			t.Fatalf("SearchFiles followed dir symlink: %v", got)
		}
	}
	if len(got) != 1 || got[0] != "ok.go" {
		t.Fatalf("SearchFiles = %v, want [ok.go]", got)
	}
}

func TestReadScopedHappyPath(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "pkg", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)
	fc, err := files.ReadScoped("pkg/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if fc.Skip || fc.Path != "pkg/main.go" || fc.Content != "package main\n" {
		t.Fatalf("ReadScoped = %+v", fc)
	}
}

func TestReadScopedRejectsDotDotEscape(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nested dir so ../.. leaves work.
	if err := os.Mkdir(filepath.Join(work, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)

	// Relative escape toward a sibling temp dir is hard to express portably;
	// ".." from work root must fail.
	fc, err := files.ReadScoped("../" + filepath.Base(outside) + "/secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !fc.Skip || !strings.Contains(fc.Notice, "escapes") {
		// Also accept when the cleaned path simply does not exist under root.
		if !fc.Skip {
			t.Fatalf("ReadScoped(.. escape) = %+v, want Skip", fc)
		}
	}
	if strings.Contains(fc.Content, "classified") {
		t.Fatalf("leaked outside content: %+v", fc)
	}

	// Absolute path outside work.
	fc, err = files.ReadScoped(secret)
	if err != nil {
		t.Fatal(err)
	}
	if !fc.Skip || strings.Contains(fc.Content, "classified") {
		t.Fatalf("ReadScoped(abs outside) = %+v, want skip without content", fc)
	}
}

func TestReadScopedRejectsSymlinkEscape(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, secret, filepath.Join(work, "link.txt"))
	// Directory symlink that points outside.
	mustSymlink(t, outside, filepath.Join(work, "outlink"))

	files := NewFiles(work)
	for _, p := range []string{"link.txt", "outlink/secret.txt"} {
		fc, err := files.ReadScoped(p)
		if err != nil {
			t.Fatalf("ReadScoped(%q) err = %v", p, err)
		}
		if !fc.Skip {
			t.Fatalf("ReadScoped(%q) = %+v, want Skip", p, fc)
		}
		if strings.Contains(fc.Content, "classified") {
			t.Fatalf("ReadScoped(%q) leaked content: %+v", p, fc)
		}
	}
}

func TestReadScopedSkipsBinaryAndTruncatesHuge(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "bin.dat"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	huge := bytesRepeat('x', maxFileBytes+64)
	if err := os.WriteFile(filepath.Join(work, "huge.txt"), huge, 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)

	fc, err := files.ReadScoped("bin.dat")
	if err != nil {
		t.Fatal(err)
	}
	if !fc.Skip || !strings.Contains(fc.Notice, "binary") {
		t.Fatalf("binary = %+v", fc)
	}

	fc, err = files.ReadScoped("huge.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fc.Skip || len(fc.Content) != maxFileBytes || !strings.Contains(fc.Notice, "truncated") {
		t.Fatalf("huge = path=%s skip=%v len=%d notice=%q", fc.Path, fc.Skip, len(fc.Content), fc.Notice)
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestFilesApplyEditSuccessAndAlready(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "main.go")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)

	res, err := files.ApplyEdit(host.EditApply{
		Path:      "main.go",
		OldString: "hello",
		NewString: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "main.go" || res.Count != 1 || res.Already {
		t.Fatalf("ApplyEdit = %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi world\n" {
		t.Fatalf("file = %q", got)
	}

	// Re-apply when already applied reports Already without changing content.
	res, err = files.ApplyEdit(host.EditApply{
		Path:      "main.go",
		OldString: "hello",
		NewString: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Already || res.Count != 0 {
		t.Fatalf("already = %+v", res)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi world\n" {
		t.Fatalf("file changed on already: %q", got)
	}
}

func TestFilesApplyEditFailureLeavesFileUnchanged(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "main.go")
	orig := []byte("alpha beta alpha\n")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)

	_, err := files.ApplyEdit(host.EditApply{
		Path:      "main.go",
		OldString: "alpha",
		NewString: "A",
	})
	if err == nil || !strings.Contains(err.Error(), "matches 2") {
		t.Fatalf("err = %v, want multi-match", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(orig) {
		t.Fatalf("file mutated on failed apply: %q", got)
	}

	_, err = files.ApplyEdit(host.EditApply{
		Path:      "main.go",
		OldString: "missing",
		NewString: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(orig) {
		t.Fatalf("file mutated: %q", got)
	}
}

func TestFilesApplyEditRejectsEscape(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)
	_, err := files.ApplyEdit(host.EditApply{
		Path:      secret,
		OldString: "classified",
		NewString: "leaked",
	})
	if err == nil {
		t.Fatal("expected escape error")
	}
	got, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "classified" {
		t.Fatalf("outside file mutated: %q", got)
	}
}

func TestFilesApplyPatchSuccessAndFailure(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		" one",
		"-two",
		"+TWO",
		"*** End Patch",
	}, "\n")
	summary, err := files.ApplyPatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "M a.txt") {
		t.Fatalf("summary = %q", summary)
	}
	got, err := os.ReadFile(filepath.Join(work, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\nTWO\n" {
		t.Fatalf("file = %q", got)
	}

	// Context mismatch fails before write.
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		" one",
		"-missing",
		"+x",
		"*** End Patch",
	}, "\n")
	if _, err := files.ApplyPatch(bad); err == nil {
		t.Fatal("expected patch failure")
	}
	got, err = os.ReadFile(filepath.Join(work, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\ntwo\n" {
		t.Fatalf("file mutated on failed patch: %q", got)
	}
}
