package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestSubagentResultCellCollapsedHidesLongBody(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 120; i++ {
		b.WriteString("result line ")
		b.WriteString(itoa(i))
		b.WriteByte('\n')
	}
	cell := &subagentResultCell{
		sessionID: "Y7DHEPWB-full-id",
		agent:     "explore",
		status:    string(protocol.ChildStatusCompleted),
		summary:   b.String(),
		elapsed:   38 * time.Second,
	}
	th := theme.Default().Resolve()
	plain := ansi.Strip(cell.render(80, th))
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("collapsed lines = %d, want 1:\n%s", len(lines), plain)
	}
	if !strings.Contains(plain, th.Icons.TreeCollapsed) {
		t.Errorf("missing collapse marker: %q", plain)
	}
	if !strings.Contains(plain, "explore") {
		t.Errorf("missing agent: %q", plain)
	}
	if !strings.Contains(plain, "completed") {
		t.Errorf("missing status: %q", plain)
	}
	if !strings.Contains(plain, "38s") {
		t.Errorf("missing elapsed: %q", plain)
	}
	if !strings.Contains(plain, "result line 0") {
		t.Errorf("missing first-line snippet: %q", plain)
	}
	for _, hide := range []string{"result line 50", "result line 99", "result line 119"} {
		if strings.Contains(plain, hide) {
			t.Errorf("collapsed should hide %q:\n%s", hide, plain)
		}
	}

	if !cell.toggleExpanded() || !cell.expanded {
		t.Fatal("toggle expand failed")
	}
	plain = ansi.Strip(cell.render(80, th))
	if !strings.Contains(plain, th.Icons.TreeExpanded) {
		t.Errorf("expanded missing marker: %q", plain)
	}
	if !strings.Contains(plain, "result line 50") || !strings.Contains(plain, "result line 119") {
		t.Errorf("expanded missing body lines:\n%s", plain)
	}
	if !cell.toggleExpanded() || cell.expanded {
		t.Fatal("toggle collapse failed")
	}
	// Data retained after collapse.
	if !strings.Contains(cell.summary, "result line 119") {
		t.Error("summary lost after collapse")
	}
	plain = ansi.Strip(cell.render(80, th))
	if strings.Count(plain, "\n") != 0 {
		t.Errorf("re-collapsed should be one row:\n%s", plain)
	}
}

func TestSubagentResultCellFailureAndCancelTones(t *testing.T) {
	th := theme.Default().Resolve()
	fail := &subagentResultCell{
		agent:   "build",
		status:  string(protocol.ChildStatusFailed),
		summary: "boom\nstack trace line",
	}
	fail.expanded = true
	plain := ansi.Strip(fail.render(60, th))
	if !strings.Contains(plain, "failed") || !strings.Contains(plain, "boom") {
		t.Errorf("failed expand: %q", plain)
	}
	// Expanded error body uses Error style — ensure content present.
	if !strings.Contains(plain, "stack trace line") {
		t.Errorf("failed expand missing detail: %q", plain)
	}

	cancel := &subagentResultCell{
		agent:   "explore",
		status:  string(protocol.ChildStatusCanceled),
		summary: "task canceled",
	}
	plain = ansi.Strip(cancel.render(60, th))
	if !strings.Contains(plain, "canceled") {
		t.Errorf("canceled row: %q", plain)
	}
}

func TestSubagentResultCellWidthSafe(t *testing.T) {
	th := theme.Default().Resolve()
	cell := &subagentResultCell{
		sessionID: "ABCDEFGH1234",
		agent:     "explore-with-a-very-long-agent-name",
		status:    string(protocol.ChildStatusCompleted),
		summary:   strings.Repeat("word ", 40),
		elapsed:   time.Minute + 5*time.Second,
	}
	for _, w := range []int{80, 40, 20, 10} {
		plain := ansi.Strip(cell.render(w, th))
		for _, line := range strings.Split(plain, "\n") {
			if ansi.StringWidth(line) > w {
				t.Errorf("width %d line too wide (%d): %q", w, ansi.StringWidth(line), line)
			}
		}
	}
}

