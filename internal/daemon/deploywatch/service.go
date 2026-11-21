package deploywatch

import (
	"context"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	repoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	shaRE  = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

// DefaultTick is how often Run looks for watches that are due.
const DefaultTick = 10 * time.Second

// Config wires the outbound boundaries. Zero values use production defaults.
type Config struct {
	GitHub *GitHub
	// HTTP is the client for http probes.
	HTTP *http.Client
	// Now overrides the clock. Tests use it.
	Now func() time.Time
}

// Service owns watch creation, polling, and verdicts.
type Service struct {
	store *Store
	gh    *GitHub
	http  *http.Client
	now   func() time.Time
	// pollMu serializes polls so a manual poll and the loop cannot race on
	// the same record.
	pollMu sync.Mutex
}

// New binds a store to the check backends.
func New(store *Store, cfg Config) *Service {
	s := &Service{store: store, gh: cfg.GitHub, http: cfg.HTTP, now: cfg.Now}
	if s.gh == nil {
		s.gh = &GitHub{}
	}
	if s.http == nil {
		s.http = &http.Client{Timeout: probeTimeout}
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// List returns every watch, newest first.
func (s *Service) List() []Watch { return s.store.List() }

// Get returns one watch.
func (s *Service) Get(id string) (Watch, error) { return s.store.Get(id) }

// Create validates in and starts a watch. It does not poll; the loop (or a
// manual Poll) takes the first observation.
func (s *Service) Create(in Input) (Watch, error) {
	w, err := normalize(in)
	if err != nil {
		return Watch{}, err
	}
	now := s.now().UTC()
	w.ID = newID()
	w.Status = StatusWatching
	w.CreatedAt = now
	w.Deadline = now.Add(time.Duration(w.WindowSeconds) * time.Second)
	w.Latest = []CheckResult{}
	w.Rounds = []Round{}
	if err := s.store.Put(w); err != nil {
		return Watch{}, err
	}
	return w, nil
}

// Cancel stops an active watch. A cancelled watch did not finish observing,
// so its verdict is inconclusive. Cancelling a finished watch is a no-op.
func (s *Service) Cancel(id string) (Watch, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	w, err := s.store.Get(id)
	if err != nil {
		return Watch{}, err
	}
	if !w.Active() {
		return w, nil
	}
	w.Status = StatusCancelled
	w.Verdict = VerdictInconclusive
	w.Reason = "cancelled before the watch window closed"
	w.FinishedAt = s.now().UTC()
	if err := s.store.Put(w); err != nil {
		return Watch{}, err
	}
	return w, nil
}

// Poll runs every check once, records the round, and settles the verdict if
// it is final. Polling a finished watch returns it unchanged.
func (s *Service) Poll(ctx context.Context, id string) (Watch, error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	w, err := s.store.Get(id)
	if err != nil {
		return Watch{}, err
	}
	if !w.Active() {
		return w, nil
	}
	round := Round{At: s.now().UTC()}
	for _, spec := range w.Checks {
		state, detail := s.observe(ctx, w, spec)
		round.Results = append(round.Results, CheckResult{
			Name:   spec.Name,
			Kind:   spec.Kind,
			State:  state,
			Detail: clip(detail, maxDetail),
			At:     s.now().UTC(),
		})
	}
	// A poll cut short (daemon shutdown, client gone) observed nothing; do
	// not record its unreachable results as evidence.
	if err := ctx.Err(); err != nil {
		return Watch{}, err
	}
	now := s.now().UTC()
	w.LastPolledAt = now
	w.Latest = round.Results
	w.Rounds = append(w.Rounds, round)
	if len(w.Rounds) > maxRounds {
		w.Rounds = w.Rounds[len(w.Rounds)-maxRounds:]
	}
	if verdict, reason, final := Evaluate(w.Checks, w.Rounds, w.FailureThreshold, now, w.Deadline); final {
		w.Status = StatusDone
		w.Verdict = verdict
		w.Reason = reason
		w.FinishedAt = now
	}
	if err := s.store.Put(w); err != nil {
		return Watch{}, err
	}
	return w, nil
}

func (s *Service) observe(ctx context.Context, w Watch, spec CheckSpec) (string, string) {
	if err := ctx.Err(); err != nil {
		return StateUnreachable, err.Error()
	}
	switch spec.Kind {
	case KindGitHubChecks:
		return s.gh.Check(ctx, w.Repo, w.SHA)
	case KindHTTP:
		return probeHTTP(ctx, s.http, spec.URL, spec.ExpectStatus)
	default:
		return StateUnreachable, "unknown check kind " + spec.Kind
	}
}

// Due lists active watches whose next poll is due at now: never polled, a
// full interval since the last poll, or past the deadline.
func (s *Service) Due(now time.Time) []string {
	var ids []string
	for _, w := range s.store.List() {
		if !w.Active() {
			continue
		}
		next := w.LastPolledAt.Add(time.Duration(w.IntervalSeconds) * time.Second)
		if w.LastPolledAt.IsZero() || !now.Before(next) || !now.Before(w.Deadline) {
			ids = append(ids, w.ID)
		}
	}
	return ids
}

// Tick polls every due watch once.
func (s *Service) Tick(ctx context.Context) {
	for _, id := range s.Due(s.now()) {
		if ctx.Err() != nil {
			return
		}
		_, _ = s.Poll(ctx, id)
	}
}

// Run polls due watches every tick until ctx ends. Watches persist, so a
// daemon restart resumes them; a watch whose deadline passed while the daemon
// was down settles on its next poll.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	if tick <= 0 {
		tick = DefaultTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	s.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

func normalize(in Input) (Watch, error) {
	w := Watch{
		Repo:        strings.TrimSpace(in.Repo),
		PR:          in.PR,
		SHA:         strings.ToLower(strings.TrimSpace(in.SHA)),
		Environment: strings.TrimSpace(in.Environment),
		SessionID:   strings.TrimSpace(in.SessionID),
	}
	if !repoRE.MatchString(w.Repo) || strings.Contains(w.Repo, "..") {
		return Watch{}, invalid("repo must be owner/name")
	}
	if !shaRE.MatchString(w.SHA) {
		return Watch{}, invalid("sha must be a hex commit id")
	}
	if w.PR < 0 {
		return Watch{}, invalid("pr must be positive")
	}
	if len(w.Environment) > maxEnv {
		return Watch{}, invalid("environment is too long")
	}
	if len(w.SessionID) > 128 || strings.ContainsAny(w.SessionID, "/\\ \t\n") {
		return Watch{}, invalid("sessionId is malformed")
	}
	window, err := bounded(in.WindowSeconds, DefaultWindow, MinWindow, MaxWindow, "windowSeconds")
	if err != nil {
		return Watch{}, err
	}
	interval, err := bounded(in.IntervalSeconds, DefaultInterval, MinInterval, MaxInterval, "intervalSeconds")
	if err != nil {
		return Watch{}, err
	}
	if interval > window {
		return Watch{}, invalid("intervalSeconds must not exceed windowSeconds")
	}
	w.WindowSeconds = int(window / time.Second)
	w.IntervalSeconds = int(interval / time.Second)
	switch {
	case in.FailureThreshold == 0:
		w.FailureThreshold = DefaultFailureThreshold
	case in.FailureThreshold < 1 || in.FailureThreshold > MaxFailureThreshold:
		return Watch{}, invalid("failureThreshold must be 1-%d", MaxFailureThreshold)
	default:
		w.FailureThreshold = in.FailureThreshold
	}
	if len(in.Checks) == 0 {
		return Watch{}, invalid("at least one check is required")
	}
	if len(in.Checks) > maxChecks {
		return Watch{}, invalid("at most %d checks", maxChecks)
	}
	seen := map[string]bool{}
	for i, c := range in.Checks {
		c.Kind = strings.TrimSpace(c.Kind)
		c.Name = strings.TrimSpace(c.Name)
		c.URL = strings.TrimSpace(c.URL)
		switch c.Kind {
		case KindGitHubChecks:
			if c.URL != "" || c.ExpectStatus != 0 {
				return Watch{}, invalid("check %d: github_checks takes no url or expectStatus", i)
			}
			if c.Name == "" {
				c.Name = "GitHub checks"
			}
		case KindHTTP:
			if err := validProbeURL(c.URL); err != nil {
				return Watch{}, invalid("check %d: %v", i, err)
			}
			if c.ExpectStatus == 0 {
				c.ExpectStatus = http.StatusOK
			}
			if c.ExpectStatus < 100 || c.ExpectStatus > 599 {
				return Watch{}, invalid("check %d: expectStatus must be 100-599", i)
			}
			if c.Name == "" {
				c.Name = "HTTP probe"
			}
		default:
			return Watch{}, invalid("check %d: kind must be %s or %s", i, KindGitHubChecks, KindHTTP)
		}
		if len(c.Name) > maxName {
			return Watch{}, invalid("check %d: name is too long", i)
		}
		if seen[c.Name] {
			return Watch{}, invalid("check %d: duplicate name %q", i, c.Name)
		}
		seen[c.Name] = true
		w.Checks = append(w.Checks, c)
	}
	return w, nil
}

func bounded(secs int, def, lo, hi time.Duration, field string) (time.Duration, error) {
	if secs == 0 {
		return def, nil
	}
	d := time.Duration(secs) * time.Second
	if secs < 0 || d < lo || d > hi {
		return 0, invalid("%s must be %d-%d", field, int(lo/time.Second), int(hi/time.Second))
	}
	return d, nil
}

func validProbeURL(raw string) error {
	if raw == "" || len(raw) > maxURL {
		return errText("url is required (max 2048 bytes)")
	}
	u, err := neturl.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errText("url must be an absolute http(s) URL")
	}
	return nil
}

type errText string

func (e errText) Error() string { return string(e) }
