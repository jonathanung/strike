package session

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// RetentionPolicy bounds durable session storage by count, age, and/or total
// bytes. Zero fields are unlimited. Open sessions are never deleted.
//
// Config mapping (session.* in docs/config.md):
//   - retentionMaxSessions → MaxSessions
//   - retentionMaxAgeDays  → MaxAge (days)
//   - retentionMaxBytes    → MaxBytes
type RetentionPolicy struct {
	// MaxSessions caps how many closed sessions to retain (0 = unlimited).
	// Oldest UpdatedAt are removed first until the cap is met.
	MaxSessions int
	// MaxAge deletes closed sessions whose UpdatedAt is older than now-MaxAge
	// (0 = off).
	MaxAge time.Duration
	// MaxBytes caps the sum of closed session .jsonl + .meta.json sizes
	// (0 = off). Oldest are removed first until under the cap.
	MaxBytes int64
}

// RetentionResult summarizes what ApplyRetention deleted.
type RetentionResult struct {
	Deleted []string
	Freed   int64
}

// Active reports whether any retention limit is set.
func (p RetentionPolicy) Active() bool {
	return p.MaxSessions > 0 || p.MaxAge > 0 || p.MaxBytes > 0
}

// RetentionFromConfig builds a policy from config-shaped integers.
// maxAgeDays is converted to a 24h duration; non-positive values disable that axis.
func RetentionFromConfig(maxSessions, maxAgeDays int, maxBytes int64) RetentionPolicy {
	p := RetentionPolicy{MaxSessions: maxSessions, MaxBytes: maxBytes}
	if maxAgeDays > 0 {
		p.MaxAge = time.Duration(maxAgeDays) * 24 * time.Hour
	}
	if p.MaxSessions < 0 {
		p.MaxSessions = 0
	}
	if p.MaxBytes < 0 {
		p.MaxBytes = 0
	}
	return p
}

// ApplyRetention deletes closed sessions that exceed the policy. Open sessions
// are skipped. Deletion order is oldest UpdatedAt first (then lexical id).
// Returns the deleted ids and approximate bytes freed.
func (m *Manager) ApplyRetention(p RetentionPolicy) (RetentionResult, error) {
	var out RetentionResult
	if !p.Active() {
		return out, nil
	}
	list, err := m.List()
	if err != nil {
		return out, err
	}

	type cand struct {
		info Info
		size int64
	}
	var closed []cand
	for _, info := range list {
		if info.Open {
			continue
		}
		m.mu.Lock()
		_, open := m.sessions[info.ID]
		m.mu.Unlock()
		if open {
			continue
		}
		closed = append(closed, cand{info: info, size: sessionBytes(m.dir, info.ID)})
	}
	// Oldest first for eviction.
	sort.SliceStable(closed, func(i, j int) bool {
		if !closed[i].info.UpdatedAt.Equal(closed[j].info.UpdatedAt) {
			return closed[i].info.UpdatedAt.Before(closed[j].info.UpdatedAt)
		}
		return closed[i].info.ID < closed[j].info.ID
	})

	// keep[i] means retain closed[i].
	keep := make([]bool, len(closed))
	for i := range keep {
		keep[i] = true
	}
	now := time.Now().UTC()

	if p.MaxAge > 0 {
		cutoff := now.Add(-p.MaxAge)
		for i, c := range closed {
			if c.info.UpdatedAt.Before(cutoff) {
				keep[i] = false
			}
		}
	}

	if p.MaxSessions > 0 {
		n := 0
		for _, k := range keep {
			if k {
				n++
			}
		}
		// Drop oldest kept until at most MaxSessions remain.
		for i := 0; i < len(closed) && n > p.MaxSessions; i++ {
			if !keep[i] {
				continue
			}
			keep[i] = false
			n--
		}
	}

	if p.MaxBytes > 0 {
		var remain int64
		for i, c := range closed {
			if keep[i] {
				remain += c.size
			}
		}
		for i := 0; i < len(closed) && remain > p.MaxBytes; i++ {
			if !keep[i] {
				continue
			}
			keep[i] = false
			remain -= closed[i].size
		}
	}

	for i, c := range closed {
		if keep[i] {
			continue
		}
		if err := m.Delete(c.info.ID, false); err != nil {
			if strings.Contains(err.Error(), "is open") {
				continue
			}
			return out, fmt.Errorf("retention delete %q: %w", c.info.ID, err)
		}
		out.Deleted = append(out.Deleted, c.info.ID)
		out.Freed += c.size
	}
	return out, nil
}

func sessionBytes(dir, id string) int64 {
	var n int64
	for _, p := range []string{LogPath(dir, id), MetaPath(dir, id)} {
		st, err := os.Stat(p)
		if err == nil {
			n += st.Size()
		}
	}
	return n
}
