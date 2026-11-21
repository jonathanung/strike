package suggestion

import (
	"fmt"
	"strings"
)

// In-process producers (#1926). These sources are reserved: only Deposit,
// called from inside the daemon, may file under them. The HTTP create
// route stays open for tooling under any other source token, so a script
// cannot impersonate the scheduler or a trigger.
const (
	// SourceScheduler files from scheduled runs (#1276).
	SourceScheduler = "scheduler"
	// SourceTrigger files from trigger deliveries (#1282).
	SourceTrigger = "trigger"
	// SourceChildAgent is reserved for finished child agents.
	SourceChildAgent = "child-agent"
	// SourceAutopilot is reserved for goal autopilot (#1284).
	SourceAutopilot = "goal-autopilot"
)

var producers = []string{SourceAutopilot, SourceChildAgent, SourceScheduler, SourceTrigger}

// Producers lists the reserved in-process producer sources, sorted.
func Producers() []string { return append([]string(nil), producers...) }

// IsProducer reports whether source is a reserved in-process producer.
func IsProducer(source string) bool { return contains(producers, source) }

// Deposit files a suggestion from an in-process producer. source must be
// one of Producers; d.Source is ignored. Producers must give a stable
// EvidenceKey: it is what keeps one suggestion per key and keeps a
// dismissed idea from resurfacing. Muted sources and kinds, dismissed keys
// and quiet hours refuse the deposit (ErrMuted, ErrDismissed, ErrQuiet).
func (s *Store) Deposit(source string, d Draft) (Suggestion, bool, error) {
	if s == nil {
		return Suggestion{}, false, fmt.Errorf("%w: no store", ErrInvalid)
	}
	if !IsProducer(source) {
		return Suggestion{}, false, fmt.Errorf("%w: %q is not an in-process producer", ErrInvalid, source)
	}
	if strings.TrimSpace(d.EvidenceKey) == "" {
		return Suggestion{}, false, fmt.Errorf("%w: producers must give an evidence key", ErrInvalid)
	}
	d.Source = source
	return s.create(d)
}
