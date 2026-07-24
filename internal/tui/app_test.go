package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const appCmdTimeout = 2 * time.Second

func TestCompletionReplacementPreservesArgumentsAndLinesAtCursorPositions(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		offset int
		want   string
	}{
		{name: "cursor in middle", value: "/pr old argument\nlater line", offset: 2, want: "/provider old argument\nlater line"},
		{name: "cursor at end", value: "/pr old argument\nlater line", offset: 3, want: "/provider old argument\nlater line"},
		{name: "wide unicode skill", value: "/部 old argument\nlater 界", offset: 2, want: "/部署 old argument\nlater 界"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skills := []host.Skill{fakeSkill("部署", "deploy", "deploy")}
			m, _ := newAppTestModel(nil, skills)
			m.setComposerValueAt(tt.value, tt.offset)
			m.recomputeCompletion()
			if m.completion == nil {
				t.Fatal("completion did not open")
			}
			if strings.HasPrefix(tt.value, "/部") {
				for i, candidate := range m.completion.Candidates {
					if candidate.Spec.Name == "/部署" {
						m.completion.Selected = i
					}
				}
			}
			m.applyCompletion()
			if got := m.composer.Value(); got != tt.want {
				t.Errorf("composer value = %q, want %q", got, tt.want)
			}
			if m.completion != nil {
				t.Error("completion remained open after replacement")
			}
		})
	}
}

func TestCompletionClosesWhenCursorLeavesLeadingToken(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.setComposerValueAt("/pr argument", 3)
	m.recomputeCompletion()
	if m.completion == nil {
		t.Fatal("completion did not open at token end")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.completion != nil {
		t.Fatal("completion remained open after cursor moved into arguments")
	}
}

func TestModelUpdateCompletionConsumesEscapeTabAndEnter(t *testing.T) {
	t.Run("escape does not interrupt running turn", func(t *testing.T) {
		m, ops := newAppTestModel([]string{"build", "plan"}, nil)
		m.turnRunning = true
		m = typeAppText(t, m, "/")
		if m.completion == nil {
			t.Fatal("completion did not open")
		}
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEsc})
		if m.completion != nil {
			t.Error("escape did not close completion")
		}
		assertNoAppOp(t, ops)
	})

	t.Run("tab completes instead of cycling agent", func(t *testing.T) {
		m, ops := newAppTestModel([]string{"build", "plan"}, nil)
		m.agentName = "build"
		m = typeAppText(t, m, "/pr")
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyTab})
		if got := m.composer.Value(); got != "/provider" {
			t.Errorf("tab completion value = %q, want /provider", got)
		}
		assertNoAppOp(t, ops)
	})

	t.Run("first enter completes and second executes bare command", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		m = typeAppText(t, m, "/he")
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if got := m.composer.Value(); got != "/help" {
			t.Fatalf("first enter value = %q, want completed /help", got)
		}
		assertNoAppOp(t, ops)
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if m.composer.Value() != "" || !strings.Contains(m.notice, "commands:") {
			t.Errorf("second enter did not execute /help: value=%q notice=%q", m.composer.Value(), m.notice)
		}
		assertNoAppOp(t, ops)
	})
}

func TestModalReceivesKeysBeforeCompletionAndComposer(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.completion = leadingSlashCompletion("/", 0, 1, m.commands)
	probe := &appProbeModal{}
	m.modal = probe
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if probe.keys != 1 {
		t.Fatalf("modal received %d keys, want 1", probe.keys)
	}
	if m.completion == nil {
		t.Error("completion was consumed while modal was active")
	}
	assertNoAppOp(t, ops)
}

func TestControlCQuitsBeforeOtherInputLayers(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("unchanged")
	m.completion = leadingSlashCompletion("/", 0, 1, m.commands)
	probe := &appProbeModal{}
	m.modal = probe

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(Model)
	if _, ok := runAppCmd(t, cmd).(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not return a quit command")
	}
	if probe.keys != 0 || m.composer.Value() != "unchanged" {
		t.Errorf("ctrl+c reached an input layer: modal keys=%d composer=%q", probe.keys, m.composer.Value())
	}
	assertNoAppOp(t, ops)
}

func TestComposerEnterBindings(t *testing.T) {
	t.Run("alt enter inserts newline without sending", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		m.providerName = "echo"
		m = typeAppText(t, m, "first")
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
		m = typeAppText(t, m, "second")
		if got := m.composer.Value(); got != "first\nsecond" {
			t.Errorf("composer value = %q, want multiline input", got)
		}
		assertNoAppOp(t, ops)
	})

	for _, name := range []string{"ordinary enter", "framework-indistinguishable shift enter"} {
		t.Run(name, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.providerName = "echo"
			m = typeAppText(t, m, "send me")
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			runAppCmd(t, cmd)
			op := receiveAppOp(t, ops)
			input, ok := op.(protocol.UserInput)
			if !ok || input.Text != "send me" {
				t.Fatalf("operation = %#v, want UserInput with original text", op)
			}
			if m.composer.Value() != "" {
				t.Errorf("composer was not reset: %q", m.composer.Value())
			}
		})
	}
}

