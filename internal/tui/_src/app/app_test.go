package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const appCmdTimeout = 2 * time.Second

func rowsContaining(view, text string) []string {
	var rows []string
	for _, row := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(row), text) {
			rows = append(rows, row)
		}
	}
	return rows
}

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

func TestCompletionDelimitersRespectCommandSourceAndExistingWhitespace(t *testing.T) {
	themes := []struct {
		name string
		th   theme.Theme
	}{
		{name: "default", th: theme.Default()},
		{name: "zero XS", th: func() theme.Theme {
			th := theme.Default()
			th.Spacing = theme.NewSpacing(0, 2, 3, 4)
			return th
		}()},
		{name: "wide XS", th: func() theme.Theme {
			th := theme.Default()
			th.Spacing = theme.NewSpacing(3, 2, 3, 4)
			return th
		}()},
	}
	skills := []host.Skill{
		fakeSkill("review", "", "Review $ARGUMENTS"),
		fakeSkill("explain", "", "Explain this"),
	}
	tests := make([]struct {
		name   string
		value  string
		offset int
		skills []host.Skill
		th     theme.Theme
		want   string
	}, 0, len(builtinCommandSpecs)+len(themes)*2+4)
	for _, spec := range builtinCommandSpecs {
		tests = append(tests, struct {
			name   string
			value  string
			offset int
			skills []host.Skill
			th     theme.Theme
			want   string
		}{
			name:  "builtin " + spec.Name,
			value: spec.Name,
			want:  spec.Name,
		})
	}
	for _, themeCase := range themes {
		for _, skillName := range []string{"review", "explain"} {
			tests = append(tests, struct {
				name   string
				value  string
				offset int
				skills []host.Skill
				th     theme.Theme
				want   string
			}{
				name:   themeCase.name + " skill " + skillName,
				value:  "/" + skillName,
				skills: skills,
				th:     themeCase.th,
				want:   "/" + skillName + " ",
			})
		}
	}
	for _, value := range []string{"/provider existing", "/fast  existing", "/review existing", "/explain  existing"} {
		tests = append(tests, struct {
			name   string
			value  string
			offset int
			skills []host.Skill
			th     theme.Theme
			want   string
		}{
			name:   "existing whitespace " + value,
			value:  value,
			offset: strings.Index(value, " "),
			skills: skills,
			want:   value,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, tt.skills)
			m.th = tt.th
			offset := tt.offset
			if offset == 0 {
				offset = len([]rune(tt.value))
			}
			m.setComposerValueAt(tt.value, offset)
			m.recomputeCompletion()
			if m.completion == nil {
				t.Fatal("completion did not open")
			}
			m.applyCompletion()
			if got := m.composer.Value(); got != tt.want {
				t.Errorf("completion value = %q, want %q", got, tt.want)
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
		if m.composer.Value() != "" {
			t.Errorf("second enter did not clear composer: value=%q", m.composer.Value())
		}
		if _, ok := m.modal.(*helpModal); !ok {
			t.Errorf("second enter modal = %T, want helpModal", m.modal)
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

func TestModalVisuallyUnfocusesComposerAndSuppressesCompletionUntilClosed(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Surface = fixedColor("#112233")
	th.SurfaceFocus = fixedColor("#445566")
	th.SurfaceMuted = fixedColor("#778899")
	th.OverlayScrim = fixedColor("#99aabb")
	m, ops := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setComposerValueAt("/fa", 3)
	m.recomputeCompletion()
	m.reflow()
	if m.completion == nil || m.completion.rows == 0 {
		t.Fatal("test setup did not create a visible completion popup")
	}
	draft, line := m.composer.Value(), m.composer.Line()
	probe := &appProbeModal{}
	m.modal = probe
	m.reflow()

	withModal := m.View()
	if !m.composer.Focused() {
		t.Fatal("modal changed the underlying composer's focus state")
	}
	if m.composer.Value() != draft || m.composer.Line() != line {
		t.Errorf("modal changed composer draft/cursor line: value=%q line=%d", m.composer.Value(), m.composer.Line())
	}
	if m.completion == nil {
		t.Fatal("modal discarded completion state")
	}
	if m.completionPopupHeight() != 0 {
		t.Errorf("modal reserved completion height %d, want 0", m.completionPopupHeight())
	}
	if !strings.Contains(withModal, rgbSGR("#99aabb")) {
		t.Errorf("modal did not scrim background with OverlayScrim:\n%s", withModal)
	}
	if strings.Contains(withModal, rgbBGSGR("#445566")) {
		t.Errorf("modal left focused surface color in scrimmed background:\n%s", withModal)
	}
	if hasReverseVideo(withModal) {
		t.Errorf("modal view rendered the composer's reverse-video cursor: %q", withModal)
	}
	if strings.Contains(ansi.Strip(withModal), "/fast") {
		t.Errorf("modal view rendered the suppressed completion popup:\n%s", ansi.Strip(withModal))
	}

	m.modal = nil
	m.reflow()
	afterClose := m.View()
	if strings.Contains(afterClose, rgbSGR("#99aabb")) {
		t.Errorf("closed modal left OverlayScrim on the frame:\n%s", afterClose)
	}
	// Focus chrome is title SurfaceFocus + body Surface (no full-panel wash).
	if !strings.Contains(afterClose, rgbBGSGR("#445566")) {
		t.Errorf("closed modal did not restore focused title SurfaceFocus:\n%s", afterClose)
	}
	if !strings.Contains(afterClose, rgbBGSGR("#112233")) {
		t.Errorf("closed modal did not restore body Surface:\n%s", afterClose)
	}
	if !hasReverseVideo(afterClose) {
		t.Errorf("closed modal did not restore static composer cursor: %q", afterClose)
	}
	if m.completionPopupHeight() == 0 || !strings.Contains(ansi.Strip(afterClose), "/fast") {
		t.Errorf("closed modal did not restore completion popup: height=%d\n%s", m.completionPopupHeight(), ansi.Strip(afterClose))
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.composer.Value() != "/fast" {
		t.Errorf("modal changed composer cursor; completion produced %q, want /fast", m.composer.Value())
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

func TestExitAndQuitSlashCommandsQuitLikeCtrlC(t *testing.T) {
	for _, name := range []string{"/exit", "/quit"} {
		t.Run(name, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.composer.SetValue(name)
			m.turnRunning = true // quit must work mid-turn, like ctrl+c
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			if _, ok := runAppCmd(t, cmd).(tea.QuitMsg); !ok {
				t.Fatalf("%s did not return a quit command", name)
			}
			if m.PendingUpgrade() || m.PendingResume() != "" {
				t.Errorf("%s set upgrade/resume side effects", name)
			}
			assertNoAppOp(t, ops)
		})
	}
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

	// Shift+Enter CSI → WrapInput → KeyEnter+Alt → newline; no send, no pane cycle.
	t.Run("shift enter CSI via WrapInput inserts newline without sending", func(t *testing.T) {
		for _, wire := range []string{"\x1b[13;2u", "\x1b[27;2;13~"} {
			msg := keyMsgFromWrapInput(t, wire)
			m, ops := newAppTestModel(nil, nil)
			m.providerName = "echo"
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
			m.windows = windowRegistry{windows: []window{
				statefulTestWindow{windowID: "a", windowTitle: "A"},
				statefulTestWindow{windowID: "b", windowTitle: "B"},
			}}
			startWin := m.windows.index
			m = typeAppText(t, m, "first")
			m = updateApp(t, m, msg)
			m = typeAppText(t, m, "second")
			if got := m.composer.Value(); got != "first\nsecond" {
				t.Errorf("wire %q composer = %q, want first\\nsecond", wire, got)
			}
			if m.windows.index != startWin {
				t.Errorf("wire %q cycled window %d → %d", wire, startWin, m.windows.index)
			}
			assertNoAppOp(t, ops)
		}
	})

	// Plain KeyEnter always sends. Shift+Enter is only distinguishable after
	// the input normalizer rewrites terminal CSI to Alt+Enter; without that
	// rewrite Bubble Tea delivers an ordinary KeyEnter, which correctly sends.
	for _, name := range []string{"ordinary enter", "plain KeyEnter still sends without normalizer"} {
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
		{name: "sixteen unbroken ASCII characters", value: strings.Repeat("x", 16), wantHeight: 0, wantCursorLine: -1},
		{name: "eight wide runes", value: strings.Repeat("界", 8), wantHeight: 0, wantCursorLine: -1},
		{name: "eight combining graphemes", value: strings.Repeat("e\u0301", 8), wantHeight: 2, wantCursorLine: -1},
		{name: "blank logical line", value: "one\n\nthree", wantHeight: 3, wantCursorLine: -1},
		{name: "exact-boundary tall line before short cursor line", value: strings.Repeat("x", 16) + "\ny", wantHeight: 0, wantCursorLine: 1},
		{name: "short line before exact-boundary tall line", value: "y\n" + strings.Repeat("x", 16), wantHeight: 0, wantCursorLine: -1},
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

			want := tt.wantHeight
			if want == 0 {
				geometry := computePaneGeometry(m.width, m.th.Spacing.XS, m.focus)
				candidate := geometry.leftCandidateWidth(m.width)
				wantContentWidth := candidate - ansi.StringWidth(m.composer.Prompt)
				if m.composer.Width() != wantContentWidth {
					t.Fatalf("compact composer content width = %d, want textarea content width %d from allocated left candidate %d", m.composer.Width(), wantContentWidth, candidate)
				}
				counter := textarea.New()
				counter.Prompt = ""
				counter.ShowLineNumbers = false
				counter.SetWidth(m.composer.Width())
				for _, line := range strings.Split(tt.value, "\n") {
					counter.SetValue(line)
					want += max(1, counter.LineInfo().Height)
				}
				want = min(composerMaxHeight, max(composerMinHeight, want))
			}
			if got := m.composer.Height(); got != want {
				t.Errorf("composer height = %d, want %d using textarea semantics at allocated left width %d", got, want, m.composer.Width())
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
	if strings.Contains(ansi.Strip(m.View()), "model: echo/test-model") {
		t.Errorf("model selection unexpectedly rendered a routine notice:\n%s", ansi.Strip(m.View()))
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
		if size.Width == 0 && size.Height == 0 {
			if plain != "" {
				t.Errorf("danger indicator at size %dx%d = %q, want empty output", size.Width, size.Height, plain)
			}
			continue
		}
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
				picker.all = []host.ModelInfo{{ID: "echo-regression-model"}}
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
			content: []string{"Command palette", "Keyboard shortcuts", "/provider", "/settings"},
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
	// Wide/tall enough that the palette overlay does not clip the notice row
	// and the permission-mode badge still leaves room for the working status.
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 28})
	m.modal = newPaletteModal(m.commands, nil, m.currentPaletteAvailability())

	m.applyEvent(protocol.TurnStarted{})
	m.setNotice("unrelated notice", true)
	m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "notice-regression-model"})

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		indicator,
		"Command palette",
		"working",
		"unrelated notice",
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
		assertUserInputText(t, op, "Review: this diff")
	})
}

func TestContextSlashCommandInspectsEffectivePrompt(t *testing.T) {
	for _, cmdName := range []string{"/context", "/effective-prompt"} {
		t.Run(cmdName, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.composer.SetValue(cmdName)
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			runAppCmd(t, cmd)
			if got := receiveAppOp(t, ops); got != (protocol.InspectEffectivePrompt{}) {
				t.Errorf("operation = %#v, want InspectEffectivePrompt", got)
			}
			if !m.pendingContextDoctor {
				t.Error("pendingContextDoctor = false, want true after /context")
			}
			if m.composer.Value() != "" {
				t.Errorf("composer value = %q, want reset", m.composer.Value())
			}
			assertNoAppOp(t, ops)
		})
	}

	// Pending doctor opens a modal (not a transcript dump).
	m, _ := newAppTestModel(nil, nil)
	m.pendingContextDoctor = true
	m.contextLimit = 100_000
	m.contextLimitKnown = true
	m = updateApp(t, m, engineEventMsg{ev: protocol.EffectivePrompt{
		Layers: []protocol.PromptLayerInfo{
			{Kind: protocol.PromptLayerShared, Source: "builtin:shared", Mode: protocol.PromptLayerAppend, Chars: 100, Preview: "You are strike"},
			{Kind: protocol.PromptLayerPersona, Source: "agent:build", Mode: protocol.PromptLayerReplace, Chars: 40},
			{Kind: protocol.PromptLayerMemory, Source: "memory:prefs", Mode: protocol.PromptLayerAppend, Chars: 20, Preview: "api_key=sk-ant-secretvaluehere"},
		},
		SystemChars:    200,
		MessageCount:   3,
		FromLastStream: true,
	}})
	if m.pendingContextDoctor {
		t.Error("pendingContextDoctor still set after EffectivePrompt")
	}
	doc, ok := m.modal.(*doctorModal)
	if !ok {
		t.Fatalf("modal type = %T, want *doctorModal", m.modal)
	}
	plain := ansi.Strip(doc.view(60, theme.Default()))
	if !strings.Contains(plain, "shared") || !strings.Contains(plain, "persona") {
		t.Errorf("doctor missing layer kinds:\n%s", plain)
	}
	if !strings.Contains(plain, "200 chars") {
		t.Errorf("doctor missing system chars:\n%s", plain)
	}
	if !strings.Contains(plain, "3 msgs") {
		t.Errorf("doctor missing history count:\n%s", plain)
	}
	// Unsolicited EffectivePrompt still dumps to the transcript.
	m2, _ := newAppTestModel(nil, nil)
	m2 = updateApp(t, m2, engineEventMsg{ev: protocol.EffectivePrompt{
		Layers:      []protocol.PromptLayerInfo{{Kind: protocol.PromptLayerShared, Source: "builtin:shared", Mode: protocol.PromptLayerAppend, Chars: 10}},
		SystemChars: 10,
	}})
	if m2.modal != nil {
		t.Fatal("unsolicited EffectivePrompt opened a modal")
	}
	found := false
	for _, c := range m2.cells {
		if info, ok := c.(*infoCell); ok && strings.Contains(info.text, "effective prompt") {
			found = true
		}
	}
	if !found {
		t.Fatal("unsolicited EffectivePrompt did not append info cell")
	}
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
	if !strings.Contains(m.notice, "interrupt") {
		t.Errorf("notice = %q, want interrupting feedback", m.notice)
	}
	assertNoAppOp(t, ops)
}

func TestEscapeInterruptsImmediatelyAfterSubmitBeforeTurnStarted(t *testing.T) {
	// Optimistic turnRunning on dispatch closes the gap where esc was a no-op.
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.composer.SetValue("go")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	if !m.turnRunning {
		t.Fatal("turnRunning false after submit; esc would miss the turn")
	}
	if got := receiveAppOp(t, ops); got.(protocol.UserInput).Text != "go" {
		t.Fatalf("submit op = %#v", got)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	runAppCmd(t, cmd)
	if got := receiveAppOp(t, ops); got != (protocol.Interrupt{}) {
		t.Errorf("esc after submit = %#v, want Interrupt", got)
	}
	assertNoAppOp(t, ops)
}

func TestEscapeInterruptsDespiteArmedLeader(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.turnRunning = true
	m.leaderArmed = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	runAppCmd(t, cmd)

	if m.leaderArmed {
		t.Error("leader still armed after interrupt esc")
	}
	if got := receiveAppOp(t, ops); got != (protocol.Interrupt{}) {
		t.Errorf("operation = %#v, want Interrupt (leader must not swallow esc)", got)
	}
	assertNoAppOp(t, ops)
}

func TestMatchesInterruptAcceptsKeyEscAndBinding(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	if !m.matchesInterrupt(tea.KeyMsg{Type: tea.KeyEsc}) {
		t.Error("matchesInterrupt(KeyEsc) = false")
	}
	if m.matchesInterrupt(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Error("matchesInterrupt(Enter) = true")
	}
	// Remap interrupt away from esc: bare esc must not match.
	m.keyMap.Interrupt = key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "interrupt"))
	if m.matchesInterrupt(tea.KeyMsg{Type: tea.KeyEsc}) {
		t.Error("matchesInterrupt(KeyEsc) = true after remap away from esc")
	}
	if !m.matchesInterrupt(tea.KeyMsg{Type: tea.KeyCtrlX}) {
		t.Error("matchesInterrupt(ctrl+x) = false after remap")
	}
}

func TestTurnCompletedInterruptedSetsNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.turnRunning = true
	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "interrupted"})
	if m.turnRunning {
		t.Fatal("turnRunning still true")
	}
	if !strings.Contains(m.notice, "interrupted") {
		t.Errorf("notice = %q, want interrupted", m.notice)
	}
}

