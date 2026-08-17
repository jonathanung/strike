package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

const permissionCmdTimeout = 2 * time.Second

func TestPermissionModalRejectChoicesOpenFeedbackWithoutReply(t *testing.T) {
	tests := []struct {
		name    string
		prepare []tea.KeyPressMsg
		key     tea.KeyPressMsg
	}{
		{name: "option 4", key: permissionKey("4")},
		{name: "n", key: permissionKey("n")},
		{
			name:    "enter on selected reject",
			prepare: []tea.KeyPressMsg{permissionKey("right"), permissionKey("right"), permissionKey("right")},
			key:     permissionKey("enter"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newTestPermissionModal(t.Name())
			for _, key := range tt.prepare {
				next, cmd := m.update(key)
				if next == nil {
					t.Fatalf("modal closed while selecting reject with %q", key.String())
				}
				runPermissionCmd(t, cmd)
			}

			next, cmd := m.update(tt.key)
			if next == nil {
				t.Fatal("reject choice closed modal instead of requesting feedback")
			}
			runPermissionCmd(t, cmd)
			if !strings.Contains(strings.ToLower(next.view(70, theme.Default())), "optional feedback") {
				t.Fatal("reject choice did not show the feedback view")
			}
			assertNoPermissionReply(t, ops)
		})
	}
}

func TestPermissionModalFeedbackEnterEmitsTrimmedRejection(t *testing.T) {
	m, ops := newTestPermissionModal("feedback-request")
	enterPermissionFeedback(t, m)

	next, cmd := m.update(permissionKey("  use a safer command  "))
	if next == nil {
		t.Fatal("typing feedback closed modal")
	}
	runPermissionCmd(t, cmd)

	next, cmd = m.update(permissionKey("enter"))
	if next != nil {
		t.Fatal("enter after feedback did not close modal")
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "feedback-request", protocol.DecisionReject, "use a safer command")
}

func TestPermissionModalEscapeRejectsWithoutFeedback(t *testing.T) {
	tests := []struct {
		name     string
		feedback bool
	}{
		{name: "from initial choice"},
		{name: "from feedback", feedback: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newTestPermissionModal(t.Name())
			if tt.feedback {
				enterPermissionFeedback(t, m)
				next, cmd := m.update(permissionKey("ignored feedback"))
				if next == nil {
					t.Fatal("typing feedback closed modal")
				}
				runPermissionCmd(t, cmd)
			}

			next, cmd := m.update(permissionKey("esc"))
			if next != nil {
				t.Fatal("escape did not close modal")
			}
			reply := receiveSinglePermissionReply(t, ops, cmd)
			assertPermissionReply(t, reply, t.Name(), protocol.DecisionReject, "")
		})
	}
}

func TestPermissionModalAllowsRemainImmediateAndHaveNoFeedback(t *testing.T) {
	tests := []struct {
		name     string
		keys     []tea.KeyPressMsg
		decision protocol.Decision
	}{
		{name: "option 1", keys: []tea.KeyPressMsg{permissionKey("1")}, decision: protocol.DecisionOnce},
		{name: "y", keys: []tea.KeyPressMsg{permissionKey("y")}, decision: protocol.DecisionOnce},
		{name: "enter on allow once", keys: []tea.KeyPressMsg{permissionKey("enter")}, decision: protocol.DecisionOnce},
		{name: "option 2", keys: []tea.KeyPressMsg{permissionKey("2")}, decision: protocol.DecisionAlways},
		{name: "s", keys: []tea.KeyPressMsg{permissionKey("s")}, decision: protocol.DecisionAlways},
		{
			name:     "enter on allow session",
			keys:     []tea.KeyPressMsg{permissionKey("right"), permissionKey("enter")},
			decision: protocol.DecisionAlways,
		},
		{name: "option 3", keys: []tea.KeyPressMsg{permissionKey("3")}, decision: protocol.DecisionProject},
		{name: "p", keys: []tea.KeyPressMsg{permissionKey("p")}, decision: protocol.DecisionProject},
		{
			name:     "enter on allow project",
			keys:     []tea.KeyPressMsg{permissionKey("right"), permissionKey("right"), permissionKey("enter")},
			decision: protocol.DecisionProject,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newTestPermissionModal(t.Name())
			for i, key := range tt.keys {
				next, cmd := m.update(key)
				if i < len(tt.keys)-1 {
					if next == nil {
						t.Fatalf("modal closed before final key %q", key.String())
					}
					runPermissionCmd(t, cmd)
					continue
				}
				if next != nil {
					t.Fatalf("allow key %q did not close modal", key.String())
				}
				reply := receiveSinglePermissionReply(t, ops, cmd)
				assertPermissionReply(t, reply, t.Name(), tt.decision, "")
			}
		})
	}
}

