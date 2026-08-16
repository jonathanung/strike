package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/pkg/timeline"
)

// Trace retention coordinates observability sidecars with session retention
// (#810 + #803):
//   - ~/.strike/traces/<sessionID>/… — spilled timeline payload blobs
//   - ~/.strike/runs/<sessionID>/…   — multi-agent run snapshots / recordings
//
// Session JSONL retention (ApplyRetention) remains authoritative for transcripts.
// When a closed session is deleted, RemoveTraceSidecars drops matching trace and
// run trees. ApplyTraceRetention enforces count/age/size caps on those trees
// independently (same axes as session.retention*).

// DefaultTracesDir is ~/.strike/traces (delegates to pkg/timeline).
func DefaultTracesDir() string { return timeline.DefaultTracesDir() }

// DefaultRunsDir is ~/.strike/runs — same layout as internal/eval/replay.DefaultRunsDir
// without importing replay (session must stay free of engine/replay deps).
func DefaultRunsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "runs")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "runs")
}

// TraceRetentionPolicy is an alias of timeline.RetentionPolicy for session API
// symmetry with RetentionPolicy (sessions).
type TraceRetentionPolicy = timeline.RetentionPolicy

// TraceRetentionResult summarizes observability sidecar cleanup.
type TraceRetentionResult struct {
	Traces timeline.RetentionResult
	Runs   timeline.RetentionResult
}

// TraceRetentionFromConfig builds a sidecar policy from config integers.
func TraceRetentionFromConfig(maxFiles, maxAgeDays int, maxBytes int64) TraceRetentionPolicy {
	return timeline.RetentionFromConfig(maxFiles, maxAgeDays, maxBytes)
}

// ApplyTraceRetention enforces retention on tracesDir and runsDir top-level
// session trees. Empty paths use DefaultTracesDir / DefaultRunsDir.
// Does not touch session JSONL (use ApplyRetention).
func ApplyTraceRetention(tracesDir, runsDir string, p TraceRetentionPolicy) (TraceRetentionResult, error) {
	var out TraceRetentionResult
	if !p.Active() {
		return out, nil
	}
	if strings.TrimSpace(tracesDir) == "" {
		tracesDir = DefaultTracesDir()
	}
	if strings.TrimSpace(runsDir) == "" {
		runsDir = DefaultRunsDir()
	}
	var err error
	out.Traces, err = timeline.ApplyRetention(tracesDir, p)
	if err != nil {
		return out, fmt.Errorf("trace retention (traces): %w", err)
	}
	out.Runs, err = timeline.ApplyRetention(runsDir, p)
	if err != nil {
		return out, fmt.Errorf("trace retention (runs): %w", err)
	}
	return out, nil
}

// RemoveTraceSidecars deletes traces and runs trees for sessionID (best-effort).
// Safe when dirs are missing. Used after session Delete / ApplyRetention.
func RemoveTraceSidecars(tracesDir, runsDir, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if strings.TrimSpace(tracesDir) == "" {
		tracesDir = DefaultTracesDir()
	}
	if strings.TrimSpace(runsDir) == "" {
		runsDir = DefaultRunsDir()
	}
	// Match pkg/timeline and replay path sanitization: only safe path segments.
	safe := sanitizeSessionPathPart(sessionID)
	if safe == "" {
		return nil
	}
	for _, root := range []string{tracesDir, runsDir} {
		path := filepath.Join(root, safe)
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove sidecars %q: %w", path, err)
		}
	}
	return nil
}

// ApplyRetentionWithSidecars runs ApplyRetention then removes trace/run sidecars
// for each deleted session id. tracesDir/runsDir empty → defaults.
func (m *Manager) ApplyRetentionWithSidecars(p RetentionPolicy, tracesDir, runsDir string) (RetentionResult, error) {
	res, err := m.ApplyRetention(p)
	if err != nil {
		return res, err
	}
	for _, id := range res.Deleted {
		if err := RemoveTraceSidecars(tracesDir, runsDir, id); err != nil {
			return res, err
		}
	}
	return res, nil
}

func sanitizeSessionPathPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return ""
	}
	return out
}
