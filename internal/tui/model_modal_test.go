package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestFormatModelDetail(t *testing.T) {
	th := theme.Default()
	tests := []struct {
		name string
		info host.ModelInfo
		want string
	}{
		{name: "empty", info: host.ModelInfo{ID: "x"}, want: ""},
		{
			name: "context only",
			info: host.ModelInfo{ID: "x", Context: 128_000},
			want: "128k",
		},
		{
			name: "full",
			info: host.ModelInfo{
				ID: "x", Context: 200_000,
				HasCost: true, InputCost: 3, OutputCost: 15,
				ToolCall: true, Reasoning: true, Attachment: true,
			},
			want: dotJoin(th, "200k", "$3/$15", "tools", "reason", "vision"),
		},
		{
			name: "fractional cost",
			info: host.ModelInfo{ID: "x", HasCost: true, InputCost: 2.5, OutputCost: 10},
			want: "$2.5/$10",
		},
		{
			name: "caps only",
			info: host.ModelInfo{ID: "x", ToolCall: true, Attachment: true},
			want: dotJoin(th, "tools", "vision"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatModelDetail(th, tt.info); got != tt.want {
				t.Errorf("formatModelDetail = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModelModalShowsMetadataDetail(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	m := newModelModal("openai", "gpt-full", ops, nil)
	m.loading = false
	m.all = []host.ModelInfo{
		{
			ID: "gpt-full", Context: 128_000,
			HasCost: true, InputCost: 2.5, OutputCost: 10,
			ToolCall: true, Reasoning: true, Attachment: true,
		},
		{ID: "gpt-bare"},
	}
	plain := ansi.Strip(m.view(72, theme.Default()))
	for _, want := range []string{
		"gpt-full",
		"128k",
		"$2.5/$10",
		"tools",
		"reason",
		"vision",
		"gpt-bare",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q:\n%s", want, plain)
		}
	}
}

func TestModelModalSelectUsesID(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	m := newModelModal("openai", "", ops, nil)
	m.loading = false
	m.all = []host.ModelInfo{
		{ID: "a", Context: 1000},
		{ID: "b", Context: 2000},
	}
	m.cursor = 1
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatal("expected modal to close on enter")
	}
	if cmd == nil {
		t.Fatal("expected select cmd")
	}
	_ = cmd()
	select {
	case op := <-ops:
		sm, ok := op.(protocol.SelectModel)
		if !ok {
			t.Fatalf("op type %T", op)
		}
		if sm.Provider != "openai" || sm.Model != "b" {
			t.Errorf("SelectModel = %+v", sm)
		}
	default:
		t.Fatal("no op sent")
	}
}

func TestLoadModelsCmdDeliversMetadata(t *testing.T) {
	cat := &fakeCatalog{
		ids: map[string][]string{"openai": {"gpt-full"}},
		meta: map[string]host.ModelInfo{
			"openai/gpt-full": {
				Context: 128_000, HasCost: true, InputCost: 1, OutputCost: 2,
				ToolCall: true,
			},
		},
	}
	msg, ok := loadModelsCmd(cat, "openai")().(modelsLoadedMsg)
	if !ok {
		t.Fatalf("msg type %T", loadModelsCmd(cat, "openai")())
	}
	if msg.err != nil || len(msg.models) != 1 {
		t.Fatalf("msg = %+v", msg)
	}
	got := msg.models[0]
	if got.ID != "gpt-full" || got.Context != 128_000 || !got.HasCost || !got.ToolCall {
		t.Errorf("model = %+v", got)
	}
}