func TestPermissionModalFeedbackIsOneLineAndViewExplainsControls(t *testing.T) {
	m, ops := newTestPermissionModal("one-line-request")
	enterPermissionFeedback(t, m)

	next, cmd := m.update(permissionKey("first\nsecond\tthird"))
	if next == nil {
		t.Fatal("pasting feedback closed modal")
	}
	runPermissionCmd(t, cmd)
	view := strings.ToLower(next.view(70, theme.Default()))
	for _, want := range []string{
		"optional feedback for the rejection",
		"enter reject with feedback",
		"esc reject without feedback",
		"first second third",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("feedback view does not contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "first\nsecond") {
		t.Errorf("feedback rendered on multiple lines:\n%s", view)
	}

	next, cmd = m.update(permissionKey("enter"))
	if next != nil {
		t.Fatal("enter after feedback did not close modal")
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "one-line-request", protocol.DecisionReject, "first second third")
}

func TestPermissionModalFeedbackGeometryHonorsThemeSpacingAndInputCursor(t *testing.T) {
	setTUITrueColor(t)
	for _, tt := range []struct {
		name        string
		spacing     theme.Spacing
		inputCursor string
	}{
		{name: "default", spacing: theme.Default().Spacing},
		{name: "zero extra-small spacing", spacing: theme.NewSpacing(0, 2, 3, 4)},
		{name: "wide extra-small spacing and multicell cursor", spacing: theme.NewSpacing(4, 2, 3, 4), inputCursor: ">>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			th := theme.Default()
			th.Spacing = tt.spacing
			if tt.inputCursor != "" {
				th.Icons.InputCursor = tt.inputCursor
			}
			m, _ := newTestPermissionModal(t.Name())
			m.th = th.Resolve()
			m.feedback = newTextInput(th, "optional feedback")
			enterPermissionFeedback(t, m)
			m.feedback.SetValue(strings.Repeat("feedback ", 20))
			m.feedback.Focus()

			const width = 46
			inner := ui.PanelInnerWidth(th, width)
			_ = m.view(width, th)
			feedback := m.feedback.View()
			if got := lipgloss.Width(feedback); got > inner {
				t.Errorf("feedback width = %d, want <= dialog inner width %d: %q", got, inner, ansi.Strip(feedback))
			}
			if strings.Contains(ansi.Strip(feedback), th.Icons.Ellipsis) {
				t.Errorf("feedback input used an ellipsis instead of retaining its cursor: %q", ansi.Strip(feedback))
			}
			if !hasReverseVideo(feedback) {
				t.Errorf("feedback input lost its static cursor: %q", feedback)
			}

			view := m.view(width, th)
			for i, row := range strings.Split(view, "\n") {
				if got := lipgloss.Width(row); got != width {
					t.Errorf("dialog row %d width = %d, want exact outer width %d: %q", i, got, width, ansi.Strip(row))
				}
			}
		})
	}
}

