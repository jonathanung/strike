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
			name: "provider + full",
			info: host.ModelInfo{
				ID: "x", Provider: "openai", Context: 200_000,
				HasCost: true, InputCost: 3, OutputCost: 15,
				ToolCall: true, Reasoning: true, Attachment: true,
			},
			want: dotJoin(th, "openai", "200k", "$3/$15", "tools", "reason", "vision"),
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
			ID: "gpt-full", Provider: "openai", Context: 128_000,
			HasCost: true, InputCost: 2.5, OutputCost: 10,
			ToolCall: true, Reasoning: true, Attachment: true,
		},
		{ID: "gpt-bare", Provider: "openai"},
	}
	plain := ansi.Strip(m.view(96, theme.Default()))
	for _, want := range []string{
		"gpt-full",
		"openai",
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

func TestModelModalShowsMultiProviderModels(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	m := newModelModal("openai", "gpt-a", ops, nil)
	m.loading = false
	m.all = []host.ModelInfo{
		{ID: "gpt-a", Provider: "openai"},
		{ID: "grok-b", Provider: "xai"},
	}
	plain := ansi.Strip(m.view(72, theme.Default()))
	for _, want := range []string{"gpt-a", "grok-b", "openai", "xai", "all providers"} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q:\n%s", want, plain)
		}
	}
}

func TestModelModalSelectUsesProviderFromRow(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	m := newModelModal("openai", "", ops, nil)
	m.loading = false
	m.all = []host.ModelInfo{
		{ID: "a", Provider: "openai", Context: 1000},
		{ID: "b", Provider: "xai", Context: 2000},
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
		if sm.Provider != "xai" || sm.Model != "b" {
			t.Errorf("SelectModel = %+v", sm)
		}
	default:
		t.Fatal("no op sent")
	}
}

func TestModelModalSelectUsesID(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	m := newModelModal("openai", "", ops, nil)
	m.loading = false
	m.all = []host.ModelInfo{
		{ID: "a", Provider: "openai", Context: 1000},
		{ID: "b", Provider: "openai", Context: 2000},
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

func TestModelModalFilterMatchesProvider(t *testing.T) {
	m := newModelModal("openai", "", nil, nil)
	m.loading = false
	m.all = []host.ModelInfo{
		{ID: "gpt", Provider: "openai"},
		{ID: "grok", Provider: "xai"},
	}
	m.filter = "xai"
	got := m.filtered()
	if len(got) != 1 || got[0].ID != "grok" {
		t.Fatalf("filtered = %#v", got)
	}
}

func TestModelModalFreeformParsesProviderModel(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	m := newModelModal("openai", "", ops, nil)
	m.loading = false
	m.filter = "xai/grok-4"
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatal("expected close")
	}
	_ = cmd()
	select {
	case op := <-ops:
		sm, ok := op.(protocol.SelectModel)
		if !ok {
			t.Fatalf("op type %T", op)
		}
		if sm.Provider != "xai" || sm.Model != "grok-4" {
			t.Errorf("SelectModel = %+v", sm)
		}
	default:
		t.Fatal("no op")
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
	msg, ok := loadModelsCmd(cat, []string{"openai"}, "openai")().(modelsLoadedMsg)
	if !ok {
		t.Fatalf("msg type %T", loadModelsCmd(cat, []string{"openai"}, "openai")())
	}
	if msg.err != nil || len(msg.models) != 1 {
		t.Fatalf("msg = %+v", msg)
	}
	got := msg.models[0]
	if got.ID != "gpt-full" || got.Provider != "openai" || got.Context != 128_000 || !got.HasCost || !got.ToolCall {
		t.Errorf("model = %+v", got)
	}
}

func TestLoadModelsCmdMultiProvider(t *testing.T) {
	cat := &fakeCatalog{
		ids: map[string][]string{
			"openai": {"gpt-a"},
			"xai":    {"grok-b"},
			"echo":   {}, // will error and be skipped
		},
	}
	msg := loadModelsCmd(cat, []string{"openai", "xai", "echo"}, "openai")().(modelsLoadedMsg)
	if msg.err != nil {
		t.Fatalf("err = %v", msg.err)
	}
	if len(msg.models) != 2 {
		t.Fatalf("models = %#v", msg.models)
	}
	if msg.models[0].Provider != "openai" || msg.models[0].ID != "gpt-a" {
		t.Errorf("first = %+v", msg.models[0])
	}
	if msg.models[1].Provider != "xai" || msg.models[1].ID != "grok-b" {
		t.Errorf("second = %+v", msg.models[1])
	}
}

func TestAuthenticatedModelProviders(t *testing.T) {
	auth := &fakeAuth{statuses: []host.ProviderStatus{
		{Name: "openai", Authed: true},
		{Name: "anthropic", Authed: false},
		{Name: "xai", Authed: true},
		{Name: "echo", Authed: true, Builtin: true},
	}}
	got := authenticatedModelProviders(auth, "openai")
	want := []string{"openai", "xai", "echo"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// Current provider always included even when unauthenticated.
	got = authenticatedModelProviders(auth, "anthropic")
	if got[len(got)-1] != "anthropic" {
		t.Errorf("current missing: %v", got)
	}
	// Nil auth still returns current.
	if got := authenticatedModelProviders(nil, "openai"); len(got) != 1 || got[0] != "openai" {
		t.Errorf("nil auth = %v", got)
	}
}

func TestParseModelArg(t *testing.T) {
	tests := []struct {
		arg, fallback, wantProv, wantModel string
	}{
		{"gpt-4", "openai", "openai", "gpt-4"},
		{"xai/grok-4", "openai", "xai", "grok-4"},
		{"xai/grok/extra", "openai", "xai", "grok/extra"},
		{"", "openai", "openai", ""},
		{"/bare", "openai", "openai", "/bare"},
	}
	for _, tt := range tests {
		p, m := parseModelArg(tt.arg, tt.fallback)
		if p != tt.wantProv || m != tt.wantModel {
			t.Errorf("parseModelArg(%q,%q)=(%q,%q), want (%q,%q)",
				tt.arg, tt.fallback, p, m, tt.wantProv, tt.wantModel)
		}
	}
}
