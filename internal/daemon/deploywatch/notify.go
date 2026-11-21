package deploywatch

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
	"github.com/jonathanung/strike/pkg/redact"
)

const notifyTimeout = 30 * time.Second

// Notifier posts deploy-watch reports (#1783): the monitoring plan when the
// watch session starts and the single final verdict. Posting is best effort;
// a failure is recorded on the watch and never changes the verdict.
type Notifier interface {
	// PostPR comments body on repo#pr.
	PostPR(ctx context.Context, repo string, pr int, body string) error
	// PostSession enqueues text on the watch session under its posture.
	PostSession(ctx context.Context, sessionID, mode, title, text string) error
}

// Poster is the production Notifier: GitHub REST for PR comments and the
// shared rootstart launcher (no second spawn path) for the session.
type Poster struct {
	GitHub   *GitHub
	Launcher rootstart.Launcher
}

// PostPR implements Notifier.
func (p Poster) PostPR(ctx context.Context, repo string, pr int, body string) error {
	if p.GitHub == nil {
		return errText("GitHub posting is not configured")
	}
	return p.GitHub.Comment(ctx, repo, pr, body)
}

// PostSession implements Notifier. It continues the watch session; it never
// creates a new one.
func (p Poster) PostSession(ctx context.Context, sessionID, mode, title, text string) error {
	if p.Launcher == nil {
		return errText("session launcher is not configured")
	}
	if strings.TrimSpace(sessionID) == "" {
		return errText("watch has no session")
	}
	_, err := p.Launcher.StartRoot(ctx, rootstart.Request{
		Source:         rootstart.SourceDeployWatch,
		Prompt:         text,
		PermissionMode: mode,
		ContinueID:     sessionID,
		Title:          title,
	})
	return err
}