func TestComposerHeightCountsWrappedLogicalLinesAtExactBoundaries(t *testing.T) {
	tests := []struct {
		name           string
		value          string
		wantHeight     int
		wantCursorLine int
	}{
		{name: "empty retains minimum", value: "", wantHeight: composerMinHeight, wantCursorLine: -1},
		{name: "short retains minimum", value: "short", wantHeight: composerMinHeight, wantCursorLine: -1},
		{name: "non-boundary wide runes", value: strings.Repeat("界", 5), wantHeight: 2, wantCursorLine: -1},
		{name: "sixteen unbroken ASCII characters", value: strings.Repeat("x", 16), wantHeight: 3, wantCursorLine: -1},
		{name: "eight wide runes", value: strings.Repeat("界", 8), wantHeight: 3, wantCursorLine: -1},
		{name: "eight combining graphemes", value: strings.Repeat("e\u0301", 8), wantHeight: 2, wantCursorLine: -1},
		{name: "blank logical line", value: "one\n\nthree", wantHeight: 3, wantCursorLine: -1},
		{name: "exact-boundary tall line before short cursor line", value: strings.Repeat("x", 16) + "\ny", wantHeight: 4, wantCursorLine: 1},
		{name: "short line before exact-boundary tall line", value: "y\n" + strings.Repeat("x", 16), wantHeight: 4, wantCursorLine: -1},
		{name: "mixed explicit and soft rows cap at eight", value: strings.Repeat("x", 16) + "\na\nb\nc\nd\ne\nf", wantHeight: composerMaxHeight, wantCursorLine: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 12, Height: 30})
			m.composer.SetValue(tt.value)
			if tt.wantCursorLine >= 0 && m.composer.Line() != tt.wantCursorLine {
				t.Fatalf("cursor line = %d, want %d before reflow", m.composer.Line(), tt.wantCursorLine)
			}

			m.reflow()

			if got := m.composer.Height(); got != tt.wantHeight {
				t.Errorf("composer height = %d, want %d at app width 12", got, tt.wantHeight)
			}
		})
	}
}

func TestLayoutReflowHandlesTinyWindowsPopupPasteResizeAndReset(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	for _, size := range []tea.WindowSizeMsg{
		{Width: 0, Height: 0},
		{Width: 1, Height: 1},
		{Width: 3, Height: 2},
		{Width: 9, Height: 4},
	} {
		m = updateApp(t, m, size)
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/"), Paste: false})
		_ = m.View()
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEsc})
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("界界\nmore\nlines"), Paste: true})
		_ = m.View()
		if m.width < 0 || m.height < 0 || m.viewport.Width < 0 || m.viewport.Height < 0 || m.composer.Width() < 1 || m.composer.Height() < composerMinHeight {
			t.Fatalf("negative/invalid dimensions after %#v: model=%dx%d viewport=%dx%d composer=%dx%d", size, m.width, m.height, m.viewport.Width, m.viewport.Height, m.composer.Width(), m.composer.Height())
		}
		m.resetComposer()
		_ = m.View()
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.viewport.Width != ui.InnerWidth(80) || m.viewport.Height < 0 {
		t.Errorf("resize did not restore effective viewport dimensions: %dx%d", m.viewport.Width, m.viewport.Height)
	}
}

func TestDangerousPermissionsIndicatorPersistsAcrossStateAndNoticeChanges(t *testing.T) {
	const indicator = "DANGER: permissions bypassed"
	m, _ := newAppTestModelWithOptions(Options{DangerouslySkipPermissions: true})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	assertViewContainsPlainText(t, m.View(), indicator)
	m.applyEvent(protocol.TurnStarted{})
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, indicator) || !strings.Contains(plain, "working") {
		t.Errorf("running view does not retain danger indicator and running status:\n%s", plain)
	}

	m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "test-model"})
	assertViewContainsPlainText(t, m.View(), indicator)
	if !strings.Contains(ansi.Strip(m.View()), "model: echo/test-model") {
		t.Errorf("model selection notice was not rendered alongside danger indicator:\n%s", ansi.Strip(m.View()))
	}
	m.applyEvent(protocol.EngineError{Message: "transient engine error"})
	assertViewContainsPlainText(t, m.View(), indicator)
	m.applyEvent(protocol.TurnCompleted{})
	assertViewContainsPlainText(t, m.View(), indicator)
}

func TestDangerousPermissionsIndicatorIsOptInAndSafeAtTinyWidths(t *testing.T) {
	const indicator = "DANGER: permissions bypassed"
	normal, _ := newAppTestModelWithOptions(Options{})
	assertViewOmitsPlainText(t, normal.View(), indicator)
	normal = updateApp(t, normal, tea.WindowSizeMsg{Width: 80, Height: 24})
	assertViewOmitsPlainText(t, normal.View(), indicator)

	dangerous, _ := newAppTestModelWithOptions(Options{DangerouslySkipPermissions: true})
	assertViewContainsPlainText(t, dangerous.View(), indicator)
	for _, size := range []tea.WindowSizeMsg{
		{Width: 0, Height: 0},
		{Width: 1, Height: 1},
		{Width: 3, Height: 2},
		{Width: 9, Height: 4},
	} {
		dangerous = updateApp(t, dangerous, size)
		plain := ansi.Strip(dangerous.View())
		wantPrefix := indicator[:min(max(1, size.Width), len(indicator))]
		if !strings.Contains(plain, wantPrefix) {
			t.Errorf("danger indicator at size %dx%d = %q, want semantic prefix %q", size.Width, size.Height, plain, wantPrefix)
		}
	}
}

