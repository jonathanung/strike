package tool

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// shadowGit is a separate git directory that tracks workDir without touching
// the project's real .git history (Cline/OpenCode-style). Used to discover
// bash-driven mutations and recover pre-mutation bytes for CheckpointStore.
type shadowGit struct {
	mu      sync.Mutex
	gitDir  string
	workDir string
	ready   bool
	failed  bool // permanent disable after init failure
}

func newShadowGit(gitDir, workDir string) *shadowGit {
	gitDir = strings.TrimSpace(gitDir)
	workDir = strings.TrimSpace(workDir)
	if gitDir == "" || workDir == "" {
		return nil
	}
	return &shadowGit{gitDir: filepath.Clean(gitDir), workDir: filepath.Clean(workDir)}
}

// ensure initializes the shadow repo once. Best-effort: missing git or init
// failure disables the helper for the process lifetime of this store.
func (s *shadowGit) ensure() error {
	if s == nil {
		return fmt.Errorf("shadow git: nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return fmt.Errorf("shadow git: disabled")
	}
	if s.ready {
		return nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		s.failed = true
		return fmt.Errorf("shadow git: git not available: %w", err)
	}
	if err := os.MkdirAll(s.gitDir, 0o700); err != nil {
		s.failed = true
		return err
	}
	// Already initialized?
	if _, err := os.Stat(filepath.Join(s.gitDir, "HEAD")); err == nil {
		s.ready = true
		return nil
	}
	if out, err := s.runLocked("init"); err != nil {
		s.failed = true
		return fmt.Errorf("shadow git init: %w (%s)", err, out)
	}
	// Identity required for commits on some git versions; write-tree needs none
	// but commit is more portable for a stable baseline ref.
	_, _ = s.runLocked("config", "user.email", "strike@localhost")
	_, _ = s.runLocked("config", "user.name", "strike")
	// Never follow the project's hooks / system config surprises.
	_, _ = s.runLocked("config", "commit.gpgsign", "false")
	// Exclude nested VCS metadata from the shadow index.
	exclude := filepath.Join(s.gitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o700); err == nil {
		_ = os.WriteFile(exclude, []byte(".git/\n"), 0o644)
	}
	s.ready = true
	return nil
}

// capture stages the worktree and returns a tree object id. Empty string on
// soft failure (caller treats as unavailable).
func (s *shadowGit) capture() (string, error) {
	if err := s.ensure(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stage everything under workDir. -A picks up adds/modifies/deletes.
	// Pathspec excludes keep the project's .git out when workDir is a repo root.
	if out, err := s.runLocked("add", "-A", "--", "."); err != nil {
		return "", fmt.Errorf("shadow git add: %w (%s)", err, out)
	}
	out, err := s.runLocked("write-tree")
	if err != nil {
		return "", fmt.Errorf("shadow git write-tree: %w (%s)", err, out)
	}
	tree := strings.TrimSpace(out)
	if tree == "" {
		return "", fmt.Errorf("shadow git write-tree: empty")
	}
	return tree, nil
}

// shadowChange is one path that differs between two trees (or tree vs worktree).
type shadowChange struct {
	// Path is absolute under workDir.
	Path string
	// Status is a git name-status letter: A/M/D/T (and first letter of R/C).
	Status byte
}

// diffTrees lists paths that changed from fromTree to toTree (both tree oids).
func (s *shadowGit) diffTrees(fromTree, toTree string) ([]shadowChange, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	fromTree = strings.TrimSpace(fromTree)
	toTree = strings.TrimSpace(toTree)
	if fromTree == "" || toTree == "" {
		return nil, fmt.Errorf("shadow git diff: empty tree")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.runLocked("diff-tree", "--no-renames", "-r", "--name-status", fromTree, toTree)
	if err != nil {
		return nil, fmt.Errorf("shadow git diff-tree: %w (%s)", err, out)
	}
	return parseNameStatus(s.workDir, out), nil
}

// readAtTree returns the file bytes at rel-or-abs path in tree. exists=false
// when the path is absent from the tree (created after baseline).
func (s *shadowGit) readAtTree(tree, absPath string) (data []byte, exists bool, err error) {
	if err := s.ensure(); err != nil {
		return nil, false, err
	}
	tree = strings.TrimSpace(tree)
	if tree == "" {
		return nil, false, fmt.Errorf("shadow git cat: empty tree")
	}
	rel, err := filepath.Rel(s.workDir, filepath.Clean(absPath))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, false, fmt.Errorf("shadow git cat: path outside workdir")
	}
	// git always wants forward slashes
	relGit := filepath.ToSlash(rel)
	if relGit == "." {
		return nil, false, fmt.Errorf("shadow git cat: path is workdir root")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.runLocked("cat-file", "-p", tree+":"+relGit)
	if err != nil {
		// Missing path in tree → created after baseline.
		msg := strings.ToLower(string(out) + err.Error())
		if strings.Contains(msg, "does not exist") ||
			strings.Contains(msg, "exists on disk") ||
			strings.Contains(msg, "not in") ||
			strings.Contains(msg, "path not in") ||
			strings.Contains(msg, "bad object") ||
			strings.Contains(msg, "not found") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("shadow git cat-file: %w (%s)", err, out)
	}
	return []byte(out), true, nil
}

func parseNameStatus(workDir, out string) []shadowChange {
	var changes []shadowChange
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Formats: "M\tpath", "A\tpath", "D\tpath"
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		st := strings.TrimSpace(parts[0])
		if st == "" {
			continue
		}
		rel := strings.TrimSpace(parts[1])
		if rel == "" {
			continue
		}
		abs := filepath.Join(workDir, filepath.FromSlash(rel))
		changes = append(changes, shadowChange{Path: abs, Status: st[0]})
	}
	return changes
}

func (s *shadowGit) runLocked(args ...string) (string, error) {
	// Caller holds s.mu (except ensure's first LookPath path — ensure locks).
	cmdArgs := make([]string, 0, len(args)+4)
	cmdArgs = append(cmdArgs, "--git-dir="+s.gitDir, "--work-tree="+s.workDir)
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Dir = s.workDir
	cmd.Env = shadowGitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		return strings.TrimSpace(stdout.String() + stderr.String()), err
	}
	return out, nil
}

func shadowGitEnv() []string {
	// Keep author identity stable and avoid reading user gitconfig noise.
	base := os.Environ()
	extra := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=strike",
		"GIT_AUTHOR_EMAIL=strike@localhost",
		"GIT_COMMITTER_NAME=strike",
		"GIT_COMMITTER_EMAIL=strike@localhost",
		// Fixed timestamps keep tests deterministic when commits are used.
		"GIT_AUTHOR_DATE=" + time.Unix(0, 0).UTC().Format(time.RFC3339),
		"GIT_COMMITTER_DATE=" + time.Unix(0, 0).UTC().Format(time.RFC3339),
	}
	return append(base, extra...)
}
