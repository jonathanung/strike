// Package deploywatch watches a merged change after it ships (#1602, slice
// #1782). A Watch names a repo, a merged commit, and a set of health checks.
// The daemon polls the checks for a bounded window and settles exactly one
// verdict: healthy, regression, or inconclusive.
//
// Inconclusive is a first-class outcome. A check that could not observe its
// target (unreachable, still pending, never reported) never counts as
// healthy.
package deploywatch

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Check kinds.
const (
	// KindGitHubChecks reads GitHub check runs and commit statuses on the
	// watched commit.
	KindGitHubChecks = "github_checks"
	// KindHTTP probes a URL and compares the response status.
	KindHTTP = "http"
)

// Per-check states.
const (
	StateHealthy     = "healthy"
	StateFailing     = "failing"
	StatePending     = "pending"
	StateUnreachable = "unreachable"
)

// Watch lifecycle.
const (
	StatusWatching  = "watching"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

// Verdicts. A watching watch has an empty verdict.
const (
	VerdictHealthy      = "healthy"
	VerdictRegression   = "regression"
	VerdictInconclusive = "inconclusive"
)

const (
	DefaultWindow   = 30 * time.Minute
	MinWindow       = time.Minute
	MaxWindow       = 24 * time.Hour
	DefaultInterval = time.Minute
	MinInterval     = 10 * time.Second
	MaxInterval     = time.Hour
	maxChecks       = 16
	maxRounds       = 60
	maxDetail       = 600
	maxName         = 80
	maxEnv          = 64
	maxURL          = 2048
)

var (
	// ErrNotFound is returned for an unknown watch id.
	ErrNotFound = errors.New("deploy watch not found")
	// ErrInvalid wraps every input validation failure.
	ErrInvalid = errors.New("invalid deploy watch")
)

// CheckSpec is one configured health check.
type CheckSpec struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// URL is the HTTP probe target (kind http).
	URL string `json:"url,omitempty"`
	// ExpectStatus is the healthy HTTP status (kind http). Zero means 200.
	ExpectStatus int `json:"expectStatus,omitempty"`
}

// CheckResult is one observation of one check.
type CheckResult struct {
	Name   string    `json:"name"`
	Kind   string    `json:"kind"`
	State  string    `json:"state"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// Round is every check observed once.
type Round struct {
	At      time.Time     `json:"at"`
	Results []CheckResult `json:"results"`
}

// Watch is one post-merge watch record. Rounds keep the newest maxRounds
// observations as verdict evidence.
type Watch struct {
	ID              string        `json:"id"`
	Repo            string        `json:"repo"`
	PR              int           `json:"pr,omitempty"`
	SHA             string        `json:"sha"`
	Environment     string        `json:"environment,omitempty"`
	SessionID       string        `json:"sessionId,omitempty"`
	Checks          []CheckSpec   `json:"checks"`
	WindowSeconds   int           `json:"windowSeconds"`
	IntervalSeconds int           `json:"intervalSeconds"`
	Status          string        `json:"status"`
	Verdict         string        `json:"verdict,omitempty"`
	Reason          string        `json:"reason,omitempty"`
	Latest          []CheckResult `json:"latest"`
	Rounds          []Round       `json:"rounds"`
	CreatedAt       time.Time     `json:"createdAt"`
	Deadline        time.Time     `json:"deadline"`
	LastPolledAt    time.Time     `json:"lastPolledAt,omitzero"`
	FinishedAt      time.Time     `json:"finishedAt,omitzero"`
}

// Active reports whether the watch still polls.
func (w Watch) Active() bool { return w.Status == StatusWatching }

// Input creates a watch. Zero window/interval take the defaults.
type Input struct {
	Repo            string      `json:"repo"`
	PR              int         `json:"pr,omitempty"`
	SHA             string      `json:"sha"`
	Environment     string      `json:"environment,omitempty"`
	SessionID       string      `json:"sessionId,omitempty"`
	Checks          []CheckSpec `json:"checks"`
	WindowSeconds   int         `json:"windowSeconds,omitempty"`
	IntervalSeconds int         `json:"intervalSeconds,omitempty"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Evaluate rolls the latest per-check results into a verdict.
//
// Any failing check is a regression and settles at once. Before the deadline
// nothing else settles. At or after the deadline the watch is healthy only if
// every configured check was last observed healthy; anything else (pending,
// unreachable, never observed) is inconclusive.
func Evaluate(specs []CheckSpec, latest []CheckResult, now, deadline time.Time) (verdict, reason string, final bool) {
	byName := make(map[string]CheckResult, len(latest))
	for _, r := range latest {
		byName[r.Name] = r
	}
	var failing, unobserved []string
	for _, spec := range specs {
		r, ok := byName[spec.Name]
		switch {
		case !ok:
			unobserved = append(unobserved, spec.Name+" (never observed)")
		case r.State == StateFailing:
			failing = append(failing, spec.Name)
		case r.State != StateHealthy:
			unobserved = append(unobserved, spec.Name+" ("+r.State+")")
		}
	}
	if len(failing) > 0 {
		return VerdictRegression, "failing: " + strings.Join(failing, ", "), true
	}
	if now.Before(deadline) {
		return "", "", false
	}
	if len(specs) == 0 {
		return VerdictInconclusive, "no checks configured", true
	}
	if len(unobserved) > 0 {
		return VerdictInconclusive, "could not confirm: " + strings.Join(unobserved, ", "), true
	}
	return VerdictHealthy, fmt.Sprintf("all %d checks healthy at window close", len(specs)), true
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
