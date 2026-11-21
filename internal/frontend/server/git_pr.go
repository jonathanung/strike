package server

// Ship to PR (#1340): preview what a pull request for the current branch
// would contain, then — only after explicit confirmation — push the branch
// to origin and create the PR with the host's own git/gh credentials.
//
// Hard rules:
//   - No force-push. The push is one explicit refspec refs/heads/B:refs/heads/B
//     and git rejects non-fast-forwards.
//   - Never push or open a PR from a detached HEAD, the default branch, or
//     main/master.
//   - An existing PR for the branch is returned, never duplicated.
//   - Uncommitted changes need an explicit allowDirty; pushing needs an
//     explicit confirmPush.
//   - No token is accepted from or returned to the browser. Remote URLs are
//     read with userinfo stripped and git/gh stderr is redacted.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

const (
	shipPRCommitsMax   = 50
	shipPRTitleMax     = 256
	shipPRBodyMax      = 60000
	shipPRPushTimeout  = 90 * time.Second
	shipPRGHTimeout    = 30 * time.Second
	shipPRBodyCommits  = 20
	shipPRRemote       = "origin"
	shipPRStateOK      = "ok"
	shipPRStateBlocked = "blocked"
)

type shipPRExisting struct {
	URL     string `json:"url"`
	Number  int    `json:"number"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
}

type shipPRPreview struct {
	State         string          `json:"state"`
	Blockers      []string        `json:"blockers"`
	Warnings      []string        `json:"warnings"`
	Branch        string          `json:"branch"`
	Base          string          `json:"base"`
	DefaultBranch string          `json:"defaultBranch"`
	Repo          string          `json:"repo,omitempty"`
	Commits       []gitCommitJSON `json:"commits"`
	CommitsTotal  int             `json:"commitsTotal"`
	Files         int             `json:"files"`
	Added         int             `json:"added"`
	Deleted       int             `json:"deleted"`
	Dirty         int             `json:"dirty"`
	NeedsPush     bool            `json:"needsPush"`
	Existing      *shipPRExisting `json:"existing,omitempty"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
}

type shipPRCreateResult struct {
	URL      string `json:"url"`
	Number   int    `json:"number"`
	State    string `json:"state"`
	Draft    bool   `json:"draft"`
	Existing bool   `json:"existing"`
	Pushed   bool   `json:"pushed"`
}

