package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultGitMaxResults caps model-facing git lists (files, commits, blame lines).
	DefaultGitMaxResults = 100
	// MaxGitMaxResults is the hard upper bound for maxResults.
	MaxGitMaxResults    = 500
	gitDefaultTimeout   = 30 * time.Second
	gitMaxProcessOutput = 128_000
	gitMaxHunkLines     = 200
	gitMaxNoteRunes     = 240
)

// gitMutatingActions are rejected by name so the tool cannot be used as a
// write surface (commit/push/reset/checkout/config and other mutators).
var gitMutatingActions = map[string]struct{}{
	"add": {}, "am": {}, "apply": {}, "branch": {}, "checkout": {},
	"cherry-pick": {}, "clean": {}, "clone": {}, "commit": {}, "config": {},
	"fetch": {}, "init": {}, "merge": {}, "mv": {}, "notes": {},
	"pull": {}, "push": {}, "rebase": {}, "remote": {}, "reset": {},
	"restore": {}, "revert": {}, "rm": {}, "stash": {}, "submodule": {},
	"switch": {}, "tag": {}, "worktree": {},
}

var gitAllowedActions = map[string]struct{}{
	"status": {}, "diff": {}, "log": {}, "blame": {}, "show": {},
}

type gitTool struct{}

// NewGit returns the read-only structured git tool. Deferred when deferTools is on.
func NewGit() Tool { return gitTool{} }

func (gitTool) Name() string { return "git" }

func (gitTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (gitTool) Description() string {
	return `Read-only git status, diff, log, blame, and show for the workspace repository.

Use instead of bash git for ordinary history inspection. Returns bounded structured JSON
(paths, hunks, subjects, blame lines) — not pager dumps. Cannot commit, push, reset,
checkout, or change config.

Usage notes:
  - action is required: status, diff, log, blame, or show.
  - Workspace-root git only. Fails closed when the working directory is not a repo
    or the git toplevel is outside the workspace.
  - path is optional for status/diff/log/show and required for blame (workspace-relative or absolute).
  - ref is optional (commit/ref; defaults to HEAD for show). Must not start with "-".
  - staged=true on diff uses the index (git diff --cached).
  - maxResults bounds returned files, commits, or blame lines (default 100, max 500).
  - Mutation actions (commit, push, reset, checkout, config, …) are rejected.
  - When deferred tool schemas are enabled, discover via toolsearch ("git").`
}

func (gitTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["status", "diff", "log", "blame", "show"], "description": "Read-only git action"},
			"path": {"type": "string", "description": "Optional path scope (required for blame)"},
			"ref": {"type": "string", "description": "Commit or ref (show defaults to HEAD; optional for diff/log/blame)"},
			"staged": {"type": "boolean", "description": "For diff: compare the index to HEAD (git diff --cached)"},
			"maxResults": {"type": "integer", "description": "Maximum files, commits, or blame lines to return (default 100, max 500)"}
		},
		"required": ["action"]
	}`)
}

type gitArgs struct {
	Action     string `json:"action"`
	Path       string `json:"path"`
	Ref        string `json:"ref"`
	Staged     bool   `json:"staged"`
	MaxResults int    `json:"maxResults"`
}

type gitFileEntry struct {
	Path     string    `json:"path"`
	OldPath  string    `json:"oldPath,omitempty"`
	Index    string    `json:"index,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Status   string    `json:"status,omitempty"`
	Hunks    []gitHunk `json:"hunks,omitempty"`
}

type gitHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Header   string   `json:"header,omitempty"`
	Lines    []string `json:"lines"`
}

type gitCommit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short,omitempty"`
	Author  string `json:"author,omitempty"`
	Email   string `json:"email,omitempty"`
	Date    string `json:"date,omitempty"`
	Subject string `json:"subject"`
	Body    string `json:"body,omitempty"`
}

