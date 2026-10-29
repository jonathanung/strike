package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareStateIsolatesCanary(t *testing.T) {
	dir := t.TempDir()
	if err := prepareState(dir); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(dir, workspaceName, markerName))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != markerBody {
		t.Fatalf("marker %q", marker)
	}
	if _, err := os.Stat(filepath.Join(dir, canaryName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, workspaceName, canaryName)); !os.IsNotExist(err) {
		t.Fatalf("canary visible in workspace: %v", err)
	}
	leaked, err := workspaceHasDummy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("dummy marker was inside the workspace before boot")
	}
	if err := os.WriteFile(filepath.Join(dir, workspaceName, "notes.txt"), []byte("sees "+dummySecret), 0o644); err != nil {
		t.Fatal(err)
	}
	leaked, err = workspaceHasDummy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !leaked {
		t.Fatal("scan missed a workspace leak")
	}
}