func TestPermissionModalEditMetadataShowsDiffInChoiceAndFeedback(t *testing.T) {
	meta := json.RawMessage(`{"oldString":"foo","newString":"bar","count":1}`)
	req := protocol.PermissionAsked{
		RequestID:  "edit-diff-req",
		Permission: "edit",
		Patterns:   []string{"file.go"},
		Metadata:   meta,
	}
	m, _ := newTestPermissionModalFrom(req)

	const width = 70
	th := theme.Default()
	choiceView := ansi.Strip(m.view(width, th))
	for _, want := range []string{"-foo", "+bar", "permission", "file.go", "+1", "-1"} {
		if !strings.Contains(choiceView, want) {
			t.Errorf("choice view missing %q:\n%s", want, choiceView)
		}
	}
	// geometry: every dialog row exact outer width
	for i, row := range strings.Split(m.view(width, th), "\n") {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("choice row %d width = %d, want %d: %q", i, got, width, ansi.Strip(row))
		}
	}

	enterPermissionFeedback(t, m)
	feedbackView := ansi.Strip(m.view(width, th))
	for _, want := range []string{"-foo", "+bar", "optional feedback", "+1", "-1"} {
		if !strings.Contains(feedbackView, want) {
			t.Errorf("feedback view missing %q:\n%s", want, feedbackView)
		}
	}
	for i, row := range strings.Split(m.view(width, th), "\n") {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("feedback row %d width = %d, want %d: %q", i, got, width, ansi.Strip(row))
		}
	}
}

func TestPermissionModalWithoutEditMetadataHasNoFalseDiff(t *testing.T) {
	// bash-like ask: no metadata → permission chrome only, no +/- hunk body
	m, _ := newTestPermissionModal("bash-no-meta")
	plain := ansi.Strip(m.view(70, theme.Default()))
	if !strings.Contains(strings.ToLower(plain), "permission") {
		t.Errorf("missing permission chrome:\n%s", plain)
	}
	if !strings.Contains(plain, "rm important.txt") {
		t.Errorf("missing pattern:\n%s", plain)
	}
	// write-shaped or absent meta must not invent a unified diff body
	for _, line := range strings.Split(plain, "\n") {
		trimmed := strings.TrimSpace(line)
		// skip choice labels like "1) allow once"
		if strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "─") &&
			!strings.Contains(trimmed, "select") && len(trimmed) > 1 {
			// a real diff delete line would look like "-content"; dialog borders use box chars
			if isLikelyDiffMarkerLine(trimmed) {
				t.Errorf("unexpected diff-like line without edit metadata: %q\nfull:\n%s", trimmed, plain)
			}
		}
	}
}

func TestPermissionModalTallEditDiffKeepsExactWidth(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 20; i++ {
		oldB.WriteString("old line content that is fairly long ")
		oldB.WriteByte(byte('a' + i%26))
		oldB.WriteByte('\n')
		newB.WriteString("new line content that is fairly long ")
		newB.WriteByte(byte('A' + i%26))
		newB.WriteByte('\n')
	}
	meta, err := json.Marshal(map[string]any{
		"oldString": strings.TrimSuffix(oldB.String(), "\n"),
		"newString": strings.TrimSuffix(newB.String(), "\n"),
		"count":     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.PermissionAsked{
		RequestID:  "tall-edit",
		Permission: "edit",
		Patterns:   []string{"big.go"},
		Metadata:   meta,
	}
	m, _ := newTestPermissionModalFrom(req)
	const width = 46
	th := theme.Default()
	view := m.view(width, th)
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "more lines") && !strings.Contains(plain, "-old") {
		t.Errorf("expected tall edit diff content:\n%s", plain)
	}
	for i, row := range strings.Split(view, "\n") {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("row %d width = %d, want %d: %q", i, got, width, ansi.Strip(row))
		}
	}
}