func (s *Server) handleGitPRPreview(w http.ResponseWriter, r *http.Request) {
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
	ctx, cancel := context.WithTimeout(r.Context(), shipPRGHTimeout)
	defer cancel()
	p, err := s.shipPRPreview(ctx, cwd, base, s.shipPRSessionID(r))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleGitPRCreate(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "this daemon is read-only; pushing and creating pull requests are disabled", Code: protocol.ErrTeamReadOnly})
		return
	}
	if !s.requireMutable(w) {
		return
	}
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	var body struct {
		Base        string `json:"base"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		Draft       *bool  `json:"draft"`
		ConfirmPush bool   `json:"confirmPush"`
		AllowDirty  bool   `json:"allowDirty"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	base, err := optionalBaseName(body.Base)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || strings.ContainsRune(title, '\x00') || len([]rune(title)) > shipPRTitleMax {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "a PR title of 1-256 characters is required"})
		return
	}
	if strings.ContainsRune(body.Body, '\x00') || len(body.Body) > shipPRBodyMax {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "PR body is too long"})
		return
	}
	draft := true // #1340 Q(a): draft until marked ready on GitHub.
	if body.Draft != nil {
		draft = *body.Draft
	}
	sid := s.shipPRSessionID(r)

	ctx, cancel := context.WithTimeout(r.Context(), shipPRPushTimeout+shipPRGHTimeout)
	defer cancel()
	p, err := s.shipPRPreview(ctx, cwd, base, sid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if p.Existing != nil {
		s.linkSessionPR(sid, p.Existing.URL, p.Existing.Number, p.Existing.State)
		writeJSON(w, http.StatusOK, shipPRCreateResult{
			URL: p.Existing.URL, Number: p.Existing.Number, State: p.Existing.State,
			Draft: p.Existing.IsDraft, Existing: true,
		})
		return
	}
	if len(p.Blockers) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": p.Blockers[0], "blockers": p.Blockers})
		return
	}
	if p.Dirty > 0 && !body.AllowDirty {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "the working tree has uncommitted changes; confirm shipping without them", Code: "dirty"})
		return
	}
	if p.NeedsPush && !body.ConfirmPush {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "pushing " + p.Branch + " to origin was not confirmed", Code: "push-unconfirmed"})
		return
	}
	res := shipPRCreateResult{State: session.PRStateOpen, Draft: draft}
	if p.NeedsPush {
		pctx, pcancel := context.WithTimeout(ctx, shipPRPushTimeout)
		ref := "refs/heads/" + p.Branch
		_, stderr, err := gitRun(pctx, cwd, "push", "--porcelain", "--set-upstream", shipPRRemote, ref+":"+ref)
		pcancel()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, opErrorResponse{Error: scrubRemoteText(gitErr("push", err, stderr))})
			return
		}
		res.Pushed = true
	}
	args := []string{"pr", "create", "--repo", p.Repo, "--base", ghBaseName(p.Base), "--head", p.Branch, "--title", title, "--body-file", "-"}
	if draft {
		args = append(args, "--draft")
	}
	gctx, gcancel := context.WithTimeout(ctx, shipPRGHTimeout)
	out, err := runGH(gctx, cwd, body.Body+"\n", args...)
	gcancel()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, opErrorResponse{Error: shipPRGHMessage(err)})
		return
	}
	url, number := parsePRCreateOutput(string(out))
	if url == "" {
		writeJSON(w, http.StatusBadGateway, opErrorResponse{Error: "gh pr create did not print a pull request URL"})
		return
	}
	res.URL, res.Number = url, number
	s.linkSessionPR(sid, url, number, session.PRStateOpen)
	writeJSON(w, http.StatusCreated, res)
}

// shipPRSessionID is the session a created PR is linked to: ?root= when
// given and known, else the active live root.
func (s *Server) shipPRSessionID(r *http.Request) string {
	if root := rootParam(r); root != "" {
		if id, ok := cleanSessionID(root); ok {
			return id
		}
		return ""
	}
	return s.liveSessionID()
}

// linkSessionPR records the PR on the session meta, the same fields the
// host's gh-output capture writes, so the PR chip / PR watch see it.
func (s *Server) linkSessionPR(sid, url string, number int, state string) {
	if sid == "" || strings.TrimSpace(s.opts.SessionDir) == "" || url == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = session.UpdateMeta(s.opts.SessionDir, sid, func(m *session.Meta) {
		m.PRURL = url
		if number > 0 {
			m.PRNumber = number
		}
		if st := session.NormalizePRState(state); st != "" {
			m.PRState = st
		}
		m.PRUpdatedAt = now
	})
}