type gitBlameLine struct {
	Line    int    `json:"line"`
	Hash    string `json:"hash"`
	Author  string `json:"author,omitempty"`
	Date    string `json:"date,omitempty"`
	Content string `json:"content"`
}

type gitPayload struct {
	OK        bool           `json:"ok"`
	Action    string         `json:"action"`
	Branch    string         `json:"branch,omitempty"`
	Ahead     int            `json:"ahead,omitempty"`
	Behind    int            `json:"behind,omitempty"`
	Path      string         `json:"path,omitempty"`
	Ref       string         `json:"ref,omitempty"`
	Staged    bool           `json:"staged,omitempty"`
	Files     []gitFileEntry `json:"files,omitempty"`
	Commits   []gitCommit    `json:"commits,omitempty"`
	Lines     []gitBlameLine `json:"lines,omitempty"`
	Commit    *gitCommit     `json:"commit,omitempty"`
	Count     int            `json:"count"`
	Total     int            `json:"total"`
	Truncated bool           `json:"truncated"`
	Note      string         `json:"note,omitempty"`
}

func (gitTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if tc == nil || strings.TrimSpace(tc.WorkDir) == "" {
		return Result{}, ErrPrecondition("work directory is empty")
	}
	var a gitArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return Result{}, ErrInvalidArgs("invalid arguments: " + err.Error())
		}
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		return Result{}, ErrInvalidArgs("action is required")
	}
	if _, mut := gitMutatingActions[action]; mut {
		return Result{}, ErrInvalidArgs("git tool is read-only; " + action + " is not allowed")
	}
	if _, ok := gitAllowedActions[action]; !ok {
		return Result{}, ErrInvalidArgs("unknown git action; want status, diff, log, blame, or show")
	}
	if a.Staged && action != "diff" {
		return Result{}, ErrInvalidArgs("staged is only valid for action=diff")
	}
	ref, err := normalizeGitRef(a.Ref, action)
	if err != nil {
		return Result{}, err
	}
	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultGitMaxResults
	}
	if maxResults > MaxGitMaxResults {
		maxResults = MaxGitMaxResults
	}

	relPath := ""
	if p := strings.TrimSpace(a.Path); p != "" {
		_, rel, rerr := resolveInWorkspace(tc.WorkDir, p)
		if rerr != nil {
			return Result{}, rerr
		}
		relPath = rel
	}
	if action == "blame" && relPath == "" {
		return Result{}, ErrInvalidArgs("path is required for blame")
	}

	pattern := action
	if relPath != "" {
		pattern = action + " " + relPath
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "git",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	if _, err := assertWorkspaceGit(ctx, tc); err != nil {
		return Result{}, err
	}

	switch action {
	case "status":
		return gitStatus(ctx, tc, relPath, maxResults)
	case "diff":
		return gitDiff(ctx, tc, relPath, ref, a.Staged, maxResults)
	case "log":
		return gitLog(ctx, tc, relPath, ref, maxResults)
	case "blame":
		return gitBlame(ctx, tc, relPath, ref, maxResults)
	case "show":
		return gitShow(ctx, tc, relPath, ref, maxResults)
	default:
		return Result{}, ErrInvalidArgs("unknown git action; want status, diff, log, blame, or show")
	}
}

func normalizeGitRef(raw, action string) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		if action == "show" {
			return "HEAD", nil
		}
		return "", nil
	}
	if strings.HasPrefix(ref, "-") {
		return "", ErrInvalidArgs("ref must not start with '-'")
	}
	if strings.ContainsAny(ref, "\x00\n\r") {
		return "", ErrInvalidArgs("ref contains invalid characters")
	}
	return ref, nil
}

func gitProcessEnv() []string {
	// Do not copy HOME: user ~/.gitconfig aliases must not apply.
	keys := []string{
		"PATH", "USER", "LOGNAME",
		"LANG", "LC_ALL", "LC_CTYPE",
		"TMPDIR", "TMP", "TEMP",
	}
	env := minimalEnvFromHost(keys)
	return append(env,
		"LC_ALL=C",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)
}

