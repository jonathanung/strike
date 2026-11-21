package server

// PR watch (#1356): surface a session's linked pull request and its CI
// checks in the cockpit, and draft a user-visible "fix these checks" message
// the user may confirm into the originating session.
//
// Trust posture:
//   - Every GitHub call goes through the host's own `gh` CLI and its stored
//     credentials. The browser never sends, receives, or stores a token.
//   - gh stderr is never returned to the browser or logged: it can carry auth
//     hints. Failures are classified into fixed, actionable messages.
//   - Log excerpts in a fix draft pass through redact.ScrubToolOutput.
//   - The fix-draft route has no side effects. Posting the draft into the
//     session happens only through the normal `user.input` op after the user
//     confirms it in the cockpit (same admission, read-only and audit path as
//     any typed prompt).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/redact"
)

const (
	notePRAttention = "pr.attention"

	prWatchCacheTTL     = 30 * time.Second
	prWatchPollSec      = 60
	prWatchErrorPollSec = 300
	prWatchGHTimeout    = 15 * time.Second
	prFixMaxChecks      = 5
	prFixTailLines      = 100
	prFixMaxBytesCheck  = 12 * 1024
	prWatchRefreshFloor = 5 * time.Second
	prGHStdoutMax       = 2 << 20
)

// Check states the cockpit renders.
const (
	prCheckQueued  = "queued"
	prCheckRunning = "running"
	prCheckPass    = "pass"
	prCheckFail    = "fail"
	prCheckSkipped = "skipped"
)

var (
	errGHMissing         = errors.New("gh missing")
	errGHUnauthenticated = errors.New("gh unauthenticated")
	errPRNotFound        = errors.New("pr not found")
	errGHFailed          = errors.New("gh failed")
)

// ghRunner runs `gh args...` in dir ("" = daemon cwd) and returns stdout.
// Errors must be one of the errGH*/errPRNotFound sentinels (wrapped is
// fine); stderr never escapes.
type ghRunner func(ctx context.Context, dir string, args ...string) ([]byte, error)

type prWatchCacheEntry struct {
	status prWatchStatus
	at     time.Time
}

type prWatchState struct {
	mu    sync.Mutex
	cache map[string]prWatchCacheEntry
	run   ghRunner         // nil → defaultGHRunner
	now   func() time.Time // nil → time.Now
}

type prCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

type prWatchStatus struct {
	SessionID string `json:"sessionId"`
	// Linked is false when the session has no PR; the cockpit shows nothing.
	Linked bool `json:"linked"`
	// Available is false when tracking cannot run (no gh, gh signed out,
	// non-GitHub remote, PR not found). Reason is then an actionable line.
	Available      bool      `json:"available"`
	Reason         string    `json:"reason,omitempty"`
	URL            string    `json:"url,omitempty"`
	Number         int       `json:"number,omitempty"`
	State          string    `json:"state,omitempty"`
	ReviewDecision string    `json:"reviewDecision,omitempty"`
	Checks         []prCheck `json:"checks"`
	Rollup         string    `json:"rollup"`
	// Attention: the PR is open and a check failed or a review requested
	// changes. Only then may the cockpit offer "Fix in this session".
	Attention bool `json:"attention"`
	// Poll tells the client whether to ask again, and PollAfterSec when.
	// False on terminal PR states and on errors retrying cannot fix.
	Poll         bool      `json:"poll"`
	PollAfterSec int       `json:"pollAfterSec,omitempty"`
	CheckedAt    time.Time `json:"checkedAt"`
}

type prFixDraft struct {
	Text   string   `json:"text"`
	Checks []string `json:"checks"`
	Review bool     `json:"review"`
}

func (p *prWatchState) runner() ghRunner {
	if p.run != nil {
		return p.run
	}
	return defaultGHRunner
}

