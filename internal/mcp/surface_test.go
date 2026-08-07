package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/tool"
)

func startFake(t *testing.T, mode string) session {
	t.Helper()
	cfg := ServerConfig{
		Name:    "fake",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
			"MCP_FAKE_MODE":          mode,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	s, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCapsNegotiationPromptsResources(t *testing.T) {
	s := startFake(t, "full-caps")
	caps := s.Caps()
	if !caps.Tools || !caps.Prompts || !caps.Resources {
		t.Fatalf("caps=%+v", caps)
	}
	if !caps.ToolsListChanged || !caps.PromptsListChanged || !caps.ResourcesListChanged {
		t.Fatalf("listChanged flags=%+v", caps)
	}
}

func TestPromptsOnlyDegradesTools(t *testing.T) {
	s := startFake(t, "prompts-only")
	caps := s.Caps()
	if caps.Tools || !caps.Prompts {
		t.Fatalf("caps=%+v", caps)
	}
	tools, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("tools=%v want empty", tools)
	}
	prompts, err := s.ListPrompts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || prompts[0].Name != "greet" {
		t.Fatalf("prompts=%+v", prompts)
	}
}

func TestSurfaceToolsPromptsAndResources(t *testing.T) {
	s := startFake(t, "full-caps")
	reg := tool.NewRegistry()
	names := registerSurfaceTools(reg, s, s.Caps())
	if len(names) != 4 {
		t.Fatalf("surface names=%v", names)
	}
	// Also register normal tools via manager path simulation
	ctx := context.Background()
	tools, err := s.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ti := range tools {
		reg.Register(newBridge(s, ti))
	}

	askAllow := func(ctx context.Context, req tool.AskRequest) error { return nil }
	tc := &tool.Context{Ask: askAllow}

	lp, ok := reg.Get(NamespaceTool("fake", "list_prompts"))
	if !ok {
		t.Fatal("missing list_prompts")
	}
	res, err := lp.Execute(ctx, json.RawMessage(`{}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "greet") {
		t.Fatalf("list_prompts output=%q", res.Output)
	}

	gp, _ := reg.Get(NamespaceTool("fake", "get_prompt"))
	res, err = gp.Execute(ctx, json.RawMessage(`{"name":"greet","arguments":{"who":"Ada"}}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Hello Ada") {
		t.Fatalf("get_prompt=%q", res.Output)
	}
	if string(res.Metadata) == "" || !strings.Contains(string(res.Metadata), "fake") {
		t.Fatalf("provenance meta=%s", res.Metadata)
	}

	rr, _ := reg.Get(NamespaceTool("fake", "read_resource"))
	res, err = rr.Execute(ctx, json.RawMessage(`{"uri":"memo://notes/1"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	// Secret must be redacted.
	if strings.Contains(res.Output, "sk-ant-api03-") {
		t.Fatalf("secret leaked: %q", res.Output)
	}
	if !strings.Contains(res.Output, "note body") && !strings.Contains(res.Output, "REDACTED") {
		t.Fatalf("resource output unexpected: %q", res.Output)
	}
}

func TestCatalogListChangedRefresh(t *testing.T) {
	// Custom helper that can emit list_changed after first tools/list.
	cfg := ServerConfig{
		Name:    "dyn",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
			"MCP_FAKE_MODE":          "list-changed",
		},
	}
	// Extend fake for list-changed mode in this test via env handled below —
	// if mode unknown, still tools-only. We'll drive notification via client API.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()

	reg := tool.NewRegistry()
	m := NewManager()
	m.StartAll(ctx, []ServerConfig{cfg}, reg)
	// StartAll starts its own client; close the extra.
	_ = client.Close()

	// Wait for up
	deadline := time.After(5 * time.Second)
	for {
		st := m.Statuses()
		if len(st) == 1 && st[0].State == "up" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("not up: %+v", st)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Manually invoke catalog refresh (simulates notification).
	m.handleCatalogNotification("dyn", notifyToolsListChanged)

	// Still up with tools bound.
	st := m.Statuses()
	if len(st) != 1 || st[0].State != "up" {
		t.Fatalf("after refresh: %+v", st)
	}
	if st[0].ToolCount < 1 {
		t.Fatalf("expected tools after refresh: %+v", st[0])
	}
}

func TestMalformedNotificationIgnored(t *testing.T) {
	m := NewManager()
	// Should not panic.
	m.handleCatalogNotification("nope", "notifications/unknown")
	m.handleCatalogNotification("", "not-a-method")
}

func TestBoundTextRedacts(t *testing.T) {
	s := BoundText("key sk-ant-api03-" + strings.Repeat("b", 40) + " end")
	if strings.Contains(s, "sk-ant-") {
		t.Fatalf("not redacted: %q", s)
	}
}

func TestParseServerCaps(t *testing.T) {
	c := ParseServerCaps(map[string]any{
		"tools":     map[string]any{"listChanged": true},
		"prompts":   map[string]any{},
		"resources": nil, // present but null → not enabled
	})
	if !c.Tools || !c.ToolsListChanged || !c.Prompts || c.Resources {
		t.Fatalf("%+v", c)
	}
}