// Started links the watch session that the trigger spawned and posts the
// monitoring plan to the PR. If the watch already settled (the session took
// longer to start than the first polls), the verdict is posted too.
func (s *Service) Started(ctx context.Context, id, sessionID, mode string) (Watch, error) {
	s.pollMu.Lock()
	w, err := s.store.Get(id)
	if err != nil {
		s.pollMu.Unlock()
		return Watch{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 128 || strings.ContainsAny(sessionID, "/\\ \t\n") {
		s.pollMu.Unlock()
		return Watch{}, invalid("sessionId is malformed")
	}
	w.SessionID = sessionID
	w.SessionMode = strings.TrimSpace(mode)
	err = s.store.Put(w)
	s.pollMu.Unlock()
	if err != nil {
		return Watch{}, err
	}
	var errs []error
	if w.PR > 0 {
		errs = append(errs, s.postPR(ctx, w, PlanText(w)))
	}
	if !w.Active() {
		errs = append(errs, s.postVerdict(ctx, w))
	}
	return s.recordNotify(id, errors.Join(errs...))
}

// settled posts the verdict of a watch that just finished.
func (s *Service) settled(ctx context.Context, w Watch) Watch {
	if s.note == nil {
		return w
	}
	got, err := s.recordNotify(w.ID, s.postVerdict(ctx, w))
	if err != nil {
		return w
	}
	return got
}

func (s *Service) postVerdict(ctx context.Context, w Watch) error {
	text := VerdictText(w)
	var errs []error
	if w.PR > 0 {
		errs = append(errs, s.postPR(ctx, w, text))
	}
	// Only a session linked by Started (which records its posture) gets the
	// verdict. A sessionId supplied on create has no recorded posture, and
	// continuing it with an empty mode would reset it to plan.
	if w.SessionID != "" && w.SessionMode != "" && s.note != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
		defer cancel()
		if err := s.note.PostSession(ctx, w.SessionID, w.SessionMode, "", text); err != nil {
			errs = append(errs, fmt.Errorf("session: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) postPR(ctx context.Context, w Watch, body string) error {
	if s.note == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
	defer cancel()
	if err := s.note.PostPR(ctx, w.Repo, w.PR, body); err != nil {
		return fmt.Errorf("PR comment: %w", err)
	}
	return nil
}

// recordNotify stores the latest posting failure (or clears it) and returns
// the current record.
func (s *Service) recordNotify(id string, err error) (Watch, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	w, gerr := s.store.Get(id)
	if gerr != nil {
		return Watch{}, gerr
	}
	msg := ""
	if err != nil {
		msg = clip(redact.String(err.Error()), maxDetail)
	}
	if w.NotifyError == msg {
		return w, nil
	}
	w.NotifyError = msg
	if perr := s.store.Put(w); perr != nil {
		return Watch{}, perr
	}
	return w, nil
}

// PlanText is the deterministic half of the monitoring plan: what is watched,
// for how long, which signals, and the instrumentation gaps visible from the
// configuration. The watch session adds risks and intended effect.
func PlanText(w Watch) string {
	var b strings.Builder
	b.WriteString("## Deploy watch: monitoring plan\n\n")
	fmt.Fprintf(&b, "Watching `%s` at `%s`%s%s for %s. Checks run every %s; a check counts as a regression only after %d consecutive failures. Strike posts exactly one verdict: healthy, regression, or inconclusive.\n\n",
		w.Repo, shortSHA(w.SHA), prLabel(w.PR), envLabel(w.Environment),
		secs(w.WindowSeconds), secs(w.IntervalSeconds), threshold(w))
	b.WriteString("### Signals checked\n\n")
	for _, c := range w.Checks {
		fmt.Fprintf(&b, "- **%s** (%s): %s\n", mdInline(c.Name), c.Kind, describe(c))
	}
	b.WriteString("\n### Instrumentation gaps\n\n")
	gaps := Gaps(w)
	if len(gaps) == 0 {
		b.WriteString("- None visible from the configuration.\n")
	}
	for _, g := range gaps {
		b.WriteString("- " + g + "\n")
	}
	b.WriteString("\n### Risks and intended effect\n\nThe watch session reviews the merged change and records its risk assessment and intended effect in the session.\n")
	return b.String()
}

// Gaps lists what the configured checks cannot see.
func Gaps(w Watch) []string {
	var ci, runtime bool
	for _, c := range w.Checks {
		switch c.Kind {
		case KindGitHubChecks:
			ci = true
		case KindHTTP, KindShell:
			runtime = true
		}
	}
	var out []string
	if !runtime {
		out = append(out, "No runtime probe (http or shell): only CI on the commit is observed, not the deployed service.")
	}
	if !ci {
		out = append(out, "Post-merge CI on the commit is not observed.")
	}
	if w.PR == 0 {
		out = append(out, "No PR is linked: the plan and verdict post to the session only.")
	}
	out = append(out, "No logs, metrics, or traces are read in this slice; a regression they would show but the checks do not ends as healthy or inconclusive.")
	return out
}

// PlanPrompt is the watch session's first prompt: write the monitoring plan.
func PlanPrompt(w Watch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the deploy watch session %s for %s at commit %s%s%s.\n\n", w.ID, w.Repo, w.SHA, prLabel(w.PR), envLabel(w.Environment))
	b.WriteString("Your first act is a monitoring plan. Read the merged change")
	if w.PR > 0 {
		fmt.Fprintf(&b, " (`gh pr view %d --repo %s`, `gh pr diff %d --repo %s`)", w.PR, w.Repo, w.PR, w.Repo)
	} else {
		fmt.Fprintf(&b, " (`git show %s`)", w.SHA)
	}
	b.WriteString(", then reply with four sections: Risks, Intended effect, Signals, and Instrumentation gaps. Start from the configured signals and gaps below and add any gap you can see in the change.\n\n")
	b.WriteString("Do not edit code, open pull requests, or revert anything. Strike runs the checks itself and posts the single verdict to this session")
	if w.PR > 0 {
		b.WriteString(" and the PR")
	}
	b.WriteString(".\n\n")
	b.WriteString(PlanText(w))
	return b.String()
}

// VerdictText renders the final verdict with its evidence.
func VerdictText(w Watch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Deploy watch verdict: **%s**\n\n", w.Verdict)
	fmt.Fprintf(&b, "`%s` at `%s`%s%s. %s\n\n", w.Repo, shortSHA(w.SHA), prLabel(w.PR), envLabel(w.Environment), mdInline(w.Reason))
	if len(w.Latest) > 0 {
		b.WriteString("| Check | State | Evidence |\n|---|---|---|\n")
		for _, r := range w.Latest {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", mdCell(r.Name), r.State, mdCell(r.Detail))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%d observation rounds; watch `%s`.", len(w.Rounds), w.ID)
	switch w.Verdict {
	case VerdictRegression:
		b.WriteString(" Nothing is reverted automatically: a fix session or a draft revert PR needs your confirmation.")
	case VerdictInconclusive:
		b.WriteString(" Inconclusive means the watch could not confirm health; it is not a pass.")
	}
	b.WriteString("\n")
	return b.String()
}

func describe(c CheckSpec) string {
	switch c.Kind {
	case KindGitHubChecks:
		return "check runs and commit statuses on the merged commit"
	case KindHTTP:
		// Never echo the full URL; its path or query may carry a credential.
		host := "the configured URL"
		if u, err := neturl.Parse(c.URL); err == nil && u.Host != "" {
			host = "`" + u.Host + "`"
		}
		return fmt.Sprintf("GET %s expects status %d", host, c.ExpectStatus)
	case KindShell:
		return "workspace command, exit 0 is healthy (runs under your sandbox preference)"
	default:
		return c.Kind
	}
}

func threshold(w Watch) int {
	if w.FailureThreshold <= 0 {
		return DefaultFailureThreshold
	}
	return w.FailureThreshold
}

func prLabel(pr int) string {
	if pr <= 0 {
		return ""
	}
	return fmt.Sprintf(" (PR #%d)", pr)
}

func envLabel(env string) string {
	if env == "" {
		return ""
	}
	return " in " + mdInline(env)
}

func secs(n int) string {
	return (time.Duration(n) * time.Second).String()
}

func mdInline(s string) string {
	// A zero-width space after @ keeps config text from mentioning users.
	s = strings.NewReplacer("\r", " ", "\n", " ", "`", "'", "@", "@\u200b").Replace(s)
	return redact.String(s)
}

func mdCell(s string) string {
	return strings.ReplaceAll(mdInline(s), "|", "\\|")
}
