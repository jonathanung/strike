package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

func TestKickerUppercasesLabel(t *testing.T) {
	th := theme.Default()
	got := ansi.Strip(kicker(th.S().Muted, "instruction"))
	if got != "INSTRUCTION" {
		t.Fatalf("kicker = %q", got)
	}
}

func TestInspectorFrameHasNoBoxCorners(t *testing.T) {
	th := theme.Default()
	out := inspectorFrame(th, "context", "", "model  echo", 32, 6, true, false)
	plain := ansi.Strip(out)
	if strings.ContainsAny(plain, "┌┐└┘╭╮╰╯") {
		t.Fatalf("inspector used boxed corners:\n%s", plain)
	}
	if !strings.Contains(plain, "CONTEXT") {
		t.Fatalf("inspector missing kicker:\n%s", plain)
	}
	if !strings.Contains(plain, th.Icons.ToolGuide) {
		t.Fatalf("inspector missing left rule:\n%s", plain)
	}
	rows := strings.Split(plain, "\n")
	if len(rows) != 6 {
		t.Fatalf("inspector rows = %d, want 6", len(rows))
	}
	for i, row := range rows {
		if got := ansi.StringWidth(row); got != 32 {
			t.Errorf("row %d width = %d, want 32: %q", i, got, row)
		}
	}
}

func TestInspectorInnerGeometry(t *testing.T) {
	th := theme.Default()
	if got := inspectorInnerWidth(th, 32); got != 30 {
		t.Errorf("inner width = %d, want 30", got)
	}
	if got := inspectorInnerHeight(10, false); got != 9 {
		t.Errorf("inner height = %d, want 9", got)
	}
	if got := inspectorInnerHeight(10, true); got != 8 {
		t.Errorf("inner height with footer = %d, want 8", got)
	}
}

func TestEmptyStateBlockVoice(t *testing.T) {
	th := theme.Default()
	plain := ansi.Strip(emptyStateBlock(th, 40, "01 / ready", "Direct the work.", "Describe an outcome."))
	for _, want := range []string{"01 / READY", "Direct the work.", "Describe an outcome."} {
		if !strings.Contains(plain, want) {
			t.Errorf("empty-state missing %q:\n%s", want, plain)
		}
	}
}

func TestHeaderKickerIsNotAPill(t *testing.T) {
	th := theme.Default()
	chip := headerKicker(th, ui.ToneMuted, "no model")
	if ansi.Strip(chip) != "NO MODEL" {
		t.Fatalf("chip = %q", ansi.Strip(chip))
	}
	if chip == ui.Badge(th, ui.ToneMuted, "no model") {
		t.Fatal("header kicker should not be a surface pill")
	}
}

func TestMessageHeadUsesFocusBar(t *testing.T) {
	th := theme.Default()
	head := ansi.Strip(messageHead(th, th.S().UserLabel, "you"))
	if !strings.HasPrefix(head, th.Icons.FocusBar) {
		t.Fatalf("message head = %q, want FocusBar prefix", head)
	}
	if !strings.Contains(head, "YOU") {
		t.Fatalf("message head missing YOU: %q", head)
	}
}
