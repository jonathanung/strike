package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestC3WelcomeEligibilityAndCardContent(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		auth      *fakeAuth
		wantSetup bool
	}{
		{"no provider", "", newFakeAuth(), true},
		{"selected matching unauthenticated", "openai", &fakeAuth{statuses: []host.ProviderStatus{{Name: "anthropic", Authed: true}, {Name: "openai", Detail: "sign in", OAuth: true}}}, true},
		{"selected authenticated", "openai", &fakeAuth{statuses: []host.ProviderStatus{{Name: "openai", Authed: true}}}, false},
		{"selected absent from statuses", "missing", &fakeAuth{statuses: []host.ProviderStatus{{Name: "openai", Authed: false}}}, false},
		{"unknown auth service", "openai", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.providerName = tt.provider
			if tt.auth == nil {
				m.services.Auth = nil
			} else {
				m.services.Auth = tt.auth
			}
			view := ansi.Strip(m.welcomeView(120, 40))
			hasSetup := strings.Contains(view, "get started")
			if hasSetup != tt.wantSetup {
				t.Fatalf("get-started eligibility = %v, want %v", hasSetup, tt.wantSetup)
			}
			if !strings.Contains(view, "keys") {
				t.Fatal("keys card must be available for every provider state")
			}
			if tt.name == "selected matching unauthenticated" {
				body := ansi.Strip(m.welcomeProviders(tt.auth.Statuses(), 40, 5))
				if strings.Index(body, "openai") > strings.Index(body, "anthropic") {
					t.Fatalf("selected unauthenticated provider was not prioritized:\n%s", body)
				}
			}
		})
	}
}

func TestC3WelcomeAgentsSkillsAndRecentConditions(t *testing.T) {
	tests := []struct {
		name   string
		agents []string
		skills []host.Skill
		want   []string
	}{
		{"absent", nil, nil, nil},
		{"invalid", []string{"", "bad\x1bname", " leading"}, []host.Skill{fakeSkill("bad name", "", "")}, nil},
		{"agents only", []string{"build"}, nil, []string{"agents", "build"}},
		{"skills only", nil, []host.Skill{fakeSkill("review", "", "")}, []string{"skills", "/review"}},
		{"both fair", []string{"build", "plan", "ship", "test"}, []host.Skill{fakeSkill("review", "", ""), fakeSkill("audit", "", ""), fakeSkill("fix", "", "")}, []string{"agents", "skills", "build", "/review"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(tt.agents, tt.skills)
			cards := m.welcomeCards(m.services.Auth.Statuses())
			has := hasWelcomeCard(cards, "agents & skills")
			if has != (len(tt.want) > 0) {
				t.Fatalf("agents/skills card = %v, want %v", has, len(tt.want) > 0)
			}
			if has {
				body := ansi.Strip(m.welcomeAgentsSkills(tt.agents, tt.skills, 40, 6))
				for _, want := range tt.want {
					if !strings.Contains(body, want) {
						t.Errorf("body missing %q: %q", want, body)
					}
				}
				if strings.Count(body, "\n")+1 > 6 {
					t.Errorf("body exceeds six rows: %q", body)
				}
			}
		})
	}
	m, _ := newAppTestModel([]string{"a\nagent", "界界界界界界界界界界"}, []host.Skill{fakeSkill("review", "", "")})
	body := ansi.Strip(m.welcomeAgentsSkills([]string{"a\nagent", "界界界界界界界界界界"}, m.skills, 12, 6))
	if strings.Contains(body, "\x1b") || strings.Count(body, "\n")+1 > 6 {
		t.Errorf("long/control data escaped its one-row allocation: %q", body)
	}

	without, _ := newAppTestModel(nil, nil)
	if hasWelcomeCard(without.welcomeCards(without.services.Auth.Statuses()), "recent prompts") {
		t.Fatal("recent card without history")
	}
	with, _ := newAppTestModelWithHistory(nil, nil, newFakeHistory("old", "new"))
	if !hasWelcomeCard(with.welcomeCards(with.services.Auth.Statuses()), "recent prompts") {
		t.Fatal("recent card missing with history")
	}
}

