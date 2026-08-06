package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestPermissionExplainHint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		perm, pat, want string
	}{
		{"", "", "/permission explain <tool> [pattern]"},
		{"bash", "", "/permission explain bash"},
		{"bash", "*", "/permission explain bash"},
		{"bash", "ls", "/permission explain bash ls"},
		{"bash", "git status", `/permission explain bash "git status"`},
	}
	for _, tc := range cases {
		got := permissionExplainHint(tc.perm, tc.pat)
		if got != tc.want {
			t.Errorf("explain(%q,%q) = %q, want %q", tc.perm, tc.pat, got, tc.want)
		}
	}
}

func TestVerificationBadgeLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		rep       *protocol.VerificationReport
		wantLabel string
		wantOK    bool
	}{
		{"nil", nil, "", false},
		{"verified", &protocol.VerificationReport{Claimed: true, Verified: true, Passed: true}, "verified", true},
		{"claimed not verified", &protocol.VerificationReport{Claimed: true, Verified: false, Passed: false, Checks: []protocol.VerificationCheck{{Passed: false}}}, "claimed", true},
		{"failed unclaimed", &protocol.VerificationReport{Claimed: false, Verified: false, Passed: false}, "unverified", true},
		{"passed only", &protocol.VerificationReport{Passed: true}, "verified", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, _, ok := verificationBadgeLabel(tc.rep)
			if ok != tc.wantOK || label != tc.wantLabel {
				t.Fatalf("got label=%q ok=%v, want label=%q ok=%v", label, ok, tc.wantLabel, tc.wantOK)
			}
		})
	}
}

func TestTurnCompletedInterruptedShowsCanceledState(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.turnRunning = true
	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "interrupted"})
	if m.turnRunning {
		t.Fatal("turnRunning still true")
	}
	if m.lastStopReason != "interrupted" {
		t.Fatalf("lastStopReason = %q", m.lastStopReason)
	}
	if !strings.Contains(m.notice, "interrupted") {
		t.Errorf("notice = %q, want interrupted", m.notice)
	}
	found := false
	for _, c := range m.cells {
		if ic, ok := c.(*infoCell); ok && strings.Contains(ic.text, "canceled") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected turn canceled info cell")
	}
	// Header right side shows canceled while idle after interrupt.
	m.width, m.height, m.ready = 120, 40, true
	view := ansi.Strip(m.headerView(120))
	if !strings.Contains(view, "canceled") {
		t.Errorf("header missing canceled:\n%s", view)
	}
	// Next turn clears sticky canceled chrome.
	m = runEvent(t, m, protocol.TurnStarted{})
	if m.lastStopReason != "" {
		t.Fatalf("lastStopReason not cleared: %q", m.lastStopReason)
	}
}

func TestVerificationCompletedSurfacesClaimVsVerified(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.turnRunning = true
	m = runEvent(t, m, protocol.VerificationStarted{GateCount: 2})
	if !m.verifying {
		t.Fatal("verifying not set on VerificationStarted")
	}
	if m.agentState().Label() == "" {
		t.Fatal("agent state empty while verifying")
	}
	// Still working chrome during gates.
	if !m.turnRunning && !m.verifying {
		t.Fatal("expected working-capable state")
	}

	rep := protocol.VerificationReport{
		Claimed:  true,
		Verified: true,
		Passed:   true,
		Summary:  "verified: 2/2 gates passed",
		Checks: []protocol.VerificationCheck{
			{Name: "test", Kind: "cmd", Passed: true},
			{Name: "vet", Kind: "cmd", Passed: true},
		},
	}
	m = runEvent(t, m, protocol.VerificationCompleted{Report: rep})
	if m.verifying {
		t.Fatal("verifying still set after completed")
	}
	if m.lastVerification == nil || !m.lastVerification.Verified {
		t.Fatal("lastVerification not stored")
	}
	if !strings.Contains(m.notice, "verified") {
		t.Errorf("notice = %q, want verified", m.notice)
	}
	found := false
	for _, c := range m.cells {
		if ic, ok := c.(*infoCell); ok && strings.Contains(ic.text, "verified") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected verification info cell")
	}

	// Claimed-not-verified path.
	m2, _ := newAppTestModel(nil, nil)
	claimed := protocol.VerificationReport{
		Claimed:  true,
		Verified: false,
		Passed:   false,
		Summary:  "1/1 gates failed",
		Checks:   []protocol.VerificationCheck{{Name: "test", Kind: "cmd", Passed: false}},
	}
	m2 = runEvent(t, m2, protocol.VerificationCompleted{Report: claimed})
	if !strings.Contains(m2.notice, "claimed") {
		t.Errorf("notice = %q, want claimed", m2.notice)
	}
	if !m2.noticeErr {
		t.Error("claimed-not-verified should be error-toned notice")
	}

	// Header badge.
	m2.width, m2.height, m2.ready = 120, 40, true
	view := ansi.Strip(m2.headerView(120))
	if !strings.Contains(view, "claimed") {
		t.Errorf("header missing claimed badge:\n%s", view)
	}
}

