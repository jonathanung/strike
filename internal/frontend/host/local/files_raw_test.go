package local

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDocumentRaw(t *testing.T) {
	work := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00binary")
	if err := os.WriteFile(filepath.Join(work, "shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "big.bin"), bytes.Repeat([]byte{1}, 64), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(work), filepath.Base(work)+"-outside.png")
	if err := os.WriteFile(outside, png, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	if err := os.Symlink(outside, filepath.Join(work, "link.png")); err != nil {
		t.Fatal(err)
	}
	files := NewFiles(work)

	doc, ok, err := ReadDocumentRaw(files, "shot.png", 1<<20)
	if !ok || err != nil {
		t.Fatalf("read ok=%v err=%v", ok, err)
	}
	if doc.Path != "shot.png" || !bytes.Equal(doc.Data, png) || doc.Size != int64(len(png)) || doc.MtimeUnixNano == 0 {
		t.Fatalf("doc = %+v", doc)
	}

	cases := []struct {
		name, path string
		max        int64
		want       string
		tooLarge   bool
	}{
		{name: "escape", path: "../" + filepath.Base(outside), max: 1 << 20, want: ""},
		{name: "absolute outside", path: outside, max: 1 << 20, want: ""},
		{name: "symlink", path: "link.png", max: 1 << 20, want: "symlink"},
		{name: "directory", path: "sub", max: 1 << 20, want: "not a regular file"},
		{name: "missing", path: "nope.png", max: 1 << 20, want: "not found"},
		{name: "empty", path: "", max: 1 << 20, want: ""},
		{name: "over cap", path: "big.bin", max: 63, tooLarge: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := ReadDocumentRaw(files, tc.path, tc.max)
			if !ok {
				t.Fatal("local files service must report ok")
			}
			if err == nil {
				t.Fatalf("want error, got %+v", got)
			}
			if len(got.Data) != 0 {
				t.Fatalf("leaked %d bytes", len(got.Data))
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if tc.tooLarge != errors.Is(err, ErrRawTooLarge) {
				t.Fatalf("too-large = %v for %v", errors.Is(err, ErrRawTooLarge), err)
			}
			if tc.tooLarge && got.Size != 64 {
				t.Fatalf("too-large size = %d", got.Size)
			}
		})
	}

	// Allowed-roots ceiling: same rule as ReadDocument.
	SetFilesRootCeiling(files, []string{t.TempDir()}, true)
	if _, _, err := ReadDocumentRaw(files, "shot.png", 1<<20); err == nil || !strings.Contains(err.Error(), "allowed roots") {
		t.Fatalf("ceiling err = %v", err)
	}
	SetFilesRootCeiling(files, nil, false)

	if _, ok, _ := ReadDocumentRaw(nil, "shot.png", 1<<20); ok {
		t.Fatal("non-local files must report ok=false")
	}
}