func TestC3WelcomeColumnsAndNoOuterPanel(t *testing.T) {
	th := theme.Default()
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	for _, width := range []int{2*welcomeCardMinWidth + th.Spacing.SM - 1, 2*welcomeCardMinWidth + th.Spacing.SM} {
		view := ansi.Strip(m.welcomeView(width, 12))
		if width < 2*welcomeCardMinWidth+th.Spacing.SM && strings.Count(strings.Split(view, "\n")[0], "╭") > 1 {
			t.Errorf("%d unexpectedly used a second column: %q", width, view)
		}
	}
	th.Spacing = th.Spacing.WithSM(5)
	m, _ = newAppTestModelWithOptions(Options{Theme: &th})
	if strings.Count(strings.Split(ansi.Strip(m.welcomeView(2*welcomeCardMinWidth+4, 12)), "\n")[0], "╭") > 1 {
		t.Error("custom gutter threshold split too early")
	}
	if strings.Count(strings.Split(ansi.Strip(m.welcomeView(2*welcomeCardMinWidth+5, 12)), "\n")[0], "╭") != 2 {
		t.Error("custom gutter threshold did not split")
	}

	m, _ = newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "⚡ strike") || strings.Contains(plain, "S T R I K E") {
		t.Errorf("header brand/dashboard framing is wrong:\n%s", plain)
	}
	assertNoWelcomeOuterPanel(t, m.View())
	m.applyEvent(protocol.UserMessage{Text: "populated"})
	m.refreshViewport()
	if plain = ansi.Strip(m.View()); !strings.Contains(plain, "session") || strings.Contains(plain, "get started") {
		t.Errorf("populated transcript did not replace dashboard with session panel:\n%s", plain)
	}
}

func TestC3WelcomeTwoColumnGutterUsesResolvedSmallSpacing(t *testing.T) {
	custom := theme.Default()
	custom.Spacing = custom.Spacing.WithSM(4)
	zero := theme.Default()
	zero.Spacing = zero.Spacing.WithSM(0)

	for _, tt := range []struct {
		name string
		th   theme.Theme
	}{
		{"default", theme.Default()},
		{"custom", custom},
		{"explicit zero", zero},
	} {
		t.Run(tt.name, func(t *testing.T) {
			th := tt.th.Resolve()
			m, _ := newAppTestModelWithOptions(Options{Theme: &tt.th})
			width := 2*welcomeCardMinWidth + th.Spacing.SM
			row := strings.Split(ansi.Strip(m.welcomeView(width, 12)), "\n")[0]
			firstRight := strings.Index(row, th.BorderStyle.TopRight)
			if firstRight < 0 {
				t.Fatalf("first welcome card right border missing from %q", row)
			}
			secondLeftOffset := strings.Index(row[firstRight+len(th.BorderStyle.TopRight):], th.BorderStyle.TopLeft)
			if secondLeftOffset < 0 {
				t.Fatalf("second welcome card left border missing from %q", row)
			}
			secondLeft := firstRight + len(th.BorderStyle.TopRight) + secondLeftOffset
			gutter := row[firstRight+len(th.BorderStyle.TopRight) : secondLeft]
			if got := ansi.StringWidth(gutter); got != th.Spacing.SM {
				t.Errorf("gutter width = %d, want resolved Spacing.SM %d; row=%q", got, th.Spacing.SM, row)
			}
			if strings.TrimSpace(gutter) != "" {
				t.Errorf("gutter contains non-space cells: %q", gutter)
			}
			if got := ansi.StringWidth(row); got != width {
				t.Errorf("two-column row width = %d, want %d; row=%q", got, width, row)
			}
		})
	}
}

func TestC3WelcomeCapacityAndFocusTokens(t *testing.T) {
	for _, danger := range []bool{false, true} {
		m, _ := newAppTestModelWithHistory([]string{"build", "plan", "ship", "test"}, []host.Skill{fakeSkill("review", "", ""), fakeSkill("audit", "", "")}, newFakeHistory("one", "two", "three"))
		m.dangerouslySkipPermissions = danger
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		plain := ansi.Strip(m.View())
		for _, want := range []string{"get started", "keys", "agents & skills", "recent prompts"} {
			if !strings.Contains(plain, want) {
				t.Errorf("danger=%v dropped eligible %q:\n%s", danger, want, plain)
			}
		}
		assertCanvas(t, m.View(), 80, 24)
	}

	setTUITrueColor(t)
	th := theme.Default()
	th.BorderFocus = fixedColor("#010203")
	th.BorderMuted = fixedColor("#040506")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(m.View(), rgbSGR("#010203")) || !strings.Contains(m.View(), rgbSGR("#040506")) {
		t.Fatal("focused and dim dashboard borders are not tokenized")
	}
	m.focus = focusRight
	m.reflow()
	if !strings.Contains(m.View(), rgbSGR("#010203")) || !strings.Contains(m.View(), rgbSGR("#040506")) {
		t.Fatal("right focus did not preserve focused/dim border tokens")
	}
	m.modal = &appProbeModal{}
	m.reflow()
	if strings.Contains(m.View(), rgbSGR("#010203")) {
		t.Fatal("modal did not dim dashboard borders")
	}
}