func TestTurnCompletedVerificationWithoutPriorEvent(t *testing.T) {
	// Replay / older path: only TurnCompleted.Verification is set.
	m, _ := newAppTestModel(nil, nil)
	m.turnRunning = true
	rep := protocol.VerificationReport{
		Claimed: true, Verified: true, Passed: true,
		Summary: "ok",
	}
	m = runEvent(t, m, protocol.TurnCompleted{
		StopReason:   "end_turn",
		Verification: &rep,
	})
	if m.lastVerification == nil || !m.lastVerification.Passed {
		t.Fatal("expected lastVerification from TurnCompleted")
	}
	found := false
	for _, c := range m.cells {
		if ic, ok := c.(*infoCell); ok && strings.Contains(ic.text, "verified") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected verification cell from TurnCompleted-only path")
	}
}

func TestTurnCompletedDoesNotDuplicateVerificationCell(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.turnRunning = true
	rep := protocol.VerificationReport{
		Claimed: true, Verified: true, Passed: true,
		Summary: "verified: 1/1",
		Checks:  []protocol.VerificationCheck{{Passed: true}},
	}
	m = runEvent(t, m, protocol.VerificationCompleted{Report: rep})
	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "end_turn", Verification: &rep})
	n := 0
	for _, c := range m.cells {
		if ic, ok := c.(*infoCell); ok && strings.Contains(ic.text, "verified") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("verification cells = %d, want 1", n)
	}
}

func TestPermissionDecidedDenyShowsExplain(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = runEvent(t, m, protocol.PermissionDecided{
		Permission:     "bash",
		Patterns:       []string{"rm -rf /"},
		Action:         "deny",
		Layer:          "config",
		RulePermission: "bash",
		RulePattern:    "rm *",
		RuleAction:     "deny",
	})
	if m.lastDenial.Permission != "bash" {
		t.Fatalf("lastDenial = %+v", m.lastDenial)
	}
	if !strings.Contains(m.notice, "permission denied") {
		t.Errorf("notice = %q", m.notice)
	}
	if !strings.Contains(m.notice, "/permission explain bash") {
		t.Errorf("notice missing explain hint: %q", m.notice)
	}
	if !m.noticeErr {
		t.Error("deny notice should be error-toned")
	}
}

type stubPermissions struct {
	summary string
}

func (s stubPermissions) Explain(permission, pattern string) host.PermissionExplanation {
	return host.PermissionExplanation{
		Permission: permission,
		Pattern:    pattern,
		Action:     "deny",
		Summary:    s.summary,
	}
}

func (s stubPermissions) Presets() []host.PermissionPresetInfo { return nil }

