package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/providers/anthropic"
	"github.com/jonathanung/strike-cli/harness/providers/openaicompat"
	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/product/auth"
	"github.com/jonathanung/strike-cli/internal/product/config"
)

func TestStatusesHonorsDisableDefaultProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	customs := config.NewCustomStore([]config.CustomProvider{{
		Name:    "acme",
		BaseURL: "https://api.acme.example/v1",
		API:     config.WireOpenAI,
		Models:  []string{"m1"},
	}}, "")
	customs.SetDisableDefault(true, map[string]bool{"openai": false})
	svc := New(store, nil, nil, nil, nil, nil, customs, "")

	by := statusByName(svc.Auth.Statuses())
	if _, ok := by["anthropic"]; ok {
		t.Fatal("anthropic should be hidden")
	}
	if _, ok := by["echo"]; ok {
		t.Fatal("echo should be hidden when bulk-disabled")
	}
	if s, ok := by["openai"]; !ok || s.Name != "openai" {
		t.Fatalf("openai should remain via override: %+v ok=%v", s, ok)
	}
	if s, ok := by["acme"]; !ok || !s.Custom {
		t.Fatalf("custom acme must remain: %+v ok=%v", s, ok)
	}
}

func TestCustomProviderHostRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("ACME_API_KEY", "")

	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	customs := config.NewCustomStore(nil, "")
	svc := New(store, nil, nil, nil, nil, nil, customs, "")

	if svc.Providers == nil {
		t.Fatal("Providers is nil")
	}
	p := host.CustomProvider{
		Name:      "acme",
		BaseURL:   "https://api.acme.example/v1",
		API:       "openai",
		APIKeyEnv: "ACME_API_KEY",
		Models:    []string{"acme-v1"},
	}
	if err := svc.Providers.Upsert(p); err != nil {
		t.Fatal(err)
	}
	got, ok := svc.Providers.Get("acme")
	if !ok || got.BaseURL != p.BaseURL || got.API != "openai" {
		t.Fatalf("Get = %+v ok=%v", got, ok)
	}

	// Key via auth store, never config.
	if err := svc.Auth.SetAPIKey("acme", "sk-test-acme"); err != nil {
		t.Fatal(err)
	}
	cfgData, _ := os.ReadFile(config.GlobalPath())
	if contains(string(cfgData), "sk-test-acme") {
		t.Fatal("api key leaked into config")
	}
	authData, _ := os.ReadFile(filepath.Join(home, ".strike", "auth.json"))
	if !contains(string(authData), "sk-test-acme") {
		t.Fatal("api key missing from auth store")
	}

	// Statuses include custom row beside builtins.
	by := statusByName(svc.Auth.Statuses())
	acme, ok := by["acme"]
	if !ok || !acme.Custom || !acme.APIKey || !acme.Authed {
		t.Fatalf("acme status = %+v ok=%v", acme, ok)
	}
	if acme.WireAPI != "openai" {
		t.Errorf("WireAPI = %q", acme.WireAPI)
	}

	// Catalog prefers configured models.
	ids, err := svc.Catalog.ModelIDs(context.Background(), "acme")
	if err != nil || len(ids) != 1 || ids[0] != "acme-v1" {
		t.Fatalf("ModelIDs = %v err=%v", ids, err)
	}

	// Env fallback.
	_ = store.Delete("acme")
	t.Setenv("ACME_API_KEY", "env-acme")
	if d := svc.Auth.Describe("acme"); d != "env" {
		t.Errorf("Describe after env = %q, want env", d)
	}

	if err := svc.Providers.Remove("acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Providers.Get("acme"); ok {
		t.Fatal("expected removed")
	}
}

func TestCustomOpenAICompatWire(t *testing.T) {
	var sawAuth string
	var sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "hi"}, "finish_reason": "stop"},
			},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	p := openaicompat.New("kimi", srv.URL+"/v1", func(context.Context) (string, error) {
		return "secret", nil
	})
	ch, err := p.Stream(context.Background(), provider.Request{Model: "m", Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev := range ch {
		if ev.Type == provider.EventTextDelta {
			text += ev.Text
		}
		if ev.Type == provider.EventError {
			t.Fatal(ev.Err)
		}
	}
	if text != "hi" {
		t.Errorf("text = %q", text)
	}
	if sawPath != "/v1/chat/completions" {
		t.Errorf("path = %q", sawPath)
	}
	if sawAuth != "Bearer secret" {
		t.Errorf("auth = %q", sawAuth)
	}
	if p.Name() != "kimi" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestCustomAnthropicWire(t *testing.T) {
	var sawPath string
	var sawKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	p, err := anthropic.NewCustom("my-claude-proxy", srv.URL, "proxy-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Model:     "claude-test",
		MaxTokens: 16,
		Messages:  []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev := range ch {
		if ev.Type == provider.EventTextDelta {
			text += ev.Text
		}
		if ev.Type == provider.EventError {
			t.Fatal(ev.Err)
		}
	}
	if text != "ok" {
		t.Errorf("text = %q", text)
	}
	if sawPath != "/v1/messages" {
		t.Errorf("path = %q", sawPath)
	}
	if sawKey != "proxy-key" {
		t.Errorf("key = %q", sawKey)
	}
	if p.Name() != "my-claude-proxy" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestLogoutDeletesCustomProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")

	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	customs := config.NewCustomStore(nil, "")
	svc := New(store, nil, nil, nil, nil, nil, customs, "")
	if err := svc.Providers.Upsert(host.CustomProvider{
		Name: "acme", BaseURL: "https://api.moonshot.cn/v1", API: "openai",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Auth.SetAPIKey("acme", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Auth.Logout("acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Providers.Get("acme"); ok {
		t.Fatal("custom provider should be deleted on logout")
	}
	if _, ok := store.Get("acme"); ok {
		t.Fatal("credential should be cleared")
	}
	by := statusByName(svc.Auth.Statuses())
	if _, ok := by["acme"]; ok {
		t.Fatal("acme still listed in Statuses")
	}
}

func TestLogoutBuiltinKeepsProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("openai", auth.Credential{Type: auth.TypeAPIKey, APIKey: "sk"}); err != nil {
		t.Fatal(err)
	}
	svc := New(store, nil, nil, nil, nil, nil, config.NewCustomStore(nil, ""), "")
	if err := svc.Auth.Logout("openai"); err != nil {
		t.Fatal(err)
	}
	by := statusByName(svc.Auth.Statuses())
	if _, ok := by["openai"]; !ok {
		t.Fatal("builtin openai row must remain")
	}
	if by["openai"].Detail != "none" {
		t.Errorf("detail = %q", by["openai"].Detail)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
