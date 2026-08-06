package swebench

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExtractPatch returns a unified diff of workDir against HEAD, including
// untracked files (via git add -N). Empty string means no changes.
func ExtractPatch(workDir string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("swebench: extract patch: empty workDir")
	}
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err != nil {
		// Some materializations may be a plain tree; try diff against nothing.
		return extractPatchNoGit(workDir)
	}
	// Intent-to-add untracked so they appear in git diff.
	_ = runGit(workDir, "add", "-N", ".")
	// Unstage is unnecessary for diff; -N is enough.
	out, err := runGitOutput(workDir, "diff", "HEAD")
	if err != nil {
		// Fallback: diff without HEAD (unborn / odd states).
		out2, err2 := runGitOutput(workDir, "diff")
		if err2 != nil {
			return "", fmt.Errorf("swebench: git diff: %w", err)
		}
		out = out2
	}
	// Also include staged-only changes.
	staged, err := runGitOutput(workDir, "diff", "--cached")
	if err == nil && len(bytes.TrimSpace(staged)) > 0 {
		if len(bytes.TrimSpace(out)) == 0 {
			out = staged
		} else if !bytes.Contains(out, staged) {
			out = append(out, staged...)
		}
	}
	return string(out), nil
}

func extractPatchNoGit(workDir string) (string, error) {
	// Without git we cannot produce a reliable SWE-bench patch.
	return "", fmt.Errorf("swebench: workDir %s is not a git checkout", workDir)
}

func runGit(workDir string, args ...string) error {
	_, err := runGitOutput(workDir, args...)
	return err
}

func runGitOutput(workDir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// WritePatch writes patch text to path (0600).
func WritePatch(path, patch string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(patch), 0o600)
}

// NormalizePatch ensures a trailing newline when non-empty (harness-friendly).
func NormalizePatch(patch string) string {
	if patch == "" {
		return ""
	}
	if !strings.HasSuffix(patch, "\n") {
		return patch + "\n"
	}
	return patch
}
