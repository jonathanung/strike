package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestPickStrikeTipRotatesByDay(t *testing.T) {
	if len(strikeTipParts) < 2 {
		t.Fatal("need at least two tips")
	}
	a := formatStrikeTip(theme.Default(), pickStrikeTipParts(1))
	b := formatStrikeTip(theme.Default(), pickStrikeTipParts(2))
	if a == "" || b == "" {
		t.Fatal("empty tip")
	}
	if a == b {
		t.Fatalf("day 1 and day 2 should differ: %q", a)
	}
	if formatStrikeTip(theme.Default(), pickStrikeTipParts(1)) != a {
		t.Fatal("tip must be stable for a given day index")
	}
	wrapped := formatStrikeTip(theme.Default(), pickStrikeTipParts(1+len(strikeTipParts)))
	if wrapped != a {
		t.Fatalf("wrap: got %q want %q", wrapped, a)
	}
}

func TestStrikeTipsAreProductSpecific(t *testing.T) {
	var b strings.Builder
	for _, parts := range strikeTipParts {
		b.WriteString(strings.Join(parts, " "))
		b.WriteByte('\n')
	}
	joined := b.String()
	for _, needle := range []string{"/", "!", "/agent", "/context", "task"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("catalog missing Strike tip about %q:\n%s", needle, joined)
		}
	}
}

func TestComposerTipHiddenWhenTyping(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.showComposerTip() {
		t.Fatal("empty composer should show tip")
	}
	m.composer.SetValue("hello")
	if m.showComposerTip() {
		t.Fatal("typing should hide tip")
	}
	if m.tipRowsFor() != 0 {
		t.Fatal("tipRowsFor want 0 while typing")
	}
}

func TestComposerTipHiddenOnNotice(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.notice = "something went wrong"
	if m.showComposerTip() {
		t.Fatal("notice should suppress tip")
	}
}

func TestComputeLayoutBudgetsTipAndDropsUnderPressure(t *testing.T) {
	l := computeLayout(80, 30, 3, 0, false, 0, 1)
	if l.tip != 1 {
		t.Fatalf("tip=%d want 1 on tall screen", l.tip)
	}
	l2 := computeLayout(80, 4, 3, 0, false, 0, 1)
	if l2.tip != 0 {
		t.Fatalf("tip=%d want 0 under shortfall", l2.tip)
	}
	if l2.composer < 1 {
		t.Fatalf("composer=%d should keep at least a row when tip drops", l2.composer)
	}
}

func TestTipViewRendersMutedStrikeCopy(t *testing.T) {
	prevTipDay := tipDayOverride
	tipDayOverride = 1
	t.Cleanup(func() { tipDayOverride = prevTipDay })
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	out := ansi.Strip(m.tipView(60))
	if out == "" {
		t.Fatal("expected tip line")
	}
	if !strings.Contains(out, "/") && !strings.Contains(out, "task") {
		t.Fatalf("tip not Strike-specific: %q", out)
	}
	m.composer.SetValue("x")
	if ansi.Strip(m.tipView(60)) != "" {
		t.Fatal("tipView should be empty while typing")
	}
}

func tipCatalogPlain() []string {
	th := theme.Default()
	out := make([]string, 0, len(strikeTipParts))
	for i := range strikeTipParts {
		out = append(out, formatStrikeTip(th, pickStrikeTipParts(i+1)))
	}
	return out
}

func TestHomeCenterBandIncludesTipWhenEmpty(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.composer.SetValue("")
	band := m.homeCenterBand(60, 16, 5, false, "", 0)
	plain := ansi.Strip(band)
	found := false
	for _, tline := range tipCatalogPlain() {
		if tline != "" && strings.Contains(plain, tline) {
			found = true
			break
		}
	}
	// Also accept partial match on distinctive segments.
	if !found {
		for _, parts := range strikeTipParts {
			for _, seg := range parts {
				if strings.Contains(plain, seg) {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Fatalf("home band missing tip:\n%s", plain)
	}
	m.composer.SetValue("typed")
	band2 := ansi.Strip(m.homeCenterBand(60, 16, 5, false, "", 0))
	for _, parts := range strikeTipParts {
		for _, seg := range parts {
			// Skip very short segments that might appear in placeholder.
			if len(seg) < 12 {
				continue
			}
			if strings.Contains(band2, seg) {
				t.Fatalf("tip segment %q still visible while typing", seg)
			}
		}
	}
}

func TestSessionLayoutTipAboveComposerWhenEmpty(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m.testForceMultiPane = true
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.composer.SetValue("")
	l := computeLayout(80, 30, m.composer.Height(), 0, false, m.noticeRowsFor(80), m.tipRowsFor())
	if l.tip != 1 {
		t.Fatalf("session tip rows=%d want 1", l.tip)
	}
	plain := ansi.Strip(viewString(m))
	found := false
	for _, parts := range strikeTipParts {
		for _, seg := range parts {
			if strings.Contains(plain, seg) {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("session view missing tip strip:\n%s", plain)
	}
}
