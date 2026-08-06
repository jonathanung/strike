package tui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

// timelineFinishedMsg is delivered after async timeline export completes.
type timelineFinishedMsg struct {
	path string
	err  error
}

func (m *Model) resetRunTimeline() {
	if m == nil {
		return
	}
	m.runTimeline = timeline.NewBuilder(timeline.Options{SessionID: m.sessionID})
}

func (m *Model) observeTimeline(ev protocol.Event, t time.Time) {
	if m == nil || ev == nil {
		return
	}
	if m.runTimeline == nil {
		m.resetRunTimeline()
	}
	m.runTimeline.Observe(ev, t)
}

// timelineTrace returns the best available redacted trace.
// Prefer the live builder (includes events not yet flushed to JSONL). Fall back
// to durable session JSONL when the live builder is empty (cold root switch).
func (m Model) timelineTrace() timeline.Trace {
	if m.runTimeline != nil {
		if tr := m.runTimeline.Trace(); len(tr.Entries) > 0 {
			return tr
		}
	}
	if m.services.Sessions != nil && strings.TrimSpace(m.sessionID) != "" {
		if data, err := m.services.Sessions.ReplayJSONL(m.sessionID); err == nil && len(data) > 0 {
			if events, err := timedEventsFromJSONL(data); err == nil && len(events) > 0 {
				return timeline.Build(events, timeline.Options{SessionID: m.sessionID})
			}
		}
	}
	if m.runTimeline != nil {
		return m.runTimeline.Trace()
	}
	return timeline.NewBuilder(timeline.Options{SessionID: m.sessionID}).Trace()
}

func timedEventsFromJSONL(data []byte) ([]timeline.TimedEvent, error) {
	var out []timeline.TimedEvent
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 32<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		// Skip session.header schema marker (#803); not a protocol event.
		if isSessionLogHeaderLine(raw) {
			continue
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		ev, err := env.Decode()
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, timeline.TimedEvent{Time: env.Time.UTC(), Event: ev})
	}
	return out, sc.Err()
}

func (m Model) handleTimelineCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	if len(args) > 0 {
		switch args[0] {
		case "export":
			return m.handleTimelineExport(args[1:])
		case "help", "-h", "--help":
			m.setNotice("usage: /timeline | /timeline export [path]", true)
			return m, nil
		default:
			m.setNotice("usage: /timeline | /timeline export [path]", true)
			return m, nil
		}
	}
	tr := m.timelineTrace()
	if len(tr.Entries) == 0 {
		m.setNotice("timeline is empty — run a turn first", true)
		return m, nil
	}
	m.modal = newTimelineModal(tr)
	m.reflow()
	return m, nil
}

func (m Model) handleTimelineExport(args []string) (tea.Model, tea.Cmd) {
	pathArg := ""
	if len(args) > 1 {
		m.setNotice("usage: /timeline export [path]", true)
		return m, nil
	}
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") {
			m.setNotice("usage: /timeline export [path]", true)
			return m, nil
		}
		pathArg = args[0]
	}
	tr := m.timelineTrace()
	if len(tr.Entries) == 0 {
		m.setNotice("timeline is empty — nothing to export", true)
		return m, nil
	}
	var path string
	if pathArg == "" {
		path = defaultTimelineExportPath(m.workDir, m.sessionID, time.Now())
	} else {
		resolved, err := resolveExportPath(m.workDir, pathArg)
		if err != nil {
			m.setNotice("timeline export: "+err.Error(), true)
			return m, nil
		}
		path = resolved
	}
	m.setNotice("exporting timeline…", false)
	return m, timelineExportCmd(path, tr)
}

func defaultTimelineExportPath(workDir, sessionID string, now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().Format("20060102-150405")
	short := shortSessionID(sessionID)
	if short == "" {
		short = "session"
	}
	name := fmt.Sprintf("strike-timeline-%s-%s.json", short, stamp)
	workDir = strings.TrimSpace(workDir)
	if workDir != "" {
		return filepath.Join(workDir, ".strike", "exports", name)
	}
	return filepath.Join(os.TempDir(), name)
}

func timelineExportCmd(path string, tr timeline.Trace) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jsonl":
			err = timeline.ExportJSONL(path, tr)
		default:
			err = timeline.ExportJSON(path, tr)
		}
		if err != nil {
			return timelineFinishedMsg{err: err}
		}
		return timelineFinishedMsg{path: path}
	}
}

func (m Model) applyTimelineFinished(msg timelineFinishedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setNotice("timeline export failed: "+msg.err.Error(), true)
		return m, nil
	}
	display := displayPath(m.workDir, msg.path)
	if display == "" {
		display = msg.path
	}
	m.setNotice("timeline exported to "+display, false)
	return m, nil
}
