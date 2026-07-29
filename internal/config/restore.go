package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RestoreKind selects which directory layout Restore rebuilds.
type RestoreKind int

const (
	// RestoreGlobal rebuilds ~/.strike (sessions, history, auth sidecars, …).
	RestoreGlobal RestoreKind = iota
	// RestoreProject rebuilds <workDir>/.strike (agents, worktrees, exports, …).
	RestoreProject
)

// RestoreOptions configures Restore.
type RestoreOptions struct {
	// Root is the absolute .strike directory to repair. Required.
	Root string
	// Kind selects global vs project layout defaults.
	Kind RestoreKind
	// Now stamps quarantine backups; nil uses time.Now.
	Now func() time.Time
}

// RestoreAction is one filesystem change or keep decision.
type RestoreAction struct {
	// Op is created|kept|quarantined.
	Op string
	// Path is the primary path (dir/file restored or kept).
	Path string
	// Backup is set when Op is quarantined (corrupt original moved here).
	Backup string
	// Detail is a short human reason (e.g. "invalid JSON").
	Detail string
}

// RestoreResult summarizes a Restore run.
type RestoreResult struct {
	Root    string
	Actions []RestoreAction
}

// defaultGlobalConfigJSON matches scripts/setup.sh starter config.
const defaultGlobalConfigJSON = `{
  "provider": "anthropic",
  "defaultAgent": "build"
}
`

// defaultCommitSkillMarkdown matches scripts/setup.sh starter skill.
const defaultCommitSkillMarkdown = `---
description: stage and commit the current changes with a good message
---
Look at the uncommitted changes (git status, git diff), stage the relevant
files, and commit them with a concise, descriptive message. $ARGUMENTS
`

// Restore recreates missing .strike directories and repairs corrupted metadata
// JSON files. Valid existing files are never overwritten. Corrupt JSON is
// moved aside as <name>.corrupt-<timestamp> before a safe default is written
// (config only) or the path is left absent (optional sidecars / auth.json).
// Session logs, memory, issues, goals, and history data files are never
// deleted or rewritten.
func Restore(opts RestoreOptions) (RestoreResult, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return RestoreResult{}, fmt.Errorf("restore: root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("restore: resolve root: %w", err)
	}
	root = abs
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	res := RestoreResult{Root: root}

	if err := ensureRestoreRoot(root, &res); err != nil {
		return res, err
	}

	for _, d := range restoreDirs(opts.Kind) {
		path := filepath.Join(root, d.name)
		if err := ensureDir(path, d.mode, now, &res); err != nil {
			return res, err
		}
	}

	// Required config: create default when missing; quarantine+replace when corrupt.
	cfgPath := filepath.Join(root, "config")
	if err := restoreJSONFile(cfgPath, []byte(defaultGlobalConfigJSON), true, now, &res); err != nil {
		return res, err
	}

	// Optional sidecars: quarantine corrupt only; never invent empty files.
	for _, name := range []string{
		"mcp.jsonc", "mcp.json",
		"providers.jsonc", "providers.json",
		"keybinds.jsonc", "keybinds.json",
	} {
		path := filepath.Join(root, name)
		if err := restoreJSONFile(path, nil, false, now, &res); err != nil {
			return res, err
		}
	}

	// auth.json is global-only credentials; quarantine corrupt, never rewrite.
	if opts.Kind == RestoreGlobal {
		authPath := filepath.Join(root, "auth.json")
		if err := restoreJSONFile(authPath, nil, false, now, &res); err != nil {
			return res, err
		}
		// Starter skill (same as setup.sh); never overwrite.
		skillPath := filepath.Join(root, "skills", "commit.md")
		if err := writeIfAbsent(skillPath, []byte(defaultCommitSkillMarkdown), &res); err != nil {
			return res, err
		}
	}

	return res, nil
}