func (s *Server) shipPRPreview(ctx context.Context, cwd, base, sid string) (shipPRPreview, error) {
	p := shipPRPreview{
		State: shipPRStateOK, Blockers: []string{}, Warnings: []string{},
		Commits: []gitCommitJSON{},
	}
	bc, err := gitBranchChanges(ctx, cwd, base, shipPRCommitsMax)
	if err != nil {
		return p, err
	}
	p.Branch, p.Base = bc.Branch, bc.Base
	p.DefaultBranch = defaultBaseBranch(ctx, cwd)
	p.Commits, p.CommitsTotal = bc.Commits, bc.CommitsTotal
	p.Files = len(bc.Files)
	for _, f := range bc.Files {
		p.Added += f.Added
		p.Deleted += f.Deleted
	}
	switch bc.State {
	case branchStateDetached:
		p.Blockers = append(p.Blockers, "HEAD is detached. Check out a branch before creating a pull request.")
	case branchStateNoCommits:
		p.Blockers = append(p.Blockers, "This repository has no commits yet.")
	case branchStateChooseBase:
		p.Blockers = append(p.Blockers, "No default branch was detected. Choose a base branch.")
	case branchStateOnBase:
		p.Blockers = append(p.Blockers, "You are on the base branch ("+bc.Branch+"). Create a feature branch first; Strike never pushes the default branch.")
	case branchStateNoMergeBase:
		p.Blockers = append(p.Blockers, "This branch shares no history with "+bc.Base+".")
	case branchStateOK:
		if bc.CommitsTotal == 0 {
			p.Blockers = append(p.Blockers, "No commits on "+bc.Branch+" ahead of "+bc.Base+". Commit your changes first.")
		}
	}
	if p.Branch != "" && isProtectedShipBranch(p.Branch, p.DefaultBranch) && bc.State != branchStateOnBase {
		p.Blockers = append(p.Blockers, "Strike never pushes "+p.Branch+" (the default branch). Create a feature branch first.")
	}
	if p.Branch != "" {
		if _, err := safeBranchName(p.Branch); err != nil {
			p.Blockers = append(p.Blockers, "The branch name cannot be pushed safely.")
		}
	}

	if st, err := gitStatus(ctx, cwd); err == nil {
		p.Dirty = len(st.Staged) + len(st.Unstaged) + len(st.Untracked)
	}
	if p.Dirty > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d uncommitted change(s) will not be in the pull request.", p.Dirty))
	}

	repo, rerr := originGitHubRepo(ctx, cwd)
	if rerr != nil {
		p.Blockers = append(p.Blockers, rerr.Error())
	}
	p.Repo = repo

	if p.Branch != "" && bc.State != branchStateDetached {
		remoteRef := "refs/remotes/" + shipPRRemote + "/" + p.Branch
		local := resolveCommit(ctx, cwd, "HEAD")
		p.NeedsPush = local == "" || resolveCommit(ctx, cwd, remoteRef) != local
	}

	// Existing-PR lookup only when the branch could have one and gh can
	// reach a GitHub repo; a gh failure is a blocker with a fixed message.
	if repo != "" && p.Branch != "" && bc.State != branchStateDetached {
		existing, err := findBranchPR(ctx, cwd, repo, p.Branch)
		switch {
		case err != nil:
			p.Blockers = append(p.Blockers, shipPRGHMessage(err))
		case existing != nil:
			p.Existing = existing
		}
	}

	p.Title, p.Body = shipPRDraft(s.shipPRSessionTitle(sid), p)
	if len(p.Blockers) > 0 && p.Existing == nil {
		p.State = shipPRStateBlocked
	}
	return p, nil
}

func isProtectedShipBranch(branch, def string) bool {
	def = strings.TrimPrefix(def, shipPRRemote+"/")
	return branch == "main" || branch == "master" || (def != "" && branch == def)
}

// ghBaseName strips a remote prefix (origin/main → main) for gh --base.
func ghBaseName(base string) string {
	return strings.TrimPrefix(base, shipPRRemote+"/")
}

// originGitHubRepo returns owner/name for origin, which is where Ship to
// PR pushes. It reads the stored URL (no insteadOf, userinfo stripped).
func originGitHubRepo(ctx context.Context, cwd string) (string, error) {
	out, _, err := gitRun(ctx, cwd, "config", "--local", "--get", "remote."+shipPRRemote+".url")
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", errors.New("This repository has no origin remote. Ship to PR pushes to a GitHub origin.")
	}
	slug, ok := parseGitHubRemote(string(out))
	if !ok {
		return "", errors.New("origin is not a GitHub remote. Ship to PR supports GitHub only; push and open the pull request from your forge.")
	}
	return slug, nil
}

type ghBranchPR struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
}

