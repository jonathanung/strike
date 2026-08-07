package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotGitRepository is returned when a worktree operation needs a git repo
// but cwd is not inside one. Callers may soft-fail and stay on the launch cwd.
var ErrNotGitRepository = errors.New("not a git repository")

// Worktree mode values (config session.worktree).
const (
	WorktreeOff    = "off"
	WorktreeAuto   = "auto"
	WorktreeAlways = "always"
)

// Worktree cleanup values (config session.worktreeCleanup).
const (
	CleanupKeep   = "keep"
	CleanupDelete = "delete"
)

const (
	worktreeSubdir    = "worktrees"
	worktreeBranch    = "strike"
	worktreeTimeout   = 30 * time.Second
	worktreeGitIgnore = "*\n"
)

// Worktree is one strike-managed git worktree bound to a session.
type Worktree struct {
	// Path is the worktree directory (tool CWD for the session).
	Path string
	// Branch is the local branch checked out in the worktree.
	Branch string
	// RepoRoot is the main repository working tree (never a linked worktree).
	RepoRoot string
}

// NormalizeWorktreeMode maps config strings to off|auto|always (default off).
func NormalizeWorktreeMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case WorktreeAlways:
		return WorktreeAlways
	case WorktreeAuto:
		return WorktreeAuto
	default:
		return WorktreeOff
	}
}

// NormalizeCleanup maps config strings to keep|delete (default keep).
func NormalizeCleanup(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case CleanupDelete:
		return CleanupDelete
	default:
		return CleanupKeep
	}
}

// WantWorktree reports whether a new root session should get an isolated
// worktree. force is an explicit CLI/opt-in. openRootCount is how many root
// sessions are already open before creating this one (auto binds on the second).
func WantWorktree(mode string, force bool, openRootCount int) bool {
	if force {
		return true
	}
	switch NormalizeWorktreeMode(mode) {
	case WorktreeAlways:
		return true
	case WorktreeAuto:
		return openRootCount >= 1
	default:
		return false
	}
}

// WorktreePath is the strike-managed path for a session worktree under the
// main repo: <repoRoot>/.strike/worktrees/<sessionID>/. Covered by repo
// gitignore pattern */worktrees; a local .gitignore is also written.
func WorktreePath(repoRoot, sessionID string) string {
	return filepath.Join(repoRoot, ".strike", worktreeSubdir, sessionID)
}

// BranchName is the local branch created for a session worktree.
func BranchName(sessionID string) string {
	id := strings.TrimSpace(sessionID)
	id = strings.ReplaceAll(id, string(filepath.Separator), "-")
	id = strings.ReplaceAll(id, "/", "-")
	id = strings.ReplaceAll(id, "\\", "-")
	return worktreeBranch + "/" + id
}

// MainRoot returns the primary working tree for the git repository containing
// cwd (not a linked worktree path). Non-git directories return an error.
func MainRoot(ctx context.Context, cwd string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("worktree: cwd is empty")
	}
	canonicalCWD, err := canonicalDir(cwd)
	if err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}
	gitCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(gitCtx, "git", "-C", canonicalCWD, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return "", fmt.Errorf("worktree: %q is not a git repository: %w", canonicalCWD, ErrNotGitRepository)
	}
	commonOut, err := exec.CommandContext(gitCtx, "git", "-C", canonicalCWD, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("worktree: resolving git common dir: %w", err)
	}
	common := strings.TrimSpace(string(commonOut))
	common = strings.TrimSuffix(strings.TrimSuffix(common, "\n"), "\r")
	if common == "" {
		return "", fmt.Errorf("worktree: empty git common dir")
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(canonicalCWD, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return "", fmt.Errorf("worktree: abs common dir: %w", err)
	}
	// Common dir is <main>/.git (directory) for a normal repo.
	if filepath.Base(common) == ".git" {
		root, err := canonicalDir(filepath.Dir(common))
		if err != nil {
			return "", fmt.Errorf("worktree: main root: %w", err)
		}
		return root, nil
	}
	// Fallback: toplevel of the current work tree (may be a linked worktree).
	topOut, err := exec.CommandContext(gitCtx, "git", "-C", canonicalCWD, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("worktree: show-toplevel: %w", err)
	}
	top := strings.TrimSpace(string(topOut))
	root, err := canonicalDir(top)
	if err != nil {
		return "", fmt.Errorf("worktree: toplevel: %w", err)
	}
	return root, nil
}