func TestOptionalHistoryIsBackwardCompatibleWhenOmitted(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.composer.SetValue("no history configured")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)

	assertUserInputText(t, receiveAppOp(t, ops), "no history configured")
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

func TestSubmitDuringRunningTurnEnqueuesInsteadOfRejecting(t *testing.T) {
	tests := []struct {
		name        string
		skills      []host.Skill
		composer    string
		wantModel   string
		wantHistory string
	}{
		{name: "ordinary", composer: "draft while busy", wantModel: "draft while busy", wantHistory: "draft while busy"},
		{name: "skill", skills: []host.Skill{fakeSkill("review", "", "Rendered: $ARGUMENTS")}, composer: "/review keep me", wantModel: "Rendered: keep me", wantHistory: "/review keep me"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeHistory()
			m, ops := newAppTestModelWithHistory(nil, tt.skills, store)
			m.providerName = "echo"
			m.turnRunning = true
			m.composer.SetValue(tt.composer)

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			if cmd != nil {
				for _, msg := range runAllAppCmds(t, cmd) {
					m = updateApp(t, m, msg)
				}
			}
			assertNoAppOp(t, ops)
			if got := m.composer.Value(); got != "" {
				t.Errorf("composer = %q, want cleared after enqueue", got)
			}
			if len(m.inputQueue) != 1 || m.inputQueue[0].modelText != tt.wantModel {
				t.Errorf("inputQueue = %#v, want one item %q", m.inputQueue, tt.wantModel)
			}
			if got := store.Entries(); !slices.Equal(got, []string{tt.wantHistory}) {
				t.Errorf("history = %q, want %q", got, []string{tt.wantHistory})
			}
			if m.noticeErr || !strings.Contains(m.notice, "queued (1)") {
				t.Errorf("notice = %q (error=%v), want queued notice", m.notice, m.noticeErr)
			}
		})
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
			assertUserInputText(t, receiveAppOp(t, ops), tt.wantInput)
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
	// First enter dispatches immediately (optimistic turnRunning). A second enter
	// while busy goes to the TUI input queue + history; both history writes are
	// ordered by submission time even if completions arrive out of order.
	store := newFakeHistory()
	m, ops := newAppTestModelWithHistory(nil, nil, store)
	m.providerName = "echo"

	m.composer.SetValue("first prompt")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	firstMsg := runAppCmd(t, cmd)
	firstBatch, ok := firstMsg.(tea.BatchMsg)
	if !ok || len(firstBatch) != 2 {
		t.Fatalf("first submission = %T %#v, want send+history batch", firstMsg, firstMsg)
	}
	if !m.turnRunning {
		t.Fatal("first submit did not set optimistic turnRunning")
	}

	m.composer.SetValue("second prompt")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if len(m.inputQueue) != 1 || m.inputQueue[0].modelText != "second prompt" {
		t.Fatalf("second submit queue = %#v, want one queued prompt", m.inputQueue)
	}
	secondPersist := cmd
	if secondPersist == nil {
		t.Fatal("second submit produced no history cmd")
	}

	if msg := runAppCmd(t, firstBatch[0]); msg != nil {
		t.Errorf("engine send returned unexpected message %#v", msg)
	}
	assertUserInputText(t, receiveAppOp(t, ops), "first prompt")
	assertNoAppOp(t, ops)

	// Await persistence in reverse completion order; durable order stays submission order.
	if msg := runAppCmd(t, secondPersist); msg != nil {
		if added, ok := msg.(historyAddedMsg); !ok || added.err != nil {
			// enqueue returns a single persist cmd (not a batch).
			t.Fatalf("second persistence = %#v", msg)
		}
	}
	if msg := runAppCmd(t, firstBatch[1]); msg != nil {
		if added, ok := msg.(historyAddedMsg); !ok || added.err != nil {
			t.Fatalf("first persistence = %#v", msg)
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
	assertUserInputText(t, receiveAppOp(t, ops), "send despite persistence failure")
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
	assertUserInputText(t, receiveAppOp(t, ops), "recalled prompt")
	if m.historyPos != -1 || m.historyDraft != "" || m.composer.Value() != "" {
		t.Errorf("recalled submission retained browsing state: pos=%d draft=%q value=%q", m.historyPos, m.historyDraft, m.composer.Value())
	}
}

func TestControlKPreservesComposerClosesCompletionAndOpensPalette(t *testing.T) {
	m, ops := newAppTestModel([]string{"build"}, nil)
	m.providerName = "echo"
	// EOL so kill-to-end does not claim; ctrl+k opens palette (#414).
	m.setComposerValueAt("keep this suffix", len([]rune("keep this suffix")))
	m.completion = leadingSlashCompletion("/", 0, 1, m.commands)

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if got := m.composer.Value(); got != "keep this suffix" {
		t.Errorf("ctrl+k changed composer: composer=%q", got)
	}
	if m.completion != nil {
		t.Error("ctrl+k left inline completion open")
	}
	if _, ok := m.modal.(*paletteModal); !ok {
		t.Fatalf("ctrl+k modal = %T, want command palette", m.modal)
	}
	assertNoAppOp(t, ops)
}

func TestActiveModalOwnsControlP(t *testing.T) {
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
		if _, ok := m.modal.(*helpModal); !ok {
			t.Errorf("help palette action modal = %T, want helpModal", m.modal)
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

func TestPaletteSkillInsertionUsesOneCommandArgumentSeparatorAcrossThemes(t *testing.T) {
	themes := []struct {
		name string
		th   theme.Theme
	}{
		{name: "default", th: theme.Default()},
		{name: "explicit zero XS", th: func() theme.Theme {
			th := theme.Default()
			th.Spacing = theme.NewSpacing(0, 2, 3, 4)
			return th
		}()},
		{name: "custom XS", th: func() theme.Theme {
			th := theme.Default()
			th.Spacing = theme.NewSpacing(3, 2, 3, 4)
			return th
		}()},
	}
	for _, themeCase := range themes {
		t.Run(themeCase.name, func(t *testing.T) {
			for _, skillName := range []string{"review", "audit", "test"} {
				t.Run(skillName, func(t *testing.T) {
					store := newFakeHistory()
					skill := fakeSkill(skillName, "", "executed $ARGUMENTS")
					m, ops := newAppTestModelWithHistory(nil, []host.Skill{skill}, store)
					m.th = themeCase.th
					m.providerName = "echo"

					m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
					m = typeAppText(t, m, "/"+skillName)
					updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
					m = updated.(Model)
					m = updateApp(t, m, runAppCmd(t, cmd))
					if got := m.composer.Value(); got != "/"+skillName+" " {
						t.Fatalf("palette insertion = %q, want %q", got, "/"+skillName+" ")
					}

					m = typeAppText(t, m, "main.go")
					if got := m.composer.Value(); got != "/"+skillName+" main.go" {
						t.Fatalf("composer after argument = %q", got)
					}
					updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
					m = updated.(Model)
					for _, msg := range runAllAppCmds(t, cmd) {
						m = updateApp(t, m, msg)
					}
					assertUserInputText(t, receiveAppOp(t, ops), "executed main.go")
					if got := store.Entries(); !slices.Equal(got, []string{"/" + skillName + " main.go"}) {
						t.Errorf("history = %q, want inserted command", got)
					}
				})
			}
		})
	}
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
		if entry.Label == "/help" || entry.Action.Kind == paletteActionKeybinds {
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

func hasReverseVideo(s string) bool {
	return strings.Contains(s, "\x1b[7m") || strings.Contains(s, "\x1b[7;")
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

// assertUserInputText checks a received op is UserInput with the given text
// (Images are ignored so text-only cases stay comparable after multimodal).
func assertUserInputText(t *testing.T, got protocol.Op, wantText string) protocol.UserInput {
	t.Helper()
	in, ok := got.(protocol.UserInput)
	if !ok {
		t.Fatalf("op = %T %#v, want UserInput", got, got)
	}
	if in.Text != wantText {
		t.Fatalf("UserInput.Text = %q, want %q", in.Text, wantText)
	}
	return in
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

func TestPaneFocusStartsLeftAndPreservesComposerDraftAndCursor(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	if m.focus != focusLeft || !m.composer.Focused() {
		t.Fatalf("initial focus = %v/composer=%v, want left/focused", m.focus, m.composer.Focused())
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setComposerValueAt("first\nsecond", len([]rune("first\nsec")))
	draft, line, info := m.composer.Value(), m.composer.Line(), m.composer.LineInfo()

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.focus != focusRight || m.composer.Focused() {
		t.Errorf("ctrl+l focus = %v/composer=%v, want right/blurred", m.focus, m.composer.Focused())
	}
	if got := m.composer.Value(); got != draft {
		t.Errorf("blur changed draft = %q, want %q", got, draft)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlH})
	if m.focus != focusLeft || !m.composer.Focused() {
		t.Errorf("ctrl+h focus = %v/composer=%v, want left/focused", m.focus, m.composer.Focused())
	}
	if got, gotLine, gotInfo := m.composer.Value(), m.composer.Line(), m.composer.LineInfo(); got != draft || gotLine != line || gotInfo != info {
		t.Errorf("focus round trip changed composer: value=%q line=%d info=%+v; want %q/%d/%+v", got, gotLine, gotInfo, draft, line, info)
	}
}

func TestFocusAndPaletteClearCompletionBeforeChangingInputOwner(t *testing.T) {
	for _, tt := range []struct {
		name  string
		key   tea.KeyMsg
		check func(*testing.T, Model)
	}{
		{"focus right", tea.KeyMsg{Type: tea.KeyCtrlL}, func(t *testing.T, m Model) {
			if m.focus != focusRight {
				t.Errorf("focus = %v, want right", m.focus)
			}
		}},
		{"cycle next", tea.KeyMsg{Type: tea.KeyCtrlO}, func(t *testing.T, m Model) {
			if m.windows.index != 1 {
				t.Errorf("window index = %d, want 1", m.windows.index)
			}
		}},
		{"palette", tea.KeyMsg{Type: tea.KeyCtrlK}, func(t *testing.T, m Model) {
			if _, ok := m.modal.(*paletteModal); !ok {
				t.Errorf("modal = %T, want palette", m.modal)
			}
		}},
		{"keyhelp", tea.KeyMsg{Type: tea.KeyF1}, func(t *testing.T, m Model) {
			if _, ok := m.modal.(*keysModal); !ok {
				t.Errorf("modal = %T, want keys", m.modal)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.windows = windowRegistry{windows: []window{
				statefulTestWindow{windowID: "a", windowTitle: "A"},
				statefulTestWindow{windowID: "b", windowTitle: "B"},
			}}
			// ctrl+o cycles from left focus (#414).
			m.completion = leadingSlashCompletion("/", 0, 1, m.commands)
			m = updateApp(t, m, tt.key)
			if m.completion != nil {
				t.Error("completion remained open")
			}
			tt.check(t, m)
		})
	}
}

func TestCycleWindowKeysClearOpenCompletionAndCycleOnce(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  tea.KeyMsg
	}{
		// ctrl+o cycles from either focus (#414).
		{name: "ctrl+o", key: tea.KeyMsg{Type: tea.KeyCtrlO}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
			m.windows = windowRegistry{index: 1, windows: []window{
				statefulTestWindow{windowID: "first", windowTitle: "First"},
				statefulTestWindow{windowID: "second", windowTitle: "Second"},
				statefulTestWindow{windowID: "third", windowTitle: "Third", updates: []string{"prior state"}},
			}}
			m.setComposerValueAt("/provider echo", len([]rune("/pro")))
			m.recomputeCompletion()
			if m.completion == nil {
				t.Fatal("test setup did not open completion")
			}
			m.completion.Selected = 1
			draft, cursor := m.composer.Value(), m.composer.LineInfo().ColumnOffset
			// Stale left completion + right focus: cycle still clears completion.
			m.focus = focusRight

			updated, cmd := m.Update(tt.key)
			m = updated.(Model)
			if cmd != nil {
				t.Error("cycle returned a command, want no composer or engine work")
			}
			if m.completion != nil {
				t.Error("cycle left completion open")
			}
			if m.windows.index != 2 {
				t.Errorf("window index = %d, want 2 after one cycle", m.windows.index)
			}
			if got := m.windows.active().title(); got != "Third" {
				t.Errorf("active window title = %q, want Third", got)
			}
			if got := testWindow(t, m.windows.active()).updates; !slices.Equal(got, []string{"prior state"}) {
				t.Errorf("active window state = %q, want preserved prior state", got)
			}
			if got, gotCursor := m.composer.Value(), m.composer.LineInfo().ColumnOffset; got != draft || gotCursor != cursor {
				t.Errorf("cycle consumed composer input: value=%q cursor=%d, want %q/%d", got, gotCursor, draft, cursor)
			}
			assertNoAppOp(t, ops)
		})
	}
}

func TestCompletionEscapeDismissesBeforeInterruptAndFocusChange(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.turnRunning = true
	m.completion = leadingSlashCompletion("/", 0, 1, m.commands)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	runAppCmd(t, cmd)
	if m.completion != nil || m.focus != focusLeft {
		t.Errorf("first escape completion/focus = %v/%v, want closed/left", m.completion, m.focus)
	}
	assertNoAppOp(t, ops)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	runAppCmd(t, cmd)
	if got := receiveAppOp(t, ops); got != (protocol.Interrupt{}) {
		t.Errorf("second escape operation = %#v, want Interrupt", got)
	}
	assertNoAppOp(t, ops)
}

func TestModalOwnsGlobalKeysExceptQuit(t *testing.T) {
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyCtrlO}, {Type: tea.KeyCtrlL}, {Type: tea.KeyCtrlH}, {Type: tea.KeyCtrlK}, {Type: tea.KeyCtrlP}, {Type: tea.KeyF1},
	} {
		t.Run(msg.String(), func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.completion = leadingSlashCompletion("/", 0, 1, m.commands)
			probe := &appProbeModal{}
			m.modal = probe
			m = updateApp(t, m, msg)
			if probe.keys != 1 || m.focus != focusLeft || m.windows.index != 0 || m.completion == nil || m.modal != probe {
				t.Errorf("modal routing changed state: keys=%d focus=%v index=%d completion=%v modal=%T", probe.keys, m.focus, m.windows.index, m.completion, m.modal)
			}
			assertNoAppOp(t, ops)
		})
	}
}

func TestRightPaneOwnsOrdinaryKeysAndGlobalKeysRemainGlobal(t *testing.T) {
	m, ops := newAppTestModel([]string{"build", "plan"}, nil)
	m.providerName = "echo"
	m.composer.SetValue("unchanged")
	m.entries = []string{"history"}
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "right-one"},
		statefulTestWindow{windowID: "right-two"},
	}}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	completion := leadingSlashCompletion("/", 0, 1, m.commands)
	m.completion = completion // stale completion must not take ownership on the right.
	startOffset, startLine := m.composer.LineInfo().ColumnOffset, m.composer.Line()
	// Ordinary keys go to the right pane; pgup/pgdn scroll the transcript globally.
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("x")}, {Type: tea.KeyEnter}, {Type: tea.KeyTab}, {Type: tea.KeyCtrlD},
		{Type: tea.KeyUp}, {Type: tea.KeyDown},
	} {
		m = updateApp(t, m, msg)
	}
	if m.composer.Value() != "unchanged" || m.composer.Line() != startLine || m.composer.LineInfo().ColumnOffset != startOffset || m.historyPos != -1 || m.agentName != "" {
		t.Errorf("right-pane keys changed left state: composer=%q line=%d offset=%d history=%d agent=%q", m.composer.Value(), m.composer.Line(), m.composer.LineInfo().ColumnOffset, m.historyPos, m.agentName)
	}
	if m.completion != completion || m.completion.Selected != completion.Selected {
		t.Error("right-pane keys changed stale completion")
	}
	if got := testWindow(t, m.windows.active()).updates; len(got) != 6 {
		t.Errorf("right pane received %d keys, want 6: %q", len(got), got)
	}
	assertNoAppOp(t, ops)

	for _, msg := range []tea.KeyMsg{{Type: tea.KeyCtrlO}, {Type: tea.KeyCtrlP}} {
		before := totalWindowUpdates(t, m.windows)
		index := m.windows.index
		m = updateApp(t, m, msg)
		if m.windows.index == index || m.completion != nil {
			t.Errorf("%s did not globally cycle and clear completion", msg.String())
		}
		if got := totalWindowUpdates(t, m.windows); got != before {
			t.Errorf("%s was recorded by window: updates %d, want %d", msg.String(), got, before)
		}
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if _, ok := m.modal.(*paletteModal); !ok {
		t.Errorf("right-focused ctrl+k modal = %T, want palette", m.modal)
	}
}