func TestPermissionModalLargeDiffExpandCollapse(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&oldB, "old-perm-%d\n", i)
		fmt.Fprintf(&newB, "new-perm-%d\n", i)
	}
	meta, err := json.Marshal(map[string]any{
		"oldString": strings.TrimSuffix(oldB.String(), "\n"),
		"newString": strings.TrimSuffix(newB.String(), "\n"),
		"count":     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.PermissionAsked{
		RequestID:  "expand-diff",
		Permission: "edit",
		Patterns:   []string{"big.go"},
		Metadata:   meta,
	}
	m, _ := newTestPermissionModalFrom(req)
	if !m.diffCollapsible() {
		t.Fatal("large edit permission diff should be collapsible")
	}
	const width = 70
	th := theme.Default()
	plain := ansi.Strip(m.view(width, th))
	if !strings.Contains(plain, "more lines") {
		t.Errorf("collapsed missing truncation:\n%s", plain)
	}
	if !strings.Contains(plain, "d to expand") {
		t.Errorf("collapsed missing expand hint:\n%s", plain)
	}
	if !strings.Contains(plain, "d expand diff") {
		t.Errorf("choice hint missing expand affordance:\n%s", plain)
	}
	if strings.Contains(plain, "+new-perm-11") {
		t.Errorf("collapsed should hide late insert:\n%s", plain)
	}

	// d expands
	next, _ := m.update(tea.KeyPressMsg{Code: 'd', Text: string([]rune{'d'})})
	pm, ok := next.(*permissionModal)
	if !ok || pm == nil || !pm.diffExpanded {
		t.Fatalf("d should expand diff: next=%T expanded=%v", next, pm != nil && pm.diffExpanded)
	}
	plain = ansi.Strip(pm.view(width, th))
	if strings.Contains(plain, "more lines") {
		t.Errorf("expanded still truncated:\n%s", plain)
	}
	if !strings.Contains(plain, "d collapse diff") {
		t.Errorf("expanded missing collapse hint:\n%s", plain)
	}
	for _, want := range []string{"-old-perm-0", "-old-perm-11", "+new-perm-0", "+new-perm-11"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expanded missing %q:\n%s", want, plain)
		}
	}

	// d collapses again
	next, _ = pm.update(tea.KeyPressMsg{Code: 'd', Text: string([]rune{'d'})})
	pm, ok = next.(*permissionModal)
	if !ok || pm == nil || pm.diffExpanded {
		t.Fatalf("d should collapse diff: expanded=%v", pm != nil && pm.diffExpanded)
	}
	plain = ansi.Strip(pm.view(width, th))
	if !strings.Contains(plain, "more lines") || !strings.Contains(plain, "d to expand") {
		t.Errorf("re-collapsed missing affordance:\n%s", plain)
	}

	// Short edit: d is a no-op
	short, _ := newTestPermissionModalFrom(protocol.PermissionAsked{
		RequestID:  "short-diff",
		Permission: "edit",
		Patterns:   []string{"s.go"},
		Metadata:   json.RawMessage(`{"oldString":"a","newString":"b"}`),
	})
	if short.diffCollapsible() {
		t.Fatal("short edit should not be collapsible")
	}
	next, _ = short.update(tea.KeyPressMsg{Code: 'd', Text: string([]rune{'d'})})
	sm, ok := next.(*permissionModal)
	if !ok || sm.diffExpanded {
		t.Fatalf("d on short diff should not expand: expanded=%v", sm != nil && sm.diffExpanded)
	}
}

// isLikelyDiffMarkerLine reports whether s looks like a unified-diff body line
// (leading +/- followed by content), as opposed to UI chrome.
func isLikelyDiffMarkerLine(s string) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] != '+' && s[0] != '-' {
		return false
	}
	// stats like "+0" / choice chrome shouldn't appear without edit meta either,
	// but "+1)" style choice numbers are not diff lines.
	if s[1] >= '0' && s[1] <= '9' {
		return false
	}
	return true
}

