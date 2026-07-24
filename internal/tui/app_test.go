package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
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
			skills := []config.Skill{{Name: "部署", Description: "deploy", Template: "deploy"}}
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
	if m.viewport.Width != 80 || m.viewport.Height < 0 {
		t.Errorf("resize did not restore effective viewport dimensions: %dx%d", m.viewport.Width, m.viewport.Height)
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
		skill := config.Skill{Name: "review", Description: "review code", Template: "Review: $ARGUMENTS"}
		m, ops := newAppTestModel(nil, []config.Skill{skill})
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

	t.Run("agent selects named agent", func(t *testing.T) {
		m, ops := newAppTestModel([]string{"build", "plan"}, nil)
		m.composer.SetValue("/agent plan")

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		runAppCmd(t, cmd)

		want := protocol.SelectAgent{Name: "plan"}
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
	m := New(ops, events, &auth.Store{}, nil, nil)
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

type appProbeModal struct {
	keys int
}

func (m *appProbeModal) update(tea.KeyMsg) (modal, tea.Cmd) {
	m.keys++
	return m, nil
}

func (m *appProbeModal) view(int, theme.Theme) string { return "probe" }

func newAppTestModel(agents []string, skills []config.Skill) (Model, chan protocol.Op) {
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event)
	return New(ops, events, nil, agents, skills), ops
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
