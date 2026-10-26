package local

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
)

func TestDocumentReadWriteAndConflict(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "note.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)

	doc, err := files.ReadDocument("note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Path != "note.txt" || doc.Content != "alpha\n" || doc.Binary || doc.Truncated {
		t.Fatalf("read = %+v", doc)
	}
	if len(doc.SHA256) != 64 || doc.MtimeUnixNano == 0 || doc.Size != 6 {
		t.Fatalf("revision = %+v", doc)
	}

	res, err := files.WriteDocument(host.DocumentWrite{
		Path:                "note.txt",
		Content:             "beta\n",
		ExpectSHA256:        doc.SHA256,
		ExpectMtimeUnixNano: doc.MtimeUnixNano,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "beta\n" {
		t.Fatalf("content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o", info.Mode().Perm())
	}
	again, err := files.ReadDocument("note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if again.SHA256 != res.SHA256 || again.Content != "beta\n" {
		t.Fatalf("after write = %+v result %+v", again, res)
	}

	// Hash mismatch leaves the agent edit in place.
	if err := os.WriteFile(path, []byte("agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = files.WriteDocument(host.DocumentWrite{
		Path:                "note.txt",
		Content:             "stale\n",
		ExpectSHA256:        again.SHA256,
		ExpectMtimeUnixNano: again.MtimeUnixNano,
	})
	var conflict *host.DocumentConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("hash conflict err = %v", err)
	}
	if conflict.SHA256 == again.SHA256 || strings.Contains(conflict.Error(), "agent") {
		t.Fatalf("conflict = %+v", conflict)
	}
	left, _ := os.ReadFile(path)
	if string(left) != "agent\n" {
		t.Fatalf("stale write landed: %q", left)
	}

	// Same bytes, newer mtime, is still a conflict.
	fresh, err := files.ReadDocument("note.txt")
	if err != nil {
		t.Fatal(err)
	}
	next := time.Unix(0, fresh.MtimeUnixNano).Add(2 * time.Second)
	if err := os.Chtimes(path, next, next); err != nil {
		t.Fatal(err)
	}
	_, err = files.WriteDocument(host.DocumentWrite{
		Path:                "note.txt",
		Content:             "agent\n",
		ExpectSHA256:        fresh.SHA256,
		ExpectMtimeUnixNano: fresh.MtimeUnixNano,
	})
	if !errors.As(err, &conflict) {
		t.Fatalf("mtime conflict err = %v", err)
	}
	left, _ = os.ReadFile(path)
	if string(left) != "agent\n" {
		t.Fatalf("mtime conflict wrote: %q", left)
	}
}

func TestDocumentRejectsEscapeSymlinkBinaryAndSize(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, secret, filepath.Join(work, "link.txt"))
	mustSymlink(t, outside, filepath.Join(work, "outlink"))
	if err := os.WriteFile(filepath.Join(work, "bin.dat"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	huge := bytesRepeat('x', maxFileBytes+32)
	if err := os.WriteFile(filepath.Join(work, "huge.txt"), huge, 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)

	for _, p := range []string{"../" + filepath.Base(outside) + "/secret.txt", secret, "link.txt", "outlink/secret.txt", "missing.txt", "no-such"} {
		doc, err := files.ReadDocument(p)
		if err == nil {
			t.Fatalf("ReadDocument(%q) = %+v, want error", p, doc)
		}
		if strings.Contains(doc.Content, "classified") || strings.Contains(err.Error(), "classified") {
			t.Fatalf("ReadDocument(%q) leaked: %v %+v", p, err, doc)
		}
	}

	bin, err := files.ReadDocument("bin.dat")
	if err != nil {
		t.Fatal(err)
	}
	if !bin.Binary || bin.Content != "" {
		t.Fatalf("binary = %+v", bin)
	}
	hugeDoc, err := files.ReadDocument("huge.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !hugeDoc.Truncated || len(hugeDoc.Content) != maxFileBytes {
		t.Fatalf("huge = truncated %v len %d", hugeDoc.Truncated, len(hugeDoc.Content))
	}
	if _, err := files.WriteDocument(host.DocumentWrite{
		Path: "huge.txt", Content: "nope", ExpectSHA256: hugeDoc.SHA256, ExpectMtimeUnixNano: hugeDoc.MtimeUnixNano,
	}); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("huge write err = %v", err)
	}

	ok, err := files.ReadDocument("ok.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, err = files.WriteDocument(host.DocumentWrite{
		Path: "link.txt", Content: "x", ExpectSHA256: ok.SHA256, ExpectMtimeUnixNano: ok.MtimeUnixNano,
	})
	if err == nil || strings.Contains(err.Error(), "classified") {
		t.Fatalf("symlink write err = %v", err)
	}
	if target, _ := os.ReadFile(secret); string(target) != "classified" {
		t.Fatalf("symlink write changed target: %q", target)
	}

	_, err = files.WriteDocument(host.DocumentWrite{
		Path: "new.txt", Content: "x", ExpectSHA256: ok.SHA256,
	})
	if err == nil {
		t.Fatal("create should fail")
	}
	if _, statErr := os.Stat(filepath.Join(work, "new.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("new.txt stat = %v", statErr)
	}

	_, err = files.WriteDocument(host.DocumentWrite{
		Path: "ok.txt", Content: "a\x00b", ExpectSHA256: ok.SHA256, ExpectMtimeUnixNano: ok.MtimeUnixNano,
	})
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("nul write err = %v", err)
	}
	left, _ := os.ReadFile(filepath.Join(work, "ok.txt"))
	if string(left) != "ok" {
		t.Fatalf("nul write changed file: %q", left)
	}

	big := strings.Repeat("y", maxFileBytes+1)
	_, err = files.WriteDocument(host.DocumentWrite{
		Path: "ok.txt", Content: big, ExpectSHA256: ok.SHA256, ExpectMtimeUnixNano: ok.MtimeUnixNano,
	})
	if err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("oversize write err = %v", err)
	}
	_, err = files.WriteDocument(host.DocumentWrite{Path: "ok.txt", Content: "z", ExpectSHA256: "abcd"})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("bad hash err = %v", err)
	}
}