// Add creates a linked git worktree for sessionID under
// <mainRoot>/.strike/worktrees/<sessionID>/ on branch strike/<sessionID> at HEAD.
// On failure, partial directories are removed so the session is not half-bound.
func Add(ctx context.Context, cwd, sessionID string) (Worktree, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Worktree{}, fmt.Errorf("worktree: session id is empty")
	}
	if err := validateSessionSegment(sessionID); err != nil {
		return Worktree{}, err
	}
	repoRoot, err := MainRoot(ctx, cwd)
	if err != nil {
		return Worktree{}, err
	}
	path := WorktreePath(repoRoot, sessionID)
	branch := BranchName(sessionID)

	if _, err := os.Stat(path); err == nil {
		return Worktree{}, fmt.Errorf("worktree: path %q already exists", path)
	} else if err != nil && !os.IsNotExist(err) {
		return Worktree{}, fmt.Errorf("worktree: stat path: %w", err)
	}

	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Worktree{}, fmt.Errorf("worktree: mkdir: %w", err)
	}
	// Belt-and-suspenders ignore so worktree contents never stage even if the
	// repo-level */worktrees pattern is missing.
	_ = os.WriteFile(filepath.Join(parent, ".gitignore"), []byte(worktreeGitIgnore), 0o644)

	gitCtx, cancel := context.WithTimeout(ctx, worktreeTimeout)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", "-C", repoRoot, "worktree", "add", "-b", branch, path, "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(path)
		return Worktree{}, fmt.Errorf("worktree: git worktree add: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	canon, err := canonicalDir(path)
	if err != nil {
		_ = Remove(context.Background(), repoRoot, path, branch)
		return Worktree{}, fmt.Errorf("worktree: canonicalize: %w", err)
	}
	return Worktree{Path: canon, Branch: branch, RepoRoot: repoRoot}, nil
}

// Remove deletes a strike-managed worktree and its branch. Refuses to remove
// the main repository checkout. Missing paths are not an error (idempotent).
func Remove(ctx context.Context, repoRoot, path, branch string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("worktree: path is empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("worktree: abs path: %w", err)
	}
	if repoRoot != "" {
		main, err := canonicalDir(repoRoot)
		if err == nil {
			if p, perr := canonicalDir(absPath); perr == nil && p == main {
				return fmt.Errorf("worktree: refusing to remove primary checkout %q", main)
			}
		}
	}
	if _, err := os.Stat(absPath); err != nil {
		if os.IsNotExist(err) {
			// Still try to drop the branch if named.
			if branch != "" && repoRoot != "" {
				_ = deleteBranch(ctx, repoRoot, branch)
			}
			return nil
		}
		return fmt.Errorf("worktree: stat: %w", err)
	}

	gitCtx, cancel := context.WithTimeout(ctx, worktreeTimeout)
	defer cancel()
	root := repoRoot
	if root == "" {
		root = absPath
	}
	cmd := exec.CommandContext(gitCtx, "git", "-C", root, "worktree", "remove", "--force", absPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback: unregister + delete directory if git worktree remove fails
		// (e.g. already pruned). Never delete when path equals main root.
		_ = exec.CommandContext(gitCtx, "git", "-C", root, "worktree", "prune").Run()
		if rmErr := os.RemoveAll(absPath); rmErr != nil {
			return fmt.Errorf("worktree: remove: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	}
	if branch != "" {
		_ = deleteBranch(ctx, root, branch)
	}
	return nil
}

func deleteBranch(ctx context.Context, repoRoot, branch string) error {
	gitCtx, cancel := context.WithTimeout(ctx, worktreeTimeout)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", "-C", repoRoot, "branch", "-D", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("worktree: delete branch %q: %w\n%s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func validateSessionSegment(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("worktree: invalid session id %q", id)
	}
	if strings.Contains(id, string(filepath.Separator)) || strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return fmt.Errorf("worktree: session id %q must be a single path segment", id)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("worktree: session id %q is invalid", id)
	}
	return nil
}

// DiffUnified returns a unified diff of workDir against HEAD, including
// untracked files. Used to export an inspectable child patch when filesystem
// isolation is on. Empty string means no changes. The worktree index may be
// briefly staged then reset — safe for strike-managed child worktrees only.
func DiffUnified(ctx context.Context, workDir string) (string, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return "", fmt.Errorf("worktree: workDir is empty")
	}
	gitCtx, cancel := context.WithTimeout(ctx, worktreeTimeout)
	defer cancel()
	// Stage everything (including untracked) so one diff covers the full delta.
	if out, err := exec.CommandContext(gitCtx, "git", "-C", workDir, "add", "-A").CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree: git add: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	cmd := exec.CommandContext(gitCtx, "git", "-C", workDir, "diff", "--cached", "HEAD")
	out, err := cmd.CombinedOutput()
	// Best-effort unstage so the worktree is not left dirty-staged for inspect.
	_ = exec.CommandContext(gitCtx, "git", "-C", workDir, "reset", "-q", "HEAD").Run()
	if err != nil {
		// No HEAD (empty repo): still return staged diff against empty tree.
		cmd2 := exec.CommandContext(gitCtx, "git", "-C", workDir, "diff", "--cached")
		out2, err2 := cmd2.CombinedOutput()
		_ = exec.CommandContext(gitCtx, "git", "-C", workDir, "reset", "-q").Run()
		if err2 != nil {
			return "", fmt.Errorf("worktree: git diff: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		return string(out2), nil
	}
	return string(out), nil
}

// HeadRev returns the short HEAD commit of cwd's repo, or empty when unavailable.
func HeadRev(ctx context.Context, cwd string) string {
	gitCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(gitCtx, "git", "-C", cwd, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