func TestC3WelcomeProviderAndPromptLimits(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	statuses := []host.ProviderStatus{{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"}, {Name: "five"}}
	body := ansi.Strip(m.welcomeProviders(statuses, 30, 8))
	// Four provider rows + /provider action + "type below · enter to send" tip.
	if strings.Count(body, "\n")+1 != 6 || !strings.Contains(body, "/provider") || strings.Contains(body, "five") {
		t.Errorf("provider card did not cap four rows plus action and tip: %q", body)
	}
	if !strings.Contains(body, "type below") || !strings.Contains(body, "enter") {
		t.Errorf("provider card missing type-below/enter tip: %q", body)
	}
	for _, tt := range []struct {
		name, prompt string
	}{
		{"ASCII", "plain ASCII prompt"},
		{"wide", "wide 界界界 prompt"},
		{"combining", "combining e\u0301e\u0301e\u0301 prompt"},
		{"newlines", "line one\nline two\rline three"},
		{"controls", "before\x00\x1b[2J\u0085after"},
	} {
		t.Run("recent "+tt.name, func(t *testing.T) {
			m.entries = []string{tt.prompt}
			line := ansi.Strip(m.welcomeRecent(18, 3))
			if strings.Count(line, "\n") != 0 || !strings.HasPrefix(line, "· ") || ansi.StringWidth(line) > 18 || strings.ContainsRune(line, '\x1b') || strings.ContainsAny(line, "\r\x00\u0085") {
				t.Errorf("prompt was not safely sanitized before one-row truncation: %q", line)
			}
		})
	}

	const dangerousPrompt = "before\x1b[2J\u0085after"
	hostModel, _ := newAppTestModelWithHistory(nil, nil, newFakeHistory(dangerousPrompt))
	hostModel = updateApp(t, hostModel, tea.WindowSizeMsg{Width: 120, Height: 80})
	raw := hostModel.View()
	if strings.Contains(raw, "\x1b[2J") {
		t.Errorf("raw welcome output retained injected clear-screen sequence: %q", raw)
	}
	if !strings.Contains(raw, "before�[2J�after") {
		t.Errorf("raw welcome output did not visibly replace injected controls: %q", raw)
	}

	entries := []string{
		"ASCII newest", "wide 界界界", "combining e\u0301e\u0301e\u0301", "newline\nCR\r", "control\x00\x1b[31mC1\u0085",
	}
	m.entries = entries
	for _, width := range []int{8, 30} {
		out := ansi.Strip(m.welcomeRecent(width, 3))
		lines := strings.Split(out, "\n")
		if len(lines) != 3 {
			t.Fatalf("width %d recent rows = %d, want 3: %q", width, len(lines), out)
		}
		for _, line := range lines {
			if !strings.HasPrefix(line, "· ") || ansi.StringWidth(line) > width || strings.ContainsRune(line, '\x1b') || strings.ContainsAny(line, "\n\r\x00\u0085") {
				t.Errorf("unsafe/overflow recent row width=%d: %q", width, line)
			}
			if !utf8.ValidString(line) {
				t.Errorf("invalid UTF-8 recent row: %q", line)
			}
		}
		if width == 30 && !strings.Contains(lines[0], "C1") {
			t.Errorf("recent prompts not newest-first: %q", out)
		}
	}
}

func TestC3ModelSelectionPreservesExistingNoticeAndRefreshesPalette(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.setNotice("unrelated failure", true)
	m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())
	m.applyEvent(protocol.ModelSelected{Provider: "unique-provider", Model: "unique-model"})
	if m.providerName != "unique-provider" || m.modelName != "unique-model" || m.notice != "unrelated failure" || !m.noticeErr {
		t.Fatalf("model selection changed fields or unrelated notice: %#v", m)
	}
	plain := ansi.Strip(m.View())
	// Header badge and context pane both show provider/model when the right
	// pane is visible; reject the legacy "model: …" chrome form.
	if strings.Contains(plain, "model: unique-provider/unique-model") {
		t.Errorf("legacy model chrome leaked into view:\n%s", plain)
	}
	if got := strings.Count(plain, "unique-provider/unique-model"); got < 1 || got > 2 {
		t.Errorf("model occurrences = %d, want 1 or 2 (header ± context):\n%s", got, plain)
	}
	if !strings.Contains(plain, "unrelated failure") {
		t.Errorf("existing error notice disappeared:\n%s", plain)
	}
}

func TestC3DangerNoticeHintsAndWorkingRows(t *testing.T) {
	const danger = "DANGER: permissions bypassed"
	for _, modal := range []func(*Model){nil, func(m *Model) { m.modal = newPaletteModal(m.commands, nil, m.currentPaletteAvailability()) }, func(m *Model) { m.applyEvent(protocol.PermissionAsked{RequestID: "c3", Permission: "bash"}) }} {
		m, _ := newAppTestModelWithOptions(Options{DangerouslySkipPermissions: true})
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		if modal != nil {
			modal(&m)
		}
		plain := ansi.Strip(m.View())
		lines := strings.Split(plain, "\n")
		if strings.Count(plain, danger) != 1 || lines[len(lines)-1] != danger+strings.Repeat(" ", 80-len(danger)) {
			t.Errorf("danger must be exact once in final full row:\n%s", plain)
		}
		if strings.Contains(m.hintsView(80), "DANGER") {
			t.Error("danger leaked into hints")
		}
	}
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setNotice("separate notice", false)
	m.applyEvent(protocol.TurnStarted{})
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	l := computeLayout(80, 24, m.composer.Height(), 0, false)
	noticeRow := l.header + l.transcript
	if !strings.Contains(lines[0], "working") || !strings.Contains(lines[noticeRow], "separate notice") || !strings.Contains(lines[len(lines)-1], "ctrl+h") {
		t.Errorf("working header, notice, and hints do not retain separate rows:\n%s", strings.Join(lines, "\n"))
	}
}

func TestC3CanonicalLayoutCanvas(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		focus         paneFocus
		danger        bool
	}{
		{"80x24 left dashboard", 80, 24, focusLeft, false}, {"80x24 danger dashboard", 80, 24, focusLeft, true}, {"80x24 right", 80, 24, focusRight, false}, {"93x40", 93, 40, focusLeft, false}, {"120x40", 120, 40, focusLeft, false}, {"160x45 long data", 160, 45, focusLeft, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModelWithHistory([]string{"build", "plan", "ship", "test"}, []host.Skill{fakeSkill("review", "", ""), fakeSkill("audit", "", "")}, newFakeHistory("old prompt", "new prompt"))
			m.dangerouslySkipPermissions = tt.danger
			m.focus = tt.focus
			m.agents = []string{"very-long-agent-name-with-control-\x1b"}
			m.skills = []host.Skill{fakeSkill("review", "", "")}
			m.setNotice("long status "+strings.Repeat("界", 80), true)
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: tt.height})
			view, plain := m.View(), ansi.Strip(m.View())
			assertCanvas(t, view, tt.width, tt.height)
			if strings.Contains(plain, "S T R I K E") {
				t.Errorf("legacy welcome chrome in canonical view:\n%s", plain)
			}
			assertNoWelcomeOuterPanel(t, m.View())
			if tt.focus == focusRight && strings.Contains(plain, "get started") {
				t.Errorf("right-only layout rendered dashboard:\n%s", plain)
			}
			if tt.danger && strings.Count(plain, "DANGER: permissions bypassed") != 1 {
				t.Errorf("danger uniqueness failed:\n%s", plain)
			}
			assertVisibleWelcomeCardsClosed(t, m, view)
		})
	}
}