func TestPermissionModalAutoApproveFiresAllowOnce(t *testing.T) {
	m, ops := newTestPermissionModal("auto-fire")
	if cmd := m.armAutoApprove(3); cmd == nil {
		t.Fatal("armAutoApprove returned nil cmd")
	}
	view := ansi.Strip(m.view(70, theme.Default()))
	if !strings.Contains(view, "Auto-approving once in 3s…") {
		t.Fatalf("view missing countdown:\n%s", view)
	}

	// Two ticks leave remaining=1; third fires allow-once.
	for i := 0; i < 2; i++ {
		next, cmd := m.onCountdown(permissionCountdownMsg{requestID: "auto-fire", gen: m.autoGen})
		if next == nil {
			t.Fatalf("countdown closed early on tick %d", i+1)
		}
		pm, ok := next.(*permissionModal)
		if !ok {
			t.Fatalf("modal type = %T", next)
		}
		m = pm
		runPermissionCmd(t, cmd)
		assertNoPermissionReply(t, ops)
	}
	if m.remaining != 1 {
		t.Fatalf("remaining = %d, want 1", m.remaining)
	}

	next, cmd := m.onCountdown(permissionCountdownMsg{requestID: "auto-fire", gen: m.autoGen})
	if next != nil {
		t.Fatal("final tick did not close modal")
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "auto-fire", protocol.DecisionOnce, "")

	// Stale post-decision tick must not double-submit.
	if _, cmd := m.onCountdown(permissionCountdownMsg{requestID: "auto-fire", gen: m.autoGen}); cmd != nil {
		runPermissionCmd(t, cmd)
	}
	assertNoPermissionReply(t, ops)
}

func TestPermissionModalEscCancelsAutoApprove(t *testing.T) {
	m, ops := newTestPermissionModal("auto-esc")
	_ = m.armAutoApprove(5)
	gen := m.autoGen

	next, cmd := m.update(permissionKey("esc"))
	if next != nil {
		t.Fatal("esc did not close modal")
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "auto-esc", protocol.DecisionReject, "")

	// In-flight tick after cancel must not allow.
	if _, tickCmd := m.onCountdown(permissionCountdownMsg{requestID: "auto-esc", gen: gen}); tickCmd != nil {
		runPermissionCmd(t, tickCmd)
	}
	assertNoPermissionReply(t, ops)
}

func TestPermissionModalExplicitAllowCancelsCountdown(t *testing.T) {
	m, ops := newTestPermissionModal("auto-y")
	_ = m.armAutoApprove(10)
	gen := m.autoGen

	next, cmd := m.update(permissionKey("y"))
	if next != nil {
		t.Fatal("y did not close modal")
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "auto-y", protocol.DecisionOnce, "")

	if _, tickCmd := m.onCountdown(permissionCountdownMsg{requestID: "auto-y", gen: gen}); tickCmd != nil {
		runPermissionCmd(t, tickCmd)
	}
	assertNoPermissionReply(t, ops)
}

func TestPermissionModalRejectPathCancelsCountdown(t *testing.T) {
	m, ops := newTestPermissionModal("auto-n")
	_ = m.armAutoApprove(8)
	genBefore := m.autoGen

	next, cmd := m.update(permissionKey("n"))
	if next == nil {
		t.Fatal("n closed modal instead of feedback")
	}
	runPermissionCmd(t, cmd)
	if m.remaining != 0 {
		t.Fatalf("remaining = %d after reject path, want 0", m.remaining)
	}
	if m.autoGen == genBefore {
		t.Fatal("reject path did not bump autoGen")
	}
	assertNoPermissionReply(t, ops)

	if _, tickCmd := m.onCountdown(permissionCountdownMsg{requestID: "auto-n", gen: genBefore}); tickCmd != nil {
		runPermissionCmd(t, tickCmd)
	}
	assertNoPermissionReply(t, ops)
}