func runGit(ctx context.Context, tc *Context, args []string) (ProcessResult, error) {
	if len(args) == 0 || args[0] == "" {
		return ProcessResult{}, ErrInternal("git argv missing subcommand")
	}
	cmd := args[0]
	// alias.<cmd>=<cmd> forces the builtin (aliases do not recurse).
	argv := append([]string{
		"git", "--no-pager",
		"-c", "core.quotepath=false",
		"-c", "log.showSignature=false",
		"-c", "alias." + cmd + "=" + cmd,
	}, args...)
	proc, err := RunProcess(ctx, ProcessSpec{
		Argv:      argv,
		Dir:       tc.WorkDir,
		Env:       gitProcessEnv(),
		Timeout:   gitDefaultTimeout,
		MaxOutput: gitMaxProcessOutput,
	}, ProcessObserver{})
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			return ProcessResult{}, ErrPrecondition("git executable not found")
		}
		return ProcessResult{}, err
	}
	switch proc.Status {
	case ProcessStatusTimeout:
		return proc, ErrTimeout("git timed out")
	case ProcessStatusCanceled:
		return proc, ErrCanceled("git canceled")
	}
	return proc, nil
}

func gitFail(proc ProcessResult) error {
	msg := strings.TrimSpace(proc.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(proc.Stdout)
	}
	msg = firstLine(msg)
	if msg == "" {
		msg = fmt.Sprintf("git exited %d", proc.ExitCode)
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "not a git repository") {
		return ErrPrecondition("not a git repository")
	}
	return ErrPrecondition(clipRunes(msg, gitMaxNoteRunes))
}

func assertWorkspaceGit(ctx context.Context, tc *Context) (string, error) {
	proc, err := runGit(ctx, tc, []string{"rev-parse", "--show-toplevel"})
	if err != nil {
		return "", err
	}
	if proc.ExitCode != 0 {
		return "", gitFail(proc)
	}
	top := strings.TrimSpace(proc.Stdout)
	if top == "" {
		return "", ErrPrecondition("not a git repository")
	}
	if _, _, err := resolveInWorkspace(tc.WorkDir, top); err != nil {
		return "", ErrPrecondition("git toplevel is outside the workspace")
	}
	return top, nil
}