func TestC3LongDashboardHistoryAndSelectedModelEvidence(t *testing.T) {
	entries := []string{
		"C3-OLD-SENTINEL-MUST-NOT-RENDER",
		"C3-ASCII-LONG-MARKER " + strings.Repeat("abcdefghijklmnopqrstuvwxyz ", 12),
		"C3-WIDE-COMBINING-MARKER 界界界界界界界界 e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301",
		"C3-CONTROL-SAFE-MARKER before\x00\x1b[2J\r\n\u0085after",
	}
	m, _ := newAppTestModelWithHistory([]string{"build", "plan", "ship", "test", "overflow"}, []host.Skill{fakeSkill("review", "", ""), fakeSkill("audit", "", ""), fakeSkill("fix", "", ""), fakeSkill("extra", "", "")}, newFakeHistory(entries...))
	m.dangerouslySkipPermissions = true
	m.setNotice("long status "+strings.Repeat("界", 80), true)
	m.applyEvent(protocol.ModelSelected{Provider: "c3-unique-provider", Model: "c3-unique-model"})
	m.applyEvent(protocol.TurnStarted{})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 160, Height: 45})

	view, plain := m.View(), ansi.Strip(m.View())
	assertCanvas(t, view, 160, 45)
	// Header badge and context pane both surface the selection in split layout.
	if got := strings.Count(plain, "c3-unique-provider/c3-unique-model"); got < 1 || got > 2 {
		t.Errorf("selected provider/model occurrences = %d, want 1 or 2 (header ± context):\n%s", got, plain)
	}
	if strings.Contains(view, "\x1b[2J") || strings.ContainsRune(plain, '\x1b') || strings.ContainsAny(plain, "\x00\r\u0085") {
		t.Errorf("dashboard retained dangerous prompt controls:\n%q", view)
	}
	if !strings.Contains(strings.Split(plain, "\n")[0], "working") {
		t.Errorf("working status missing from header:\n%s", plain)
	}
	l := computeLayout(160, 45, m.composer.Height(), m.completionPopupHeight(), true, true)
	if notice := strings.Split(plain, "\n")[l.header+l.transcript]; !strings.Contains(notice, "long status") {
		t.Errorf("notice moved out of its allocated row: %q", notice)
	}
	if strings.Count(plain, "DANGER: permissions bypassed") != 1 {
		t.Errorf("danger uniqueness failed:\n%s", plain)
	}

	recent := welcomeCardBounds(t, dashboardLines(t, plain, l), "recent prompts")
	// Keys card now includes the newline binding, shifting recent prompts one
	// row down versus the pre-onboarding layout.
	if want := (welcomeBounds{top: 22, bottom: 36, left: 0, right: 51}); recent != want {
		t.Errorf("recent prompts geometry = %+v, want %+v", recent, want)
	}
	rows := welcomeCardPromptRows(dashboardLines(t, plain, l), recent)
	if len(rows) != 3 {
		t.Fatalf("recent prompt rows = %d, want exactly 3: %q", len(rows), rows)
	}
	inner := recent.right - recent.left - 1 // columns strictly between the two borders
	for i, row := range rows {
		if got := ansi.StringWidth(row); got > inner {
			t.Errorf("recent prompt row width = %d, want <= card inner width %d: %q", got, inner, row)
		}
		if strings.ContainsAny(row, "\n\r\x1b\x00\u0085") {
			t.Errorf("recent prompt row %d retained a raw control character: %q", i, row)
		}
	}
	for _, evidence := range []struct {
		name     string
		contains []string
	}{
		{"long ASCII", []string{"C3-ASCII-LONG-MARKER"}},
		{"wide Unicode plus combining mark", []string{"C3-WIDE-COMBINING-MARKER", "界", "e\u0301"}},
		{"sanitized control content", []string{"C3-CONTROL-SAFE-MARKER", "�"}},
	} {
		found := false
		for _, row := range rows {
			matches := true
			for _, value := range evidence.contains {
				matches = matches && strings.Contains(row, value)
			}
			if matches {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("recent prompts omitted %s evidence %q: %q", evidence.name, evidence.contains, rows)
		}
	}
	if strings.Contains(plain, "C3-OLD-SENTINEL-MUST-NOT-RENDER") {
		t.Errorf("old history sentinel survived max-three exclusion:\n%s", plain)
	}
	assertVisibleWelcomeCardsClosed(t, m, view)
}

