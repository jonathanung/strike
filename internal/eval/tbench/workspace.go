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
	WorkDir string
	Image   string
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
	return MaterializeResult{WorkDir: app, Image: image}, nil
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