func gitStatus(ctx context.Context, tc *Context, relPath string, maxResults int) (Result, error) {
	args := []string{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal"}
	if relPath != "" {
		args = append(args, "--", relPath)
	}
	proc, err := runGit(ctx, tc, args)
	if err != nil {
		return Result{}, err
	}
	if proc.ExitCode != 0 {
		return Result{}, gitFail(proc)
	}
	branch, ahead, behind, files := parseGitStatusV2(proc.Stdout)
	payload := gitPayload{
		OK:     true,
		Action: "status",
		Branch: branch,
		Ahead:  ahead,
		Behind: behind,
		Path:   relPath,
		Files:  []gitFileEntry{},
	}
	payload.Total = len(files)
	if len(files) > maxResults {
		payload.Files = files[:maxResults]
		payload.Truncated = true
	} else {
		payload.Files = files
	}
	if proc.Truncated {
		payload.Truncated = true
	}
	payload.Count = len(payload.Files)
	return gitResult(payload)
}

func gitDiff(ctx context.Context, tc *Context, relPath, ref string, staged bool, maxResults int) (Result, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff", "--unified=3"}
	if staged {
		args = append(args, "--cached")
	}
	if ref != "" {
		args = append(args, ref)
	}
	if relPath != "" {
		args = append(args, "--", relPath)
	}
	proc, err := runGit(ctx, tc, args)
	if err != nil {
		return Result{}, err
	}
	if proc.ExitCode != 0 {
		return Result{}, gitFail(proc)
	}
	files, total, trunc := parseUnifiedDiff(proc.Stdout, maxResults, gitMaxHunkLines)
	payload := gitPayload{
		OK:        true,
		Action:    "diff",
		Path:      relPath,
		Ref:       ref,
		Staged:    staged,
		Files:     files,
		Count:     len(files),
		Total:     total,
		Truncated: trunc || proc.Truncated,
	}
	return gitResult(payload)
}

func gitLog(ctx context.Context, tc *Context, relPath, ref string, maxResults int) (Result, error) {
	args := []string{
		"log",
		"--format=%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%s",
		"-n", strconv.Itoa(maxResults + 1),
	}
	if ref != "" {
		args = append(args, ref)
	}
	if relPath != "" {
		args = append(args, "--", relPath)
	}
	proc, err := runGit(ctx, tc, args)
	if err != nil {
		return Result{}, err
	}
	if proc.ExitCode != 0 {
		return Result{}, gitFail(proc)
	}
	commits := parseGitLog(proc.Stdout)
	payload := gitPayload{
		OK:      true,
		Action:  "log",
		Path:    relPath,
		Ref:     ref,
		Commits: []gitCommit{},
	}
	payload.Total = len(commits)
	if len(commits) > maxResults {
		payload.Commits = commits[:maxResults]
		payload.Truncated = true
		payload.Total = maxResults + 1
	} else {
		payload.Commits = commits
	}
	if proc.Truncated {
		payload.Truncated = true
	}
	payload.Count = len(payload.Commits)
	return gitResult(payload)
}

func gitBlame(ctx context.Context, tc *Context, relPath, ref string, maxResults int) (Result, error) {
	args := []string{"blame", "--line-porcelain"}
	if ref != "" {
		args = append(args, ref)
	}
	args = append(args, "--", relPath)
	proc, err := runGit(ctx, tc, args)
	if err != nil {
		return Result{}, err
	}
	if proc.ExitCode != 0 {
		return Result{}, gitFail(proc)
	}
	lines := parseGitBlame(proc.Stdout)
	payload := gitPayload{
		OK:     true,
		Action: "blame",
		Path:   relPath,
		Ref:    ref,
		Lines:  []gitBlameLine{},
	}
	payload.Total = len(lines)
	if len(lines) > maxResults {
		payload.Lines = lines[:maxResults]
		payload.Truncated = true
	} else {
		payload.Lines = lines
	}
	if proc.Truncated {
		payload.Truncated = true
	}
	payload.Count = len(payload.Lines)
	return gitResult(payload)
}

func gitShow(ctx context.Context, tc *Context, relPath, ref string, maxResults int) (Result, error) {
	metaArgs := []string{"show", "-s", "--format=%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%s%x1e%n%b", ref}
	metaProc, err := runGit(ctx, tc, metaArgs)
	if err != nil {
		return Result{}, err
	}
	if metaProc.ExitCode != 0 {
		return Result{}, gitFail(metaProc)
	}
	commit, ok := parseGitShowMeta(metaProc.Stdout)
	if !ok {
		return Result{}, ErrPrecondition("could not parse git show")
	}

	patchArgs := []string{"show", "--format=", "--no-color", "--no-ext-diff", ref}
	if relPath != "" {
		patchArgs = append(patchArgs, "--", relPath)
	}
	patchProc, err := runGit(ctx, tc, patchArgs)
	if err != nil {
		return Result{}, err
	}
	if patchProc.ExitCode != 0 {
		return Result{}, gitFail(patchProc)
	}
	files, total, trunc := parseUnifiedDiff(patchProc.Stdout, maxResults, gitMaxHunkLines)
	payload := gitPayload{
		OK:        true,
		Action:    "show",
		Path:      relPath,
		Ref:       ref,
		Commit:    &commit,
		Files:     files,
		Count:     len(files),
		Total:     total,
		Truncated: trunc || metaProc.Truncated || patchProc.Truncated,
	}
	return gitResult(payload)
}

func parseGitStatusV2(raw string) (branch string, ahead, behind int, files []gitFileEntry) {
	parts := strings.Split(raw, "\x00")
	for i := 0; i < len(parts); i++ {
		rec := parts[i]
		if rec == "" {
			continue
		}
		if strings.HasPrefix(rec, "# ") {
			key, val, ok := strings.Cut(strings.TrimPrefix(rec, "# "), " ")
			if !ok {
				continue
			}
			switch key {
			case "branch.head":
				if val != "(detached)" {
					branch = val
				} else {
					branch = "HEAD"
				}
			case "branch.ab":
				ahead, behind = parseGitAheadBehind(val)
			}
			continue
		}
		switch {
		case strings.HasPrefix(rec, "1 "):
			fields := strings.SplitN(rec, " ", 9)
			if len(fields) < 9 {
				continue
			}
			xy := fields[1]
			files = append(files, gitFileEntry{
				Path:     fields[8],
				Index:    xyLetter(xy, 0),
				Worktree: xyLetter(xy, 1),
				Status:   describeGitXY(xy),
			})
		case strings.HasPrefix(rec, "2 "):
			fields := strings.SplitN(rec, " ", 10)
			if len(fields) < 10 {
				continue
			}
			xy := fields[1]
			path := fields[9]
			oldPath := ""
			if i+1 < len(parts) {
				i++
				oldPath = parts[i]
			}
			files = append(files, gitFileEntry{
				Path:     path,
				OldPath:  oldPath,
				Index:    xyLetter(xy, 0),
				Worktree: xyLetter(xy, 1),
				Status:   describeGitXY(xy),
			})
		case strings.HasPrefix(rec, "u "):
			fields := strings.SplitN(rec, " ", 11)
			if len(fields) < 11 {
				continue
			}
			xy := fields[1]
			files = append(files, gitFileEntry{
				Path:     fields[10],
				Index:    xyLetter(xy, 0),
				Worktree: xyLetter(xy, 1),
				Status:   "unmerged",
			})
		case strings.HasPrefix(rec, "? "):
			files = append(files, gitFileEntry{
				Path:     strings.TrimPrefix(rec, "? "),
				Index:    "?",
				Worktree: "?",
				Status:   "untracked",
			})
		}
	}
	return branch, ahead, behind, files
}

func parseGitAheadBehind(val string) (ahead, behind int) {
	for _, tok := range strings.Fields(val) {
		if len(tok) < 2 {
			continue
		}
		n, err := strconv.Atoi(tok[1:])
		if err != nil {
			continue
		}
		switch tok[0] {
		case '+':
			ahead = n
		case '-':
			behind = n
		}
	}
	return ahead, behind
}

func xyLetter(xy string, i int) string {
	if i < 0 || i >= len(xy) {
		return ""
	}
	return string(xy[i])
}

func describeGitXY(xy string) string {
	if xy == "??" {
		return "untracked"
	}
	if len(xy) < 2 {
		return "unknown"
	}
	if xy[0] == 'R' || xy[1] == 'R' {
		return "renamed"
	}
	if xy[0] == 'C' || xy[1] == 'C' {
		return "copied"
	}
	if xy[0] == 'U' || xy[1] == 'U' || xy == "DD" || xy == "AA" || xy == "AU" || xy == "UA" || xy == "DU" || xy == "UD" {
		return "unmerged"
	}
	if xy[0] == 'D' || xy[1] == 'D' {
		return "deleted"
	}
	if xy[0] == 'A' || xy[1] == 'A' {
		return "added"
	}
	if xy[0] == 'M' || xy[1] == 'M' || xy[0] == 'T' || xy[1] == 'T' {
		return "modified"
	}
	return strings.TrimSpace(xy)
}

func parseUnifiedDiff(text string, maxFiles, maxHunkLines int) (files []gitFileEntry, total int, truncated bool) {
	if strings.TrimSpace(text) == "" {
		return []gitFileEntry{}, 0, false
	}
	var cur *gitFileEntry
	var hunk *gitHunk
	hunkLines := 0
	flushHunk := func() {
		if cur == nil || hunk == nil {
			return
		}
		cur.Hunks = append(cur.Hunks, *hunk)
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur == nil {
			return
		}
		total++
		if len(files) < maxFiles {
			files = append(files, *cur)
		} else {
			truncated = true
		}
		cur = nil
	}
	startFile := func(path, status string) {
		flushFile()
		cur = &gitFileEntry{Path: path, Status: status, Hunks: []gitHunk{}}
	}

	for _, line := range splitLines(text) {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			aPath, bPath := parseDiffGitPaths(line)
			path := bPath
			if path == "/dev/null" || path == "" {
				path = aPath
			}
			startFile(path, "modified")
			if aPath != "" && bPath != "" && aPath != bPath && aPath != "/dev/null" && bPath != "/dev/null" {
				cur.OldPath = aPath
				cur.Status = "renamed"
			}
		case cur != nil && strings.HasPrefix(line, "new file mode "):
			cur.Status = "added"
		case cur != nil && strings.HasPrefix(line, "deleted file mode "):
			cur.Status = "deleted"
		case cur != nil && strings.HasPrefix(line, "rename from "):
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
			cur.Status = "renamed"
		case cur != nil && strings.HasPrefix(line, "rename to "):
			cur.Path = strings.TrimPrefix(line, "rename to ")
			cur.Status = "renamed"
		case cur != nil && strings.HasPrefix(line, "Binary files "):
			cur.Status = "binary"
		case strings.HasPrefix(line, "+++ "):
			if cur == nil {
				startFile(diffPath(line), "modified")
			} else if p := diffPath(line); p != "" && p != "/dev/null" {
				cur.Path = p
			}
		case strings.HasPrefix(line, "--- "):
			if cur != nil {
				if p := diffPath(line); p != "" && p != "/dev/null" && cur.Path == "" {
					cur.Path = p
				}
			}
		case strings.HasPrefix(line, "@@ "):
			if cur == nil {
				startFile("", "modified")
			}
			flushHunk()
			oldS, oldN, newS, newN := parseHunkHeader(line)
			hunk = &gitHunk{
				OldStart: oldS,
				OldLines: oldN,
				NewStart: newS,
				NewLines: newN,
				Header:   line,
				Lines:    []string{},
			}
		case hunk != nil && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "\\")):
			if hunkLines >= maxHunkLines {
				truncated = true
				continue
			}
			hunk.Lines = append(hunk.Lines, line)
			hunkLines++
		}
	}
	flushFile()
	if files == nil {
		files = []gitFileEntry{}
	}
	return files, total, truncated
}