func TestC3ModalDangerGeometry(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{DangerouslySkipPermissions: true})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.modal = newPaletteModal(m.commands, nil, m.currentPaletteAvailability())
	view, plain := m.View(), ansi.Strip(m.View())
	assertCanvas(t, view, 120, 40)
	if strings.Count(plain, "DANGER: permissions bypassed") != 1 {
		t.Errorf("modal duplicated or hid danger:\n%s", plain)
	}
}

func assertNoWelcomeOuterPanel(t *testing.T, view string) {
	t.Helper()
	for _, row := range strings.Split(ansi.Strip(view), "\n") {
		if strings.HasPrefix(row, "╭─ welcome ") {
			t.Errorf("welcome dashboard is wrapped in a titled outer panel: %q", row)
		}
	}
}

type welcomeBounds struct{ top, bottom, left, right int }

// dashboardLines returns only the dashboard allocation, excluding the notice,
// composer, hints, and danger rows that may contain unrelated panel borders.
func dashboardLines(t *testing.T, plain string, l layout) []string {
	t.Helper()
	lines := strings.Split(plain, "\n")
	if len(lines) < l.header+l.transcript {
		t.Fatalf("view has %d rows, dashboard allocation ends at %d", len(lines), l.header+l.transcript)
	}
	return lines[l.header : l.header+l.transcript]
}

