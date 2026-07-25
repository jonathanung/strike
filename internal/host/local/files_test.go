package local

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

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
