package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestComputeLayoutConstrainedVerticalBudget(t *testing.T) {
	tests := []struct {
		name                         string
		width, height, composer, pop int
		danger                       bool
		liveNotice                   bool
		want                         *layout
	}{
		{
			name: "canonical 80x24 remains unchanged", width: 80, height: 24, composer: 2,
			want: &layout{header: 1, transcript: 17, notice: 1, composer: 4, hints: 1},
		},
		{name: "80x20 blank notice", width: 80, height: 20, composer: 10, pop: 8},
		{name: "80x20 live notice", width: 80, height: 20, composer: 10, pop: 8, liveNotice: true},
		{name: "80x20 danger notice", width: 80, height: 20, composer: 10, pop: 8, danger: true, liveNotice: true},
		{name: "one row live notice wins over chrome", width: 80, height: 1, composer: 10, pop: 8, liveNotice: true},
	}
	for _, height := range []int{0, 1, 2, 3, 4, 19} {
		tests = append(tests, struct {
			name                         string
			width, height, composer, pop int
			danger                       bool
			liveNotice                   bool
			want                         *layout
		}{name: "oversized composer and popup height " + itoa(height), width: 80, height: height, composer: 10, pop: 8, danger: height%2 == 1})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noticeRows := 0
			if tt.liveNotice {
				noticeRows = 1
			}
			got := computeLayout(tt.width, tt.height, tt.composer, tt.pop, tt.danger, noticeRows)
			if tt.want != nil && got != *tt.want {
				t.Errorf("canonical layout = %+v, want %+v", got, *tt.want)
			}
			regions := []int{got.header, got.transcript, got.notice, got.popup, got.composer, got.hints, got.danger}
			for _, region := range regions {
				if region < 0 {
					t.Fatalf("layout has a negative region: %+v", got)
				}
			}
			body := got.transcript + got.notice + got.popup + got.composer
			if body != tt.height-got.header-got.hints-got.danger {
				t.Errorf("body = %d, want exact remaining height %d: %+v", body, tt.height-got.header-got.hints-got.danger, got)
			}
			if total := got.header + body + got.hints + got.danger; total != tt.height {
				t.Errorf("regions total %d, want screen height %d: %+v", total, tt.height, got)
			}
			// Transcript is the flex region: it must give way before fixed UI is
			// permitted to exceed the available canvas.
			if got.transcript != 0 && tt.composer+tt.pop+got.header+got.notice+got.hints+got.danger >= tt.height {
				t.Errorf("transcript = %d despite an exhausted vertical budget: %+v", got.transcript, got)
			}
			if tt.height == 20 && tt.composer == 10 && tt.pop == 8 {
				wantPopup := 5
				if tt.danger {
					wantPopup = 4
				}
				if got.composer != 12 || got.popup != wantPopup {
					t.Errorf("completion should shrink before composer: popup/composer = %d/%d, want %d/12", got.popup, got.composer, wantPopup)
				}
			}
			if tt.liveNotice && tt.height > 0 && got.notice != 1 {
				t.Errorf("live notice height = %d, want preserved row", got.notice)
			}
		})
	}
}

func TestTranscriptViewWithZeroHeightIsEmpty(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.UserMessage{Text: "existing transcript"})
	m.refreshViewport()
	for _, populated := range []bool{false, true} {
		if !populated {
			m.cells = nil
		}
		if got := m.transcriptView(false, 80, 0); got != "" {
			t.Errorf("populated=%v transcript at zero height = %q, want empty", populated, ansi.Strip(got))
		}
	}
}

func TestConstrainedCanvasKeepsHeaderTranscriptDraftAndCompletionWithin20Rows(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m.applyEvent(protocol.UserMessage{Text: "existing transcript"})
	m.refreshViewport()
	draft := "/\na\nb\nc\nd\ne\nf\ng"
	m.setComposerValueAt(draft, 1)
	m.recomputeCompletion()
	m.reflow()
	if m.completion == nil {
		t.Fatal("slash draft did not open completion")
	}

	view := viewString(m)
	assertCanvas(t, view, 80, 20)
	plain := ansi.Strip(view)
	if !strings.Contains(strings.Split(plain, "\n")[0], "strike") {
		t.Errorf("header was cropped despite a 20-row canvas:\n%s", plain)
	}
	if strings.Contains(plain, "session") {
		t.Errorf("compact constrained transcript rendered a phantom session panel:\n%s", plain)
	}
	if m.composer.Height() > composerMaxHeight || m.completionPopupHeight() > completionMaxRows+2 {
		t.Errorf("unbounded constrained input allocation: composer=%d popup=%d", m.composer.Height(), m.completionPopupHeight())
	}
}