func TestPermissionModalStaleCountdownIgnored(t *testing.T) {
	m, ops := newTestPermissionModal("stale")
	_ = m.armAutoApprove(2)
	gen := m.autoGen

	next, cmd := m.onCountdown(permissionCountdownMsg{requestID: "other", gen: gen})
	if next == nil {
		t.Fatal("wrong requestID closed modal")
	}
	runPermissionCmd(t, cmd)
	if m.remaining != 2 {
		t.Fatalf("remaining = %d after wrong id, want 2", m.remaining)
	}

	next, cmd = m.onCountdown(permissionCountdownMsg{requestID: "stale", gen: gen + 1})
	if next == nil {
		t.Fatal("wrong gen closed modal")
	}
	runPermissionCmd(t, cmd)
	if m.remaining != 2 {
		t.Fatalf("remaining = %d after wrong gen, want 2", m.remaining)
	}
	assertNoPermissionReply(t, ops)
}

func TestPermissionModalArmZeroIsNoop(t *testing.T) {
	m, _ := newTestPermissionModal("noop")
	if cmd := m.armAutoApprove(0); cmd != nil {
		t.Fatal("armAutoApprove(0) returned cmd")
	}
	if m.remaining != 0 {
		t.Fatalf("remaining = %d", m.remaining)
	}
}

func TestPermissionAutoApproveDisabledByDefault(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	cmd := m.applyEvent(protocol.PermissionAsked{RequestID: "d", Permission: "edit", Patterns: []string{"a.go"}})
	runPermissionCmd(t, cmd)
	pm, ok := m.modal.(*permissionModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	if pm.remaining != 0 {
		t.Fatalf("remaining = %d, want 0 (disabled)", pm.remaining)
	}
}

func TestPermissionAutoApproveArmsFromOptions(t *testing.T) {
	m, ops := newAppTestModelWithOptions(Options{
		PermissionAutoApproveSeconds: 4,
		PermissionAutoApproveExclude: []string{"bash"},
	})
	header := ansi.Strip(m.headerView(120))
	if !strings.Contains(header, "AUTO-ALLOW") || !strings.Contains(header, "4S") {
		t.Fatalf("header missing armed badge:\n%s", header)
	}

	// Excluded permission: no countdown.
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "ex", Permission: "bash", Patterns: []string{"rm"}})
	pm, ok := m.modal.(*permissionModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	if pm.remaining != 0 {
		t.Fatalf("excluded bash remaining = %d, want 0", pm.remaining)
	}
	m.modal = nil

	// Non-excluded: armed. Do not run the returned tick cmd (tea.Tick blocks 1s).
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "ok", Permission: "edit", Patterns: []string{"a.go"}})
	pm, ok = m.modal.(*permissionModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	if pm.remaining != 4 {
		t.Fatalf("remaining = %d, want 4", pm.remaining)
	}

	// Drive countdown to fire via Update path (inject ticks; ignore next Tick cmds).
	for range 3 {
		gen := pm.autoGen
		updated, _ := m.Update(permissionCountdownMsg{requestID: "ok", gen: gen})
		m = updated.(Model)
		pm, ok = m.modal.(*permissionModal)
		if !ok {
			t.Fatal("modal closed early")
		}
	}
	gen := pm.autoGen
	updated, cmd := m.Update(permissionCountdownMsg{requestID: "ok", gen: gen})
	m = updated.(Model)
	if m.modal != nil {
		t.Fatalf("modal still open after fire: %T", m.modal)
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "ok", protocol.DecisionOnce, "")
}

