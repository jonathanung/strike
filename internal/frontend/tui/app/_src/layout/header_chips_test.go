package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestFitHeaderChipsDropsLowestPriorityFirst(t *testing.T) {
	// Synthetic chips: lower prio drops first. Views are plain ASCII so widths are stable.
	chips := []headerChip{
		{prio: 100, view: "MODEL"},
		{prio: 85, view: "AUTO"},
		{prio: 40, view: "DOT"},
		{prio: 30, view: "PHASE"},
		{prio: 20, view: "EFFORT"},
		{prio: 10, view: "THINK"},
	}
	firstGap, restGap := " ", " "
	full := joinHeaderChips(chips, firstGap, restGap)
	fullW := lipgloss.Width(full)

	// Comfortable budget keeps all.
	if got := fitHeaderChips(chips, fullW, firstGap, restGap); got != full {
		t.Fatalf("full budget: got %q want %q", got, full)
	}

	// Drop THINK only: budget just below full.
	withoutThink := joinHeaderChips(chips[:5], firstGap, restGap)
	got := fitHeaderChips(chips, lipgloss.Width(withoutThink), firstGap, restGap)
	if got != withoutThink {
		t.Fatalf("drop think: got %q want %q", got, withoutThink)
	}
	if strings.Contains(got, "THINK") {
		t.Fatalf("think should drop first: %q", got)
	}

	// Drop THINK then EFFORT.
	withoutEffort := joinHeaderChips(chips[:4], firstGap, restGap)
	got = fitHeaderChips(chips, lipgloss.Width(withoutEffort), firstGap, restGap)
	if got != withoutEffort {
		t.Fatalf("drop effort: got %q want %q", got, withoutEffort)
	}
	if strings.Contains(got, "EFFORT") || strings.Contains(got, "THINK") {
		t.Fatalf("effort/think should be gone: %q", got)
	}

	// Drop through phase, keep model+auto before decorative.
	core := joinHeaderChips([]headerChip{chips[0], chips[1]}, firstGap, restGap)
	got = fitHeaderChips(chips, lipgloss.Width(core), firstGap, restGap)
	if !strings.Contains(got, "MODEL") || !strings.Contains(got, "AUTO") {
		t.Fatalf("core chips missing under pressure: %q", got)
	}
	for _, bad := range []string{"THINK", "EFFORT", "PHASE", "DOT"} {
		if strings.Contains(got, bad) {
			t.Fatalf("decorative %s should drop before core: %q", bad, got)
		}
	}

	// Zero budget yields empty.
	if got := fitHeaderChips(chips, 0, firstGap, restGap); got != "" {
		t.Fatalf("zero budget: %q", got)
	}
}

func TestHeaderIsolationBadge(t *testing.T) {
	m := Model{isolation: protocol.IsolationContainer, th: theme.Default(), width: 120}
	// ensure model has enough width fields
	m.width = 120
	plain := ansi.Strip(m.headerView(120))
	if !strings.Contains(plain, "container") {
		t.Fatalf("header missing isolation badge:\n%s", plain)
	}
}

func TestHeaderShowsInspectedChildModel(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "parent-model"
	m.sessionID = "root"
	m.viewingID = "child-1"
	m.children = []childActivity{{
		sessionID: "child-1",
		provider:  "xai",
		model:     "grok-4",
		status:    "running",
	}}
	plain := ansi.Strip(m.headerView(120))
	if !strings.Contains(plain, "xai/grok-4") {
		t.Fatalf("header missing child model: %q", plain)
	}
	if strings.Contains(plain, "echo/parent-model") {
		t.Fatalf("header still shows parent model while inspecting child: %q", plain)
	}
}

func TestHeaderDoesNotInheritParentModelWhenChildUnknown(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "parent-model"
	m.sessionID = "root"
	m.viewingID = "child-1"
	m.children = []childActivity{{
		sessionID: "child-1",
		status:    "running",
	}}
	plain := ansi.Strip(m.headerView(120))
	if strings.Contains(plain, "parent-model") {
		t.Fatalf("unknown child model must not inherit parent: %q", plain)
	}
	if !strings.Contains(plain, "no model") {
		t.Fatalf("unknown child model should show no model: %q", plain)
	}
}
