package local

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
)

func TestNewProjectInitNilWorkDir(t *testing.T) {
	if NewProjectInit("") != nil {
		t.Fatal("empty workDir should yield nil Init")
	}
}

func TestProjectInitWriteCreateAndExists(t *testing.T) {
	dir := t.TempDir()
	init := NewProjectInit(dir)
	if init == nil {
		t.Fatal("expected Init")
	}
	exists, path, err := init.Exists()
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expected missing AGENTS.md")
	}
	if path != filepath.Join(dir, "AGENTS.md") {
		t.Fatalf("path = %q", path)
	}

	got, created, err := init.Write(false)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !created {
		t.Fatal("created = false")
	}
	if got != path {
		t.Fatalf("got path %q want %q", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "## Notes for agents") {
		t.Fatalf("body incomplete:\n%s", data)
	}

	_, _, err = init.Write(false)
	if !errors.Is(err, host.ErrInitExists) {
		t.Fatalf("err = %v, want ErrInitExists", err)
	}
	exists, _, err = init.Exists()
	if err != nil || !exists {
		t.Fatalf("Exists after write = %v %v", exists, err)
	}

	if _, _, err := init.Write(true); err != nil {
		t.Fatalf("force Write: %v", err)
	}
}
