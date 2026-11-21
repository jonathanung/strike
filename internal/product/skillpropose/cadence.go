package skillpropose

import (
	"sort"
	"strings"
	"time"
)

// WeeklyCadence is the slow background scan interval (#1913). It runs only
// for workspaces that opted in, never per session, and never while the
// opt-in is off.
const WeeklyCadence = 7 * 24 * time.Hour

// DueWeekly reports whether the cadence should scan w at now: opted in,
// with a known directory, and either never scanned or last scanned at
// least WeeklyCadence ago.
func (w Workspace) DueWeekly(now time.Time) bool {
	if !w.Enabled || strings.TrimSpace(w.Dir) == "" {
		return false
	}
	return w.LastScan.IsZero() || now.Sub(w.LastScan) >= WeeklyCadence
}

// DueWorkspace is one workspace the cadence should scan.
type DueWorkspace struct {
	Key string
	Dir string
}

// Due lists the workspaces DueWeekly at now, sorted by key.
func (s *Store) Due(now time.Time) ([]DueWorkspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []DueWorkspace
	for key, ws := range st.Workspaces {
		if ws.DueWeekly(now) {
			out = append(out, DueWorkspace{Key: key, Dir: ws.Dir})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// SetDir records the workspace directory for key. It writes only when the
// directory changed, so calling it on every cockpit request is cheap.
func (s *Store) SetDir(key, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	if ws, err := s.Get(key); err == nil && ws.Dir == dir {
		return nil
	}
	_, err := s.update(key, func(ws *Workspace) error {
		ws.Dir = dir
		return nil
	})
	return err
}
