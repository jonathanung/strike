package session

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/pkg/timeline"
)

// ExportTrace builds a redacted structured run timeline from a session JSONL
// log and writes it to outPath. Format is chosen by extension:
//   - .jsonl → JSONL (header + entries)
//   - otherwise → pretty JSON (default)
//
// The timeline is derived from the durable event log (complement, not a second
// transcript). Secrets are scrubbed via pkg/redact before write.
func ExportTrace(sessionLogPath, outPath string, opts timeline.Options) (timeline.Trace, error) {
	sessionLogPath = strings.TrimSpace(sessionLogPath)
	outPath = strings.TrimSpace(outPath)
	if sessionLogPath == "" {
		return timeline.Trace{}, fmt.Errorf("session log path is empty")
	}
	if outPath == "" {
		return timeline.Trace{}, fmt.Errorf("export path is empty")
	}
	timed, err := ReplayTimed(sessionLogPath)
	if err != nil {
		return timeline.Trace{}, err
	}
	events := make([]timeline.TimedEvent, len(timed))
	for i, te := range timed {
		events[i] = timeline.TimedEvent{Time: te.Time, Event: te.Event}
	}
	if opts.SessionID == "" {
		// Best-effort: filename stem is the session id.
		base := filepath.Base(sessionLogPath)
		opts.SessionID = strings.TrimSuffix(base, filepath.Ext(base))
	}
	tr := timeline.Build(events, opts)
	switch strings.ToLower(filepath.Ext(outPath)) {
	case ".jsonl":
		if err := timeline.ExportJSONL(outPath, tr); err != nil {
			return timeline.Trace{}, err
		}
	default:
		if err := timeline.ExportJSON(outPath, tr); err != nil {
			return timeline.Trace{}, err
		}
	}
	return tr, nil
}

// BuildTrace constructs a timeline.Trace from a session JSONL path without writing.
func BuildTrace(sessionLogPath string, opts timeline.Options) (timeline.Trace, error) {
	sessionLogPath = strings.TrimSpace(sessionLogPath)
	if sessionLogPath == "" {
		return timeline.Trace{}, fmt.Errorf("session log path is empty")
	}
	timed, err := ReplayTimed(sessionLogPath)
	if err != nil {
		return timeline.Trace{}, err
	}
	events := make([]timeline.TimedEvent, len(timed))
	for i, te := range timed {
		events[i] = timeline.TimedEvent{Time: te.Time, Event: te.Event}
	}
	if opts.SessionID == "" {
		base := filepath.Base(sessionLogPath)
		opts.SessionID = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return timeline.Build(events, opts), nil
}
