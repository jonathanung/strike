package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/project"
)

// Git routes back the Git and Worktree panels. Every invocation is
// exec.Command with an argument vector — never a shell. There is no push
// route, so the daemon cannot force-push. Hard reset, discard, branch
// delete, worktree remove, and stale prune refuse to run unless the body
// carries the exact confirm token.
//
// These are not engine tool calls. The permission ask service is not a
// daemon gate for arbitrary git (approvals stream). Until that gate exists,
// the confirm token is the destructive-action check.

const (
	gitReadTimeout  = 15 * time.Second
	gitWriteTimeout = 35 * time.Second
	gitDiffCap      = 200_000
	gitLogDefault   = 30
	gitLogMax       = 100

	confirmDiscard        = "discard"
	confirmResetHard      = "reset-hard"
	confirmDeleteBranch   = "delete-branch"
	confirmRemoveWorktree = "remove-worktree"
	confirmPruneStale     = "prune-stale"
)

type gitFileJSON struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Staged  bool   `json:"staged"`
}

type gitStatusJSON struct {
	Branch    string        `json:"branch"`
	Detached  bool          `json:"detached"`
	Ahead     int           `json:"ahead"`
	Behind    int           `json:"behind"`
	Staged    []gitFileJSON `json:"staged"`
	Unstaged  []gitFileJSON `json:"unstaged"`
	Untracked []gitFileJSON `json:"untracked"`
}

type gitBranchJSON struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

type gitCommitJSON struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// Branch-changes states (#1450). Anything but "ok" is an honest empty
// state the panel names instead of showing a misleading list.
const (
	branchStateOK          = "ok"
	branchStateOnBase      = "on-base"
	branchStateNoMergeBase = "no-merge-base"
	branchStateDetached    = "detached"
	branchStateNoCommits   = "no-commits"
	branchStateChooseBase  = "choose-base"
)

type gitBranchFileJSON struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary"`
}

// gitBranchChangesJSON is what the current branch contains versus its
// base: commits base..HEAD and the three-dot (merge-base...HEAD) files.
type gitBranchChangesJSON struct {
	State        string              `json:"state"`
	Branch       string              `json:"branch"`
	Base         string              `json:"base"`
	BaseDetected bool                `json:"baseDetected"`
	MergeBase    string              `json:"mergeBase"`
	Commits      []gitCommitJSON     `json:"commits"`
	CommitsTotal int                 `json:"commitsTotal"`
	Files        []gitBranchFileJSON `json:"files"`
}

type gitWorktreeJSON struct {
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	Head          string `json:"head"`
	SessionID     string `json:"sessionId,omitempty"`
	Owner         string `json:"owner"`
	OwnerTitle    string `json:"ownerTitle,omitempty"`
	ParentID      string `json:"parentId,omitempty"`
	Stale         bool   `json:"stale"`
	Primary       bool   `json:"primary"`
	StrikeManaged bool   `json:"strikeManaged"`
	Detached      bool   `json:"detached"`
}

type gitWorktreesJSON struct {
	ChildIsolation   string            `json:"childIsolation"`
	IsolatesChildren bool              `json:"isolatesChildren"`
	WorktreeCleanup  string            `json:"worktreeCleanup"`
	SessionWorktree  string            `json:"sessionWorktree"`
	RepoRoot         string            `json:"repoRoot,omitempty"`
	Worktrees        []gitWorktreeJSON `json:"worktrees"`
}

func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	defer cancel()
	body, err := gitStatus(ctx, cwd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	rel, err := safeRepoPath(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	staged := r.URL.Query().Get("staged") == "1" || r.URL.Query().Get("staged") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	defer cancel()
	diff, truncated, err := gitDiff(ctx, cwd, rel, staged)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": rel, "diff": diff, "truncated": truncated, "staged": staged,
	})
}

func (s *Server) handleGitBranches(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	defer cancel()
	current, branches, err := gitBranches(ctx, cwd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"current": current, "branches": branches})
}

