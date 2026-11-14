package deploywatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// DefaultGitHubAPI is the public GitHub REST base URL.
const DefaultGitHubAPI = "https://api.github.com"

const githubBodyMax = 4 << 20

// GitHub reads post-merge CI on a commit: check runs plus legacy commit
// statuses. BaseURL and Token are injectable so tests run against httptest.
type GitHub struct {
	BaseURL string
	Client  *http.Client
	// Token returns the API token. Empty means unauthenticated (public repos,
	// low rate limit). Nil uses DefaultToken.
	Token func(ctx context.Context) string
}

// DefaultToken reads GH_TOKEN, then GITHUB_TOKEN, then `gh auth token`.
// The token stays on the daemon; it is never returned to a client.
func DefaultToken(ctx context.Context) string {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type checkRunsPage struct {
	TotalCount int `json:"total_count"`
	CheckRuns  []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"check_runs"`
}

type combinedStatus struct {
	Statuses []struct {
		Context string `json:"context"`
		State   string `json:"state"`
	} `json:"statuses"`
}

// apiError is a non-2xx GitHub response.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("GitHub API %d", e.Status)
	}
	return fmt.Sprintf("GitHub API %d: %s", e.Status, e.Msg)
}

// Check observes every check run and commit status on repo@sha and rolls
// them into one result: any failure is failing, else any unfinished run is
// pending, else healthy. No runs at all is pending (CI may not have started).
// Transport and API errors are unreachable, never healthy.
func (g *GitHub) Check(ctx context.Context, repo, sha string) (state, detail string) {
	tokenFn := g.Token
	if tokenFn == nil {
		tokenFn = DefaultToken
	}
	token := tokenFn(ctx)
	var runs checkRunsPage
	if err := g.get(ctx, token, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, sha), &runs); err != nil {
		return StateUnreachable, "check runs: " + err.Error()
	}
	var statuses combinedStatus
	if err := g.get(ctx, token, fmt.Sprintf("/repos/%s/commits/%s/status?per_page=100", repo, sha), &statuses); err != nil {
		return StateUnreachable, "commit status: " + err.Error()
	}
	var failing, pending []string
	passed := 0
	for _, r := range runs.CheckRuns {
		switch runState(r.Status, r.Conclusion) {
		case StateFailing:
			failing = append(failing, r.Name+"="+r.Conclusion)
		case StatePending:
			label := r.Status
			if r.Conclusion != "" {
				label = r.Conclusion
			}
			pending = append(pending, r.Name+"="+label)
		default:
			passed++
		}
	}
	for _, st := range statuses.Statuses {
		switch st.State {
		case "success":
			passed++
		case "failure", "error":
			failing = append(failing, st.Context+"="+st.State)
		default:
			pending = append(pending, st.Context+"="+st.State)
		}
	}
	sort.Strings(failing)
	sort.Strings(pending)
	total := len(runs.CheckRuns) + len(statuses.Statuses)
	more := ""
	if runs.TotalCount > len(runs.CheckRuns) {
		more = fmt.Sprintf(" (first %d of %d check runs)", len(runs.CheckRuns), runs.TotalCount)
	}
	switch {
	case len(failing) > 0:
		return StateFailing, clip(fmt.Sprintf("%d of %d failing: %s%s", len(failing), total, strings.Join(failing, ", "), more), maxDetail)
	case total == 0:
		return StatePending, "no checks reported on " + shortSHA(sha) + " yet"
	case len(pending) > 0:
		return StatePending, clip(fmt.Sprintf("%d of %d not finished: %s%s", len(pending), total, strings.Join(pending, ", "), more), maxDetail)
	case runs.TotalCount > len(runs.CheckRuns):
		// Unseen runs could be failing; do not call a partial view healthy.
		return StatePending, clip(fmt.Sprintf("%d passed%s", passed, more), maxDetail)
	default:
		return StateHealthy, fmt.Sprintf("%d of %d passed", passed, total)
	}
}

// runState maps one check run. Cancelled, stale, and action_required did
// not observe the change, so they stay pending (inconclusive at window close)
// rather than counting as pass or regression.
func runState(status, conclusion string) string {
	if status != "completed" {
		return StatePending
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return StateHealthy
	case "failure", "timed_out", "startup_failure":
		return StateFailing
	default:
		return StatePending
	}
}

func (g *GitHub) get(ctx context.Context, token, path string, dst any) error {
	base := strings.TrimRight(g.BaseURL, "/")
	if base == "" {
		base = DefaultGitHubAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, githubBodyMax))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &msg)
		return &apiError{Status: resp.StatusCode, Msg: clip(msg.Message, 200)}
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
