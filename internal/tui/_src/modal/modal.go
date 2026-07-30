package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// modal is anything that temporarily takes over input, rendered as a
// centered overlay dialog (opencode's dialog-stack pattern). A nil return
// from update closes it. The width passed to view is the dialog box width.
type modal interface {
	update(msg tea.KeyPressMsg) (modal, tea.Cmd)
	view(width int, th theme.Theme) string
}

// permissionCountdownMsg advances an armed permission auto-approve timer.
// Stale ticks (wrong request or generation) are ignored.
type permissionCountdownMsg struct {
	requestID string
	gen       int
}

// permissionModal renders a pending permission ask with once/session/project/
// reject choices. Esc always means reject — dismissal never silently continues.
// When auto-approve is armed, remaining counts down to a single DecisionOnce.
// Large edit diffs collapse by default; d toggles full hunk visibility.
type permissionModal struct {
	req          protocol.PermissionAsked
	ops          chan<- protocol.Op
	choice       int
	state        permissionModalState
	feedback     textinput.Model
	th           theme.Theme
	remaining    int  // seconds left; 0 = no active countdown
	autoGen      int  // bumps to cancel in-flight ticks
	decided      bool // true after a reply is queued (no double-submit)
	diffExpanded bool // full edit diff vs collapsed MaxLines preview
}

type permissionModalState int

const (
	permissionModalChoice permissionModalState = iota
	permissionModalFeedback
)

var permChoices = []struct {
	label    string
	decision protocol.Decision
}{
	{"allow once", protocol.DecisionOnce},
	{"allow session", protocol.DecisionAlways},
	{"allow project", protocol.DecisionProject},
	{"reject", protocol.DecisionReject},
}

func newPermissionModal(req protocol.PermissionAsked, ops chan<- protocol.Op, themes ...theme.Theme) *permissionModal {
	th := theme.Default()
	if len(themes) > 0 {
		th = themes[0]
	}
	in := newTextInput(th, "optional feedback")
	return &permissionModal{req: req, ops: ops, feedback: in, th: th.Resolve()}
}

// permissionCountdownInterval is the delay between soft-approve ticks.
// Production is 1s; tests may shrink it for race coverage without sleeping 15s.
var permissionCountdownInterval = time.Second

// armAutoApprove starts a visible N-second countdown that submits allow-once.
// seconds ≤ 0 is a no-op. Returns the first tick command.
func (m *permissionModal) armAutoApprove(seconds int) tea.Cmd {
	if m == nil || seconds <= 0 || m.decided {
		return nil
	}
	m.remaining = seconds
	m.autoGen++
	return m.countdownTick()
}

func (m *permissionModal) cancelCountdown() {
	if m == nil {
		return
	}
	m.remaining = 0
	m.autoGen++
}

func (m *permissionModal) countdownTick() tea.Cmd {
	if m == nil || m.remaining <= 0 || m.decided {
		return nil
	}
	gen := m.autoGen
	id := m.req.RequestID
	interval := permissionCountdownInterval
	if interval <= 0 {
		interval = time.Second
	}
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return permissionCountdownMsg{requestID: id, gen: gen}
	})
}

// onCountdown handles a tick. At zero remaining it submits allow-once once.
func (m *permissionModal) onCountdown(msg permissionCountdownMsg) (modal, tea.Cmd) {
	if m == nil || m.decided {
		return m, nil
	}
	if msg.requestID != m.req.RequestID || msg.gen != m.autoGen || m.remaining <= 0 {
		return m, nil
	}
	m.remaining--
	if m.remaining > 0 {
		return m, m.countdownTick()
	}
	return nil, m.reply(protocol.DecisionOnce)
}

func (m *permissionModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if m.state == permissionModalFeedback {
		if isEscape(msg) {
			return nil, m.replyWithMessage(protocol.DecisionReject, "")
		}
		switch msg.String() {
		case "enter":
			return nil, m.replyWithMessage(protocol.DecisionReject, strings.TrimSpace(m.feedback.Value()))
		}
		var cmd tea.Cmd
		m.feedback, cmd = m.feedback.Update(msg)
		return m, cmd
	}

	if isEscape(msg) {
		return nil, m.reply(protocol.DecisionReject)
	}
	switch msg.String() {
	case "left", "h", "shift+tab":
		m.choice = (m.choice + len(permChoices) - 1) % len(permChoices)
	case "right", "l", "tab":
		m.choice = (m.choice + 1) % len(permChoices)
	case "d":
		if m.diffCollapsible() {
			m.diffExpanded = !m.diffExpanded
		}
	case "1", "y":
		return nil, m.reply(protocol.DecisionOnce)
	case "2", "s":
		return nil, m.reply(protocol.DecisionAlways)
	case "3", "p":
		return nil, m.reply(protocol.DecisionProject)
	case "4", "n":
		m.cancelCountdown()
		m.state = permissionModalFeedback
		return m, m.feedback.Focus()
	case "enter":
		if permChoices[m.choice].decision == protocol.DecisionReject {
			m.cancelCountdown()
			m.state = permissionModalFeedback
			return m, m.feedback.Focus()
		}
		return nil, m.reply(permChoices[m.choice].decision)
	}
	return m, nil
}