func TestDangerousPermissionsIndicatorRemainsVisibleWithActiveModals(t *testing.T) {
	const indicator = "DANGER: permissions bypassed"
	tests := []struct {
		name    string
		open    func(*Model)
		content []string
	}{
		{
			name: "model picker",
			open: func(m *Model) {
				picker := newModelModal("echo", "", m.ops, m.services.Settings)
				picker.loading = false
				picker.all = []string{"echo-regression-model"}
				m.modal = picker
			},
			content: []string{"Select model — echo", "echo-regression-model"},
		},
		{
			name: "provider picker",
			open: func(m *Model) {
				m.modal = newProviderModal(m.services, "", m.ops, m.th)
			},
			content: []string{"Select provider", "echo"},
		},
		{
			name: "permission prompt",
			open: func(m *Model) {
				m.applyEvent(protocol.PermissionAsked{
					RequestID:  "danger-modal-regression",
					Permission: "bash",
					Patterns:   []string{"go test ./..."},
				})
			},
			content: []string{"Permission required:", "bash", "allow once", "reject"},
		},
		{
			name: "command palette",
			open: func(m *Model) {
				m.modal = newPaletteModal(m.commands, nil, m.currentPaletteAvailability())
			},
			content: []string{"Command palette", "/provider", "/help"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, size := range []struct {
				window tea.WindowSizeMsg
				tiny   bool
			}{
				{window: tea.WindowSizeMsg{Width: 80, Height: 22}},
				{window: tea.WindowSizeMsg{Width: 32, Height: 14}},
				{window: tea.WindowSizeMsg{Width: 9, Height: 4}, tiny: true},
			} {
				name := itoa(size.window.Width) + "x" + itoa(size.window.Height)
				t.Run(name, func(t *testing.T) {
					dangerous, _ := newAppTestModelWithOptions(Options{DangerouslySkipPermissions: true})
					dangerous = updateApp(t, dangerous, size.window)
					tt.open(&dangerous)
					plain := ansi.Strip(dangerous.View())
					content := tt.content
					wantIndicator := indicator
					if size.tiny {
						// At an extreme 9x4 the dialog frame truncates its title
						// and rows; only the danger banner is asserted there.
						content = nil
						wantIndicator = indicator[:size.window.Width]
					}
					for _, want := range append([]string{wantIndicator}, content...) {
						contains := strings.Contains(plain, want)
						if size.tiny && want != wantIndicator {
							contains = strings.Contains(compactAppPlainText(plain), compactAppPlainText(want))
						}
						if !contains {
							t.Errorf("dangerous modal view at %dx%d does not contain %q:\n%s", size.window.Width, size.window.Height, want, plain)
						}
					}

					normal, _ := newAppTestModelWithOptions(Options{})
					normal = updateApp(t, normal, size.window)
					tt.open(&normal)
					normalPlain := ansi.Strip(normal.View())
					if strings.Contains(normalPlain, wantIndicator) {
						t.Errorf("normal modal view at %dx%d unexpectedly contains danger indicator:\n%s", size.window.Width, size.window.Height, normalPlain)
					}
					for _, want := range content {
						contains := strings.Contains(normalPlain, want)
						if size.tiny {
							contains = strings.Contains(compactAppPlainText(normalPlain), compactAppPlainText(want))
						}
						if !contains {
							t.Errorf("normal modal view at %dx%d does not contain %q:\n%s", size.window.Width, size.window.Height, want, normalPlain)
						}
					}
				})
			}
		})
	}
}

func TestDangerousPermissionsIndicatorAndModalPersistAcrossRunningStateAndNotice(t *testing.T) {
	const indicator = "DANGER: permissions bypassed"
	m, _ := newAppTestModelWithOptions(Options{DangerouslySkipPermissions: true})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 22})
	m.modal = newPaletteModal(m.commands, nil, m.currentPaletteAvailability())

	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "notice-regression-model"})

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		indicator,
		"Command palette",
		"working",
		"model: echo/notice-regression-model",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("running modal view with notice does not contain %q:\n%s", want, plain)
		}
	}
}