func TestSoftApproveModeArmsFifteenSecondCountdown(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.permMode = protocol.PermissionModeSoftApprove
	header := ansi.Strip(m.headerView(120))
	if !strings.Contains(header, "SOFT") {
		t.Fatalf("header missing soft mode badge:\n%s", header)
	}
	if !strings.Contains(header, "AUTO-ALLOW") || !strings.Contains(header, "15S") {
		t.Fatalf("header missing soft-approve armed badge:\n%s", header)
	}

	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "soft15", Permission: "edit", Patterns: []string{"a.go"}})
	pm, ok := m.modal.(*permissionModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	if pm.remaining != protocol.SoftApproveSeconds {
		t.Fatalf("remaining = %d, want %d", pm.remaining, protocol.SoftApproveSeconds)
	}
	view := ansi.Strip(pm.view(70, theme.Default()))
	if !strings.Contains(view, "Auto-approving once in 15s…") {
		t.Fatalf("view missing product copy:\n%s", view)
	}

	// Fake-clock: inject SoftApproveSeconds ticks → exactly one allow-once.
	for i := 0; i < protocol.SoftApproveSeconds-1; i++ {
		gen := pm.autoGen
		updated, _ := m.Update(permissionCountdownMsg{requestID: "soft15", gen: gen})
		m = updated.(Model)
		pm, ok = m.modal.(*permissionModal)
		if !ok {
			t.Fatalf("closed early on tick %d", i+1)
		}
	}
	gen := pm.autoGen
	updated, cmd := m.Update(permissionCountdownMsg{requestID: "soft15", gen: gen})
	m = updated.(Model)
	if m.modal != nil {
		t.Fatalf("modal still open: %T", m.modal)
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "soft15", protocol.DecisionOnce, "")
	assertNoPermissionReply(t, ops)
}

func TestSoftApproveConfigSecondsOverride(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{PermissionAutoApproveSeconds: 7})
	m.permMode = protocol.PermissionModeSoftApprove
	if got := m.effectivePermissionAutoApproveSeconds(); got != 7 {
		t.Fatalf("effective = %d, want config 7", got)
	}
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "ov", Permission: "edit", Patterns: []string{"a.go"}})
	pm := m.modal.(*permissionModal)
	if pm.remaining != 7 {
		t.Fatalf("remaining = %d, want 7", pm.remaining)
	}
}

func TestPermissionModalRaceTimerVsUserEsc(t *testing.T) {
	// Shrink tick interval so tea.Tick commands complete quickly under -race.
	prev := permissionCountdownInterval
	permissionCountdownInterval = time.Millisecond
	t.Cleanup(func() { permissionCountdownInterval = prev })

	m, ops := newTestPermissionModal("race-esc")
	tickCmd := m.armAutoApprove(protocol.SoftApproveSeconds)
	if tickCmd == nil {
		t.Fatal("arm returned nil")
	}
	// Fire the real tick cmd concurrently with an Esc decision.
	done := make(chan tea.Msg, 1)
	go func() { done <- tickCmd() }()

	next, replyCmd := m.update(permissionKey("esc"))
	if next != nil {
		t.Fatal("esc did not close")
	}
	reply := receiveSinglePermissionReply(t, ops, replyCmd)
	assertPermissionReply(t, reply, "race-esc", protocol.DecisionReject, "")

	// Stale tick after Esc must not allow (decided + gen bump).
	tickMsg := <-done
	if msg, ok := tickMsg.(permissionCountdownMsg); ok {
		if _, c := m.onCountdown(msg); c != nil {
			runPermissionCmd(t, c)
		}
	}
	assertNoPermissionReply(t, ops)
}

func TestPermissionModalRaceTimerVsUserAllow(t *testing.T) {
	prev := permissionCountdownInterval
	permissionCountdownInterval = time.Millisecond
	t.Cleanup(func() { permissionCountdownInterval = prev })

	m, ops := newTestPermissionModal("race-y")
	tickCmd := m.armAutoApprove(5)
	done := make(chan tea.Msg, 1)
	go func() { done <- tickCmd() }()

	// Concurrent-ish: user allow-once wins; timer must not double-submit.
	next, replyCmd := m.update(permissionKey("y"))
	if next != nil {
		t.Fatal("y did not close")
	}
	reply := receiveSinglePermissionReply(t, ops, replyCmd)
	assertPermissionReply(t, reply, "race-y", protocol.DecisionOnce, "")

	tickMsg := <-done
	if msg, ok := tickMsg.(permissionCountdownMsg); ok {
		if _, c := m.onCountdown(msg); c != nil {
			runPermissionCmd(t, c)
		}
	}
	// Final zero tick also ignored.
	if _, c := m.onCountdown(permissionCountdownMsg{requestID: "race-y", gen: m.autoGen}); c != nil {
		runPermissionCmd(t, c)
	}
	assertNoPermissionReply(t, ops)
}

