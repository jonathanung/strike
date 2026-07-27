package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Session-scoped recurring LLM jobs (/loop). Distinct from /goal (criteria
// harness): this only schedules UserInput on an interval until stop or quit.

const (
	maxScheduledLoops = 16
	maxLoopInterval   = 168 * time.Hour // 7d
)

// minLoopInterval rejects sub-second spam.
const minLoopInterval = time.Second

// scheduledLoop is one in-session recurring job. gen invalidates in-flight ticks.
type scheduledLoop struct {
	id       string
	interval time.Duration
	job      string
	gen      int
	runs     int
}

// loopTickMsg fires when a scheduled loop's interval elapses.
type loopTickMsg struct {
	id  string
	gen int
}

func (m Model) handleLoopCommand(text string, args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	usage := `usage: /loop <interval> <job>  |  /loop list  |  /loop stop [id]
interval: Go duration (15m, 2h, 30s, 1h30m); session-only, canceled on quit`

	if len(args) == 0 {
		return m.loopListOrUsage(usage)
	}

	switch strings.ToLower(args[0]) {
	case "list", "ls":
		return m.loopListOrUsage(usage)
	case "help", "?":
		m.setNotice(usage, false)
		return m, nil
	case "stop", "cancel", "rm", "remove":
		return m.stopLoops(args[1:])
	}

	interval, job, err := parseLoopStart(text, args)
	if err != nil {
		m.setNotice("loop: "+err.Error(), true)
		return m, nil
	}
	if m.providerName == "" {
		m.setNeedsModelNotice("No model selected — use /provider <anthropic|openai|xai|echo> [model]", true)
		return m, nil
	}
	if len(m.loops) >= maxScheduledLoops {
		m.setNotice(fmt.Sprintf("loop: at most %d concurrent loops — /loop stop [id]", maxScheduledLoops), true)
		return m, nil
	}

	m.loopSeq++
	id := "l" + strconv.Itoa(m.loopSeq)
	loop := scheduledLoop{
		id:       id,
		interval: interval,
		job:      job,
		gen:      1,
	}
	m.loops = append(m.loops, loop)
	m.clearNotice()
	m.setNotice(fmt.Sprintf("loop: started %s every %s — %s (/loop stop %s)",
		id, formatLoopInterval(interval), truncateRunes(job, 48), id), false)
	return m, m.armLoopTick(id, loop.gen, interval)
}

func (m Model) loopListOrUsage(usage string) (tea.Model, tea.Cmd) {
	if len(m.loops) == 0 {
		m.setNotice(usage, false)
		return m, nil
	}
	var b strings.Builder
	b.WriteString("loop: ")
	for i, loop := range m.loops {
		if i > 0 {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "%s every %s runs=%d %s",
			loop.id, formatLoopInterval(loop.interval), loop.runs, truncateRunes(loop.job, 32))
	}
	m.setNotice(b.String(), false)
	return m, nil
}

func (m Model) stopLoops(args []string) (tea.Model, tea.Cmd) {
	if len(m.loops) == 0 {
		m.setNotice("loop: no active loops", true)
		return m, nil
	}
	if len(args) == 0 {
		n := len(m.loops)
		m.loops = nil
		m.setNotice(fmt.Sprintf("loop: stopped all (%d)", n), false)
		return m, nil
	}
	id := strings.TrimSpace(args[0])
	for i, loop := range m.loops {
		if loop.id != id {
			continue
		}
		// Bump gen so any in-flight tick is ignored.
		m.loops[i].gen++
		m.loops = append(m.loops[:i], m.loops[i+1:]...)
		if len(m.loops) == 0 {
			m.loops = nil
		}
		m.setNotice(fmt.Sprintf("loop: stopped %s", id), false)
		return m, nil
	}
	m.setNotice(fmt.Sprintf("loop: unknown id %s — /loop list", id), true)
	return m, nil
}

// applyLoopTick submits the job when the matching loop is still live, then re-arms.
func (m Model) applyLoopTick(msg loopTickMsg) (tea.Model, tea.Cmd) {
	idx := -1
	for i, loop := range m.loops {
		if loop.id == msg.id && loop.gen == msg.gen {
			idx = i
			break
		}
	}
	if idx < 0 {
		return m, nil
	}
	loop := m.loops[idx]
	if m.providerName == "" {
		// Keep the schedule; surface once so the user can fix selection.
		m.setNeedsModelNotice("loop "+loop.id+": no model selected — use /provider", true)
		return m, m.armLoopTick(loop.id, loop.gen, loop.interval)
	}

	job := loop.job
	display := fmt.Sprintf("[loop %s] %s", loop.id, job)
	m.loops[idx].runs++
	next, submitCmd := m.submit(protocol.UserInput{Text: job}, display)
	nm := next.(Model)
	// submit may have replaced m; re-find the loop (id+gen) to re-arm.
	arm := nm.armLoopTick(loop.id, loop.gen, loop.interval)
	return nm, tea.Batch(submitCmd, arm)
}

func (m Model) armLoopTick(id string, gen int, d time.Duration) tea.Cmd {
	if d <= 0 {
		return nil
	}
	return tea.Tick(d, func(time.Time) tea.Msg {
		return loopTickMsg{id: id, gen: gen}
	})
}

// parseLoopStart extracts interval + job from /loop args.
// text is the full slash line (for preserving job wording after the interval token).
func parseLoopStart(text string, args []string) (time.Duration, string, error) {
	if len(args) < 2 {
		return 0, "", fmt.Errorf("need <interval> <job> — e.g. /loop 15m check pipeline")
	}
	interval, err := parseLoopInterval(args[0])
	if err != nil {
		return 0, "", err
	}
	// Job is everything after the interval token in the original line.
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "/loop"))
	rest = strings.TrimSpace(rest)
	// Strip the interval token (first field of rest).
	job := rest
	if f := strings.Fields(rest); len(f) > 0 {
		// Prefer exact prefix strip of first field when present at start.
		if strings.HasPrefix(rest, f[0]) {
			job = strings.TrimSpace(rest[len(f[0]):])
		} else {
			job = strings.TrimSpace(strings.Join(f[1:], " "))
		}
	}
	if job == "" {
		return 0, "", fmt.Errorf("job text required after interval")
	}
	// Reject control characters in job (composer is usually clean; still guard).
	for _, r := range job {
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n') {
			return 0, "", fmt.Errorf("job contains control characters")
		}
	}
	return interval, job, nil
}

// parseLoopInterval accepts Go durations (15m, 2h, 30s, 1h30m) and plain
// integers as minutes (15 → 15m) for convenience.
func parseLoopInterval(raw string) (time.Duration, error) {
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
	if d < minLoopInterval {
		return 0, fmt.Errorf("interval %s too short (min %s)", formatLoopInterval(d), formatLoopInterval(minLoopInterval))
	}
	if d > maxLoopInterval {
		return 0, fmt.Errorf("interval %s too long (max %s)", formatLoopInterval(d), formatLoopInterval(maxLoopInterval))
	}
	return d, nil
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

func formatLoopInterval(d time.Duration) string {
	if d <= 0 {
		return d.String()
	}
	// Prefer compact whole units when exact.
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