// RestoreGlobalHome restores ~/.strike using GlobalRoot().
func RestoreGlobalHome() (RestoreResult, error) {
	root := GlobalRoot()
	if root == "" {
		return RestoreResult{}, fmt.Errorf("restore: cannot resolve home directory")
	}
	return Restore(RestoreOptions{Root: root, Kind: RestoreGlobal})
}

// RestoreProjectDir restores <workDir>/.strike.
func RestoreProjectDir(workDir string) (RestoreResult, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return RestoreResult{}, fmt.Errorf("restore: workDir is required")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("restore: resolve workDir: %w", err)
	}
	return Restore(RestoreOptions{Root: projectRoot(abs), Kind: RestoreProject})
}

type restoreDirSpec struct {
	name string
	mode os.FileMode
}

func restoreDirs(kind RestoreKind) []restoreDirSpec {
	switch kind {
	case RestoreProject:
		return []restoreDirSpec{
			{"agents", 0o755},
			{"skills", 0o755},
			{"themes", 0o755},
			{"workflows", 0o755},
			{"worktrees", 0o755},
			{"exports", 0o755},
		}
	default:
		return []restoreDirSpec{
			{"agents", 0o755},
			{"skills", 0o755},
			{"sessions", 0o755},
			{"history", 0o700},
			{"memory", 0o700},
			{"issues", 0o700},
			{"goals", 0o700},
			{"cache", 0o755},
			{"themes", 0o755},
			{"workflows", 0o755},
			{"bin", 0o755},
		}
	}
}

func ensureRestoreRoot(root string, res *RestoreResult) error {
	info, err := os.Lstat(root)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			// Resolve symlink targets that already exist as directories.
			real, err := filepath.EvalSymlinks(root)
			if err != nil {
				return fmt.Errorf("restore: resolve %s: %w", root, err)
			}
			fi, err := os.Stat(real)
			if err != nil {
				return fmt.Errorf("restore: stat %s: %w", real, err)
			}
			if !fi.IsDir() {
				return fmt.Errorf("restore: %s is not a directory", root)
			}
			res.Actions = append(res.Actions, RestoreAction{Op: "kept", Path: root, Detail: "symlink root"})
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf("restore: %s exists and is not a directory", root)
		}
		res.Actions = append(res.Actions, RestoreAction{Op: "kept", Path: root, Detail: "root"})
		return nil
	case os.IsNotExist(err):
		if err := os.MkdirAll(root, 0o700); err != nil {
			return fmt.Errorf("restore: create %s: %w", root, err)
		}
		res.Actions = append(res.Actions, RestoreAction{Op: "created", Path: root, Detail: "root"})
		return nil
	default:
		return fmt.Errorf("restore: stat %s: %w", root, err)
	}
}

func ensureDir(path string, mode os.FileMode, now func() time.Time, res *RestoreResult) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.IsDir() {
			res.Actions = append(res.Actions, RestoreAction{Op: "kept", Path: path})
			return nil
		}
		// File where a directory should be: quarantine then create dir.
		backup, qerr := quarantinePath(path, now)
		if qerr != nil {
			return qerr
		}
		res.Actions = append(res.Actions, RestoreAction{
			Op: "quarantined", Path: path, Backup: backup, Detail: "expected directory",
		})
		if err := os.Mkdir(path, mode); err != nil {
			return fmt.Errorf("restore: create dir %s: %w", path, err)
		}
		res.Actions = append(res.Actions, RestoreAction{Op: "created", Path: path, Detail: "directory"})
		return nil
	case os.IsNotExist(err):
		if err := os.MkdirAll(path, mode); err != nil {
			return fmt.Errorf("restore: create dir %s: %w", path, err)
		}
		res.Actions = append(res.Actions, RestoreAction{Op: "created", Path: path, Detail: "directory"})
		return nil
	default:
		return fmt.Errorf("restore: stat %s: %w", path, err)
	}
}