func TestPermissionModalSoftApproveFakeClockFullCountdown(t *testing.T) {
	m, ops := newTestPermissionModal("fake-15")
	if cmd := m.armAutoApprove(protocol.SoftApproveSeconds); cmd == nil {
		t.Fatal("arm nil")
	}
	// Drive all seconds via injected ticks (no wall clock).
	for i := protocol.SoftApproveSeconds; i > 1; i-- {
		if m.remaining != i {
			t.Fatalf("remaining = %d, want %d", m.remaining, i)
		}
		view := ansi.Strip(m.view(70, theme.Default()))
		want := "Auto-approving once in " + itoa(i) + "s…"
		if !strings.Contains(view, want) {
			t.Fatalf("tick view missing %q:\n%s", want, view)
		}
		next, cmd := m.onCountdown(permissionCountdownMsg{requestID: "fake-15", gen: m.autoGen})
		if next == nil {
			t.Fatalf("closed early at remaining=%d", i)
		}
		m = next.(*permissionModal)
		runPermissionCmd(t, cmd)
		assertNoPermissionReply(t, ops)
	}
	next, cmd := m.onCountdown(permissionCountdownMsg{requestID: "fake-15", gen: m.autoGen})
	if next != nil {
		t.Fatal("final tick did not close")
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "fake-15", protocol.DecisionOnce, "")
}

func newTestPermissionModal(requestID string) (*permissionModal, chan protocol.Op) {
	req := protocol.PermissionAsked{
		RequestID:  requestID,
		Permission: "bash",
		Patterns:   []string{"rm important.txt"},
	}
	return newTestPermissionModalFrom(req)
}

func newTestPermissionModalFrom(req protocol.PermissionAsked) (*permissionModal, chan protocol.Op) {
	ops := make(chan protocol.Op, 4)
	return newPermissionModal(req, ops), ops
}

func enterPermissionFeedback(t *testing.T, m *permissionModal) {
	t.Helper()
	next, cmd := m.update(permissionKey("n"))
	if next == nil {
		t.Fatal("reject choice closed modal instead of requesting feedback")
	}
	runPermissionCmd(t, cmd)
}

func permissionKey(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	default:
		return tea.KeyPressMsg{Text: key}
	}
}

func runPermissionCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()
	select {
	case msg := <-done:
		return msg
	case <-time.After(permissionCmdTimeout):
		t.Fatalf("tea command did not complete within %s", permissionCmdTimeout)
		return nil
	}
}

func receiveSinglePermissionReply(t *testing.T, ops <-chan protocol.Op, cmd tea.Cmd) protocol.PermissionReply {
	t.Helper()
	runPermissionCmd(t, cmd)
	select {
	case op := <-ops:
		reply, ok := op.(protocol.PermissionReply)
		if !ok {
			t.Fatalf("operation type = %T, want protocol.PermissionReply", op)
		}
		assertNoPermissionReply(t, ops)
		return reply
	default:
		t.Fatal("permission command emitted no reply")
		return protocol.PermissionReply{}
	}
}

func assertNoPermissionReply(t *testing.T, ops <-chan protocol.Op) {
	t.Helper()
	select {
	case op := <-ops:
		t.Fatalf("unexpected extra permission operation: %#v", op)
	default:
	}
}

func assertPermissionReply(t *testing.T, got protocol.PermissionReply, requestID string, decision protocol.Decision, message string) {
	t.Helper()
	want := protocol.PermissionReply{RequestID: requestID, Decision: decision, Message: message}
	if got != want {
		t.Errorf("permission reply = %#v, want %#v", got, want)
	}
}
