package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestHelpModalListsCatalogAndFilters(t *testing.T) {
	catalog := commandCatalog([]host.Skill{fakeSkill("review", "review a change", "Review $ARGUMENTS")})
	m := newHelpModal(catalog)
	if len(m.entries) != len(catalog)+1 {
		t.Fatalf("entries = %d, want catalog+tab tip (%d)", len(m.entries), len(catalog)+1)
	}
	for _, want := range []string{"/session [id]", "/export [path] [--open]", "/theme [name|dark|light|auto]", "/memory [list|get|set|rm|export|import] ...", "/issues [list|add|get|close|export|import] ...", "/compact", "/fork", "/undo [chat|files]", "/fast [on|off]", "/think [on|off]", "/layout", "/md-read <path>", "/keys [reset]", "/settings", "/review $ARGUMENTS", "tab"} {
		found := false
		for _, entry := range m.entries {
			if entry.Label == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("help modal omitted %q", want)
		}
	}

	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("memory")})
	if next != m || cmd != nil {
		t.Fatal("typing filter closed help modal or emitted a command")
	}
	filtered := m.filtered()
	if len(filtered) != 1 || !strings.HasPrefix(filtered[0].Label, "/memory") {
		t.Fatalf("filter memory = %#v, want only /memory", filtered)
	}
	view := ansi.Strip(m.view(80, theme.Default()))
	if !strings.Contains(view, "/memory") || !strings.Contains(view, "Commands") {
		t.Errorf("help view missing expected content:\n%s", view)
	}

	next, cmd = m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if next != nil || cmd != nil {
		t.Fatal("escape did not close help modal")
	}
}

func TestHelpModalTinyWidths(t *testing.T) {
	m := newHelpModal(commandCatalog(nil))
	for _, width := range []int{0, 1, 2, 3, 4} {
		if got := m.view(width, theme.Default()); got == "" {
			t.Errorf("view at width %d was empty", width)
		}
	}
}
