package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
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
		top := strings.Split(view, "\n")[0]
		// Two-column welcome packs "get started" and "keys" on the first chrome row.
		twoCol := strings.Contains(top, "get started") && strings.Contains(top, "keys")
		if width < 2*welcomeCardMinWidth+th.Spacing.SM && twoCol {
			t.Errorf("%d unexpectedly used a second column: %q", width, view)
		}
	}
	th.Spacing = th.Spacing.WithSM(5)
	m, _ = newAppTestModelWithOptions(Options{Theme: &th})
	early := strings.Split(ansi.Strip(m.welcomeView(2*welcomeCardMinWidth+4, 12)), "\n")[0]
	if strings.Contains(early, "get started") && strings.Contains(early, "keys") {
		t.Error("custom gutter threshold split too early")
	}
	split := strings.Split(ansi.Strip(m.welcomeView(2*welcomeCardMinWidth+5, 12)), "\n")[0]
	if !(strings.Contains(split, "get started") && strings.Contains(split, "keys")) {
		t.Error("custom gutter threshold did not split")
	}

	m, _ = newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	plain := ansi.Strip(viewString(m))
	// Header always uses the compact wordmark; full logo band needs more height.
	if !strings.Contains(plain, "⚡ strike") {
		t.Errorf("header brand missing compact wordmark:\n%s", plain)
	}
	assertNoWelcomeOuterPanel(t, viewString(m))
	m.applyEvent(protocol.UserMessage{Text: "populated"})
	m.refreshViewport()
	// Panel title is the auto-title (first user text) once the transcript has cells.
	if plain = ansi.Strip(viewString(m)); !strings.Contains(plain, "populated") || strings.Contains(plain, "get started") {
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
			// Solid chrome: cards are fixed-width surfaces joined by Spacing.SM spaces.
			// Measure from end of first card title zone to start of second title.
			firstTitle := strings.Index(row, "get started")
			secondTitle := strings.Index(row, "keys")
			if firstTitle < 0 || secondTitle < 0 || secondTitle <= firstTitle {
				t.Fatalf("two-column titles missing from %q", row)
			}
			// Each card is welcomeCardMinWidth; gutter is the gap between card blocks.
			firstEnd := welcomeCardMinWidth
			if firstEnd > len(row) {
				t.Fatalf("row shorter than one card: %q", row)
			}
			secondStart := firstEnd + th.Spacing.SM
			if secondStart > len(row) || secondTitle < secondStart-1 {
				// Fall back to measuring space run between non-space clusters after first title.
			}
			gutter := ""
			if firstEnd+th.Spacing.SM <= len(row) {
				gutter = row[firstEnd : firstEnd+th.Spacing.SM]
			}
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
		plain := ansi.Strip(viewString(m))
		for _, want := range []string{"get started", "keys", "agents & skills", "recent prompts"} {
			if !strings.Contains(plain, want) {
				t.Errorf("danger=%v dropped eligible %q:\n%s", danger, want, plain)
			}
		}
		assertCanvas(t, viewString(m), 80, 24)
	}

	setTUITrueColor(t)
	th := theme.Default()
	th.SurfaceFocus = fixedColor("#010203")
	th.SurfaceMuted = fixedColor("#040506")
	th.OverlayScrim = fixedColor("#070809")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(viewString(m), rgbBGSGR("#010203")) || !strings.Contains(viewString(m), rgbBGSGR("#040506")) {
		t.Fatal("focused and dim dashboard surfaces are not tokenized")
	}
	m.focus = focusRight
	m.reflow()
	if !strings.Contains(viewString(m), rgbBGSGR("#010203")) || !strings.Contains(viewString(m), rgbBGSGR("#040506")) {
		t.Fatal("right focus did not preserve focused/dim surface tokens")
	}
	m.modal = &appProbeModal{}
	m.reflow()
	view := viewString(m)
	if strings.Contains(view, rgbBGSGR("#010203")) || !strings.Contains(view, rgbSGR("#070809")) {
		t.Fatal("modal did not scrim dashboard background")
	}
}