func parseDiffGitPaths(line string) (aPath, bPath string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if !strings.HasPrefix(rest, "a/") {
		fields := strings.Fields(rest)
		if len(fields) >= 2 {
			return strings.TrimPrefix(fields[0], "a/"), strings.TrimPrefix(fields[1], "b/")
		}
		return "", ""
	}
	idx := strings.Index(rest, " b/")
	if idx < 0 {
		return strings.TrimPrefix(rest, "a/"), ""
	}
	return rest[len("a/"):idx], rest[idx+len(" b/"):]
}

func diffPath(line string) string {
	rest := strings.TrimSpace(line)
	if i := strings.IndexAny(rest, "\t"); i >= 0 {
		rest = rest[:i]
	}
	fields := strings.SplitN(rest, " ", 2)
	if len(fields) < 2 {
		return ""
	}
	p := fields[1]
	if p == "/dev/null" {
		return p
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

func parseHunkHeader(line string) (oldStart, oldLines, newStart, newLines int) {
	oldLines, newLines = 1, 1
	body := strings.TrimPrefix(line, "@@ ")
	if i := strings.Index(body, " @@"); i >= 0 {
		body = body[:i]
	}
	fields := strings.Fields(body)
	if len(fields) >= 1 {
		oldStart, oldLines = parseHunkSpan(fields[0])
	}
	if len(fields) >= 2 {
		newStart, newLines = parseHunkSpan(fields[1])
	}
	return oldStart, oldLines, newStart, newLines
}

func parseHunkSpan(tok string) (start, count int) {
	tok = strings.TrimPrefix(tok, "-")
	tok = strings.TrimPrefix(tok, "+")
	startS, countS, ok := strings.Cut(tok, ",")
	start, _ = strconv.Atoi(startS)
	if ok {
		count, _ = strconv.Atoi(countS)
	} else {
		count = 1
	}
	return start, count
}

func parseGitLog(raw string) []gitCommit {
	var out []gitCommit
	for _, line := range splitLines(raw) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		c, ok := parseGitCommitFields(line)
		if ok {
			out = append(out, c)
		}
	}
	return out
}

func parseGitShowMeta(raw string) (gitCommit, bool) {
	meta, body, _ := strings.Cut(raw, "\x1e")
	c, ok := parseGitCommitFields(strings.TrimSpace(meta))
	if !ok {
		return gitCommit{}, false
	}
	c.Body = strings.TrimSpace(body)
	return c, true
}

func parseGitCommitFields(line string) (gitCommit, bool) {
	fields := strings.Split(line, "\x1f")
	if len(fields) < 6 {
		return gitCommit{}, false
	}
	return gitCommit{
		Hash:    fields[0],
		Short:   fields[1],
		Author:  fields[2],
		Email:   fields[3],
		Date:    fields[4],
		Subject: fields[5],
	}, true
}

func parseGitBlame(raw string) []gitBlameLine {
	var out []gitBlameLine
	var cur gitBlameLine
	haveHeader := false
	for _, line := range splitLines(raw) {
		if !haveHeader {
			fields := strings.Fields(line)
			if len(fields) >= 3 && len(fields[0]) >= 4 {
				n, err := strconv.Atoi(fields[2])
				if err == nil {
					cur = gitBlameLine{Hash: fields[0], Line: n}
					haveHeader = true
				}
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "author "):
			cur.Author = strings.TrimPrefix(line, "author ")
		case strings.HasPrefix(line, "author-time "):
			sec, err := strconv.ParseInt(strings.TrimPrefix(line, "author-time "), 10, 64)
			if err == nil {
				cur.Date = time.Unix(sec, 0).UTC().Format(time.RFC3339)
			}
		case strings.HasPrefix(line, "\t"):
			cur.Content = strings.TrimPrefix(line, "\t")
			out = append(out, cur)
			cur = gitBlameLine{}
			haveHeader = false
		}
	}
	return out
}

func gitResult(payload gitPayload) (Result, error) {
	if payload.Files != nil {
		for i := range payload.Files {
			if payload.Files[i].Hunks == nil && payload.Action != "status" {
				payload.Files[i].Hunks = []gitHunk{}
			}
		}
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encode git: %w", err)
	}
	meta, _ := json.Marshal(payload)
	return Result{
		Title:    gitTitle(payload),
		Output:   string(raw) + "\n",
		Metadata: meta,
	}, nil
}

func gitTitle(p gitPayload) string {
	noun := "item"
	n := p.Count
	switch p.Action {
	case "status", "diff", "show":
		noun = "file"
		if n != 1 {
			noun = "files"
		}
	case "log":
		noun = "commit"
		if n != 1 {
			noun = "commits"
		}
	case "blame":
		noun = "line"
		if n != 1 {
			noun = "lines"
		}
	}
	if p.Action == "show" && p.Commit != nil && p.Count == 0 {
		return "git show " + p.Commit.Short
	}
	title := fmt.Sprintf("git %s (%d %s)", p.Action, n, noun)
	if p.Truncated {
		title += " truncated"
	}
	return title
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func firstLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func clipRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}
