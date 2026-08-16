package tui

import (
	"strings"
	"testing"
)

func TestUpgradeCommandQueuesPendingAndQuits(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	next, cmd := m.handleCommand("/upgrade")
	nm := next.(Model)
	if !nm.PendingUpgrade() {
		t.Fatal("PendingUpgrade = false, want true")
	}
	if cmd == nil {
		t.Fatal("expected quit cmd")
	}
	_ = cmd
}

func TestUpgradeBlockedWhileTurnRunning(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.turnRunning = true
	next, cmd := m.handleCommand("/upgrade")
	nm := next.(Model)
	if nm.PendingUpgrade() {
		t.Fatal("PendingUpgrade set while turn running")
	}
	if cmd != nil {
		t.Fatal("unexpected cmd while blocked")
	}
	if !strings.Contains(nm.notice, "wait for the current turn") {
		t.Fatalf("notice = %q", nm.notice)
	}
}

func TestUpgradeInCommandCatalog(t *testing.T) {
	found := false
	for _, spec := range builtinCommandSpecs {
		if spec.ID == commandUpgrade && spec.Name == "/upgrade" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("/upgrade missing from builtinCommandSpecs")
	}
	if _, ok := reservedCommandNames["upgrade"]; !ok {
		t.Fatal("upgrade not reserved")
	}
}
