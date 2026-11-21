package deploywatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
)

// Regression handoffs (#1784). Both are user-confirmed actions on a watch
// that settled regression; neither runs automatically, and each runs at most
// once per watch (a repeat returns the recorded result).

var (
	// ErrNotRegression refuses a handoff on a watch without a regression
	// verdict.
	ErrNotRegression = errors.New("handoffs need a regression verdict")
	// ErrUnavailable means the server is not wired for this handoff.
	ErrUnavailable = errors.New("handoff unavailable on this server")
)

const revertTimeout = 3 * time.Minute

// Handoff is a recorded confirmed action.
type Handoff struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"sessionId,omitempty"`
	PR        int       `json:"pr,omitempty"`
	URL       string    `json:"url,omitempty"`
	Branch    string    `json:"branch,omitempty"`
}

// OpenFixSession starts a root seeded with the regression evidence through
// the shared launcher. mode is the session posture; empty is plan.
func (s *Service) OpenFixSession(ctx context.Context, id, mode string) (Watch, error) {
	if s.launch == nil {
		return Watch{}, ErrUnavailable
	}
	s.handoffMu.Lock()
	defer s.handoffMu.Unlock()
	w, err := s.regression(id)
	if err != nil || w.FixSession != nil {
		return w, err
	}
	if _, err := rootstart.ResolveMode(mode); err != nil {
		return Watch{}, invalid("%v", err)
	}
	res, err := s.launch.StartRoot(context.WithoutCancel(ctx), rootstart.Request{
		Source:         rootstart.SourceDeployWatch,
		SourceID:       w.ID,
		Prompt:         FixPrompt(w),
		PermissionMode: strings.TrimSpace(mode),
		Title:          clip("Fix regression · "+w.Repo+" "+shortSHA(w.SHA), 80),
	})
	if err != nil {
		return Watch{}, fmt.Errorf("fix session did not start: %w", err)
	}
	return s.recordHandoff(id, func(w *Watch) {
		w.FixSession = &Handoff{At: s.now().UTC(), SessionID: res.SessionID}
	})
}

// DraftRevertPR reverts the watched commit on a new branch and opens a draft
// PR for it. The PR goes through the repository's normal gates; Strike never
// merges it.
func (s *Service) DraftRevertPR(ctx context.Context, id string) (Watch, error) {
	if s.revert == nil {
		return Watch{}, ErrUnavailable
	}
	s.handoffMu.Lock()
	defer s.handoffMu.Unlock()
	w, err := s.regression(id)
	if err != nil || w.RevertPR != nil {
		return w, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revertTimeout)
	defer cancel()
	h, err := s.revert.Draft(ctx, w)
	if err != nil {
		return Watch{}, fmt.Errorf("revert PR: %w", err)
	}
	h.At = s.now().UTC()
	return s.recordHandoff(id, func(w *Watch) { w.RevertPR = &h })
}

func (s *Service) regression(id string) (Watch, error) {
	w, err := s.store.Get(id)
	if err != nil {
		return Watch{}, err
	}
	if w.Verdict != VerdictRegression {
		return Watch{}, ErrNotRegression
	}
	return w, nil
}

func (s *Service) recordHandoff(id string, set func(*Watch)) (Watch, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	w, err := s.store.Get(id)
	if err != nil {
		return Watch{}, err
	}
	set(&w)
	if err := s.store.Put(w); err != nil {
		return Watch{}, err
	}
	return w, nil
}

// FixPrompt seeds the fix session with the regression evidence.
func FixPrompt(w Watch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A deploy watch found a regression after %s at commit %s%s%s shipped. Investigate and propose a fix.\n\n", w.Repo, w.SHA, prLabel(w.PR), envLabel(w.Environment))
	b.WriteString("Find the cause in the change, write a failing test that reproduces it when you can, then fix it on a branch and open a pull request through the normal review gates. Do not merge, force-push, or revert unrelated work.\n\n")
	b.WriteString(VerdictText(w))
	return b.String()
}

// Reverter drafts revert PRs from a local clone of the watched repo: a
// temporary worktree off the remote default branch, git revert of the
// watched commit, push to a strike/revert-* branch, then a draft PR.
type Reverter struct {
	// RepoDir is a clone of the watched repository (the serve directory).
	RepoDir string
	// Remote is the git remote to fetch from and push to. Empty is origin.
	Remote string
	GitHub *GitHub
}

// Draft implements the revert. It refuses when RepoDir is not a clone of
// w.Repo, and only ever pushes the strike/revert-<sha> branch.
func (r *Reverter) Draft(ctx context.Context, w Watch) (Handoff, error) {
	if r.GitHub == nil {
		return Handoff{}, errText("GitHub is not configured")
	}
	remote := r.Remote
	if remote == "" {
		remote = "origin"
	}
	url, err := r.git(ctx, r.RepoDir, "remote", "get-url", remote)
	if err != nil {
		return Handoff{}, err
	}
	if !remoteIs(url, w.Repo) {
		return Handoff{}, fmt.Errorf("%s is not a clone of %s", r.RepoDir, w.Repo)
	}
	base, err := r.defaultBranch(ctx, remote)
	if err != nil {
		return Handoff{}, err
	}
	// The merged commit must already be on the default branch; reverting
	// anything else is not this handoff.
	if _, err := r.git(ctx, r.RepoDir, "fetch", "--quiet", remote, base); err != nil {
		return Handoff{}, err
	}
	if _, err := r.git(ctx, r.RepoDir, "merge-base", "--is-ancestor", w.SHA, remote+"/"+base); err != nil {
		return Handoff{}, fmt.Errorf("%s is not on %s/%s", shortSHA(w.SHA), remote, base)
	}
	branch := "strike/revert-" + shortSHA(w.SHA)
	// A branch left by an earlier attempt whose PR call failed is reused
	// rather than pushed over (never force).
	if heads, err := r.git(ctx, r.RepoDir, "ls-remote", "--heads", remote, "refs/heads/"+branch); err != nil {
		return Handoff{}, err
	} else if heads == "" {
		if err := r.pushRevert(ctx, w, remote, base, branch); err != nil {
			return Handoff{}, err
		}
	}
	title := fmt.Sprintf("Revert %s (deploy watch regression)", shortSHA(w.SHA))
	if w.PR > 0 {
		title = fmt.Sprintf("Revert #%d (deploy watch regression)", w.PR)
	}
	body := "Draft revert opened from a confirmed deploy-watch handoff. It goes through the normal review and CI gates; Strike does not merge it.\n\n" + VerdictText(w)
	pr, err := r.GitHub.CreatePR(ctx, w.Repo, PRInput{Title: title, Head: branch, Base: base, Body: body, Draft: true})
	if err != nil {
		return Handoff{Branch: branch}, err
	}
	return Handoff{PR: pr.Number, URL: pr.HTMLURL, Branch: branch}, nil
}

// pushRevert reverts w.SHA in a temporary detached worktree off
// remote/base and pushes it to branch.
func (r *Reverter) pushRevert(ctx context.Context, w Watch, remote, base, branch string) error {
	tmp, err := os.MkdirTemp("", "strike-revert-*")
	if err != nil {
		return err
	}
	wt := filepath.Join(tmp, "wt")
	defer func() {
		_, _ = r.git(context.WithoutCancel(ctx), r.RepoDir, "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(tmp)
	}()
	if _, err := r.git(ctx, r.RepoDir, "worktree", "add", "--detach", wt, remote+"/"+base); err != nil {
		return err
	}
	parents, err := r.git(ctx, wt, "rev-list", "--parents", "-n", "1", w.SHA)
	if err != nil {
		return err
	}
	args := []string{"revert", "--no-edit"}
	if len(strings.Fields(parents)) > 2 {
		args = append(args, "-m", "1")
	}
	if email, _ := r.git(ctx, wt, "config", "user.email"); email == "" {
		args = append([]string{"-c", "user.name=Strike", "-c", "user.email=strike@localhost"}, args...)
	}
	args = append(args, w.SHA)
	if _, err := r.git(ctx, wt, args...); err != nil {
		return fmt.Errorf("git revert did not apply cleanly; open a fix session instead: %w", err)
	}
	_, err = r.git(ctx, wt, "push", "--quiet", remote, "HEAD:refs/heads/"+branch)
	return err
}

func (r *Reverter) defaultBranch(ctx context.Context, remote string) (string, error) {
	out, err := r.git(ctx, r.RepoDir, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			if name, _, found := strings.Cut(ref, "\t"); found && name != "" {
				return name, nil
			}
		}
	}
	return "", errText("could not resolve the remote default branch")
}

func (r *Reverter) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", gitVerb(args), clip(lastLine(msg), 300))
	}
	return strings.TrimSpace(out.String()), nil
}

func gitVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

// remoteIs reports whether a git remote URL points at owner/name, for
// https, ssh, scp-style, and local-path remotes.
func remoteIs(url, repo string) bool {
	u := strings.TrimSuffix(strings.TrimSpace(url), "/")
	u = strings.TrimSuffix(u, ".git")
	u = strings.ReplaceAll(u, ":", "/")
	return strings.HasSuffix(strings.ToLower(u), "/"+strings.ToLower(repo))
}

// PRInput creates a pull request.
type PRInput struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
	Draft bool   `json:"draft"`
}

// PRRef is the created pull request.
type PRRef struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// CreatePR opens a pull request on repo.
func (g *GitHub) CreatePR(ctx context.Context, repo string, in PRInput) (PRRef, error) {
	tokenFn := g.Token
	if tokenFn == nil {
		tokenFn = DefaultToken
	}
	token := tokenFn(ctx)
	if token == "" {
		return PRRef{}, errText("no GitHub token (GH_TOKEN, GITHUB_TOKEN, or gh auth)")
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return PRRef{}, err
	}
	var out PRRef
	if err := g.do(ctx, "POST", token, "/repos/"+repo+"/pulls", payload, &out); err != nil {
		return PRRef{}, err
	}
	return out, nil
}
