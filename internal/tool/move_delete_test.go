package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMoveFileHappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	td := &TurnDiff{}
	tc.TurnDiff = td
	var syncs []struct {
		path    string
		content string
		deleted bool
	}
	tc.FileSync = func(absPath, content string, deleted bool) {
		syncs = append(syncs, struct {
			path    string
			content string
			deleted bool
		}{absPath, content, deleted})
	}

	res, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "a.txt",
		"destination": "sub/b.txt",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Moved") || !strings.Contains(res.Output, "a.txt") {
		t.Fatalf("output = %q", res.Output)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source still exists")
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub", "b.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("dest = %q err=%v", got, err)
	}
	snap := td.Snapshot()
	kinds := map[string]ChangeKind{}
	for _, c := range snap {
		kinds[c.Path] = c.Kind
	}
	if kinds["a.txt"] != ChangeDelete {
		t.Fatalf("turn diff source = %v, want delete", kinds["a.txt"])
	}
	if kinds["sub/b.txt"] != ChangeCreate {
		t.Fatalf("turn diff dest = %v, want create", kinds["sub/b.txt"])
	}
	if len(syncs) != 2 {
		t.Fatalf("syncs = %#v", syncs)
	}
	if !syncs[0].deleted || syncs[1].deleted || syncs[1].content != "hello" {
		t.Fatalf("syncs = %#v", syncs)
	}
}

func TestMoveOverwritePolicy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("src"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dst.txt"), []byte("dst"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)

	_, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "src.txt",
		"destination": "dst.txt",
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("want precondition_failed without overwrite, got %v", err)
	}
	// Unchanged.
	got, _ := os.ReadFile(filepath.Join(dir, "dst.txt"))
	if string(got) != "dst" {
		t.Fatalf("dst mutated: %q", got)
	}

	if _, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "src.txt",
		"destination": "dst.txt",
		"overwrite":   true,
	}), tc); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "dst.txt"))
	if string(got) != "src" {
		t.Fatalf("dst = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "src.txt")); !os.IsNotExist(err) {
		t.Fatal("source still exists after overwrite move")
	}
}

func TestMoveBaseHashStale(t *testing.T) {
	dir := t.TempDir()
	content := []byte("payload\n")
	if err := os.WriteFile(filepath.Join(dir, "m.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	_, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "m.txt",
		"destination": "n.txt",
		"baseHash":    strings.Repeat("0", 64),
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("want precondition_failed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "m.txt")); err != nil {
		t.Fatal("source should remain on stale baseHash")
	}

	if _, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "m.txt",
		"destination": "n.txt",
		"baseHash":    ContentHash(content),
	}), tc); err != nil {
		t.Fatal(err)
	}
}

func TestMoveRejectsDirectoryAndSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "f.txt"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)

	_, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "d",
		"destination": "d2",
	}), tc)
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("dir move want invalid_args, got %v", err)
	}

	_, err = NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "link",
		"destination": "link2",
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("symlink move want precondition_failed, got %v", err)
	}
}

func TestMoveEscapeAndPermission(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)

	_, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "in.txt",
		"destination": "../outside.txt",
	}), tc)
	if err == nil {
		t.Fatal("expected escape error")
	}
	var esc *WorkspaceEscapeError
	if !errors.As(err, &esc) && CodeOf(Classify(err)) != string(CodePreconditionFailed) {
		// resolve returns WorkspaceEscapeError; Classify maps it.
		if _, ok := err.(*WorkspaceEscapeError); !ok {
			// either form is fine
			if CodeOf(err) == "" && !strings.Contains(err.Error(), "escapes") {
				t.Fatalf("unexpected escape err: %v", err)
			}
		}
	}

	denied := allowAll(dir)
	denied.Ask = func(context.Context, AskRequest) error { return errors.New("denied") }
	_, err = NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "in.txt",
		"destination": "out.txt",
	}), denied)
	if err == nil {
		t.Fatal("expected permission deny")
	}
}

func TestMoveCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewMove().Execute(ctx, mustJSON(t, map[string]any{
		"source":      "c.txt",
		"destination": "d.txt",
	}), allowAll(dir))
	if err == nil {
		t.Fatal("expected canceled")
	}
	if !errors.Is(err, context.Canceled) && CodeOf(Classify(err)) != string(CodeCanceled) {
		t.Fatalf("want canceled, got %v", err)
	}
}

func TestMoveSamePathRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "s.txt",
		"destination": "./s.txt",
	}), allowAll(dir))
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("want invalid_args, got %v", err)
	}
}

func TestMoveCrossFilesystemPath(t *testing.T) {
	// Unit-test the EXDEV branch by invoking atomicMoveFile after forcing
	// rename failure is hard without a second mount. Instead verify isEXDEV
	// classification and that a normal same-FS move does not report cross-FS.
	dir := t.TempDir()
	src := filepath.Join(dir, "x.txt")
	dst := filepath.Join(dir, "y.txt")
	data := []byte("cross")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cross, err := atomicMoveFile(dir, "", "x.txt", "y.txt", src, dst, data)
	if err != nil {
		t.Fatal(err)
	}
	if cross {
		t.Fatal("same-FS rename should not report cross-filesystem")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source remains")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "cross" {
		t.Fatalf("dst = %q", got)
	}

	// Synthetic EXDEV detection.
	linkErr := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.EXDEV}
	if !isEXDEV(linkErr) {
		t.Fatal("isEXDEV should detect LinkError EXDEV")
	}
	if isEXDEV(errors.New("other")) {
		t.Fatal("isEXDEV false positive")
	}
}

func TestDeleteFileHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	td := &TurnDiff{}
	tc.TurnDiff = td
	var deletedSync bool
	tc.FileSync = func(absPath, content string, deleted bool) {
		if deleted && absPath == path {
			deletedSync = true
		}
	}

	res, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "gone.txt",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Deleted file") {
		t.Fatalf("output = %q", res.Output)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file still exists")
	}
	if !deletedSync {
		t.Fatal("expected FileSync deleted")
	}
	snap := td.Snapshot()
	if len(snap) != 1 || snap[0].Path != "gone.txt" || snap[0].Kind != ChangeDelete {
		t.Fatalf("turn diff = %+v", snap)
	}
}

func TestDeleteEmptyAndRecursiveDir(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(filepath.Join(nested, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)

	if _, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "empty",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatal("empty dir remains")
	}

	_, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "nested",
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("non-empty without recursive: %v", err)
	}

	if _, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path":      "nested",
		"recursive": true,
	}), tc); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatal("nested dir remains")
	}
}

func TestDeleteBaseHashAndSymlink(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hash-me\n")
	if err := os.WriteFile(filepath.Join(dir, "h.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "h.txt"), filepath.Join(dir, "sl")); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)

	_, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path":     "h.txt",
		"baseHash": strings.Repeat("a", 64),
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("stale baseHash: %v", err)
	}

	_, err = NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "sl",
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("symlink delete: %v", err)
	}

	if _, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path":     "h.txt",
		"baseHash": ContentHash(content),
	}), tc); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRefuseRootAndEscape(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	_, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": ".",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("want root refuse, got %v", err)
	}

	_, err = NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "../outside",
	}), tc)
	if err == nil {
		t.Fatal("expected escape")
	}
}

func TestDeletePermissionAndCancel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	denied := allowAll(dir)
	denied.Ask = func(context.Context, AskRequest) error { return errors.New("no") }
	if _, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "p.txt",
	}), denied); err == nil {
		t.Fatal("expected deny")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewDelete().Execute(ctx, mustJSON(t, map[string]any{
		"path": "p.txt",
	}), allowAll(dir)); err == nil {
		t.Fatal("expected cancel")
	}
}

func TestDeleteMissingAndInvalidArgs(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	_, err := NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "nope.txt",
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("missing: %v", err)
	}
	_, err = NewDelete().Execute(context.Background(), jsonRaw(`{`), tc)
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("bad json: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = NewDelete().Execute(context.Background(), mustJSON(t, map[string]any{
		"path":     "d",
		"baseHash": ContentHash([]byte("x")),
	}), tc)
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("baseHash on dir: %v", err)
	}
}

func jsonRaw(s string) []byte { return []byte(s) }

func TestMoveDeleteOwnershipClaim(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "o.txt"), []byte("own"), 0o644); err != nil {
		t.Fatal(err)
	}
	own := NewPathOwnership(OverlapWarn)
	tc := allowAll(dir)
	tc.Ownership = own
	tc.SessionID = "agent-a"
	tc.MemberName = "A"

	if _, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "o.txt",
		"destination": "p.txt",
	}), tc); err != nil {
		t.Fatal(err)
	}

	// Second agent writing overlapping path should warn under warn policy.
	tc2 := allowAll(dir)
	tc2.Ownership = own
	tc2.SessionID = "agent-b"
	tc2.MemberName = "B"
	res, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "p.txt",
		"content":  "other",
	}), tc2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "warning:") {
		t.Fatalf("expected overlap warning, got %q", res.Output)
	}
}

func TestMoveConcurrentContentRace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "race.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	tc := allowAll(dir)
	tc.Ask = func(ctx context.Context, req AskRequest) error {
		once.Do(func() {
			time.Sleep(5 * time.Millisecond)
			_ = os.WriteFile(path, []byte("hello\nEXTERNAL"), 0o644)
		})
		return nil
	}
	_, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      "race.txt",
		"destination": "out.txt",
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("want concurrent precondition_failed, got %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("source should remain after failed race")
	}
}

func TestMoveSessionTemp(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	ws := t.TempDir()
	temp, err := EnsureSessionTemp("move-temp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CleanupSessionTemp("move-temp") })
	src := filepath.Join(temp, "scratch.txt")
	if err := os.WriteFile(src, []byte("tmp"), 0o600); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(ws)
	tc.SessionTempDir = temp
	dst := filepath.Join(temp, "moved.txt")
	if _, err := NewMove().Execute(context.Background(), mustJSON(t, map[string]any{
		"source":      src,
		"destination": dst,
	}), tc); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "tmp" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestFileStateForget(t *testing.T) {
	fs := &FileState{}
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	fs.Record(path, info)
	fs.MarkDirty(path)
	fs.Forget(path)
	if err := fs.CheckFresh(path, "f.txt"); err != nil {
		t.Fatalf("after Forget should be fresh (never-read): %v", err)
	}
}

func TestIsEXDEVNil(t *testing.T) {
	if isEXDEV(nil) {
		t.Fatal("nil should not be EXDEV")
	}
}