func TestDocumentRootCeiling(t *testing.T) {
	work := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "note.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)
	doc, err := files.ReadDocument("note.txt")
	if err != nil {
		t.Fatal(err)
	}
	SetFilesRootCeiling(files, []string{other}, true)
	if _, err := files.ReadDocument("note.txt"); err == nil || !strings.Contains(err.Error(), "allowed roots") {
		t.Fatalf("ceiling read err = %v", err)
	}
	_, err = files.WriteDocument(host.DocumentWrite{
		Path: "note.txt", Content: "nope", ExpectSHA256: doc.SHA256, ExpectMtimeUnixNano: doc.MtimeUnixNano,
	})
	if err == nil || !strings.Contains(err.Error(), "allowed roots") {
		t.Fatalf("ceiling write err = %v", err)
	}
	left, _ := os.ReadFile(filepath.Join(work, "note.txt"))
	if string(left) != "alpha" {
		t.Fatalf("ceiling write changed file: %q", left)
	}

	SetFilesRootCeiling(files, []string{work}, true)
	if _, err := files.WriteDocument(host.DocumentWrite{
		Path: "note.txt", Content: "ok", ExpectSHA256: doc.SHA256, ExpectMtimeUnixNano: doc.MtimeUnixNano,
	}); err != nil {
		t.Fatal(err)
	}
	SetFilesRootCeiling(files, nil, false)
	if _, err := files.ReadDocument("note.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentEmptyCeilingRejects(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "note.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)
	SetFilesRootCeiling(files, nil, true)
	if _, err := files.ReadDocument("note.txt"); err == nil {
		t.Fatal("empty ceiling should reject")
	}
}

func TestDocumentRejectsInRootSymlink(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "real.txt"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(work, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "sub", "inner.txt"), []byte("inner"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "real.txt", filepath.Join(work, "alias.txt"))
	mustSymlink(t, "sub", filepath.Join(work, "sublink"))
	files := NewFiles(work)

	if _, err := files.ReadDocument("alias.txt"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("alias read err = %v", err)
	}
	if _, err := files.ReadDocument("sublink/inner.txt"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("dir symlink read err = %v", err)
	}
	doc, err := files.ReadDocument("real.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.WriteDocument(host.DocumentWrite{
		Path: "alias.txt", Content: "nope", ExpectSHA256: doc.SHA256, ExpectMtimeUnixNano: doc.MtimeUnixNano,
	}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("alias write err = %v", err)
	}
	left, _ := os.ReadFile(filepath.Join(work, "real.txt"))
	if string(left) != "real" {
		t.Fatalf("alias write changed target: %q", left)
	}
}

func TestSumSHAStable(t *testing.T) {
	if !bytes.Equal(sumSHA(nil), sumSHA([]byte{})) {
		t.Fatal("nil and empty hash differ")
	}
	if !isHex(strings.Repeat("ab", 32)) || isHex("zz") || isHex("ABC") {
		t.Fatal("isHex")
	}
}