func (s *Server) handleGitLog(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	limit, err := parseGitLogLimit(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	defer cancel()
	commits, err := gitLog(ctx, cwd, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": commits})
}

func (s *Server) handleGitBranchChanges(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	base, err := optionalBaseName(r.URL.Query().Get("base"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	limit, err := parseGitLogLimit(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	defer cancel()
	body, err := gitBranchChanges(ctx, cwd, base, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// parseGitLogLimit reads ?limit= for commit lists: default gitLogDefault,
// clamped to gitLogMax, 400 on a non-positive or non-numeric value.
func parseGitLogLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return gitLogDefault, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, errors.New("limit must be a positive integer")
	}
	return min(n, gitLogMax), nil
}

func (s *Server) handleGitBranchDiff(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	base, err := optionalBaseName(r.URL.Query().Get("base"))
	if err == nil && base == "" {
		err = errors.New("base is required")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	rel, err := safeRepoPath(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	defer cancel()
	diff, truncated, err := gitBranchDiff(ctx, cwd, base, rel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": rel, "base": base, "diff": diff, "truncated": truncated,
	})
}

func (s *Server) handleGitStage(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	paths, err := safeRepoPaths(body.Paths)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	if _, stderr, err := gitRun(ctx, cwd, append([]string{"add", "--"}, paths...)...); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: gitErr("stage", err, stderr)})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleGitUnstage(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	paths, err := safeRepoPaths(body.Paths)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	// Index only. The working tree stays as the user left it.
	args := append([]string{"restore", "--staged", "--"}, paths...)
	if _, stderr, err := gitRun(ctx, cwd, args...); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: gitErr("unstage", err, stderr)})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleGitCommit(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Message string   `json:"message"`
		Paths   []string `json:"paths"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	message := strings.TrimSpace(body.Message)
	if message == "" || strings.ContainsRune(message, '\x00') {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "commit message is required"})
		return
	}
	if len([]rune(message)) > 10000 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "commit message is too long"})
		return
	}
	paths, err := safeRepoPathsOptional(body.Paths)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	if len(paths) > 0 {
		if _, stderr, err := gitRun(ctx, cwd, append([]string{"add", "--"}, paths...)...); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: gitErr("stage", err, stderr)})
			return
		}
	}
	args := []string{"commit", "-m", message}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	if _, stderr, err := gitRun(ctx, cwd, args...); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: gitErr("commit", err, stderr)})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleGitCheckout(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Branch string `json:"branch"`
		Create bool   `json:"create"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	branch, err := safeBranchName(body.Branch)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	// git switch refuses a dirty tree. No --force, no --discard-changes.
	args := []string{"switch", branch}
	if body.Create {
		args = []string{"switch", "-c", branch}
	}
	if _, stderr, err := gitRun(ctx, cwd, args...); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: gitErr("switch", err, stderr)})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleGitDiscard(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Staged  bool   `json:"staged"`
		Confirm string `json:"confirm"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if body.Confirm != confirmDiscard {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "confirm must be " + confirmDiscard})
		return
	}
	rel, err := safeRepoPath(body.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	if err := gitDiscard(ctx, cwd, rel, body.Staged); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleGitReset(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode    string `json:"mode"`
		Confirm string `json:"confirm"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if body.Mode != "hard" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "only mode hard is supported"})
		return
	}
	if body.Confirm != confirmResetHard {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "confirm must be " + confirmResetHard})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	// HEAD only. Never a remote ref, never a push.
	if _, stderr, err := gitRun(ctx, cwd, "reset", "--hard", "HEAD"); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: gitErr("reset", err, stderr)})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleGitBranchDelete(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Confirm string `json:"confirm"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if body.Confirm != confirmDeleteBranch {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "confirm must be " + confirmDeleteBranch})
		return
	}
	name, err := safeBranchName(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	current, _, err := gitBranches(ctx, cwd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if name == current {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "cannot delete the current branch"})
		return
	}
	// -d, not -D: git refuses an unmerged branch.
	if _, stderr, err := gitRun(ctx, cwd, "branch", "-d", name); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: gitErr("branch delete", err, stderr)})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleGitWorktrees(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	defer cancel()
	body, err := s.gitWorktreeList(ctx, cwd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleGitWorktreeCreate(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	id := strings.TrimSpace(body.SessionID)
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if !s.knownSession(id) {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "session not found"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	wt, err := project.Add(ctx, cwd, id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"path": wt.Path, "branch": wt.Branch, "repoRoot": wt.RepoRoot, "sessionId": id,
	})
}

func (s *Server) handleGitWorktreeRemove(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Confirm string `json:"confirm"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if body.Confirm != confirmRemoveWorktree {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "confirm must be " + confirmRemoveWorktree})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	if err := s.removeStrikeWorktree(ctx, cwd, body.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleGitWorktreePrune(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if body.Confirm != confirmPruneStale {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "confirm must be " + confirmPruneStale})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	removed, err := s.pruneStaleWorktrees(ctx, cwd)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if removed == nil {
		removed = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

func (s *Server) gitCWD(w http.ResponseWriter, r *http.Request) (string, bool) {
	cwd := strings.TrimSpace(s.currentCWD(r))
	if cwd == "" {
		capabilityUnavailable(w, "git")
		return "", false
	}
	return cwd, true
}

func (s *Server) knownSession(id string) bool {
	if liveID := s.liveSessionID(); liveID != "" && liveID == id {
		return true
	}
	for _, info := range s.sessionInfos() {
		if info.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) liveSessionID() string {
	if s.opts.LiveHub != nil {
		if live := s.opts.LiveHub.Active(); live != nil {
			return live.SessionID()
		}
	}
	if s.opts.Live != nil {
		return s.opts.Live.SessionID()
	}
	return ""
}

func (s *Server) sessionInfos() []session.Info {
	if strings.TrimSpace(s.opts.SessionDir) == "" {
		return nil
	}
	list, err := session.NewManager(s.opts.SessionDir).List()
	if err != nil {
		return nil
	}
	return list
}

func gitStatus(ctx context.Context, cwd string) (gitStatusJSON, error) {
	out, stderr, err := gitRun(ctx, cwd, "status", "--porcelain=v1", "-z", "-b")
	if err != nil {
		return gitStatusJSON{}, errors.New(gitErr("status", err, stderr))
	}
	branchLine, entries := parseStatusZ(out)
	branch, detached, ahead, behind := parseBranchLine(branchLine)
	unstagedN, _, _ := gitRun(ctx, cwd, "diff", "--numstat", "-z")
	stagedN, _, _ := gitRun(ctx, cwd, "diff", "--cached", "--numstat", "-z")
	unstagedCounts := parseNumstatZ(unstagedN)
	stagedCounts := parseNumstatZ(stagedN)
	body := gitStatusJSON{
		Branch: branch, Detached: detached, Ahead: ahead, Behind: behind,
		Staged: []gitFileJSON{}, Unstaged: []gitFileJSON{}, Untracked: []gitFileJSON{},
	}
	for _, ent := range entries {
		switch {
		case ent.xy == "!!":
			continue
		case ent.xy == "??":
			body.Untracked = append(body.Untracked, gitFileJSON{
				Path: ent.path, Status: "?", Added: countLines(cwd, ent.path), Staged: false,
			})
		default:
			if ent.xy[0] != ' ' && ent.xy[0] != '?' {
				added, deleted := countsFor(stagedCounts, ent.path)
				body.Staged = append(body.Staged, gitFileJSON{
					Path: ent.path, Status: string(ent.xy[0]), Added: added, Deleted: deleted, Staged: true,
				})
			}
			if len(ent.xy) > 1 && ent.xy[1] != ' ' && ent.xy[1] != '?' {
				added, deleted := countsFor(unstagedCounts, ent.path)
				body.Unstaged = append(body.Unstaged, gitFileJSON{
					Path: ent.path, Status: string(ent.xy[1]), Added: added, Deleted: deleted, Staged: false,
				})
			}
		}
	}
	return body, nil
}

type statusEntry struct {
	xy   string
	path string
}

func parseStatusZ(out []byte) (string, []statusEntry) {
	parts := bytes.Split(out, []byte{0})
	branch := ""
	var entries []statusEntry
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if len(p) == 0 {
			continue
		}
		if bytes.HasPrefix(p, []byte("## ")) {
			branch = string(p)
			continue
		}
		if len(p) < 3 || p[2] != ' ' {
			continue
		}
		xy := string(p[:2])
		name := string(p[3:])
		if xy[0] == 'R' || xy[0] == 'C' || xy[1] == 'R' || xy[1] == 'C' {
			// Next record is the original path. The panel shows the new name.
			if i+1 < len(parts) {
				i++
			}
		}
		if _, err := safeRepoPath(name); err != nil {
			continue
		}
		entries = append(entries, statusEntry{xy: xy, path: name})
	}
	return branch, entries
}

func parseBranchLine(line string) (branch string, detached bool, ahead, behind int) {
	line = strings.TrimPrefix(line, "## ")
	if strings.Contains(line, "(no branch)") {
		return "HEAD", true, 0, 0
	}
	const yet = "No commits yet on "
	if strings.HasPrefix(line, yet) {
		return strings.TrimSpace(strings.TrimPrefix(line, yet)), false, 0, 0
	}
	name := line
	if i := strings.IndexAny(name, " .["); i >= 0 {
		// "main...origin/main [ahead 1]" — cut at "..." or " [".
		if j := strings.Index(name, "..."); j >= 0 {
			name = name[:j]
		} else if j := strings.Index(name, " ["); j >= 0 {
			name = name[:j]
		}
	}
	if i := strings.Index(line, "["); i >= 0 {
		inner := strings.TrimSuffix(line[i+1:], "]")
		for _, part := range strings.Split(inner, ",") {
			part = strings.TrimSpace(part)
			if n, ok := strings.CutPrefix(part, "ahead "); ok {
				ahead, _ = strconv.Atoi(n)
			}
			if n, ok := strings.CutPrefix(part, "behind "); ok {
				behind, _ = strconv.Atoi(n)
			}
		}
	}
	return strings.TrimSpace(name), false, ahead, behind
}

func parseNumstatZ(out []byte) map[string][2]int {
	m := map[string][2]int{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		parts := bytes.SplitN(rec, []byte{'\t'}, 3)
		if len(parts) != 3 {
			continue
		}
		added, _ := strconv.Atoi(string(parts[0]))
		deleted, _ := strconv.Atoi(string(parts[1]))
		p := string(parts[2])
		if i := strings.LastIndex(p, " => "); i >= 0 {
			p = strings.TrimSpace(p[i+4:])
		}
		m[p] = [2]int{added, deleted}
	}
	return m
}

func countsFor(m map[string][2]int, p string) (int, int) {
	if c, ok := m[p]; ok {
		return c[0], c[1]
	}
	return 0, 0
}

func countLines(cwd, rel string) int {
	abs, err := filepath.Abs(filepath.Join(cwd, filepath.FromSlash(rel)))
	if err != nil {
		return 0
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() || info.Size() > 1<<20 {
		return 0
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return 0
	}
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func gitDiff(ctx context.Context, cwd, rel string, staged bool) (string, bool, error) {
	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--", rel)
	out, stderr, err := gitRun(ctx, cwd, args...)
	if err != nil {
		return "", false, errors.New(gitErr("diff", err, stderr))
	}
	if len(bytes.TrimSpace(out)) == 0 && !staged {
		tracked, _, _ := gitRun(ctx, cwd, "ls-files", "--", rel)
		if len(bytes.TrimSpace(tracked)) == 0 {
			abs := filepath.Join(cwd, filepath.FromSlash(rel))
			out, _, _ = gitRun(ctx, cwd, "diff", "--no-index", "--", "/dev/null", abs)
		}
	}
	truncated := false
	if len(out) > gitDiffCap {
		out = out[:gitDiffCap]
		truncated = true
	}
	return string(out), truncated, nil
}

func gitBranches(ctx context.Context, cwd string) (string, []gitBranchJSON, error) {
	// A literal tab separates the fields: for-each-ref formats on
	// git < 2.44 pass "%x1f" through uninterpreted, which leaked the
	// escape into branch names in the panel (prep port fix).
	out, stderr, err := gitRun(ctx, cwd, "for-each-ref", "--format=%(refname:short)\t%(HEAD)", "refs/heads")
	if err != nil {
		return "", nil, errors.New(gitErr("branches", err, stderr))
	}
	current := ""
	branches := []gitBranchJSON{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, head, _ := strings.Cut(line, "\t")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		on := strings.TrimSpace(head) == "*"
		if on {
			current = name
		}
		branches = append(branches, gitBranchJSON{Name: name, Current: on})
	}
	if current == "" {
		rev, _, err := gitRun(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
		if err == nil {
			current = strings.TrimSpace(string(rev))
			if current == "HEAD" {
				current = ""
			}
		}
	}
	return current, branches, nil
}

func gitLog(ctx context.Context, cwd string, limit int) ([]gitCommitJSON, error) {
	out, stderr, err := gitRun(ctx, cwd, "log", "-n", strconv.Itoa(limit), "--format="+gitLogFormat)
	if err != nil {
		if strings.Contains(stderr, "does not have any commits") {
			return []gitCommitJSON{}, nil
		}
		return nil, errors.New(gitErr("log", err, stderr))
	}
	return parseLogLines(out), nil
}

const gitLogFormat = "%H%x1f%an%x1f%aI%x1f%s"

// parseLogLines reads gitLogFormat records, one per line.
func parseLogLines(out []byte) []gitCommitJSON {
	commits := []gitCommitJSON{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, gitCommitJSON{
			Hash: parts[0], Author: parts[1], Date: parts[2], Subject: parts[3],
		})
	}
	return commits
}

// defaultBaseBranch picks the repo's default branch: local main, else
// master, else the remote's HEAD (origin/HEAD). "" means no reliable
// default; the panel then asks the user to choose (#1450 Q2).
func defaultBaseBranch(ctx context.Context, cwd string) string {
	for _, cand := range []string{"main", "master"} {
		if resolveCommit(ctx, cwd, "refs/heads/"+cand) != "" {
			return cand
		}
	}
	out, _, err := gitRun(ctx, cwd, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return ""
	}
	// A dangling origin/HEAD (remote branch pruned) is no default: ask.
	name := strings.TrimSpace(string(out))
	if name == "" || resolveCommit(ctx, cwd, "refs/remotes/"+name) == "" {
		return ""
	}
	return name
}

// resolveBase resolves a base branch name preferring branch refs over a
// same-named tag: refs/heads/<name>, then refs/remotes/<name>, then the
// plain name (any other validated ref).
func resolveBase(ctx context.Context, cwd, name string) string {
	for _, ref := range []string{"refs/heads/" + name, "refs/remotes/" + name, name} {
		if rev := resolveCommit(ctx, cwd, ref); rev != "" {
			return rev
		}
	}
	return ""
}

// resolveCommit returns the full hash of rev or "" when it does not name
// a commit. rev must already be validated (no leading "-").
func resolveCommit(ctx context.Context, cwd, rev string) string {
	out, _, err := gitRun(ctx, cwd, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitBranchChanges answers "what is on this branch versus base" (#1450):
// commits base..HEAD (newest first, up to limit, with the full count) and
// files changed merge-base...HEAD (three-dot, Q1) with +/- counts. It is
// read-only. base "" means detect the default branch.
func gitBranchChanges(ctx context.Context, cwd, base string, limit int) (gitBranchChangesJSON, error) {
	body := gitBranchChangesJSON{
		State: branchStateOK, Base: base,
		Commits: []gitCommitJSON{}, Files: []gitBranchFileJSON{},
	}
	head := resolveCommit(ctx, cwd, "HEAD")
	if head == "" {
		body.State = branchStateNoCommits
		return body, nil
	}
	branch, _, err := gitRun(ctx, cwd, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		body.State = branchStateDetached
		return body, nil
	}
	body.Branch = strings.TrimSpace(string(branch))
	if body.Base == "" {
		body.Base = defaultBaseBranch(ctx, cwd)
		body.BaseDetected = body.Base != ""
	}
	if body.Base == "" {
		body.State = branchStateChooseBase
		return body, nil
	}
	if body.Base == body.Branch {
		body.State = branchStateOnBase
		return body, nil
	}
	baseRev := resolveBase(ctx, cwd, body.Base)
	if baseRev == "" {
		return gitBranchChangesJSON{}, errors.New("unknown base branch: " + body.Base)
	}
	mb, _, err := gitRun(ctx, cwd, "merge-base", baseRev, head)
	if err != nil {
		body.State = branchStateNoMergeBase
		return body, nil
	}
	body.MergeBase = strings.TrimSpace(string(mb))
	span := baseRev + ".." + head
	out, stderr, err := gitRun(ctx, cwd, "log", "-n", strconv.Itoa(limit), "--format="+gitLogFormat, span)
	if err != nil {
		return gitBranchChangesJSON{}, errors.New(gitErr("log", err, stderr))
	}
	body.Commits = parseLogLines(out)
	count, stderr, err := gitRun(ctx, cwd, "rev-list", "--count", span)
	if err != nil {
		return gitBranchChangesJSON{}, errors.New(gitErr("rev-list", err, stderr))
	}
	body.CommitsTotal, _ = strconv.Atoi(strings.TrimSpace(string(count)))
	numstat, stderr, err := gitRun(ctx, cwd, "diff", "--numstat", "-z", "--no-renames", body.MergeBase, head)
	if err != nil {
		return gitBranchChangesJSON{}, errors.New(gitErr("diff", err, stderr))
	}
	body.Files = parseNumstatFilesZ(numstat)
	return body, nil
}

// gitBranchDiff is one file's cumulative branch diff, merge-base...HEAD.
func gitBranchDiff(ctx context.Context, cwd, base, rel string) (string, bool, error) {
	head := resolveCommit(ctx, cwd, "HEAD")
	baseRev := resolveBase(ctx, cwd, base)
	if head == "" || baseRev == "" {
		return "", false, errors.New("unknown base branch: " + base)
	}
	mb, _, err := gitRun(ctx, cwd, "merge-base", baseRev, head)
	if err != nil {
		return "", false, errors.New("no common history with " + base)
	}
	// --literal-pathspecs: "app/[id]/page.tsx" is a path, not a glob.
	out, stderr, err := gitRun(ctx, cwd, "--literal-pathspecs", "diff", "--no-renames", strings.TrimSpace(string(mb)), head, "--", rel)
	if err != nil {
		return "", false, errors.New(gitErr("diff", err, stderr))
	}
	truncated := false
	if len(out) > gitDiffCap {
		out = out[:gitDiffCap]
		truncated = true
	}
	return string(out), truncated, nil
}

// parseNumstatFilesZ reads `--numstat -z --no-renames` records
// ("added\tdeleted\tpath\0"); binary files report "-" counts.
func parseNumstatFilesZ(out []byte) []gitBranchFileJSON {
	files := []gitBranchFileJSON{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		rec = bytes.TrimLeft(rec, "\n")
		if len(rec) == 0 {
			continue
		}
		parts := bytes.SplitN(rec, []byte{'\t'}, 3)
		if len(parts) != 3 || len(parts[2]) == 0 {
			continue
		}
		f := gitBranchFileJSON{Path: string(parts[2])}
		if string(parts[0]) == "-" && string(parts[1]) == "-" {
			f.Binary = true
		} else {
			f.Added, _ = strconv.Atoi(string(parts[0]))
			f.Deleted, _ = strconv.Atoi(string(parts[1]))
		}
		files = append(files, f)
	}
	return files
}

// optionalBaseName validates a base branch for read-only comparison:
// local ("main") or remote-tracking ("origin/main") names, never an
// option or revision expression. "" is allowed (detect the default).
func optionalBaseName(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", nil
	}
	name, err := safeBranchName(base)
	if err != nil {
		return "", errors.New("invalid base branch")
	}
	return name, nil
}

func gitDiscard(ctx context.Context, cwd, rel string, staged bool) error {
	tracked, _, _ := gitRun(ctx, cwd, "ls-files", "--", rel)
	if len(bytes.TrimSpace(tracked)) == 0 {
		abs, err := absInside(cwd, rel)
		if err != nil {
			return err
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return errors.New("refusing to delete an untracked directory")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("refusing to delete a symlink")
		}
		return os.Remove(abs)
	}
	args := []string{"restore", "--worktree", "--", rel}
	if staged {
		args = []string{"restore", "--source=HEAD", "--staged", "--worktree", "--", rel}
	}
	if _, stderr, err := gitRun(ctx, cwd, args...); err != nil {
		return errors.New(gitErr("discard", err, stderr))
	}
	return nil
}

func (s *Server) gitWorktreeList(ctx context.Context, cwd string) (gitWorktreesJSON, error) {
	repo, err := project.MainRoot(ctx, cwd)
	if err != nil {
		return gitWorktreesJSON{}, err
	}
	out, stderr, err := gitRun(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return gitWorktreesJSON{}, errors.New(gitErr("worktree list", err, stderr))
	}
	infos := s.sessionInfos()
	byID := map[string]session.Info{}
	byPath := map[string]session.Info{}
	for _, info := range infos {
		byID[info.ID] = info
		if p := strings.TrimSpace(info.WorktreePath); p != "" {
			if c, err := filepath.Abs(p); err == nil {
				byPath[c] = info
			}
		}
	}
	body := gitWorktreesJSON{
		ChildIsolation:  "off",
		WorktreeCleanup: project.CleanupKeep,
		SessionWorktree: project.WorktreeOff,
		RepoRoot:        repo,
		Worktrees:       []gitWorktreeJSON{},
	}
	if cfg, err := config.Load(cwd); err == nil {
		raw := strings.TrimSpace(cfg.Session.ChildIsolation)
		if raw == "" {
			raw = "off"
		}
		body.ChildIsolation = raw
		body.IsolatesChildren = config.WantChildWorktree(cfg.Session.ChildIsolation, "")
		if c := strings.TrimSpace(cfg.Session.WorktreeCleanup); c != "" {
			body.WorktreeCleanup = c
		}
		if m := strings.TrimSpace(cfg.Session.Worktree); m != "" {
			body.SessionWorktree = m
		}
	}
	for _, wt := range parseWorktreePorcelain(string(out)) {
		row := gitWorktreeJSON{
			Path: wt.path, Branch: wt.branch, Head: wt.head, Detached: wt.detached,
			Owner: "other",
		}
		abs := wt.path
		if c, err := filepath.Abs(wt.path); err == nil {
			abs = c
		}
		repoAbs := repo
		if c, err := filepath.Abs(repo); err == nil {
			repoAbs = c
		}
		if abs == repoAbs {
			row.Primary = true
			row.Owner = "primary"
			body.Worktrees = append(body.Worktrees, row)
			continue
		}
		info, known := byPath[abs]
		managedID := ""
		if id, ok := strikeWorktreeID(repoAbs, abs); ok {
			row.StrikeManaged = true
			managedID = id
			if !known {
				if hit, ok := byID[id]; ok {
					info, known = hit, true
				}
			}
		}
		if known {
			row.SessionID = info.ID
			row.OwnerTitle = info.Title
			row.ParentID = info.ParentSessionID
			if info.ParentSessionID != "" {
				row.Owner = "child"
			} else {
				row.Owner = "session"
			}
		} else if row.StrikeManaged {
			row.SessionID = managedID
			row.Owner = "stale"
			row.Stale = true
		}
		if _, err := os.Stat(abs); err != nil && os.IsNotExist(err) {
			row.Stale = true
		}
		body.Worktrees = append(body.Worktrees, row)
	}
	return body, nil
}

type porcelainWorktree struct {
	path     string
	head     string
	branch   string
	detached bool
}

func parseWorktreePorcelain(text string) []porcelainWorktree {
	var out []porcelainWorktree
	var cur porcelainWorktree
	flush := func() {
		if cur.path != "" {
			out = append(out, cur)
		}
		cur = porcelainWorktree{}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			if cur.path != "" {
				flush()
			}
			cur.path = val
		case "HEAD":
			cur.head = val
		case "branch":
			cur.branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.detached = true
		}
	}
	flush()
	return out
}

func strikeWorktreeID(repo, abs string) (string, bool) {
	root := filepath.Join(repo, ".strike", "worktrees")
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	if strings.Contains(rel, string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func (s *Server) removeStrikeWorktree(ctx context.Context, cwd, rawPath string) error {
	repo, err := project.MainRoot(ctx, cwd)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(strings.TrimSpace(rawPath))
	if err != nil {
		return err
	}
	repoAbs, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	if abs == repoAbs {
		return errors.New("refusing to remove the primary checkout")
	}
	id, ok := strikeWorktreeID(repoAbs, abs)
	if !ok {
		return errors.New("refusing to remove a worktree outside .strike/worktrees")
	}
	return project.Remove(ctx, repoAbs, abs, project.BranchName(id))
}

func (s *Server) pruneStaleWorktrees(ctx context.Context, cwd string) ([]string, error) {
	repo, err := project.MainRoot(ctx, cwd)
	if err != nil {
		return nil, err
	}
	_, stderr, err := gitRun(ctx, repo, "worktree", "prune")
	if err != nil {
		return nil, errors.New(gitErr("worktree prune", err, stderr))
	}
	body, err := s.gitWorktreeList(ctx, cwd)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, wt := range body.Worktrees {
		if !wt.Stale || !wt.StrikeManaged || wt.Primary {
			continue
		}
		if err := s.removeStrikeWorktree(ctx, cwd, wt.Path); err != nil {
			return removed, err
		}
		removed = append(removed, wt.Path)
	}
	return removed, nil
}

func gitRun(ctx context.Context, cwd string, args ...string) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), strings.TrimSpace(stderr.String()), err
}

func gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_WORK_TREE=") || strings.HasPrefix(e, "GIT_INDEX_FILE=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	return env
}

func gitErr(what string, err error, stderr string) string {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	return "git " + what + ": " + msg
}

func safeRepoPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsRune(p, '\x00') {
		return "", errors.New("path is empty")
	}
	if filepath.IsAbs(p) || strings.Contains(p, "\\") {
		return "", errors.New("path must be relative")
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path escapes the repository")
	}
	return clean, nil
}

func safeRepoPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, errors.New("paths is required")
	}
	if len(paths) > 50 {
		return nil, errors.New("too many paths")
	}
	return safeRepoPathsOptional(paths)
}

func safeRepoPathsOptional(paths []string) ([]string, error) {
	if len(paths) > 50 {
		return nil, errors.New("too many paths")
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		clean, err := safeRepoPath(p)
		if err != nil {
			return nil, err
		}
		out = append(out, clean)
	}
	return out, nil
}

func safeBranchName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return "", errors.New("invalid branch name")
	}
	if strings.HasPrefix(name, "-") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return "", errors.New("invalid branch name")
	}
	if strings.ContainsAny(name, " \t\n\r~^:?*[\\") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") {
		return "", errors.New("invalid branch name")
	}
	return name, nil
}

func absInside(cwd, rel string) (string, error) {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	abs, err = filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", errors.New("path escapes the repository")
	}
	return abs, nil
}