func TestC3WelcomeProviderAndPromptLimits(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	statuses := []host.ProviderStatus{{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"}, {Name: "five"}, {Name: "six"}, {Name: "seven"}}
	body := ansi.Strip(m.welcomeProviders(statuses, 30, 8))
	// Six provider rows + /provider action + "type below · enter to send" tip.
	if strings.Count(body, "\n")+1 != 8 || !strings.Contains(body, "/provider") || strings.Contains(body, "seven") {
		t.Errorf("provider card did not cap six rows plus action and tip: %q", body)
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
	raw := viewString(hostModel)
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
	plain := ansi.Strip(viewString(m))
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
		plain := ansi.Strip(viewString(m))
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
	lines := strings.Split(ansi.Strip(viewString(m)), "\n")
	l := computeLayout(80, 24, m.composer.Height(), 0, false, m.noticeRowsFor(80))
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
			view, plain := viewString(m), ansi.Strip(viewString(m))
			assertCanvas(t, view, tt.width, tt.height)
			// Full logo band is intentional chrome when the dashboard has room;
			// compact header brand is always present on left-focused layouts.
			if tt.focus != focusRight && !strings.Contains(plain, "⚡ strike") && !strings.Contains(plain, "S T R I K E") {
				t.Errorf("welcome missing logo/header brand:\n%s", plain)
			}
			assertNoWelcomeOuterPanel(t, viewString(m))
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

	view, plain := viewString(m), ansi.Strip(viewString(m))
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
	// Split layout budgets the left stack at the left pane width; multi-line
	// notices (wide CJK status) reserve noticeRowsFor rows, not a hard-coded 1.
	leftW := computePaneGeometry(160, m.th.Resolve().Spacing.XS, focusLeft).leftWidth
	noticeRows := m.noticeRowsFor(leftW)
	if noticeRows < 2 {
		t.Fatalf("long CJK notice should wrap to multiple rows, got %d", noticeRows)
	}
	l := computeLayout(leftW, 45, m.composer.Height(), m.completionPopupHeight(), true, noticeRows)
	lines := strings.Split(plain, "\n")
	noticeStart := l.header + l.transcript
	if noticeStart >= len(lines) || !strings.Contains(lines[noticeStart], "long status") {
		got := ""
		if noticeStart < len(lines) {
			got = lines[noticeStart]
		}
		t.Errorf("notice moved out of its allocated row (start=%d rows=%d): %q", noticeStart, noticeRows, got)
	}
	if strings.Count(plain, "DANGER: permissions bypassed") != 1 {
		t.Errorf("danger uniqueness failed:\n%s", plain)
	}

	dash := dashboardLines(t, plain, l)
	recent := welcomeCardBounds(t, dash, "recent prompts")
	if recent.right-recent.left+1 < welcomeCardMinWidth-1 {
		t.Errorf("recent prompts card too narrow: %+v", recent)
	}
	rows := welcomeCardPromptRows(dash, recent)
	if len(rows) != 3 {
		t.Fatalf("recent prompt rows = %d, want exactly 3: %q (card=%+v)", len(rows), rows, recent)
	}
	// Solid chrome: body fits PanelInnerWidth of the card outer width.
	inner := ui.PanelInnerWidth(m.th, recent.right-recent.left+1)
	for i, row := range rows {
		if got := ansi.StringWidth(row); got > inner+1 { // +1 tolerates pad-edge trim variance
			t.Errorf("recent prompt row width = %d, want <= card inner width %d: %q", got, inner, row)
		}
		if strings.ContainsAny(row, "\n\r\x1b\x00\u0085") {
			t.Errorf("recent prompt row %d retained a raw control character: %q", i, row)
		}
	}
	// Evidence may be ellipsis-truncated inside the card; require distinctive prefixes.
	for _, evidence := range []struct {
		name string
		want string
	}{
		{"long ASCII", "C3-ASCII-LONG-MARKER"},
		{"wide Unicode", "C3-WIDE-COMBINING-MARKE"},
		{"sanitized control content", "C3-CONTROL-SAFE-MARKER"},
	} {
		found := false
		for _, row := range rows {
			if strings.Contains(row, evidence.want) {
				found = true
				break
			}
		}
		if !found {
			// Also accept full marker if the card is wide enough to keep it.
			if evidence.want == "C3-WIDE-COMBINING-MARKE" {
				for _, row := range rows {
					if strings.Contains(row, "C3-WIDE-COMBINING-MARKER") {
						found = true
						break
					}
				}
			}
		}
		if !found {
			t.Errorf("recent prompts omitted %s evidence %q: %q", evidence.name, evidence.want, rows)
		}
	}
	// Full plain text still carries wide/control evidence somewhere in the card body.
	cardPlain := strings.Join(dash[recent.top:recent.bottom+1], "\n")
	if !strings.Contains(cardPlain, "界") || !strings.Contains(cardPlain, "e\u0301") {
		t.Errorf("recent prompts card dropped wide/combining evidence:\n%s", cardPlain)
	}
	if !strings.Contains(cardPlain, "�") {
		t.Errorf("recent prompts card dropped sanitized control replacement:\n%s", cardPlain)
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
	view, plain := viewString(m), ansi.Strip(viewString(m))
	assertCanvas(t, view, 120, 40)
	if strings.Count(plain, "DANGER: permissions bypassed") != 1 {
		t.Errorf("modal duplicated or hid danger:\n%s", plain)
	}
}

func assertNoWelcomeOuterPanel(t *testing.T, view string) {
	t.Helper()
	for _, row := range strings.Split(ansi.Strip(view), "\n") {
		trimmed := strings.TrimSpace(row)
		if trimmed == "welcome" || strings.HasPrefix(trimmed, "welcome ") {
			// Outer titled panel would be a lone chrome title for the whole dashboard.
			if !strings.Contains(row, "get started") && !strings.Contains(row, "keys") {
				t.Errorf("welcome dashboard is wrapped in a titled outer panel: %q", row)
			}
		}
	}
}

type welcomeBounds struct{ top, bottom, left, right int }

// dashboardLines returns only the dashboard allocation, excluding the notice,
// composer, hints, and danger rows that may contain unrelated panel chrome.
func dashboardLines(t *testing.T, plain string, l layout) []string {
	t.Helper()
	lines := strings.Split(plain, "\n")
	if len(lines) < l.header+l.transcript {
		t.Fatalf("view has %d rows, dashboard allocation ends at %d", len(lines), l.header+l.transcript)
	}
	return lines[l.header : l.header+l.transcript]
}

// welcomeCardBounds locates a solid-chrome card by its title row.
func welcomeCardBounds(t *testing.T, lines []string, title string) welcomeBounds {
	t.Helper()
	for top, line := range lines {
		titleByte := strings.Index(line, title)
		if titleByte < 0 {
			continue
		}
		titleCol := ansi.StringWidth(line[:titleByte])
		left := max(0, titleCol-1)
		lineW := ansi.StringWidth(line)
		// Default single-column card: min width. Expand when the title row has
		// no second card title to the right (full-width recent/agents cards).
		right := min(lineW-1, left+welcomeCardMinWidth-1)
		rest := strings.TrimSpace(sliceDisplayCols(line, right+1, lineW))
		if rest == "" {
			right = lineW - 1
		} else {
			// Two-column: stop before the next title cluster.
			for _, other := range []string{"get started", "keys", "agents & skills", "recent prompts"} {
				if other == title {
					continue
				}
				if at := strings.Index(line, other); at > titleByte {
					otherCol := ansi.StringWidth(line[:at])
					// Gutter sits between cards; card ends just before other pad.
					right = max(left, otherCol-2)
					break
				}
			}
		}
		if right <= left {
			continue
		}
		bottom := top
		for r := top + 1; r < len(lines); r++ {
			span := stripWelcomeChromePrefix(sliceDisplayCols(lines[r], left, right+1))
			if r > top+1 && span != "" && !strings.HasPrefix(span, "·") && !strings.HasPrefix(span, "◦") && !strings.HasPrefix(span, "✓") {
				isTitle := false
				for _, other := range []string{"get started", "keys", "agents & skills", "recent prompts"} {
					if other != title && strings.HasPrefix(span, other) {
						isTitle = true
						break
					}
				}
				if isTitle {
					break
				}
			}
			bottom = r
		}
		return welcomeBounds{top: top, bottom: bottom, left: left, right: right}
	}
	t.Fatalf("visible %q card has no titled top chrome within dashboard allocation", title)
	return welcomeBounds{}
}

// stripWelcomeChromePrefix drops outer-pane pad/focus-bar cells so card body
// detection sees content glyphs (· ◦ ✓) rather than the thin FocusBar rule.
func stripWelcomeChromePrefix(s string) string {
	s = strings.TrimSpace(s)
	if fb := theme.DefaultIcons().FocusBar; fb != "" && strings.HasPrefix(s, fb) {
		s = strings.TrimSpace(strings.TrimPrefix(s, fb))
	}
	return s
}

func assertVisibleWelcomeCardsClosed(t *testing.T, m Model, view string) {
	t.Helper()
	if m.focus == focusRight {
		return
	}
	l := computeLayout(m.width, m.height, m.composer.Height(), m.completionPopupHeight(), m.dangerouslySkipPermissions, m.noticeRowsFor(m.width))
	lines := dashboardLines(t, ansi.Strip(view), l)
	for _, card := range m.welcomeCards(m.services.Auth.Statuses()) {
		welcomeCardBounds(t, lines, card.title)
	}
}

func welcomeCardPromptRows(lines []string, card welcomeBounds) []string {
	var rows []string
	for _, line := range lines[card.top+1 : card.bottom] {
		body := stripWelcomeChromePrefix(sliceDisplayCols(line, card.left, card.right+1))
		if strings.HasPrefix(body, "· ") {
			rows = append(rows, body)
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
