package tbench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/eval/swebench"
)

// MaterializeResult is the host workspace path for one task.
type MaterializeResult struct {
	WorkDir     string
	ContainerID string
	Image       string
}

// MaterializeWorkspace pulls (optional) the task image, creates a container,
// and copies WorkDirInContainer (/app) to hostDir/app for strike exec cwd.
func MaterializeWorkspace(ctx context.Context, rt swebench.Runtime, image, hostDir string, pull bool) (MaterializeResult, error) {
	if rt == nil {
		return MaterializeResult{}, fmt.Errorf("tbench: nil runtime")
	}
	if image == "" {
		return MaterializeResult{}, fmt.Errorf("tbench: empty image")
	}
	if hostDir == "" {
		return MaterializeResult{}, fmt.Errorf("tbench: empty hostDir")
	}
	if pull {
		if err := rt.Pull(ctx, image); err != nil {
			return MaterializeResult{}, err
		}
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return MaterializeResult{}, err
	}
	id, err := rt.Create(ctx, image, swebench.CreateOpts{
		WorkDir:    WorkDirInContainer,
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

	tmpCopy := filepath.Join(hostDir, ".app-copy")
	_ = os.RemoveAll(tmpCopy)
	if err := rt.CopyFrom(ctx, id, WorkDirInContainer, tmpCopy); err != nil {
		return MaterializeResult{}, err
	}
	app := filepath.Join(hostDir, "app")
	_ = os.RemoveAll(app)
	src := tmpCopy
	// docker cp of a directory may yield tmpCopy/app.
	if st, err := os.Stat(filepath.Join(tmpCopy, "app")); err == nil && st.IsDir() {
		src = filepath.Join(tmpCopy, "app")
	}
	if err := os.Rename(src, app); err != nil {
		if err2 := copyDir(src, app); err2 != nil {
			return MaterializeResult{}, fmt.Errorf("tbench: place app: %v / %v", err, err2)
		}
		_ = os.RemoveAll(tmpCopy)
	} else {
		_ = os.RemoveAll(tmpCopy)
	}

	cleanup = false
	_ = rt.Remove(context.Background(), id)

	liveID := startLiveEvalContainer(ctx, rt, image, app)
	return MaterializeResult{WorkDir: app, Image: image, ContainerID: liveID}, nil
}

// startLiveEvalContainer bind-mounts the host /app checkout at /app so bash
// can docker-exec into the official task image. Best-effort: empty id on
// failure (agent can still edit the host tree).
func startLiveEvalContainer(ctx context.Context, rt swebench.Runtime, image, app string) string {
	if rt == nil || app == "" {
		return ""
	}
	abs, err := filepath.Abs(app)
	if err != nil {
		abs = app
	}
	id, err := rt.Create(ctx, image, swebench.CreateOpts{
		WorkDir:    WorkDirInContainer,
		Entrypoint: []string{"sleep", "infinity"},
		HostBinds:  []string{abs + ":" + WorkDirInContainer},
	})
	if err != nil {
		return ""
	}
	if err := rt.Start(ctx, id); err != nil {
		_ = rt.Remove(context.Background(), id)
		return ""
	}
	return id
}

const evalExecHelper = `#!/bin/bash
set -euo pipefail
cid="${STRIKE_EVAL_CONTAINER:-}"
if [[ -z "$cid" ]]; then
  echo "eval-exec: STRIKE_EVAL_CONTAINER is unset" >&2
  exit 1
fi
if [[ $# -eq 0 ]]; then
  echo "usage: eval-exec <command>..." >&2
  exit 2
fi
cmd=$(printf '%q ' "$@")
exec docker exec -w /app "$cid" bash -lc "${cmd}"
`

// reclaimWorkspaceOwner chowns bind-mounted /app back to the host uid so
// docker cp (grade) can read root-created 0600 files (openssl keys, etc.).
func reclaimWorkspaceOwner(ctx context.Context, rt swebench.Runtime, containerID string) {
	if rt == nil || containerID == "" {
		return
	}
	cmd := fmt.Sprintf("chown -R %d:%d %s", os.Getuid(), os.Getgid(), WorkDirInContainer)
	_, _, _, _ = rt.Exec(ctx, containerID, []string{"bash", "-lc", cmd}, swebench.ExecOpts{
		Timeout: 2 * time.Minute,
	})
}

// WriteEvalExecHelper installs a PATH wrapper that docker-execs into the
// live task image. Lives next to app/ so it is not part of the workspace copy.
func WriteEvalExecHelper(instDir string) error {
	if instDir == "" {
		return fmt.Errorf("tbench: empty instDir")
	}
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(instDir, "eval-exec")
	return os.WriteFile(path, []byte(evalExecHelper), 0o755)
}

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

// DefaultWorkRoot is ~/.strike/eval/tbench when HOME is set.
func DefaultWorkRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "strike-eval-tbench")
	}
	return filepath.Join(home, ".strike", "eval", "tbench")
}

// DefaultRunID returns a timestamp-based run id.
func DefaultRunID(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC().Format("20060102T150405Z")
}

// DefaultImage returns the conventional TB2 prebuilt image for a task id.
func DefaultImage(instanceID string) string {
	return fmt.Sprintf("alexgshaw/%s:20251031", instanceID)
}