func TestSlashCommandExecutionAndSkillRenderingRemainIntact(t *testing.T) {
	t.Run("provider arguments select model", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		m.composer.SetValue("/provider echo test-model")
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		runAppCmd(t, cmd)
		op := receiveAppOp(t, ops)
		want := protocol.SelectModel{Provider: "echo", Model: "test-model"}
		if op != want {
			t.Errorf("operation = %#v, want %#v", op, want)
		}
		if m.composer.Value() != "" {
			t.Error("provider command did not reset composer")
		}
	})

	t.Run("skill substitutes arguments and sends rendered prompt", func(t *testing.T) {
		skill := fakeSkill("review", "review code", "Review: $ARGUMENTS")
		m, ops := newAppTestModel(nil, []host.Skill{skill})
		m.providerName = "echo"
		m.composer.SetValue("/review this diff")
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		runAppCmd(t, cmd)
		op := receiveAppOp(t, ops)
		want := protocol.UserInput{Text: "Review: this diff"}
		if op != want {
			t.Errorf("operation = %#v, want %#v", op, want)
		}
	})
}

func TestModelAndAgentSlashCommandsEmitSelections(t *testing.T) {
	t.Run("model selects id for current provider", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		m.providerName = "openai"
		m.composer.SetValue("/model gpt-test")

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		runAppCmd(t, cmd)

		want := protocol.SelectModel{Provider: "openai", Model: "gpt-test"}
		if got := receiveAppOp(t, ops); got != want {
			t.Errorf("operation = %#v, want %#v", got, want)
		}
		if m.composer.Value() != "" {
			t.Errorf("composer value = %q, want reset after /model", m.composer.Value())
		}
		assertNoAppOp(t, ops)
	})

	t.Run("agent preserves multi-word name", func(t *testing.T) {
		m, ops := newAppTestModel([]string{"build", "code reviewer"}, nil)
		m.composer.SetValue("/agent code reviewer")

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		runAppCmd(t, cmd)

		want := protocol.SelectAgent{Name: "code reviewer"}
		if got := receiveAppOp(t, ops); got != want {
			t.Errorf("operation = %#v, want %#v", got, want)
		}
		if m.composer.Value() != "" {
			t.Errorf("composer value = %q, want reset after /agent", m.composer.Value())
		}
		assertNoAppOp(t, ops)
	})
}

func TestBareAuthSlashCommandOpensProviderStatusModalWithoutSideEffects(t *testing.T) {
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event)
	m := New(ops, events, testServices(nil, nil))
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("/auth")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("/auth returned unexpected message %#v", msg)
	}

	if got := m.View(); !strings.Contains(got, "Select provider") {
		t.Errorf("/auth view does not show provider status modal:\n%s", got)
	}
	if m.composer.Value() != "" {
		t.Errorf("composer value = %q, want reset after /auth", m.composer.Value())
	}
	assertNoAppOp(t, ops)
}

func TestTabCyclesAgentsWhenIdleWithoutCompletionOrModal(t *testing.T) {
	tests := []struct {
		name    string
		current string
		want    string
	}{
		{name: "advances to next agent", current: "build", want: "plan"},
		{name: "wraps to first agent", current: "plan", want: "build"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newAppTestModel([]string{"build", "plan"}, nil)
			m.agentName = tt.current

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(Model)
			runAppCmd(t, cmd)

			want := protocol.SelectAgent{Name: tt.want}
			if got := receiveAppOp(t, ops); got != want {
				t.Errorf("operation = %#v, want %#v", got, want)
			}
			if m.composer.Value() != "" {
				t.Errorf("tab changed composer to %q", m.composer.Value())
			}
			assertNoAppOp(t, ops)
		})
	}
}

func TestEscapeInterruptsRunningTurnExactlyOnceWithoutInputOwner(t *testing.T) {
	m, ops := newAppTestModel([]string{"build", "plan"}, nil)
	m.turnRunning = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	runAppCmd(t, cmd)

	if got := receiveAppOp(t, ops); got != (protocol.Interrupt{}) {
		t.Errorf("operation = %#v, want protocol.Interrupt", got)
	}
	assertNoAppOp(t, ops)
}

func TestOptionalHistoryIsBackwardCompatibleWhenOmitted(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.composer.SetValue("no history configured")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)

	if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: "no history configured"}) {
		t.Errorf("operation = %#v, want ordinary UserInput", got)
	}
	if m.composer.Value() != "" || m.historyPos != -1 {
		t.Errorf("submission did not reset composer state: value=%q historyPos=%d", m.composer.Value(), m.historyPos)
	}
}

func TestHistoryNavigationRecallsEntriesAndRestoresEmptyDraftAtNewestBoundary(t *testing.T) {
	store := newFakeHistory("oldest", "middle", "newest")
	m, _ := newAppTestModelWithHistory(nil, nil, store)

	for i, want := range []string{"newest", "middle", "oldest", "oldest"} {
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyUp})
		if got := m.composer.Value(); got != want {
			t.Fatalf("up %d value = %q, want %q", i+1, got, want)
		}
	}
	for i, want := range []string{"middle", "newest", ""} {
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown})
		if got := m.composer.Value(); got != want {
			t.Fatalf("down %d value = %q, want %q", i+1, got, want)
		}
	}
	if m.historyPos != -1 {
		t.Errorf("historyPos = %d after moving past newest, want browsing exited", m.historyPos)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.composer.Value(); got != "" {
		t.Errorf("down outside history changed empty draft to %q", got)
	}
}