func (p *prWatchState) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (s *Server) handleSessionPR(w http.ResponseWriter, r *http.Request) {
	id, ok := cleanSessionID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid session id"})
		return
	}
	st, err := s.prWatchStatusFor(r.Context(), id, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "session metadata unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleSessionPRFixDraft builds the message "Fix in this session" would
// post. It does not post it: the cockpit shows the text in a confirmation
// and, only on confirm, submits it as a normal user.input op.
func (s *Server) handleSessionPRFixDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := cleanSessionID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid session id"})
		return
	}
	var body struct{}
	if r.ContentLength > 0 {
		if err := decodeBody(w, r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
	}
	st, err := s.prWatchStatusFor(r.Context(), id, false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "session metadata unavailable"})
		return
	}
	if !st.Linked || !st.Available {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "PR tracking is unavailable for this session"})
		return
	}
	if !st.Attention {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "nothing to fix: no failed checks or change requests"})
		return
	}
	writeJSON(w, http.StatusOK, s.buildPRFixDraft(r.Context(), st))
}

func (s *Server) prWatchStatusFor(ctx context.Context, id string, refresh bool) (prWatchStatus, error) {
	meta, err := session.ReadMeta(s.opts.SessionDir, id)
	if err != nil {
		return prWatchStatus{}, err
	}
	now := s.prWatch.clock()
	base := prWatchStatus{SessionID: id, Checks: []prCheck{}, Rollup: "none", CheckedAt: now.UTC()}
	if strings.TrimSpace(meta.PRURL) == "" && meta.PRNumber <= 0 {
		return base, nil
	}
	base.Linked = true
	base.URL = meta.PRURL
	base.Number = meta.PRNumber
	base.State = session.NormalizePRState(meta.PRState)
	if base.State == "" {
		base.State = session.PRStateOpen
	}
	if base.URL != "" && !isGitHubPRURL(base.URL) {
		base.Reason = "PR tracking unavailable: this pull request is not on GitHub. Strike tracks GitHub pull requests only."
		return base, nil
	}

	// A bare number resolves against gh's working directory, so it must
	// run in the session's own repo, never the daemon's cwd.
	// A URL needs no working directory (a stale one would only break gh).
	dir := ""
	if base.URL == "" {
		dir = strings.TrimSpace(meta.ProjectKey)
		if dir == "" {
			base.Reason = "PR tracking unavailable: this session recorded a PR number without a link or project directory."
			return base, nil
		}
	}

	key := id + "\n" + base.URL + "\n" + strconv.Itoa(base.Number)
	p := &s.prWatch
	ttl := prWatchCacheTTL
	if refresh {
		ttl = prWatchRefreshFloor
	}
	p.mu.Lock()
	if ent, ok := p.cache[key]; ok && now.Sub(ent.at) < ttl {
		p.mu.Unlock()
		return ent.status, nil
	}
	p.mu.Unlock()

	st := fetchPRWatch(ctx, p.runner(), dir, base)
	if st.Available && st.State != "" && st.State != session.NormalizePRState(meta.PRState) {
		_, _ = session.UpdateMeta(s.opts.SessionDir, id, func(m *session.Meta) {
			m.PRState = st.State
			if st.URL != "" {
				m.PRURL = st.URL
			}
			if st.Number > 0 {
				m.PRNumber = st.Number
			}
			m.PRUpdatedAt = now.UTC().Format(time.RFC3339Nano)
		})
	}
	if st.Attention {
		_ = s.notifications().add(prAttentionNote(st, now))
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]prWatchCacheEntry{}
	}
	p.cache[key] = prWatchCacheEntry{status: st, at: now}
	p.mu.Unlock()
	return st, nil
}

