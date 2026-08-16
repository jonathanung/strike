package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestConstrainedCompletionAndComposerViewsUseSoftChrome locks default
// bordered chrome for height ≥ 3 (square outline). Replaces the old
// "no box-drawing" assertion from the solid-default era.
func TestConstrainedCompletionAndComposerViewsUseSoftChrome(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.setComposerValueAt("/", 1)
	m.recomputeCompletion()
	if m.completion == nil {
		t.Fatal("slash draft did not open completion")
	}
	candidate := m.completion.Candidates[0].Spec.Name
	for _, tt := range []struct {
		height int
		chrome bool
	}{{height: 0}, {height: 1}, {height: 2}, {height: 3, chrome: true}} {
		t.Run("height "+itoa(tt.height), func(t *testing.T) {
			assertAllocation := func(name, out, content string) {
				plain := ansi.Strip(out)
				if tt.height == 0 {
					if out != "" {
						t.Errorf("%s at zero height = %q, want empty", name, plain)
					}
					return
				}
				if rows := strings.Count(out, "\n") + 1; rows != tt.height {
					t.Errorf("%s rows = %d, want allocated %d", name, rows, tt.height)
				}
				hasBox := strings.ContainsAny(plain, "╭╰┌└│")
				if tt.chrome {
					if !hasBox {
						t.Errorf("%s missing bordered chrome: %q", name, plain)
					}
					if name == "composer" && !strings.Contains(plain, "command") && !strings.Contains(plain, "chat") {
						// Slash draft → command mode title (#678); bare drafts use chat.
						t.Errorf("%s chrome missing mode title: %q", name, plain)
					}
				} else if hasBox && tt.height < 3 {
					// height 1–2 may be borderless; soft chrome needs height ≥ 2
					// and width ≥ 6 — height 2 is title+footer only with chrome.
					// height 1 is always borderless.
					if tt.height == 1 && hasBox {
						t.Errorf("%s height 1 should be borderless: %q", name, plain)
					}
				}
				if !strings.Contains(plain, content) {
					t.Errorf("%s height %d omitted useful content %q: %q", name, tt.height, content, plain)
				}
			}
			assertAllocation("composer", m.composerView(false, 80, tt.height), "/")
			assertAllocation("completion", m.completion.view(80, tt.height, m.th), candidate)
		})
	}
}
