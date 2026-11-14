// Package loopspec is the shared /loop grammar: interval parsing, limits, and
// job validation for session-scoped recurring jobs. The TUI's /loop command
// and the daemon's /v1/sessions/{id}/loops routes both use it so the two
// surfaces accept and reject exactly the same input (#1588).
//
// Stdlib-only so internal/frontend/tui may import it.
package loopspec

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// MaxPerSession caps concurrent loops in one session.
	MaxPerSession = 16
	// MinInterval rejects sub-second spam.
	MinInterval = time.Second
	// MaxInterval is 7 days; anything longer belongs in a Schedule.
	MaxInterval = 168 * time.Hour
	// MaxJobBytes bounds one job prompt.
	MaxJobBytes = 16 << 10
)

// ParseInterval accepts Go durations (15m, 2h, 30s, 1h30m) and plain
// positive integers as minutes (15 → 15m), bounded by MinInterval and
// MaxInterval.
func ParseInterval(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty interval")
	}
	// Bare positive integer → minutes.
	if isAllDigits(s) {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid interval %q — want e.g. 15m, 2h, 30s", raw)
		}
		s = strconv.Itoa(n) + "m"
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid interval %q — want Go duration (15m, 2h, 30s, 1h30m)", raw)
	}
	if d < MinInterval {
		return 0, fmt.Errorf("interval %s too short (min %s)", FormatInterval(d), FormatInterval(MinInterval))
	}
	if d > MaxInterval {
		return 0, fmt.Errorf("interval %s too long (max %s)", FormatInterval(d), FormatInterval(MaxInterval))
	}
	return d, nil
}

// ValidateJob trims job and rejects empty, oversized, or control-character
// text (tab and newline are allowed).
func ValidateJob(job string) (string, error) {
	job = strings.TrimSpace(job)
	if job == "" {
		return "", fmt.Errorf("job text required after interval")
	}
	if len(job) > MaxJobBytes {
		return "", fmt.Errorf("job too long (max %d bytes)", MaxJobBytes)
	}
	for _, r := range job {
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n') {
			return "", fmt.Errorf("job contains control characters")
		}
	}
	return job, nil
}

// FormatInterval renders d compactly in whole units when exact (15m, 2h, 30s).
func FormatInterval(d time.Duration) string {
	if d <= 0 {
		return d.String()
	}
	if d%time.Hour == 0 && d >= time.Hour {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	if d%time.Minute == 0 && d >= time.Minute {
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	if d%time.Second == 0 && d >= time.Second {
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	return d.String()
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
