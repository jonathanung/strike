package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestContextPaneBodyEmptyShowsNoneAndProviderHint(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	body := ansi.Strip(m.contextPaneBody(40, 10))
	if !strings.Contains(body, "none") {
		t.Errorf("empty context body missing %q: %q", "none", body)
	}
	if !strings.Contains(body, "/provider") {
		t.Errorf("empty context body missing %q: %q", "/provider", body)
	}
	if strings.Contains(strings.ToLower(body), "placeholder") {
		t.Errorf("context body contains placeholder copy: %q", body)
	}
}

func TestContextPaneBodyShowsConfiguredSessionValues(t *testing.T) {
	skills := []host.Skill{fakeSkill("review", "r", "Review")}
	m, _ := newAppTestModel([]string{"build"}, skills)
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.agentName = "build"
	m.effort = protocol.EffortHigh
	m.fastEnabled = true

	body := ansi.Strip(m.contextPaneBody(60, 10))
	for _, want := range []string{
		"echo/echo-1",
		"build",
		"high",
		"supervised", // default autonomy
		"on",         // fast
		"skills",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("context body missing %q:\n%s", want, body)
		}
	}
	// Auth detail for echo is "offline dev provider".
	if !strings.Contains(body, "offline") && !strings.Contains(body, "dev") {
		t.Errorf("context body missing auth detail:\n%s", body)
	}
	if strings.Contains(strings.ToLower(body), "placeholder") {
		t.Errorf("context body contains placeholder copy: %q", body)
	}
}

func TestContextPaneBodyHeightClampDropsLowerPriorityRows(t *testing.T) {
	skills := []host.Skill{
		fakeSkill("review", "r", "Review"),
		fakeSkill("audit", "a", "Audit"),
	}
	m, _ := newAppTestModel([]string{"build"}, skills)
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.agentName = "build"
	m.effort = protocol.EffortMax
	m.fastEnabled = true

	// Full height keeps high-priority model row.
	full := ansi.Strip(m.contextPaneBody(40, 10))
	if !strings.Contains(full, "echo/echo-1") {
		t.Fatalf("full body missing model: %q", full)
	}

	// Height 1 keeps only the first (highest-priority) row: model.
	one := ansi.Strip(m.contextPaneBody(40, 1))
	lines := nonEmptyLines(one)
	if len(lines) != 1 {
		t.Fatalf("height=1 lines = %d (%q), want 1", len(lines), one)
	}
	if !strings.Contains(lines[0], "echo") && !strings.Contains(lines[0], "model") {
		t.Errorf("height=1 dropped the model row: %q", one)
	}
	// Lower-priority rows must be gone at height 1.
	for _, drop := range []string{"skills", "max", "build"} {
		if strings.Contains(one, drop) {
			t.Errorf("height=1 retained lower-priority %q: %q", drop, one)
		}
	}

	// Height 2 keeps model + agent (next priority), drops tail.
	two := ansi.Strip(m.contextPaneBody(40, 2))
	twoLines := nonEmptyLines(two)
	if len(twoLines) != 2 {
		t.Fatalf("height=2 lines = %d (%q), want 2", len(twoLines), two)
	}
	if strings.Contains(two, "skills") {
		t.Errorf("height=2 retained lowest-priority skills: %q", two)
	}
}

func TestContextPaneBodyWidthSafe(t *testing.T) {
	m, _ := newAppTestModel([]string{"build"}, nil)
	m.providerName = "very-long-provider-name-that-exceeds"
	m.modelName = "very-long-model-id-that-also-exceeds-budget"
	m.agentName = "build"
	m.effort = protocol.EffortXHigh
	m.fastEnabled = true
	// Include very tight widths (5, 6) where label+gap can exhaust the budget.
	for _, width := range []int{5, 6, 8, 12, 20, 40} {
		body := m.contextPaneBody(width, 10)
		for i, line := range strings.Split(body, "\n") {
			if line == "" {
				continue
			}
			if got := ansi.StringWidth(ansi.Strip(line)); got > width {
				t.Errorf("width=%d line %d display width = %d: %q", width, i, got, ansi.Strip(line))
			}
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width=%d line %d lipgloss width = %d: %q", width, i, got, line)
			}
		}
	}
}