func TestChildCompletedAppendsDistinctSubagentRows(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	c1 := protocol.Correlation{SessionID: "child-a", ParentSessionID: "p", Depth: 1}
	c2 := protocol.Correlation{SessionID: "child-b", ParentSessionID: "p", Depth: 1}
	m.applyEvent(protocol.ChildStarted{Correlation: c1, Agent: "explore", Prompt: "one"})
	m.applyEvent(protocol.ChildStarted{Correlation: c2, Agent: "build", Prompt: "two"})
	m.applyEvent(protocol.ChildCompleted{
		Correlation: c1,
		Status:      protocol.ChildStatusCompleted,
		Summary:     "first result",
	})
	m.applyEvent(protocol.ChildCompleted{
		Correlation: c2,
		Status:      protocol.ChildStatusFailed,
		Summary:     "second failed",
	})
	// Exactly-once: duplicate completion refreshes, does not duplicate.
	m.applyEvent(protocol.ChildCompleted{
		Correlation: c1,
		Status:      protocol.ChildStatusCompleted,
		Summary:     "first result refreshed",
	})

	var rows []*subagentResultCell
	for _, c := range m.cells {
		if sc, ok := c.(*subagentResultCell); ok {
			rows = append(rows, sc)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("subagent rows = %d, want 2", len(rows))
	}
	if rows[0].sessionID != "child-a" || rows[0].summary != "first result refreshed" {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].sessionID != "child-b" || rows[1].status != string(protocol.ChildStatusFailed) {
		t.Errorf("row1 = %+v", rows[1])
	}
	if rows[0].agent != "explore" || rows[1].agent != "build" {
		t.Errorf("agents = %q %q", rows[0].agent, rows[1].agent)
	}
}

func TestSubagentResultReplayFromEvents(t *testing.T) {
	var long strings.Builder
	for i := 0; i < 100; i++ {
		long.WriteString("line-")
		long.WriteString(itoa(i))
		long.WriteByte('\n')
	}
	cells, _ := cellsFromEvents([]protocol.Event{
		protocol.ChildStarted{
			Correlation: protocol.Correlation{SessionID: "sess-replay-1"},
			Agent:       "explore",
			Prompt:      "scan",
		},
		protocol.ChildCompleted{
			Correlation: protocol.Correlation{SessionID: "sess-replay-1"},
			Status:      protocol.ChildStatusCompleted,
			Summary:     long.String(),
		},
		protocol.UserMessage{Text: "[child.completed session=sess-rep status=completed]\n" + long.String() + "\nDo not sleep-poll for subagents; this is the terminal result."},
	})
	var sub *subagentResultCell
	var users, infos int
	for _, c := range cells {
		switch tc := c.(type) {
		case *subagentResultCell:
			sub = tc
		case *userCell:
			users++
		case *infoCell:
			infos++
		}
	}
	if sub == nil {
		t.Fatal("missing subagentResultCell on replay")
	}
	if sub.expanded {
		t.Error("replay should reset collapsed")
	}
	if sub.agent != "explore" {
		t.Errorf("agent = %q", sub.agent)
	}
	if users != 0 || infos != 0 {
		t.Errorf("notice leaked: users=%d infos=%d", users, infos)
	}
	plain := ansi.Strip(sub.render(80, theme.Default()))
	if strings.Contains(plain, "line-50") {
		t.Errorf("collapsed replay shows interior lines: %q", plain)
	}
	if !strings.Contains(plain, "line-0") {
		t.Errorf("collapsed missing snippet: %q", plain)
	}
}

func TestSubagentResultExpandViaEnter(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	corr := protocol.Correlation{SessionID: "exp-1", ParentSessionID: "p", Depth: 1}
	m.applyEvent(protocol.ChildStarted{Correlation: corr, Agent: "explore", Prompt: "p"})
	m.applyEvent(protocol.ChildCompleted{
		Correlation: corr,
		Status:      protocol.ChildStatusCompleted,
		Summary:     "alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\neta",
	})
	m.composer.SetValue("")
	m.reflow()
	// Select + expand like empty-enter tool convention.
	if !m.toggleSelectedTool() {
		t.Fatal("enter should expand subagent result")
	}
	sc, ok := m.cells[len(m.cells)-1].(*subagentResultCell)
	if !ok || !sc.expanded {
		t.Fatalf("expanded=%v type=%T", ok && sc.expanded, m.cells[len(m.cells)-1])
	}
	if !m.toggleSelectedTool() || sc.expanded {
		t.Fatal("second enter should collapse")
	}
}

func TestAppendSubagentResultCellExactlyOnce(t *testing.T) {
	ev := protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "once"},
		Status:      protocol.ChildStatusCompleted,
		Summary:     "a",
	}
	cells := appendSubagentResultCell(nil, ev, "explore", time.Second)
	cells = appendSubagentResultCell(cells, ev, "explore", 2*time.Second)
	if len(cells) != 1 {
		t.Fatalf("len = %d", len(cells))
	}
	sc := cells[0].(*subagentResultCell)
	if sc.elapsed != 2*time.Second {
		t.Errorf("elapsed = %v", sc.elapsed)
	}
}

func TestLookupChildMetaElapsed(t *testing.T) {
	start := time.Now().Add(-45 * time.Second)
	end := start.Add(45 * time.Second)
	children := []childActivity{{
		sessionID: "c1",
		agent:     "build",
		startedAt: start,
		endedAt:   end,
	}}
	agent, elapsed := lookupChildMeta(children, "c1")
	if agent != "build" {
		t.Errorf("agent = %q", agent)
	}
	if elapsed < 44*time.Second || elapsed > 46*time.Second {
		t.Errorf("elapsed = %v", elapsed)
	}
}

func TestSubagentResultCopyText(t *testing.T) {
	c := &subagentResultCell{summary: "full\nbody\n"}
	if got := c.copyText(); got != "full\nbody" {
		t.Errorf("copyText = %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  a\nb\n "); got != "a" {
		t.Errorf("got %q", got)
	}
	if got := firstLine("only"); got != "only" {
		t.Errorf("got %q", got)
	}
	if got := firstLine("  \n  "); got != "" {
		t.Errorf("got %q", got)
	}
}