func TestHistoryKeysOnNonemptyDraftRemainTextareaNavigation(t *testing.T) {
	store := newFakeHistory("must not replace draft")
	m, _ := newAppTestModelWithHistory(nil, nil, store)
	m.setComposerValueAt("first line\nsecond line", len([]rune("first line\nsecond line")))

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.composer.Value(); got != "first line\nsecond line" || m.composer.Line() != 0 {
		t.Errorf("up replaced draft or failed textarea navigation: value=%q line=%d", got, m.composer.Line())
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.composer.Value(); got != "first line\nsecond line" || m.composer.Line() != 1 {
		t.Errorf("down replaced draft or failed textarea navigation: value=%q line=%d", got, m.composer.Line())
	}
	if m.historyPos != -1 {
		t.Errorf("nonempty draft entered history browsing at position %d", m.historyPos)
	}
}

func TestHistoryRecallReflowsMultilineUnicodeAndEditingExitsBrowsing(t *testing.T) {
	prompt := "界界界界界界界界\nsecond 🙂 line"
	store := newFakeHistory(prompt)
	m, _ := newAppTestModelWithHistory(nil, nil, store)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 12, Height: 20})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyUp})

	if got := m.composer.Value(); got != prompt {
		t.Fatalf("recalled value = %q, want %q", got, prompt)
	}
	if m.composer.Height() <= composerMinHeight || m.composer.Line() != 1 {
		t.Errorf("recall did not safely reflow/place cursor: height=%d line=%d", m.composer.Height(), m.composer.Line())
	}
	_ = m.View()
	m = typeAppText(t, m, "!")
	if m.historyPos != -1 || !strings.HasSuffix(m.composer.Value(), "!") {
		t.Errorf("ordinary edit did not exit browsing: value=%q historyPos=%d", m.composer.Value(), m.historyPos)
	}
}

func TestSubmissionsPersistDisplayPromptAndStillEmitUserInput(t *testing.T) {
	tests := []struct {
		name        string
		skills      []host.Skill
		composer    string
		wantInput   string
		wantHistory string
	}{
		{name: "ordinary", composer: "  hello 界\nnext line  ", wantInput: "hello 界\nnext line", wantHistory: "hello 界\nnext line"},
		{name: "skill", skills: []host.Skill{fakeSkill("review", "", "Rendered: $ARGUMENTS")}, composer: "/review exact invocation", wantInput: "Rendered: exact invocation", wantHistory: "/review exact invocation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeHistory()
			m, ops := newAppTestModelWithHistory(nil, tt.skills, store)
			m.providerName = "echo"
			m.composer.SetValue(tt.composer)

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			for _, msg := range runAllAppCmds(t, cmd) {
				m = updateApp(t, m, msg)
			}
			if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: tt.wantInput}) {
				t.Errorf("operation = %#v, want UserInput %q", got, tt.wantInput)
			}
			if got := store.Entries(); !slices.Equal(got, []string{tt.wantHistory}) {
				t.Errorf("history = %q, want exact display prompt %q", got, tt.wantHistory)
			}
			if m.composer.Value() != "" || m.historyPos != -1 || m.historyDraft != "" {
				t.Errorf("submission did not reset history/composer state: value=%q pos=%d draft=%q", m.composer.Value(), m.historyPos, m.historyDraft)
			}
		})
	}
}

func TestRapidSubmissionsEnqueueHistoryInSubmissionOrderBeforeCommandCompletion(t *testing.T) {
	store := newFakeHistory()
	m, ops := newAppTestModelWithHistory(nil, nil, store)
	m.providerName = "echo"

	var batches []tea.BatchMsg
	for _, prompt := range []string{"first prompt", "second prompt"} {
		m.composer.SetValue(prompt)
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		msg := runAppCmd(t, cmd)
		batch, ok := msg.(tea.BatchMsg)
		if !ok || len(batch) != 2 {
			t.Fatalf("submission command = %T with %#v, want send and persistence batch", msg, msg)
		}
		batches = append(batches, batch)
	}

	// Engine sends do not wait for either persistence completion.
	for i, batch := range batches {
		if msg := runAppCmd(t, batch[0]); msg != nil {
			t.Errorf("engine send %d returned unexpected message %#v", i, msg)
		}
	}
	for i, want := range []string{"first prompt", "second prompt"} {
		if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: want}) {
			t.Errorf("engine operation %d = %#v, want UserInput %q", i, got, want)
		}
	}

	// Await persistence in the opposite order from submission. Acceptance order
	// must still determine durable and in-memory history order.
	for i := len(batches) - 1; i >= 0; i-- {
		msg := runAppCmd(t, batches[i][1])
		if added, ok := msg.(historyAddedMsg); !ok || added.err != nil {
			t.Fatalf("persistence completion %d = %#v, want successful historyAddedMsg", i, msg)
		}
	}
	if got, want := store.Entries(), []string{"first prompt", "second prompt"}; !slices.Equal(got, want) {
		t.Errorf("history = %q, want submission order %q", got, want)
	}
}