// diffCollapsible reports whether the permission ask carries an edit diff that
// exceeds the collapsed modal preview window.
func (m *permissionModal) diffCollapsible() bool {
	if m == nil {
		return false
	}
	meta, ok := parseEditMetadata(m.req.Metadata)
	if !ok {
		return false
	}
	return ui.DiffExceeds(meta.OldString, meta.NewString, diffPreviewMaxLinesModal) || m.diffExpanded
}

func (m *permissionModal) reply(d protocol.Decision) tea.Cmd {
	return m.replyWithMessage(d, "")
}

func (m *permissionModal) replyWithMessage(d protocol.Decision, message string) tea.Cmd {
	if m.decided {
		return nil
	}
	m.decided = true
	m.cancelCountdown()
	req := m.req
	ops := m.ops
	return func() tea.Msg {
		ops <- protocol.PermissionReply{RequestID: req.RequestID, Decision: d, Message: message}
		return nil
	}
}

func (m *permissionModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	cursorWidth := max(1, 1)
	heading := wrapToWidth(st.WarningStrong.Render("Permission required: "+m.req.Permission), inner)
	detail := wrapToWidth(st.Text.Render(strings.Join(m.req.Patterns, "\n")), inner)

	// Shared edit diff preview for choice and feedback states. Patterns already
	// show the path, so Path is left empty to avoid duplication. Large hunks
	// collapse by default; d toggles the full body.
	var diffSection string
	if meta, ok := parseEditMetadata(m.req.Metadata); ok {
		maxLines := diffPreviewMaxLinesModal
		moreHint := ""
		if m.diffExpanded {
			maxLines = diffExpandedMaxLines(meta)
		} else if ui.DiffExceeds(meta.OldString, meta.NewString, diffPreviewMaxLinesModal) {
			moreHint = "d to expand"
		}
		diffBlock := ui.DiffPreview(th, ui.DiffPreviewOpts{
			Path:      "",
			Old:       meta.OldString,
			New:       meta.NewString,
			MaxLines:  maxLines,
			Width:     inner,
			ShowStats: true,
			MoreHint:  moreHint,
		})
		if diffBlock != "" {
			diffSection = "\n" + diffBlock
		}
	}

	var countdownLine string
	if m.remaining > 0 && m.state == permissionModalChoice {
		// Product copy for soft-approve / config auto-allow (updates once/sec).
		line := "Auto-approving once in " + itoa(m.remaining) + "s…"
		countdownLine = "\n" + wrapToWidth(st.Warning.Render(line), inner)
	}

	if m.state == permissionModalFeedback {
		prompt := st.Text.Render("Optional feedback for the rejection:")
		m.feedback.SetWidth(max(1, inner-ansi.StringWidth(m.feedback.Prompt)-cursorWidth))
		m.feedback.SetValue(m.feedback.Value())
		body := heading + "\n" + detail + diffSection + strings.Repeat("\n", max(1, th.Spacing.SM)) + prompt + "\n" + m.feedback.View()
		return ui.Dialog(th, ui.DialogOpts{
			Title: "permission",
			Hint:  dotJoin(th, "enter reject with feedback", "esc reject without feedback"),
			Width: width,
			Tone:  ui.ToneWarning,
		}, body)
	}

	choices := make([]string, len(permChoices))
	plain := 0
	for i, c := range permChoices {
		label := itoa(i+1) + ")" + themedSpace(th.Spacing.Label) + c.label
		style := st.Muted
		if i == m.choice {
			style = st.SelectedUnderline
		}
		choices[i] = style.Render(label)
		plain += lipgloss.Width(label) + lipgloss.Width(themedSpace(th.Spacing.SM))
	}
	sep := themedSpace(th.Spacing.SM)
	if plain > inner {
		sep = "\n" // stack choices when the row would overflow a narrow dialog
	}
	body := heading + "\n" + detail + diffSection + countdownLine + strings.Repeat("\n", max(1, th.Spacing.SM)) + strings.Join(choices, sep)
	hints := []string{"←/→ select", "enter confirm", "esc reject"}
	if m.diffCollapsible() {
		if m.diffExpanded {
			hints = append(hints, "d collapse diff")
		} else {
			hints = append(hints, "d expand diff")
		}
	}
	if m.remaining > 0 {
		hints = append(hints, "auto-approve "+itoa(m.remaining)+"s")
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "permission",
		Hint:  dotJoin(th, hints...),
		Width: width,
		Tone:  ui.ToneWarning,
	}, body)
}

// wrapToWidth hard-wraps already-styled text to width display cells, preserving
// ANSI. It is how modals pre-wrap bodies before handing them to ui.Panel/Dialog
// (which truncate, not wrap).
func wrapToWidth(s string, width int) string {
	if width < 1 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}