func TestPaletteSkillInvocationReturnsFocusToComposerFromRightPane(t *testing.T) {
	m, ops := newAppTestModel(nil, []host.Skill{fakeSkill("review", "", "review $ARGUMENTS")})
	m.providerName = "echo"
	m.focus = focusRight
	m.composer.Blur()
	m = updateApp(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionSkill, Value: "review"}})
	if m.focus != focusLeft || !m.composer.Focused() || m.composer.Value() != "/review " {
		t.Errorf("palette skill focus/composer = %v/%v/%q, want left/focused /review ", m.focus, m.composer.Focused(), m.composer.Value())
	}
	assertNoAppOp(t, ops)
}

func TestPaletteHelpInvocationOpensHelpModal(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())

	m = updateApp(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/help"}})
	help, ok := m.modal.(*helpModal)
	if !ok {
		t.Fatalf("/help from palette modal = %T, want helpModal", m.modal)
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "Commands") {
		t.Errorf("/help modal view missing title: %q", plain)
	}
	for _, want := range []string{"/session", "/rename", "/export", "/theme", "/memory", "/issues", "/compact", "/fast", "/think", "/layout", "/md-read", "/keys", "/settings", "/exit", "/quit", "/palette", "/interrupt", "/agent-next", "/focus-left"} {
		found := false
		for _, entry := range help.entries {
			if strings.HasPrefix(entry.Label, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("/help modal omitted %s", want)
		}
	}
	assertNoAppOp(t, ops)
}

func TestPalettePickerActionsAndStaleNoticeDoNotStealRightFocus(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	m.setNotice("commands: stale help", false)
	m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())

	m = updateApp(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/provider"}})
	if m.focus != focusRight {
		t.Errorf("picker action changed right focus to %v", m.focus)
	}
	if _, ok := m.modal.(*providerModal); !ok {
		t.Errorf("/provider palette action modal = %T, want provider picker", m.modal)
	}
	assertNoAppOp(t, ops)
}

func TestViewportScrollOffsetSurvivesRightFocusRoundTripAndRefreshesOnResizeAndEvent(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 80 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("transcript ", 8) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	wantOffset := m.viewport.YOffset
	if wantOffset == 0 {
		t.Fatal("page up did not move long transcript off the bottom")
	}
	if m.viewport.AtBottom() {
		t.Fatal("page up left viewport AtBottom")
	}

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlH})
	if got := m.viewport.YOffset; got != wantOffset {
		t.Errorf("focus round trip viewport offset = %d, want %d", got, wantOffset)
	}

	m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 24})
	if !strings.Contains(ansi.Strip(m.viewport.View()), "transcript") {
		t.Error("resize did not re-render transcript")
	}
	// Scrolled-up users must keep their place when engine events refresh content.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "engine refresh"}})
	if m.viewport.AtBottom() {
		t.Error("TextDelta yanked scrolled-up viewport to bottom")
	}
	if got := m.viewport.YOffset; got < wantOffset-2 || got > wantOffset+2 {
		t.Errorf("engine event viewport offset = %d, want near preserved %d", got, wantOffset)
	}
}

