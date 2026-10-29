package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func prepareState(dir string) error {
	ws := filepath.Join(dir, workspaceName)
	if err := os.RemoveAll(ws); err != nil {
		return err
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ws, markerName), []byte(markerBody), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, canaryName), []byte(canaryBody), 0o644); err != nil {
		return err
	}
	body := []byte(`{"provider":"openai","key":"` + dummySecret + "\"}\n")
	if err := os.WriteFile(filepath.Join(dir, secretRel), body, 0o600); err != nil {
		return err
	}
	for _, name := range []string{rpcSocketName, qmpSocketName, virtiofsSocketName} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	return nil
}

// workspaceHasDummy reports whether the fake provider marker leaked into the
// only shared directory. It does not print the marker.
func workspaceHasDummy(dir string) (bool, error) {
	ws := filepath.Join(dir, workspaceName)
	found := false
	err := filepath.WalkDir(ws, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), dummySecret) {
			found = true
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("scan workspace: %w", err)
	}
	return found, nil
}