func TestPopulatedReviewerSessionKeepsOneTranscriptRowWithoutTwoBorderRows(t *testing.T) {
	m, _ := newAppTestModel(nil, []host.Skill{fakeSkill("review", "review a change", "Review $ARGUMENTS")})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m.applyEvent(protocol.UserMessage{Text: "existing transcript"})
	m.setComposerValueAt("/\none\ntwo\nthree\nfour\nfive", len([]rune("/")))
	m.recomputeCompletion()
	m.reflow()

	l := computeLayout(80, 20, m.composer.Height(), m.completionPopupHeight(), false)
	if l.transcript != 1 {
		t.Fatalf("transcript allocation = %d, want one row: %+v", l.transcript, l)
	}
	view := viewString(m)
	assertCanvas(t, view, 80, 20)
	plain := ansi.Strip(view)
	if !strings.Contains(strings.Split(plain, "\n")[0], "strike") {
		t.Errorf("header was cropped:\n%s", plain)
	}
	transcriptRow := strings.Split(plain, "\n")[l.header]
	if strings.ContainsAny(transcriptRow, "╭╰") {
		t.Errorf("one-row transcript retained two panel border rows: %q", transcriptRow)
	}
}

func TestConstrainedComposerKeepsCursorLineVisibleWithoutMutatingStoredState(t *testing.T) {
	setTUITrueColor(t)
	draft := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight"
	for _, tt := range []struct {
		name    string
		compact bool
	}{{"compact", true}, {"bordered renderer", false}} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			const height = 10
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: height})
			m.setComposerValueAt(draft, len([]rune(draft))-1)
			m.reflow()
			m.composer.SetVirtualCursor(true)
			styles := m.composer.Styles()
			styles.Cursor.Blink = false
			m.composer.SetStyles(styles)
			before := composerRenderState(m)
			l := computeLayout(80, height, m.composer.Height(), 0, false)
			out := m.composerView(tt.compact, 80, l.composer)
			if rows := len(strings.Split(out, "\n")); rows != l.composer {
				t.Errorf("composer rows = %d, want allocated %d", rows, l.composer)
			}
			if !strings.Contains(ansi.Strip(out), "eight") {
				t.Errorf("constrained composer omitted current cursor line: %q", ansi.Strip(out))
			}
			if !hasSGRParameter(out, "7") {
				t.Errorf("constrained composer omitted the static reverse-video cursor: %q", out)
			}
			if after := composerRenderState(m); after != before {
				t.Errorf("render mutated stored composer or independent viewport: after=%+v, before=%+v", after, before)
			}
		})
	}
}

type composerState struct {
	value          string
	height         int
	focused        bool
	line           int
	logicalColumn  int
	viewRows       string
	viewportOffset int
}

func composerRenderState(m Model) composerState {
	info := m.composer.LineInfo()
	return composerState{
		value:          m.composer.Value(),
		height:         m.composer.Height(),
		focused:        m.composer.Focused(),
		line:           m.composer.Line(),
		logicalColumn:  info.StartColumn + info.ColumnOffset,
		viewRows:       strings.Join(strings.Split(m.composer.View(), "\n"), "\n"),
		viewportOffset: m.viewport.YOffset(),
	}
}

// TestConstrainedCompletionAndComposerViewsUseExactlyAllocatedRows is covered by
// TestConstrainedCompletionAndComposerViewsUseSoftChrome (Family soft default).

