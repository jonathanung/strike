package tui

import (
	"context"
	"strings"
	"testing"
)

func TestUpdateProbeMsgSetsNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, updateProbeMsg{notice: "update available: v0.1 → v0.2 — /upgrade"})
	if m.updateProbeDone {
		// ok
	} else {
		t.Fatal("updateProbeDone not set")
	}
	if !strings.Contains(m.notice, "/upgrade") {
		t.Fatalf("notice = %q", m.notice)
	}
	// Second delivery is ignored.
	m.notice = ""
	m = updateApp(t, m, updateProbeMsg{notice: "again"})
	if m.notice != "" {
		t.Fatalf("second probe should no-op, notice=%q", m.notice)
	}
}

func TestUpdateProbeMsgEmptySilent(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, updateProbeMsg{})
	if !m.updateProbeDone {
		t.Fatal("empty probe still marks done")
	}
	if m.notice != "" {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestUpdateProbeCmdFromOptions(t *testing.T) {
	called := false
	m, _ := newAppTestModelWithOptions(Options{
		CheckUpdate: func(ctx context.Context) string {
			called = true
			return "update available: test — /upgrade"
		},
	})
	cmd := m.updateProbeCmd()
	if cmd == nil {
		t.Fatal("expected probe cmd")
	}
	msg := cmd()
	um, ok := msg.(updateProbeMsg)
	if !ok || !strings.Contains(um.notice, "/upgrade") {
		t.Fatalf("msg = %#v called=%v", msg, called)
	}
	if !called {
		t.Fatal("CheckUpdate not called")
	}
}

func TestUpdateProbeCmdNilWhenUnset(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	if m.updateProbeCmd() != nil {
		t.Fatal("expected nil cmd without CheckUpdate")
	}
}