func TestActivityPaneBodyIdleTips(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	body := ansi.Strip(m.activityPaneBody(40, 10))
	for _, want := range []string{
		"/",
		keyHint(m.keyMap.Palette).Key,         // ctrl+p
		keyHint(m.keyMap.Agent).Key,           // tab
		keyHint(m.keyMap.CycleWindowNext).Key, // ctrl+j
		keyHint(m.keyMap.Newline).Key,         // shift+enter
	} {
		if !strings.Contains(body, want) {
			t.Errorf("idle activity tips missing %q: %q", want, body)
		}
	}
	// Descriptions derive from keyMap help (plus the literal "/" commands tip).
	if !strings.Contains(body, keyHint(m.keyMap.Palette).Label) && !strings.Contains(body, "commands") {
		t.Errorf("idle tips missing command/palette descriptions: %q", body)
	}
	if !strings.Contains(body, keyHint(m.keyMap.Newline).Label) {
		t.Errorf("idle tips missing newline description: %q", body)
	}
	if strings.Contains(strings.ToLower(body), "placeholder") {
		t.Errorf("activity body contains placeholder copy: %q", body)
	}
}

func TestActivityPaneBodyShowsToolNameWithoutIdleTips(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.cells = []cell{
		&toolCell{name: "bash", title: "run tests", done: true},
	}
	body := ansi.Strip(m.activityPaneBody(40, 10))
	if !strings.Contains(body, "run tests") && !strings.Contains(body, "bash") {
		t.Errorf("activity with tools missing tool name: %q", body)
	}
	// Idle tips should not appear once tools are present.
	for _, tip := range []string{
		keyHint(m.keyMap.Palette).Key,
		keyHint(m.keyMap.Newline).Key,
		keyHint(m.keyMap.Palette).Label,
		"commands",
	} {
		if strings.Contains(body, tip) {
			t.Errorf("activity with tools still shows idle tip %q: %q", tip, body)
		}
	}
	if strings.Contains(strings.ToLower(body), "placeholder") {
		t.Errorf("activity body contains placeholder copy: %q", body)
	}
}

func TestActivityPaneBodyWidthSafeAndNeverPlaceholder(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.cells = []cell{
		&toolCell{name: "bash", title: strings.Repeat("long-title-", 20), done: true, isError: true},
		&toolCell{name: "read", title: "short", done: false},
	}
	// Tool rows budget prefix+suffix; exercise tight and typical right-pane widths.
	for _, width := range []int{8, 16, 32} {
		for _, height := range []int{1, 2, 5} {
			body := m.activityPaneBody(width, height)
			plain := ansi.Strip(body)
			if strings.Contains(strings.ToLower(plain), "placeholder") {
				t.Errorf("%dx%d activity contains placeholder: %q", width, height, plain)
			}
			for i, line := range strings.Split(body, "\n") {
				if line == "" {
					continue
				}
				if got := ansi.StringWidth(ansi.Strip(line)); got > width {
					t.Errorf("%dx%d line %d width = %d: %q", width, height, i, got, ansi.Strip(line))
				}
			}
		}
	}
	// Idle tips: widths at or above the longest tip key ("shift+enter") plus gap.
	// (Narrower than that can overshoot — see Findings.)
	m.cells = nil
	for _, width := range []int{16, 20, 28, 40} {
		body := m.activityPaneBody(width, 5)
		plain := ansi.Strip(body)
		if strings.Contains(strings.ToLower(plain), "placeholder") {
			t.Errorf("idle width=%d contains placeholder: %q", width, plain)
		}
		for i, line := range strings.Split(body, "\n") {
			if line == "" {
				continue
			}
			if got := ansi.StringWidth(ansi.Strip(line)); got > width {
				t.Errorf("idle width=%d line %d width = %d: %q", width, i, got, ansi.Strip(line))
			}
		}
	}
}

func TestSidePaneBodiesEmptyDimensions(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	if got := m.contextPaneBody(0, 10); got != "" {
		t.Errorf("context zero width = %q", got)
	}
	if got := m.contextPaneBody(10, 0); got != "" {
		t.Errorf("context zero height = %q", got)
	}
	if got := m.activityPaneBody(0, 10); got != "" {
		t.Errorf("activity zero width = %q", got)
	}
	if got := m.activityPaneBody(10, 0); got != "" {
		t.Errorf("activity zero height = %q", got)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