type ghRollupItem struct {
	Typename     string `json:"__typename"`
	Name         string `json:"name"`
	WorkflowName string `json:"workflowName"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	DetailsURL   string `json:"detailsUrl"`
	Context      string `json:"context"`
	State        string `json:"state"`
	TargetURL    string `json:"targetUrl"`
}

type ghPRWatchJSON struct {
	Number            int            `json:"number"`
	URL               string         `json:"url"`
	State             string         `json:"state"`
	ReviewDecision    string         `json:"reviewDecision"`
	StatusCheckRollup []ghRollupItem `json:"statusCheckRollup"`
}

func fetchPRWatch(ctx context.Context, run ghRunner, dir string, base prWatchStatus) prWatchStatus {
	ctx, cancel := context.WithTimeout(ctx, prWatchGHTimeout)
	defer cancel()
	ident := base.URL
	if ident == "" {
		ident = strconv.Itoa(base.Number)
	}
	out, err := run(ctx, dir, "pr", "view", ident, "--json", "number,url,state,reviewDecision,statusCheckRollup")
	if err != nil {
		base.Reason, base.Poll = prWatchErrorReason(err)
		if base.Poll {
			base.PollAfterSec = prWatchErrorPollSec
		}
		return base
	}
	var parsed ghPRWatchJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		base.Reason = "PR tracking unavailable: gh returned an unexpected response. Retrying later."
		base.Poll = true
		base.PollAfterSec = prWatchErrorPollSec
		return base
	}
	base.Available = true
	if parsed.URL != "" {
		base.URL = parsed.URL
	}
	if parsed.Number > 0 {
		base.Number = parsed.Number
	}
	if st := session.NormalizePRState(parsed.State); st != "" {
		base.State = st
	}
	base.ReviewDecision = strings.ToLower(strings.TrimSpace(parsed.ReviewDecision))
	base.Checks = mapPRChecks(parsed.StatusCheckRollup)
	base.Rollup = rollupPRChecks(base.Checks)
	open := base.State == session.PRStateOpen
	base.Attention = open && (base.Rollup == prCheckFail || base.ReviewDecision == "changes_requested")
	base.Poll = open
	if open {
		base.PollAfterSec = prWatchPollSec
	}
	return base
}

func prWatchErrorReason(err error) (reason string, poll bool) {
	switch {
	case errors.Is(err, errGHMissing):
		return "PR tracking unavailable: the GitHub CLI (gh) is not installed on the host. Install gh, then run `gh auth login`.", false
	case errors.Is(err, errGHUnauthenticated):
		return "PR tracking unavailable: gh is not signed in on the host. Run `gh auth login` in a terminal on the host.", false
	case errors.Is(err, errPRNotFound):
		return "PR tracking unavailable: GitHub has no pull request at this link, or this repo has no GitHub remote.", false
	default:
		return "PR tracking unavailable: gh pr view failed. Retrying later.", true
	}
}

// mapPRChecks maps gh statusCheckRollup items (CheckRun and StatusContext)
// onto queued/running/pass/fail/skipped, sorted by name for a stable chip.
func mapPRChecks(items []ghRollupItem) []prCheck {
	out := make([]prCheck, 0, len(items))
	for _, it := range items {
		var c prCheck
		if it.Typename == "StatusContext" || (it.Context != "" && it.Name == "") {
			c = prCheck{Name: it.Context, State: mapStatusContextState(it.State), URL: it.TargetURL}
		} else {
			name := it.Name
			if it.WorkflowName != "" && it.Name != "" && it.WorkflowName != it.Name {
				name = it.WorkflowName + " / " + it.Name
			}
			c = prCheck{Name: name, State: mapCheckRunState(it.Status, it.Conclusion), URL: it.DetailsURL}
		}
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			c.Name = "check"
		}
		if !isGitHubHTTPS(c.URL) {
			c.URL = ""
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func mapCheckRunState(status, conclusion string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "COMPLETED":
		switch strings.ToUpper(strings.TrimSpace(conclusion)) {
		case "SUCCESS":
			return prCheckPass
		case "NEUTRAL", "SKIPPED":
			return prCheckSkipped
		default: // FAILURE, TIMED_OUT, CANCELLED, ACTION_REQUIRED, STARTUP_FAILURE, STALE
			return prCheckFail
		}
	case "IN_PROGRESS":
		return prCheckRunning
	default: // QUEUED, WAITING, PENDING, REQUESTED, ""
		return prCheckQueued
	}
}

func mapStatusContextState(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "SUCCESS":
		return prCheckPass
	case "FAILURE", "ERROR":
		return prCheckFail
	case "PENDING":
		return prCheckRunning
	default: // EXPECTED, ""
		return prCheckQueued
	}
}

// rollupPRChecks: fail beats running beats queued beats pass; all-skipped
// counts as pass; no checks is "none".
func rollupPRChecks(checks []prCheck) string {
	if len(checks) == 0 {
		return "none"
	}
	has := map[string]bool{}
	for _, c := range checks {
		has[c.State] = true
	}
	switch {
	case has[prCheckFail]:
		return prCheckFail
	case has[prCheckRunning]:
		return prCheckRunning
	case has[prCheckQueued]:
		return prCheckQueued
	default:
		return prCheckPass
	}
}

func isGitHubHTTPS(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" {
		return false
	}
	return strings.EqualFold(u.Hostname(), "github.com")
}

var githubPRPath = regexp.MustCompile(`^/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]+/?$`)

func isGitHubPRURL(raw string) bool {
	if !isGitHubHTTPS(raw) {
		return false
	}
	u, _ := url.Parse(strings.TrimSpace(raw))
	return githubPRPath.MatchString(u.Path)
}

var githubJobPath = regexp.MustCompile(`^/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/actions/runs/[0-9]+/job/([0-9]+)`)

// actionsJobRef extracts owner/repo and the job id from an Actions check
// details URL. External (non-Actions) checks have no fetchable log.
func actionsJobRef(detailsURL string) (repo, job string, ok bool) {
	if !isGitHubHTTPS(detailsURL) {
		return "", "", false
	}
	u, _ := url.Parse(detailsURL)
	m := githubJobPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", false
	}
	return m[1] + "/" + m[2], m[3], true
}

func (s *Server) buildPRFixDraft(ctx context.Context, st prWatchStatus) prFixDraft {
	run := s.prWatch.runner()
	var b strings.Builder
	label := "this pull request"
	if st.Number > 0 {
		label = fmt.Sprintf("PR #%d", st.Number)
	}
	fmt.Fprintf(&b, "%s needs fixes (from PR watch).", label)
	if st.URL != "" {
		fmt.Fprintf(&b, " %s", st.URL)
	}
	b.WriteString("\n")
	draft := prFixDraft{Checks: []string{}}
	failed := make([]prCheck, 0)
	for _, c := range st.Checks {
		if c.State == prCheckFail {
			failed = append(failed, c)
		}
	}
	if len(failed) > 0 {
		b.WriteString("\nFailing checks:\n")
	}
	for i, c := range failed {
		draft.Checks = append(draft.Checks, c.Name)
		fmt.Fprintf(&b, "\n### %s\n", c.Name)
		if i >= prFixMaxChecks {
			b.WriteString("(log not fetched: only the first 5 failing checks include logs)\n")
			continue
		}
		repo, job, ok := actionsJobRef(c.URL)
		if !ok {
			b.WriteString("(no log available: not a GitHub Actions job)\n")
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, prWatchGHTimeout)
		out, err := run(cctx, "", "run", "view", "--repo", repo, "--job", job, "--log-failed")
		cancel()
		if err != nil {
			b.WriteString("(log unavailable: gh run view failed)\n")
			continue
		}
		excerpt, cut := tailLines(string(out), prFixTailLines, prFixMaxBytesCheck)
		excerpt = redact.ScrubToolOutput(excerpt)
		if cut {
			fmt.Fprintf(&b, "(last %d lines of the failed steps; earlier output truncated)\n", prFixTailLines)
		}
		b.WriteString("```\n")
		b.WriteString(strings.ReplaceAll(excerpt, "```", "'''"))
		if !strings.HasSuffix(excerpt, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n")
	}
	if st.ReviewDecision == "changes_requested" {
		draft.Review = true
		b.WriteString("\nA review requested changes on this pull request. Read the review comments (for example `gh pr view")
		if st.Number > 0 {
			fmt.Fprintf(&b, " %d", st.Number)
		}
		b.WriteString(" --comments`) and address them.\n")
	}
	b.WriteString("\nFix these in this session, run the relevant tests, and tell me what you changed. Do not push or merge without asking.")
	draft.Text = b.String()
	return draft
}

