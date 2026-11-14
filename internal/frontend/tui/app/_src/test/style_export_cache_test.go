package tui

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Regression coverage for #1684: export-data resolution must happen at most
// once per import path for the whole test binary. The previous per-scan
// importer re-ran `go list -export` for every package in every fixture scan,
// and that subprocess storm timed out under full-suite load.

func writeFakeExport(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "export")
	if err := os.WriteFile(file, []byte("fake-export-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func readLookup(t *testing.T, s *exportDataSource, path string) string {
	t.Helper()
	rc, err := s.lookup(path)
	if err != nil {
		t.Fatalf("lookup %s: %v", path, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read lookup %s: %v", path, err)
	}
	return string(b)
}

func TestExportDataSourceUsesBatchedListing(t *testing.T) {
	file := writeFakeExport(t)
	resolves := 0
	s := &exportDataSource{
		files: map[string]string{},
		data:  map[string][]byte{},
		listExports: func() (map[string]string, error) {
			return map[string]string{"example.com/pkg": file}, nil
		},
		resolveExport: func(string) (string, error) {
			resolves++
			return file, nil
		},
	}
	for i := 0; i < 2; i++ {
		if got := readLookup(t, s, "example.com/pkg"); got != "fake-export-data" {
			t.Fatalf("lookup %d = %q, want fake-export-data", i, got)
		}
	}
	if resolves != 0 {
		t.Fatalf("per-path resolves = %d, want 0 (batched listing covers the path)", resolves)
	}
}

func TestExportDataSourceResolvesMissOnce(t *testing.T) {
	file := writeFakeExport(t)
	resolves := 0
	s := &exportDataSource{
		files: map[string]string{},
		data:  map[string][]byte{},
		listExports: func() (map[string]string, error) {
			return map[string]string{}, nil
		},
		resolveExport: func(string) (string, error) {
			resolves++
			return file, nil
		},
	}
	for i := 0; i < 3; i++ {
		if got := readLookup(t, s, "example.com/other"); got != "fake-export-data" {
			t.Fatalf("lookup %d = %q, want fake-export-data", i, got)
		}
	}
	if resolves != 1 {
		t.Fatalf("per-path resolves = %d, want 1 across repeated lookups", resolves)
	}
}