func TestHistoryFailureShowsNoticeWithoutSuppressingSubmission(t *testing.T) {
	store := newFakeHistory()
	store.fail = true
	m, ops := newAppTestModelWithHistory(nil, nil, store)
	m.providerName = "echo"
	m.composer.SetValue("send despite persistence failure")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	for _, msg := range runAllAppCmds(t, cmd) {
		m = updateApp(t, m, msg)
	}
	if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: "send despite persistence failure"}) {
		t.Errorf("operation = %#v, want submission despite history failure", got)
	}
	if !m.noticeErr || !strings.Contains(m.notice, "saving prompt history failed") {
		t.Errorf("history failure notice = %q (error=%v)", m.notice, m.noticeErr)
	}
}

func TestSubmittingRecalledHistoryResetsBrowsingState(t *testing.T) {
	store := newFakeHistory("recalled prompt")
	m, ops := newAppTestModelWithHistory(nil, nil, store)
	m.providerName = "echo"
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.historyPos < 0 {
		t.Fatal("up did not enter history browsing")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	for _, msg := range runAllAppCmds(t, cmd) {
		m = updateApp(t, m, msg)
	}
	if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: "recalled prompt"}) {
		t.Errorf("operation = %#v, want recalled prompt submission", got)
	}
	if m.historyPos != -1 || m.historyDraft != "" || m.composer.Value() != "" {
		t.Errorf("recalled submission retained browsing state: pos=%d draft=%q value=%q", m.historyPos, m.historyDraft, m.composer.Value())
	}
}

func TestControlKPreservesComposerClosesCompletionAndOpensPalette(t *testing.T) {
	m, ops := newAppTestModel([]string{"build"}, nil)
	m.providerName = "echo"
	m.setComposerValueAt("keep this suffix", len([]rune("keep")))
	m.completion = leadingSlashCompletion("/", 0, 1, m.commands)

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if got := m.composer.Value(); got != "keep this suffix" {
		t.Errorf("ctrl+k applied textarea kill-to-end: composer=%q", got)
	}
	if m.completion != nil {
		t.Error("ctrl+k left inline completion open")
	}
	if _, ok := m.modal.(*paletteModal); !ok {
		t.Fatalf("ctrl+k modal = %T, want command palette", m.modal)
	}
	assertNoAppOp(t, ops)
}

func TestActiveModalOwnsControlK(t *testing.T) {
	t.Run("other modal", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		probe := &appProbeModal{}
		m.modal = probe
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
		if probe.keys != 1 || m.modal != probe {
			t.Errorf("ctrl+k did not remain with active modal: keys=%d modal=%T", probe.keys, m.modal)
		}
	})
	t.Run("permission modal", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		permission := newPermissionModal(protocol.PermissionAsked{RequestID: "req", Permission: "bash"}, ops)
		m.modal = permission
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
		if m.modal != permission {
			t.Errorf("ctrl+k replaced permission modal with %T", m.modal)
		}
		assertNoAppOp(t, ops)
	})
}

func TestPermissionResolvedOnlyClosesMatchingPermissionModal(t *testing.T) {
	t.Run("unrelated resolution leaves palette open", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		palette := newPaletteModal(m.commands, nil, paletteAvailability{})
		m.modal = palette

		m.applyEvent(protocol.PermissionResolved{RequestID: "req-1"})

		if m.modal != palette {
			t.Fatalf("unrelated permission resolution changed palette to %T", m.modal)
		}
	})

	t.Run("only matching permission request closes", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		permission := newPermissionModal(protocol.PermissionAsked{RequestID: "req-2", Permission: "bash"}, ops)
		m.modal = permission

		m.applyEvent(protocol.PermissionResolved{RequestID: "req-1"})
		if m.modal != permission {
			t.Fatalf("req-1 resolution changed req-2 permission modal to %T", m.modal)
		}
		m.applyEvent(protocol.PermissionResolved{RequestID: "req-2"})
		if m.modal != nil {
			t.Fatalf("req-2 resolution left matching modal open as %T", m.modal)
		}
	})
}

func TestPaletteInvokeUsesExistingCommandBehavior(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		ops := make(chan protocol.Op, 8)
		m := New(ops, make(chan protocol.Event), testServices(nil, nil))
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		m = updateApp(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}})
		if view := m.View(); !strings.Contains(view, "Select provider") {
			t.Errorf("provider palette action did not open picker:\n%s", view)
		}
		assertNoAppOp(t, ops)
	})
	t.Run("model", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		m.providerName = "echo"
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		updated, cmd := m.Update(paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/model"}})
		m = updated.(Model)
		if cmd == nil || !strings.Contains(m.View(), "Select model") || !strings.Contains(m.View(), "echo") {
			t.Errorf("model palette action did not reuse model picker behavior")
		}
		assertNoAppOp(t, ops)
	})
	t.Run("auth", func(t *testing.T) {
		ops := make(chan protocol.Op, 8)
		m := New(ops, make(chan protocol.Event), testServices(nil, nil))
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		m = updateApp(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/auth"}})
		if !strings.Contains(m.View(), "Select provider") {
			t.Error("auth palette action did not open auth provider status")
		}
		assertNoAppOp(t, ops)
	})
	t.Run("help", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		m = updateApp(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/help"}})
		if !strings.Contains(m.notice, "commands:") {
			t.Errorf("help palette action notice = %q", m.notice)
		}
		assertNoAppOp(t, ops)
	})
	t.Run("agent", func(t *testing.T) {
		m, ops := newAppTestModel([]string{"build", "code reviewer"}, nil)
		updated, cmd := m.Update(paletteInvokeMsg{Action: paletteAction{Kind: paletteActionAgent, Value: "code reviewer"}})
		m = updated.(Model)
		runAppCmd(t, cmd)
		if got := receiveAppOp(t, ops); got != (protocol.SelectAgent{Name: "code reviewer"}) {
			t.Errorf("agent operation = %#v", got)
		}
	})
}