// restoreJSONFile validates an existing JSON/JSONC file.
// When missing and writeDefault is true, writes defaultBytes.
// When corrupt and writeDefault is true, quarantines then writes defaultBytes.
// When corrupt and writeDefault is false, quarantines only.
func restoreJSONFile(path string, defaultBytes []byte, writeDefault bool, now func() time.Time, res *RestoreResult) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if !writeDefault {
			return nil
		}
		return writeIfAbsent(path, defaultBytes, res)
	}
	if err != nil {
		return fmt.Errorf("restore: stat %s: %w", path, err)
	}
	if info.IsDir() {
		backup, qerr := quarantinePath(path, now)
		if qerr != nil {
			return qerr
		}
		res.Actions = append(res.Actions, RestoreAction{
			Op: "quarantined", Path: path, Backup: backup, Detail: "expected file",
		})
		if writeDefault {
			return writeFile(path, defaultBytes, res)
		}
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("restore: read %s: %w", path, err)
	}
	if jsonValid(data) {
		res.Actions = append(res.Actions, RestoreAction{Op: "kept", Path: path})
		return nil
	}

	backup, qerr := quarantinePath(path, now)
	if qerr != nil {
		return qerr
	}
	res.Actions = append(res.Actions, RestoreAction{
		Op: "quarantined", Path: path, Backup: backup, Detail: "invalid JSON",
	})
	if writeDefault {
		return writeFile(path, defaultBytes, res)
	}
	return nil
}

func writeIfAbsent(path string, data []byte, res *RestoreResult) error {
	_, err := os.Lstat(path)
	if err == nil {
		res.Actions = append(res.Actions, RestoreAction{Op: "kept", Path: path})
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("restore: stat %s: %w", path, err)
	}
	return writeFile(path, data, res)
}

func writeFile(path string, data []byte, res *RestoreResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("restore: mkdir parent %s: %w", path, err)
	}
	// 0600 for auth-like names; 0644 otherwise.
	mode := os.FileMode(0o644)
	if strings.HasSuffix(path, "auth.json") {
		mode = 0o600
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("restore: write %s: %w", path, err)
	}
	res.Actions = append(res.Actions, RestoreAction{Op: "created", Path: path})
	return nil
}

func quarantinePath(path string, now func() time.Time) (string, error) {
	ts := now().UTC().Format("20060102-150405")
	backup := path + ".corrupt-" + ts
	// Avoid clobbering an existing backup from the same second.
	if _, err := os.Lstat(backup); err == nil {
		backup = fmt.Sprintf("%s.corrupt-%s-%d", path, ts, now().UnixNano()%1000)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("restore: stat backup %s: %w", backup, err)
	}
	if err := os.Rename(path, backup); err != nil {
		return "", fmt.Errorf("restore: quarantine %s: %w", path, err)
	}
	return backup, nil
}

// jsonValid reports whether data is empty, whitespace-only, or parseable JSON
// after stripping // and /* */ comments (JSONC).
func jsonValid(data []byte) bool {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return true
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		return false
	}
	if strings.TrimSpace(string(stripped)) == "" {
		return true
	}
	var v any
	return json.Unmarshal(stripped, &v) == nil
}

// FormatRestoreReport renders a human-readable summary for CLI/script output.
func FormatRestoreReport(res RestoreResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "strike restore: %s\n", res.Root)
	var created, kept, quarantined int
	for _, a := range res.Actions {
		switch a.Op {
		case "created":
			created++
			fmt.Fprintf(&b, "  created      %s\n", a.Path)
		case "kept":
			kept++
		case "quarantined":
			quarantined++
			detail := a.Detail
			if detail == "" {
				detail = "corrupt"
			}
			fmt.Fprintf(&b, "  quarantined %s → %s (%s)\n", a.Path, a.Backup, detail)
		}
	}
	if created == 0 && quarantined == 0 {
		fmt.Fprintf(&b, "  (nothing to fix; structure ok)\n")
	}
	fmt.Fprintf(&b, "  summary: %d created, %d quarantined, %d kept\n", created, quarantined, kept)
	return b.String()
}
