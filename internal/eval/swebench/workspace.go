package swebench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TestbedPath is the repository root inside SWE-bench evaluation images.
const TestbedPath = "/testbed"

// MaterializeOpts configures workspace extraction from a Docker image.
type MaterializeOpts struct {
	// HostDir is the destination directory on the host (created if needed).
	HostDir string
	// Pull pulls the image before create when true.
	Pull bool
	// KeepContainer leaves the source container running and returns its id
	// (caller must Remove). When false the container is removed after copy.
	KeepContainer bool
}

// MaterializeResult is the host workspace path (+ optional container id).
type MaterializeResult struct {
	WorkDir     string
	ContainerID string
	Image       string
}

// MaterializeWorkspace pulls (optional) the instance image, creates a
// container, and copies /testbed to hostDir/testbed (or hostDir when it should
// be the repo root).
//
// Layout: hostDir is the instance run directory; the git checkout is at
// hostDir/repo so strike exec can use it as cwd.
func MaterializeWorkspace(ctx context.Context, rt Runtime, instanceID, hostDir string, pull bool) (MaterializeResult, error) {
	if rt == nil {
		return MaterializeResult{}, fmt.Errorf("swebench: nil runtime")
	}
	if hostDir == "" {
		return MaterializeResult{}, fmt.Errorf("swebench: empty hostDir")
	}
	image := DockerImageName(instanceID)
	if pull {
		if err := rt.Pull(ctx, image); err != nil {
			return MaterializeResult{}, err
		}
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return MaterializeResult{}, err
	}
	// Create stopped container with sleep entrypoint so cp works without start
	// on some docker versions; we start anyway for compatibility.
	id, err := rt.Create(ctx, image, CreateOpts{
		WorkDir:    TestbedPath,
		Entrypoint: []string{"sleep", "infinity"},
	})
	if err != nil {
		return MaterializeResult{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = rt.Remove(context.Background(), id)
		}
	}()
	if err := rt.Start(ctx, id); err != nil {
		return MaterializeResult{}, err
	}

	// docker cp container:/testbed dest  → dest/testbed or contents depending on trailing slash.
	// Copy to hostDir/repo_tmp then rename to hostDir/repo.
	tmpCopy := filepath.Join(hostDir, ".testbed-copy")
	_ = os.RemoveAll(tmpCopy)
	if err := rt.CopyFrom(ctx, id, TestbedPath, tmpCopy); err != nil {
		return MaterializeResult{}, err
	}
	// docker cp of a directory yields tmpCopy/testbed or tmpCopy contents.
	repo := filepath.Join(hostDir, "repo")
	_ = os.RemoveAll(repo)
	src := tmpCopy
	if st, err := os.Stat(filepath.Join(tmpCopy, "testbed")); err == nil && st.IsDir() {
		src = filepath.Join(tmpCopy, "testbed")
	}
	if err := os.Rename(src, repo); err != nil {
		// Cross-device: fall back to copy via rename failure.
		if err2 := copyDir(src, repo); err2 != nil {
			return MaterializeResult{}, fmt.Errorf("swebench: place repo: %v / %v", err, err2)
		}
		_ = os.RemoveAll(tmpCopy)
	} else {
		_ = os.RemoveAll(tmpCopy)
	}

	// Ensure git safe.directory / ownership for host-side git diff.
	_ = runGit(repo, "config", "core.fileMode", "false")

	cleanup = false
	_ = rt.Remove(context.Background(), id)
	return MaterializeResult{WorkDir: repo, Image: image}, nil
}

// copyDir recursively copies a directory (best-effort fallback).
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// InstanceRunDir returns workRoot/runID/instanceID.
func InstanceRunDir(workRoot, runID, instanceID string) string {
	safe := strings.ReplaceAll(instanceID, "/", "_")
	return filepath.Join(workRoot, runID, safe)
}

// DefaultWorkRoot is ~/.strike/eval/swebench when HOME is set.
func DefaultWorkRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "strike-eval-swebench")
	}
	return filepath.Join(home, ".strike", "eval", "swebench")
}

// DefaultRunID returns a timestamp-based run id.
func DefaultRunID(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC().Format("20060102T150405Z")
}