func TestPaletteInsertOnlyFocusesComposerWithoutSubmissionOrHistoryWrite(t *testing.T) {
	store := newFakeHistory("existing")
	m, ops := newAppTestModelWithHistory(nil, []host.Skill{fakeSkill("review", "", "$ARGUMENTS")}, store)
	m.providerName = "echo"
	m.composer.Blur()
	m.historyPos = 0
	m.historyDraft = "draft"

	updated, cmd := m.Update(paletteInvokeMsg{Action: paletteAction{Kind: paletteActionSkill, Value: "review"}})
	m = updated.(Model)
	runAppCmd(t, cmd)
	if m.composer.Value() != "/review " || !m.composer.Focused() {
		t.Errorf("insert-only composer value/focus = %q/%v", m.composer.Value(), m.composer.Focused())
	}
	if m.historyPos != -1 || m.historyDraft != "" {
		t.Errorf("insert-only did not exit history browsing: pos=%d draft=%q", m.historyPos, m.historyDraft)
	}
	if got := store.Entries(); !slices.Equal(got, []string{"existing"}) {
		t.Errorf("insert-only wrote history: %q", got)
	}
	assertNoAppOp(t, ops)
}

func TestControlKPaletteAvailabilityTracksProviderAndTurn(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		turn       bool
		command    string
		wantReason string
	}{
		{name: "model needs provider", command: "/model", wantReason: "select a provider first"},
		{name: "turn disables provider", provider: "echo", turn: true, command: "/provider", wantReason: "unavailable while a turn is running"},
		{name: "idle provider enables model", provider: "echo", command: "/model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.providerName, m.turnRunning = tt.provider, tt.turn
			m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
			palette := m.modal.(*paletteModal)
			for _, entry := range palette.entries {
				if entry.Label == tt.command {
					if entry.DisabledReason != tt.wantReason {
						t.Errorf("%s disabled reason = %q, want %q", tt.command, entry.DisabledReason, tt.wantReason)
					}
					return
				}
			}
			t.Fatalf("palette omitted %s", tt.command)
		})
	}
}

func TestOpenPaletteRefreshesWhenTurnStartsAndKeepsHelpAvailable(t *testing.T) {
	m, ops := newAppTestModel([]string{"build"}, []host.Skill{fakeSkill("review", "review code", "")})
	m.providerName = "echo"
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	palette := m.modal.(*paletteModal)

	m.applyEvent(protocol.TurnStarted{})
	if m.modal != palette {
		t.Fatalf("turn start replaced open palette with %T", m.modal)
	}

	for _, label := range []string{"/provider", "/model", "/auth", "/agent build", "/review"} {
		copy := *palette
		assertPaletteDisabled(t, &copy, label, "unavailable while a turn is running")
	}
	help := *palette
	assertPaletteInvoke(t, &help, "/help", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/help"}})
	assertNoAppOp(t, ops)
}

func TestOpenPaletteReenablesRestrictedEntriesWhenTurnCompletes(t *testing.T) {
	m, ops := newAppTestModel([]string{"build"}, []host.Skill{fakeSkill("review", "review code", "")})
	m.providerName = "echo"
	m.turnRunning = true
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	palette := m.modal.(*paletteModal)

	m.applyEvent(protocol.TurnCompleted{})
	if m.modal != palette {
		t.Fatalf("turn completion replaced open palette with %T", m.modal)
	}
	for _, entry := range palette.entries {
		if entry.Label == "/help" {
			continue
		}
		copy := *palette
		assertPaletteInvoke(t, &copy, entry.Label, paletteInvokeMsg{Action: entry.Action})
	}
	assertNoAppOp(t, ops)
}

func TestOpenPaletteRefreshesProviderDependentEntriesAfterModelSelected(t *testing.T) {
	m, ops := newAppTestModel(nil, []host.Skill{fakeSkill("review", "review code", "")})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	palette := m.modal.(*paletteModal)
	for _, label := range []string{"/model", "/review"} {
		copy := *palette
		assertPaletteDisabled(t, &copy, label, "select a provider first")
	}

	m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "test-model"})
	if m.modal != palette {
		t.Fatalf("model selection replaced open palette with %T", m.modal)
	}
	for _, label := range []string{"/model", "/review"} {
		copy := *palette
		var want paletteInvokeMsg
		if label == "/model" {
			want.Action = paletteAction{Kind: paletteActionBuiltin, Value: "/model"}
		} else {
			want.Action = paletteAction{Kind: paletteActionSkill, Value: "review"}
		}
		assertPaletteInvoke(t, &copy, label, want)
	}
	assertNoAppOp(t, ops)
}

