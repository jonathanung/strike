package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// TestFrameGolden captures full-screen plain-text frames as post-E13.8 baselines
// (Charm v2 + Family soft-rounded bento). Structural layout regressions fail the
// suite; UPDATE_GOLDEN=1 rewrites the fixtures.
//
//	UPDATE_GOLDEN=1 go test ./internal/tui/app/ -run TestFrameGolden -count=1
func TestFrameGolden(t *testing.T) {
	// Pin tip rotation so day-of-year does not flake empty-composer frames (#664).
	prevTipDay := tipDayOverride
	tipDayOverride = 1
	t.Cleanup(func() { tipDayOverride = prevTipDay })

	dir := filepath.Join(moduleRoot(t), "internal", "tui", "app", "testdata", "frames")
	cases := []struct {
		file          string
		width, height int
		build         func(*Model)
	}{
		{
			file: "80x24-left-dashboard.golden", width: 80, height: 24,
			build: func(m *Model) {},
		},
		{
			file: "92x60-left-single.golden", width: 92, height: 60,
			build: func(m *Model) {},
		},
		{
			file: "93x40-split-canonical.golden", width: 93, height: 40,
			build: func(m *Model) {},
		},
		{
			file: "120x40-split.golden", width: 120, height: 40,
			build: func(m *Model) {},
		},
		{
			file: "120x40-busy-transcript.golden", width: 120, height: 40,
			build: func(m *Model) {
				m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "echo-1"})
				m.applyEvent(protocol.AgentSelected{Name: "build"})
				m.applyEvent(protocol.UserMessage{Text: "Refactor the auth store."})
				m.applyEvent(protocol.TurnStarted{})
				m.applyEvent(protocol.TextDelta{Text: "Reading the store, then running tests."})
				m.applyEvent(protocol.ToolCallBegin{CallID: "1", Name: "read", Args: json.RawMessage(`{"path":"internal/auth/store.go"}`)})
				m.applyEvent(protocol.ToolCallEnd{CallID: "1", Title: "internal/auth/store.go", Output: "package auth\n"})
				m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
			},
		},
		{
			file: "93x40-theme-modal.golden", width: 93, height: 40,
			build: func(m *Model) {
				entries := theme.Builtin()
				m.modal = newThemeModal(entries, theme.BuiltinID, &fakeSettings{})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			// Pin adaptive resolution so goldens do not depend on host terminal bg.
			savedDark := compat.HasDarkBackground
			t.Cleanup(func() { compat.HasDarkBackground = savedDark })
			compat.HasDarkBackground = true

			// Empty-frame goldens use home layout (#677); busy transcript uses multi-pane.
			var m Model
			if strings.Contains(tc.file, "busy-transcript") {
				m, _ = newAppTestModel(
					[]string{"build", "plan"},
					[]host.Skill{fakeSkill("review", "review a change", "Review $ARGUMENTS")},
				)
			} else {
				m, _ = newAppTestModelHome(
					[]string{"build", "plan"},
					[]host.Skill{fakeSkill("review", "review a change", "Review $ARGUMENTS")},
				)
			}
			m.workDir = "/tmp/strike-golden"
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			tc.build(&m)
			m.reflow()
			m.refreshViewport()
			got := normalizeFrameGolden(viewString(m))

			path := filepath.Join(dir, tc.file)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				t.Logf("updated %s", path)
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (run UPDATE_GOLDEN=1 go test ./internal/tui/app/ -run TestFrameGolden)", path, err)
			}
			if got != string(want) {
				t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tc.file, got, want)
			}
		})
	}
}

// normalizeFrameGolden strips ANSI and trims trailing spaces per line so
// goldens track layout/content rather than color-profile noise.
func normalizeFrameGolden(frame string) string {
	plain := ansi.Strip(frame)
	lines := strings.Split(plain, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return strings.Join(lines, "\n")
}

// TestThemeLightDarkAppearanceFrames checks session appearance toggles flip
// adaptive resolution and still produce a full-size frame.
func TestThemeLightDarkAppearanceFrames(t *testing.T) {
	savedDark := compat.HasDarkBackground
	t.Cleanup(func() { compat.HasDarkBackground = savedDark })

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m.appearance = appearanceDark
	m.applyAppearance()
	if !compat.HasDarkBackground {
		t.Fatal("dark appearance did not set HasDarkBackground")
	}
	dark := normalizeFrameGolden(viewString(m))
	if lines := strings.Count(dark, "\n") + 1; lines != 24 {
		t.Fatalf("dark frame lines = %d, want 24", lines)
	}

	m.appearance = appearanceLight
	m.applyAppearance()
	if compat.HasDarkBackground {
		t.Fatal("light appearance left HasDarkBackground set")
	}
	light := normalizeFrameGolden(viewString(m))
	if lines := strings.Count(light, "\n") + 1; lines != 24 {
		t.Fatalf("light frame lines = %d, want 24", lines)
	}
	// Adaptive tokens differ; plain layout structure should still fill the terminal.
	if dark == "" || light == "" {
		t.Fatal("empty frame under appearance toggle")
	}
}

// TestBuiltinThemesRenderSmoke applies every bundled theme and renders a split
// frame — catches broken JSON / missing roles after catalog load.
func TestBuiltinThemesRenderSmoke(t *testing.T) {
	savedDark := compat.HasDarkBackground
	t.Cleanup(func() { compat.HasDarkBackground = savedDark })
	compat.HasDarkBackground = true

	builtins := theme.Builtin()
	if len(builtins) < 2 {
		t.Fatalf("builtin count = %d", len(builtins))
	}
	wantIDs := map[string]bool{
		"strike": true, "dracula": true, "nord": true, "catppuccin": true,
		"gruvbox": true, "monokai": true, "tokyo-night": true,
	}
	seen := map[string]bool{}
	for _, entry := range builtins {
		seen[entry.ID] = true
		t.Run(entry.ID, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 40})
			m.applyThemeEntry(entry)
			frame := viewString(m)
			plain := ansi.Strip(frame)
			if plain == "" {
				t.Fatal("empty frame")
			}
			if lines := strings.Count(plain, "\n") + 1; lines != 40 {
				t.Fatalf("lines = %d, want 40", lines)
			}
			for i, row := range strings.Split(frame, "\n") {
				if w := ansi.StringWidth(row); w > 93 {
					t.Fatalf("row %d width = %d, want <= 93", i, w)
				}
			}
			// Theme modal listing uses Name; frame should still show chrome titles.
			if !strings.Contains(plain, "context") && !strings.Contains(plain, "get started") {
				t.Fatalf("theme %s frame missing expected chrome:\n%s", entry.ID, plain)
			}
		})
	}
	for id := range wantIDs {
		if !seen[id] {
			t.Errorf("missing builtin theme %s", id)
		}
	}
}

// TestNoBackgroundThemeFrame ensures transparent background themes still fill
// geometry without panicking (no-OSC11 / transparent terminals).
func TestNoBackgroundThemeFrame(t *testing.T) {
	th := theme.Default()
	th.Background = theme.NoBackground()
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	frame := viewString(m)
	plain := ansi.Strip(frame)
	if lines := strings.Count(plain, "\n") + 1; lines != 24 {
		t.Fatalf("lines = %d, want 24", lines)
	}
	if theme.IsTransparentBackground(m.th.Background) != true {
		t.Fatal("model lost NoBackground")
	}
}