func findBranchPR(ctx context.Context, cwd, repo, branch string) (*shipPRExisting, error) {
	gctx, cancel := context.WithTimeout(ctx, shipPRGHTimeout)
	defer cancel()
	out, err := runGH(gctx, cwd, "", "pr", "list", "--repo", repo, "--head", branch, "--state", "open", "--json", "number,url,state,isDraft", "--limit", "5")
	if err != nil {
		return nil, err
	}
	var rows []ghBranchPR
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, appFail("gh returned unexpected JSON")
	}
	for _, row := range rows {
		if row.Number > 0 && isGitHubPullURL(row.URL) {
			return &shipPRExisting{URL: row.URL, Number: row.Number, State: strings.ToLower(row.State), IsDraft: row.IsDraft}, nil
		}
	}
	return nil, nil
}

// shipPRGHMessage maps a runGH failure onto a fixed, actionable line. gh's
// own stderr is never shown (it can carry auth hints).
func shipPRGHMessage(err error) string {
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "not installed"):
		return "GitHub CLI (gh) is not installed on the host. Install gh and run `gh auth login`."
	case strings.Contains(low, "auth login"), strings.Contains(low, "not logged in"),
		strings.Contains(low, "bad credentials"), strings.Contains(low, "http 401"):
		return "gh is not signed in on the host. Run `gh auth login` in a terminal on the host."
	case strings.Contains(low, "already exists"):
		return "A pull request for this branch already exists. Refresh to open it."
	default:
		return "gh could not reach GitHub for this repository. Check `gh auth status` on the host."
	}
}

var (
	githubPullURL  = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/([0-9]+)/?$`)
	remoteUserinfo = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
)

func isGitHubPullURL(raw string) bool {
	return githubPullURL.MatchString(strings.TrimSpace(raw))
}

// parsePRCreateOutput finds the PR URL gh pr create prints (last matching
// line) and its number.
func parsePRCreateOutput(out string) (string, int) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if m := githubPullURL.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			return strings.TrimSuffix(line, "/"), n
		}
	}
	return "", 0
}

// scrubRemoteText removes URL userinfo and credential-shaped strings from
// git output before it reaches the browser.
func scrubRemoteText(s string) string {
	return redact.String(remoteUserinfo.ReplaceAllString(s, "$1"))
}

func (s *Server) shipPRSessionTitle(sid string) string {
	if sid == "" || strings.TrimSpace(s.opts.SessionDir) == "" {
		return ""
	}
	meta, err := session.ReadMeta(s.opts.SessionDir, sid)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Title)
}

// shipPRDraft prefills an editable title/body from the session title and
// the branch's commits. The user edits both before anything is created.
func shipPRDraft(sessionTitle string, p shipPRPreview) (string, string) {
	title := ""
	switch {
	case p.CommitsTotal == 1 && len(p.Commits) == 1:
		title = p.Commits[0].Subject
	case sessionTitle != "":
		title = sessionTitle
	case len(p.Commits) > 0:
		title = p.Commits[len(p.Commits)-1].Subject
	default:
		title = strings.ReplaceAll(p.Branch, "-", " ")
	}
	title = strings.TrimSpace(redact.String(title))
	if r := []rune(title); len(r) > shipPRTitleMax {
		title = string(r[:shipPRTitleMax])
	}
	var b strings.Builder
	b.WriteString("## Summary\n")
	if sessionTitle != "" {
		fmt.Fprintf(&b, "- %s\n", redact.String(sessionTitle))
	} else {
		b.WriteString("- \n")
	}
	if len(p.Commits) > 0 {
		b.WriteString("\n## Changes\n")
		for i, c := range p.Commits {
			if i >= shipPRBodyCommits {
				fmt.Fprintf(&b, "- … and %d more commit(s)\n", p.CommitsTotal-shipPRBodyCommits)
				break
			}
			short := c.Hash
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(&b, "- %s (%s)\n", redact.String(c.Subject), short)
		}
	}
	if p.Files > 0 {
		fmt.Fprintf(&b, "\n%d file(s) changed, +%d −%d\n", p.Files, p.Added, p.Deleted)
	}
	b.WriteString("\n## Test notes\n- \n")
	return title, b.String()
}