func TestConstructedRestrictedPaletteInvokeIsRejectedAgainstCurrentAvailability(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		turn       bool
		action     paletteAction
		wantNotice string
	}{
		{
			name:       "agent while turn is running",
			provider:   "echo",
			turn:       true,
			action:     paletteAction{Kind: paletteActionAgent, Value: "build"},
			wantNotice: "unavailable while a turn is running",
		},
		{
			name:       "model without provider",
			action:     paletteAction{Kind: paletteActionBuiltin, Value: "/model"},
			wantNotice: "select a provider first",
		},
		{
			name:       "skill without provider",
			action:     paletteAction{Kind: paletteActionSkill, Value: "review"},
			wantNotice: "select a provider first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newAppTestModel([]string{"build"}, []host.Skill{fakeSkill("review", "review code", "")})
			m.providerName, m.turnRunning = tt.provider, tt.turn
			m.composer.SetValue("unchanged draft")
			m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
			palette := m.modal
			focused := m.composer.Focused()

			updated, cmd := m.Update(paletteInvokeMsg{Action: tt.action})
			m = updated.(Model)
			if cmd != nil {
				t.Fatal("restricted constructed invoke returned a command")
			}
			if m.modal != palette {
				t.Errorf("restricted constructed invoke changed modal from %T to %T", palette, m.modal)
			}
			if m.composer.Value() != "unchanged draft" || m.composer.Focused() != focused {
				t.Errorf("restricted constructed invoke changed composer to %q/focused=%v", m.composer.Value(), m.composer.Focused())
			}
			if m.notice != tt.wantNotice || !m.noticeErr {
				t.Errorf("restricted constructed invoke notice = %q/error=%v, want %q/error=true", m.notice, m.noticeErr, tt.wantNotice)
			}
			assertNoAppOp(t, ops)
		})
	}
}

type appProbeModal struct {
	keys int
}

func (m *appProbeModal) update(tea.KeyMsg) (modal, tea.Cmd) {
	m.keys++
	return m, nil
}

func (m *appProbeModal) view(int, theme.Theme) string { return "probe" }

func assertViewContainsPlainText(t *testing.T, view, want string) {
	t.Helper()
	if plain := ansi.Strip(view); !strings.Contains(plain, want) {
		t.Errorf("view does not contain %q after stripping ANSI:\n%s", want, plain)
	}
}

func assertViewOmitsPlainText(t *testing.T, view, unwanted string) {
	t.Helper()
	if plain := ansi.Strip(view); strings.Contains(plain, unwanted) {
		t.Errorf("view unexpectedly contains %q after stripping ANSI:\n%s", unwanted, plain)
	}
}

func compactAppPlainText(text string) string {
	return strings.NewReplacer(
		" ", "",
		"\t", "",
		"\n", "",
		"│", "",
		"╭", "",
		"╮", "",
		"╰", "",
		"╯", "",
		"─", "",
	).Replace(text)
}

func updateApp(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	return updated.(Model)
}

func typeAppText(t *testing.T, m Model, text string) Model {
	t.Helper()
	return updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

func runAppCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(appCmdTimeout):
		t.Fatalf("tea command did not complete within %s", appCmdTimeout)
		return nil
	}
}

func runAllAppCmds(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := runAppCmd(t, cmd)
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var messages []tea.Msg
	for _, nested := range batch {
		messages = append(messages, runAllAppCmds(t, nested)...)
	}
	return messages
}

func receiveAppOp(t *testing.T, ops <-chan protocol.Op) protocol.Op {
	t.Helper()
	select {
	case op := <-ops:
		return op
	case <-time.After(appCmdTimeout):
		t.Fatalf("operation was not emitted within %s", appCmdTimeout)
		return nil
	}
}

func assertNoAppOp(t *testing.T, ops <-chan protocol.Op) {
	t.Helper()
	select {
	case op := <-ops:
		t.Fatalf("unexpected engine operation: %#v", op)
	default:
	}
}

func TestHeaderAgentBadgeGuardsDisplaySafety(t *testing.T) {
	// Agents are not host-filtered; every render site must gate the name.
	m, _ := newAppTestModel([]string{"build"}, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	m.applyEvent(protocol.AgentSelected{Name: "evil\x1b[2Jagent"})
	if view := m.View(); strings.Contains(view, "\x1b[2J") {
		t.Fatalf("header rendered raw control sequence from agent name:\n%q", view)
	}

	m.applyEvent(protocol.AgentSelected{Name: "build"})
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "build") {
		t.Errorf("header dropped a valid agent name:\n%s", plain)
	}
}
