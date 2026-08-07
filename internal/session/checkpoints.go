package session

import (
	"os"
	"path/filepath"
	"strings"
)

// CheckpointDir is the durable undo stack directory for a session
// (~/.strike/checkpoints/<id>). Mirrors tool.DefaultCheckpointDir so session
// retention/destroy can reap sidecars without importing internal/tool (#573).
func CheckpointDir(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if strings.Contains(sessionID, "/") || strings.Contains(sessionID, "\\") ||
		strings.Contains(sessionID, "..") {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "strike", "checkpoints", sessionID)
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "checkpoints", sessionID)
}

// RemoveCheckpoints deletes durable checkpoint data for id. Missing paths are OK.
func RemoveCheckpoints(sessionID string) error {
	dir := CheckpointDir(sessionID)
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