// welcomeCardBounds pairs a titled top edge to a bottom edge at the exact same
// columns, rather than accepting an unrelated border elsewhere in the canvas.
func welcomeCardBounds(t *testing.T, lines []string, title string) welcomeBounds {
	t.Helper()
	needle := "╭─ " + title + " "
	for top, line := range lines {
		leftByte := strings.Index(line, needle)
		if leftByte < 0 {
			continue
		}
		rightByte := strings.Index(line[leftByte:], "╮")
		if rightByte < 0 {
			t.Fatalf("%q top border has no right edge: %q", title, line)
		}
		rightByte += leftByte
		left := utf8.RuneCountInString(line[:leftByte])
		right := utf8.RuneCountInString(line[:rightByte])
		for bottom := top + 1; bottom < len(lines); bottom++ {
			row := []rune(lines[bottom])
			if left < len(row) && right < len(row) && row[left] == '╰' && row[right] == '╯' {
				return welcomeBounds{top: top, bottom: bottom, left: left, right: right}
			}
		}
		t.Fatalf("%q card at columns %d..%d has no matching bottom border within dashboard allocation", title, left, right)
	}
	t.Fatalf("visible %q card has no titled top border within dashboard allocation", title)
	return welcomeBounds{}
}

func assertVisibleWelcomeCardsClosed(t *testing.T, m Model, view string) {
	t.Helper()
	if m.focus == focusRight {
		return
	}
	l := computeLayout(m.width, m.height, m.composer.Height(), m.completionPopupHeight(), m.dangerouslySkipPermissions, m.notice != "")
	lines := dashboardLines(t, ansi.Strip(view), l)
	for _, card := range m.welcomeCards(m.services.Auth.Statuses()) {
		welcomeCardBounds(t, lines, card.title)
	}
}

func welcomeCardPromptRows(lines []string, card welcomeBounds) []string {
	var rows []string
	for _, line := range lines[card.top+1 : card.bottom] {
		// Truncate by display columns before locating the enclosing vertical
		// borders so wide prompt runes cannot shift byte or rune offsets.
		prefix := ansi.Truncate(line, card.right+1, "")
		right := strings.LastIndex(prefix, "│")
		if right < 0 {
			continue
		}
		left := strings.LastIndex(prefix[:right], "│")
		if left < 0 {
			continue
		}
		body := prefix[left+len("│") : right]
		if strings.HasPrefix(strings.TrimLeft(body, " "), "· ") {
			rows = append(rows, strings.TrimRight(body, " "))
		}
	}
	return rows
}

func hasWelcomeCard(cards []welcomeCard, title string) bool {
	for _, card := range cards {
		if card.title == title {
			return true
		}
	}
	return false
}

func assertCanvas(t *testing.T, view string, width, height int) {
	t.Helper()
	rows := strings.Split(view, "\n")
	if len(rows) != height {
		t.Fatalf("canvas rows = %d, want %d:\n%s", len(rows), height, ansi.Strip(view))
	}
	for i, row := range rows {
		if got := ansi.StringWidth(row); got != width {
			t.Errorf("canvas row %d width = %d, want %d: %q", i, got, width, ansi.Strip(row))
		}
	}
}