// tailLines keeps the last n lines and at most maxBytes (cut on a line
// boundary where possible). cut reports whether anything was dropped.
func tailLines(s string, n, maxBytes int) (string, bool) {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	cut := false
	if len(lines) > n {
		lines = lines[len(lines)-n:]
		cut = true
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
		if i := strings.IndexByte(out, '\n'); i >= 0 && i < len(out)-1 {
			out = out[i+1:]
		}
		cut = true
	}
	return out, cut
}

func prAttentionNote(st prWatchStatus, now time.Time) Notification {
	failing := make([]string, 0)
	for _, c := range st.Checks {
		if c.State == prCheckFail {
			failing = append(failing, c.Name)
		}
	}
	title := "PR checks failing"
	body := strings.Join(failing, ", ")
	if len(failing) == 0 {
		title = "PR changes requested"
		body = "A review requested changes."
	}
	if st.Number > 0 {
		title = fmt.Sprintf("PR #%d: %s", st.Number, strings.TrimPrefix(title, "PR "))
	}
	// One note per distinct attention set: a new failure set notifies again,
	// a repeat poll of the same failures does not.
	extra := st.URL + "\n" + strconv.Itoa(st.Number) + "\n" + st.ReviewDecision + "\n" + strings.Join(failing, "\n")
	return Notification{
		ID:        noteID(notePRAttention, st.SessionID, extra),
		Kind:      notePRAttention,
		SessionID: st.SessionID,
		Title:     title,
		Body:      body,
		Href:      noteHref(st.SessionID, ""),
		At:        now.UTC(),
	}
}