// TestViewportStickToBottomFollowsTextDeltaWhenAtBottom pins that live output
// still auto-scrolls when the user is already anchored at the bottom.
func TestViewportStickToBottomFollowsTextDeltaWhenAtBottom(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 40 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("line ", 10) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	if !m.viewport.AtBottom() {
		t.Fatal("setup: viewport not at bottom")
	}
	m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "fresh tail content"}})
	if !m.viewport.AtBottom() {
		t.Errorf("TextDelta while at bottom lost follow: YOffset=%d total=%d height=%d",
			m.viewport.YOffset, m.viewport.TotalLineCount(), m.viewport.Height)
	}
	if !strings.Contains(ansi.Strip(m.viewport.View()), "fresh tail") {
		t.Error("at-bottom TextDelta did not render new content in viewport")
	}
}

func totalWindowUpdates(t *testing.T, r windowRegistry) int {
	t.Helper()
	total := 0
	for _, w := range r.windows {
		total += len(testWindow(t, w).updates)
	}
	return total
}

func TestProtocolEventsAndSpinnerDoNotChangeRightFocus(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	for _, ev := range []protocol.Event{protocol.UserMessage{Text: "user"}, protocol.TextDelta{Text: "assistant"}} {
		m = updateApp(t, m, engineEventMsg{ev: ev})
		if m.focus != focusRight {
			t.Fatalf("event %T changed focus to %v", ev, m.focus)
		}
	}
	if got := ansi.Strip(m.viewport.View()); !strings.Contains(got, "user") || !strings.Contains(got, "assistant") {
		t.Errorf("protocol events did not refresh transcript: %q", got)
	}
	before := m.focus
	m = updateApp(t, m, spinner.TickMsg{})
	if m.focus != before {
		t.Errorf("spinner tick changed focus from %v to %v", before, m.focus)
	}
}
