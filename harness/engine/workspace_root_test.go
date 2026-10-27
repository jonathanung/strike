package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChildWorkDirAllowed(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "sub")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	extra := t.TempDir()
	if err := childWorkDirAllowed(nil, outside); err != nil {
		t.Fatal(err)
	}
	if err := childWorkDirAllowed([]string{root}, inside); err != nil {
		t.Fatal(err)
	}
	if err := childWorkDirAllowed([]string{root}, outside); err == nil {
		t.Fatal("outside directory should be rejected")
	}
	if err := childWorkDirAllowed([]string{root, extra}, extra); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "hop")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := childWorkDirAllowed([]string{root}, link); err == nil {
		t.Fatal("symlink out of the workspace should be rejected")
	}
	// Prefix sibling must not count as inside.
	sibling := root + "-other"
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := childWorkDirAllowed([]string{root}, sibling); err == nil {
		t.Fatal("prefix sibling should be rejected")
	}
}