func TestConstrainedRightPaneAndModalCanvasAreExact(t *testing.T) {
	for _, height := range []int{0, 1, 2, 3, 4, 19} {
		t.Run(itoa(height), func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.focus = focusRight
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: height})
			if height == 0 {
				if got := viewString(m); got != "" {
					t.Errorf("zero-height right pane = %q, want empty", ansi.Strip(got))
				}
			} else {
				assertCanvas(t, viewString(m), 80, height)
			}
			m.modal = newPaletteModal(m.commands, nil, m.currentPaletteAvailability())
			if height == 0 {
				if got := viewString(m); got != "" {
					t.Errorf("zero-height modal = %q, want empty", ansi.Strip(got))
				}
			} else {
				assertCanvas(t, viewString(m), 80, height)
			}
		})
	}
}

func TestModelSelectionClearsOnlyModelRequiredNotices(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, Model) Model
	}{
		{"ordinary submit", func(t *testing.T, m Model) Model {
			m.composer.SetValue("prompt")
			return updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		}},
		{"skill submit", func(t *testing.T, m Model) Model {
			m.composer.SetValue("/review code")
			return updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		}},
		{"model command", func(t *testing.T, m Model) Model {
			m.composer.SetValue("/model x")
			return updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		}},
		{"save defaults", func(t *testing.T, m Model) Model {
			return updateApp(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		}},
		{"palette rejection", func(t *testing.T, m Model) Model {
			return updateApp(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/model"}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, []host.Skill{fakeSkill("review", "", "review $ARGUMENTS")})
			m = tt.setup(t, m)
			if m.notice == "" || !m.noticeErr {
				t.Fatalf("model-required action did not show an error notice: %q / %v", m.notice, m.noticeErr)
			}
			if m.noticeCause != noticeNeedsModel {
				t.Fatalf("model-required action notice cause = %v, want needsModel", m.noticeCause)
			}
			m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "test"})
			if m.notice != "" || m.noticeErr {
				t.Errorf("ModelSelected retained needs-model notice %q / error=%v", m.notice, m.noticeErr)
			}
		})
	}
}

func TestBareProviderPickerSelectionClearsPriorModelRequiredNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("prompt")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m.composer.SetValue("/provider")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m.modal.(*providerModal); !ok {
		t.Fatalf("bare /provider modal = %T, want provider picker", m.modal)
	}
	m.applyEvent(protocol.ModelSelected{Provider: "echo"})
	if m.notice != "" || m.noticeErr {
		t.Errorf("picker selection retained needs-model notice %q / error=%v", m.notice, m.noticeErr)
	}
}

func TestModelSelectedClearsOnlyExactNoModelEngineError(t *testing.T) {
	canonical := "no model selected — use /provider <anthropic|openai|xai|google|kimi|deepseek|echo> [model]"
	for _, message := range []string{canonical, "\"" + canonical + "\"", canonical + ".", "No model selected — use /provider <echo>"} {
		m, _ := newAppTestModel(nil, nil)
		m.applyEvent(protocol.EngineError{Message: message})
		m.applyEvent(protocol.ModelSelected{Provider: "echo"})
		cleared := m.notice == "" && !m.noticeErr
		if (message == canonical) != cleared {
			t.Errorf("message %q cleared=%v, want %v", message, cleared, message == canonical)
		}
	}
}

func TestModelSelectedPreservesAuthenticationSuccessNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.setNotice("Signed in to openai", false)
	m.applyEvent(protocol.ModelSelected{Provider: "openai"})
	if m.notice != "Signed in to openai" || m.noticeErr {
		t.Errorf("selectAfter model selection changed auth success notice: %q / %v", m.notice, m.noticeErr)
	}
}

func TestModelSelectedPreservesGeneralNoticesAndRefreshesOpenPaletteAndHeader(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setNotice("unrelated info", false)
	m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "chosen"})
	if m.notice != "unrelated info" || m.noticeErr {
		t.Errorf("model selection changed general info notice: %q / %v", m.notice, m.noticeErr)
	}
	m.setNotice("unrelated error", true)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "chosen"})
	if m.notice != "unrelated error" || !m.noticeErr {
		t.Errorf("model selection changed general error notice: %q / %v", m.notice, m.noticeErr)
	}
	if !strings.Contains(ansi.Strip(m.headerView(80)), "echo/chosen") {
		t.Error("model selection did not update the header")
	}
	palette := m.modal.(*paletteModal)
	copy := *palette
	assertPaletteInvoke(t, &copy, "/model", paletteInvokeMsg{Action: paletteAction{Kind: paletteActionBuiltin, Value: "/model"}})
}