func defaultGHRunner(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(ghBin); err != nil {
		return nil, errGHMissing
	}
	cmd := exec.CommandContext(ctx, ghBin, args...)
	cmd.Dir = dir
	// Never prompt (no TTY) and never page; same child env as the GitHub app.
	cmd.Env = append(ghChildEnv(), "NO_COLOR=1", "GH_PAGER=cat")
	// Logs keep their tail (that is what a fix draft quotes); anything else
	// over the cap is refused rather than parsed truncated.
	stdout := &capBuffer{max: prGHStdoutMax, keepTail: len(args) > 0 && args[0] == "run"}
	stderr := &capBuffer{max: 64 << 10, keepTail: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errGHMissing
		}
		return nil, classifyGHStderr(string(stderr.buf))
	}
	if stdout.over && !stdout.keepTail {
		return nil, errGHFailed
	}
	return stdout.buf, nil
}

// capBuffer is an io.Writer that holds at most max bytes: the newest bytes
// when keepTail, else the first max bytes (over records the overflow).
type capBuffer struct {
	max      int
	keepTail bool
	over     bool
	buf      []byte
}

func (c *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if c.keepTail {
		c.buf = append(c.buf, p...)
		if len(c.buf) > c.max {
			c.over = true
			c.buf = append(c.buf[:0], c.buf[len(c.buf)-c.max:]...)
		}
		return n, nil
	}
	if room := c.max - len(c.buf); room < len(p) {
		c.over = true
		if room > 0 {
			c.buf = append(c.buf, p[:room]...)
		}
		return n, nil
	}
	c.buf = append(c.buf, p...)
	return n, nil
}

// classifyGHStderr maps gh's stderr onto a sentinel. The text itself is
// discarded by the caller.
func classifyGHStderr(stderr string) error {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "gh auth login"),
		strings.Contains(low, "not logged in"),
		strings.Contains(low, "authentication required"),
		strings.Contains(low, "bad credentials"),
		strings.Contains(low, "http 401"):
		return errGHUnauthenticated
	case strings.Contains(low, "could not resolve to a pullrequest"),
		strings.Contains(low, "no pull requests found"),
		strings.Contains(low, "none of the git remotes"),
		strings.Contains(low, "not a git repository"),
		strings.Contains(low, "http 404"):
		return errPRNotFound
	default:
		return errGHFailed
	}
}
