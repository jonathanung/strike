// Package project resolves the stable filesystem identity used for project-scoped state.
package project

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const gitTimeout = 500 * time.Millisecond

type Identity struct {
	Root string
	Key  string
}

// Resolve returns the canonical Git root containing cwd, or the canonical cwd
// when Git is unavailable or cannot identify a valid work tree.
func Resolve(ctx context.Context, cwd string) (Identity, error) {
	canonicalCWD, err := canonicalDir(cwd)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve project cwd: %w", err)
	}

	root := canonicalCWD
	gitCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	output, err := exec.CommandContext(gitCtx, "git", "-C", canonicalCWD, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		gitPath := strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r")
		if gitRoot, canonicalErr := canonicalDir(gitPath); canonicalErr == nil {
			root = gitRoot
		}
	}

	return Identity{Root: root, Key: root}, nil
}

func canonicalDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(physical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return filepath.Clean(physical), nil
}