func TestPermissionDecidedDenyUsesHostExplain(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Permissions = stubPermissions{summary: "bash rm -rf / → deny\n  matched config deny rm *"}
	m = runEvent(t, m, protocol.PermissionDecided{
		Permission: "bash",
		Patterns:   []string{"rm -rf /"},
		Action:     "deny",
		Layer:      "config",
	})
	if !strings.Contains(m.notice, "bash rm -rf / → deny") {
		t.Errorf("notice = %q, want host explain first line", m.notice)
	}
	if !strings.Contains(m.notice, "/permission explain") {
		t.Errorf("notice missing explain command: %q", m.notice)
	}
}

func TestPermissionRejectShowsExplain(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = runEvent(t, m, protocol.PermissionAsked{
		RequestID:  "req-1",
		Permission: "edit",
		Patterns:   []string{"/tmp/x"},
	})
	m = runEvent(t, m, protocol.PermissionResolved{
		RequestID: "req-1",
		Decision:  protocol.DecisionReject,
	})
	// Engine also emits PermissionDecided{deny, DecisionReject} after resolve.
	m = runEvent(t, m, protocol.PermissionDecided{
		RequestID:  "req-1",
		Permission: "edit",
		Patterns:   []string{"/tmp/x"},
		Action:     "deny",
		Decision:   protocol.DecisionReject,
		Layer:      "session",
	})
	if m.lastDenial.Permission != "edit" {
		t.Fatalf("lastDenial = %+v", m.lastDenial)
	}
	if !strings.Contains(m.notice, "rejected") {
		t.Errorf("notice = %q, want rejected (not overwritten by decided deny)", m.notice)
	}
	if strings.Contains(m.notice, "permission denied") {
		t.Errorf("notice overwritten by hard-deny path: %q", m.notice)
	}
	if !strings.Contains(m.notice, "/permission explain edit") {
		t.Errorf("notice missing explain: %q", m.notice)
	}
}

func TestToolCallEndPermissionDeniedExplain(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = runEvent(t, m, protocol.ToolCallBegin{
		CallID: "c1",
		Name:   "bash",
		Args:   []byte(`{"command":"sudo reboot"}`),
	})
	m = runEvent(t, m, protocol.ToolCallEnd{
		CallID:    "c1",
		Title:     "bash",
		Output:    "Permission denied.",
		IsError:   true,
		ErrorCode: protocol.ErrorCodePermissionDenied,
	})
	tc := m.toolByID["c1"]
	if tc == nil {
		t.Fatal("missing tool cell")
	}
	if !strings.Contains(tc.output, "/permission explain bash") {
		t.Errorf("tool output missing explain: %q", tc.output)
	}
	if tc.errorCode != protocol.ErrorCodePermissionDenied {
		t.Errorf("errorCode = %q", tc.errorCode)
	}
}

func TestChildCompletedVerificationBadge(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = runEvent(t, m, protocol.ChildStarted{
		Correlation: protocol.Correlation{SessionID: "child-1"},
		Agent:       "build",
	})
	rep := protocol.VerificationReport{
		Claimed: true, Verified: false, Passed: false,
		Summary: "gates failed",
	}
	m = runEvent(t, m, protocol.ChildCompleted{
		Correlation:  protocol.Correlation{SessionID: "child-1"},
		Status:       protocol.ChildStatusBlocked,
		Summary:      "done?",
		Verification: &rep,
	})
	var sc *subagentResultCell
	for _, c := range m.cells {
		if s, ok := c.(*subagentResultCell); ok {
			sc = s
			break
		}
	}
	if sc == nil {
		t.Fatal("missing subagent result cell")
	}
	if sc.verificationLabel != "claimed" {
		t.Fatalf("verificationLabel = %q, want claimed", sc.verificationLabel)
	}
	if sc.verificationOK {
		t.Fatal("verificationOK should be false")
	}
}

func TestFormatDenialNotice(t *testing.T) {
	t.Parallel()
	got := formatDenialNotice(lastDenialInfo{
		Permission: "write",
		Pattern:    "/etc/passwd",
		Layer:      "config",
		RuleAction: "deny",
		Source:     "deny",
	}, "")
	if !strings.Contains(got, "permission denied: write /etc/passwd") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, "/permission explain write /etc/passwd") {
		t.Errorf("missing hint: %q", got)
	}
}
