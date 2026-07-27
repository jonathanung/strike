package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestFastCommandBareToggleAndAliases(t *testing.T) {
	tests := []struct {
		name    string
		initial bool
		command string
		want    bool
	}{
		{name: "bare toggles on", command: "/fast", want: true},
		{name: "bare toggles off", initial: true, command: "/fast"},
		{name: "on", command: "/fast on", want: true},
		{name: "true", command: "/fast true", want: true},
		{name: "one", command: "/fast 1", want: true},
		{name: "yes", command: "/fast yes", want: true},
		{name: "off", initial: true, command: "/fast off"},
		{name: "false", initial: true, command: "/fast false"},
		{name: "zero", initial: true, command: "/fast 0"},
		{name: "no", initial: true, command: "/fast no"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.fastEnabled = tt.initial
			m.composer.SetValue(tt.command)
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			runAppCmd(t, cmd)
			if got := receiveAppOp(t, ops); got != (protocol.SetFast{Enabled: tt.want}) {
				t.Errorf("operation = %#v, want SetFast{Enabled: %v}", got, tt.want)
			}
			if m.composer.Value() != "" {
				t.Errorf("composer = %q, want reset", m.composer.Value())
			}
		})
	}
}

func TestFastCommandRejectsInvalidUsageWithoutSending(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/fast maybe")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("invalid /fast returned message %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || m.notice != "usage: /fast [on|off]" {
		t.Errorf("notice = %q, error = %v", m.notice, m.noticeErr)
	}
}

func TestFastCommandWaitsForEngineConfirmationBeforeChangingState(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m.composer.SetValue("/fast on")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	if got := receiveAppOp(t, ops); got != (protocol.SetFast{Enabled: true}) {
		t.Fatalf("operation = %#v, want SetFast{Enabled: true}", got)
	}
	if m.fastEnabled || strings.Contains(m.headerView(100), "fast on") {
		t.Error("/fast changed displayed state before FastSelected confirmation")
	}
}

func TestFastSelectedUpdatesNoticeAndRendersAlongsideEffort(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m.applyEvent(protocol.EffortSelected{Level: protocol.EffortHigh})
	m.applyEvent(protocol.FastSelected{Enabled: true})
	if !m.fastEnabled {
		t.Error("FastSelected did not enable fast state")
	}
	if !strings.Contains(m.notice, "fast on") {
		t.Errorf("notice = %q, want fast confirmation", m.notice)
	}
	header := m.headerView(100)
	for _, want := range []string{"effort high", "fast"} {
		if !strings.Contains(header, want) {
			t.Errorf("header omits %q:\n%s", want, header)
		}
	}
	if !strings.Contains(header, ui.Badge(m.th, ui.ToneWarning, "fast")) {
		t.Errorf("header does not render fast as a warning badge:\n%s", header)
	}
}

type fastCatalogProbe struct {
	calls int
}

func (c *fastCatalogProbe) ModelIDs(_ context.Context, _ string) ([]string, error) {
	c.calls++
	return []string{"unused"}, nil
}

func (c *fastCatalogProbe) Models(_ context.Context, provider string) ([]host.ModelInfo, error) {
	c.calls++
	return []host.ModelInfo{{ID: "unused", Provider: provider}}, nil
}

func (c *fastCatalogProbe) ModelsForProviders(ctx context.Context, providers []string) ([]host.ModelInfo, error) {
	var out []host.ModelInfo
	for _, p := range providers {
		infos, err := c.Models(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, infos...)
	}
	return out, nil
}

func (c *fastCatalogProbe) ContextWindow(context.Context, string, string) (int, bool, error) {
	return 0, false, nil
}

func (c *fastCatalogProbe) OutputLimit(context.Context, string, string) (int, bool, error) {
	return 0, false, nil
}

func (c *fastCatalogProbe) ResolveVariant(context.Context, string, string, string) (string, bool, error) {
	return "", false, nil
}

func TestFastSelectedHandlingDoesNotLoadCatalog(t *testing.T) {
	probe := &fastCatalogProbe{}
	services := testServices(nil, nil)
	services.Catalog = probe
	m := New(make(chan protocol.Op, 1), make(chan protocol.Event), services)
	m.applyEvent(protocol.FastSelected{Enabled: true})
	if probe.calls != 0 {
		t.Fatalf("FastSelected loaded catalog %d times, want none", probe.calls)
	}
}

var _ host.Catalog = (*fastCatalogProbe)(nil)
