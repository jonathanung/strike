package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/memory"
)

// newTestServices returns services backed by an isolated, empty auth store.
// HOME is redirected to a temp dir and the provider env vars are cleared so
// auth.Describe reports stored state, not the developer's real environment.
func newTestServices(t *testing.T) (host.Services, *auth.Store) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(store, nil, nil, []string{"build", "plan"}, nil, nil), store
}

func statusByName(statuses []host.ProviderStatus) map[string]host.ProviderStatus {
	m := make(map[string]host.ProviderStatus, len(statuses))
	for _, s := range statuses {
		m[s.Name] = s
	}
	return m
}

func TestStatusesOrderFlagsAndEcho(t *testing.T) {
	svc, _ := newTestServices(t)
	got := svc.Auth.Statuses()

	wantOrder := []string{"anthropic", "openai", "xai", "echo"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d statuses, want %d: %+v", len(got), len(wantOrder), got)
	}
	for i, name := range wantOrder {
		if got[i].Name != name {
			t.Errorf("status[%d] = %q, want %q", i, got[i].Name, name)
		}
	}

	by := statusByName(got)
	if s := by["anthropic"]; !s.APIKey || s.OAuth || s.Device || s.Builtin {
		t.Errorf("anthropic flags = %+v, want APIKey-only", s)
	}
	if s := by["openai"]; !s.APIKey || !s.OAuth || s.Device || s.Builtin {
		t.Errorf("openai flags = %+v, want OAuth+APIKey", s)
	}
	if s := by["xai"]; !s.APIKey || !s.OAuth || !s.Device || s.Builtin {
		t.Errorf("xai flags = %+v, want OAuth+Device+APIKey", s)
	}

	echo := by["echo"]
	if !echo.Builtin || !echo.Authed || echo.Detail != "offline dev provider" {
		t.Errorf("echo status = %+v", echo)
	}

	// With an empty store and no env keys, credential providers are unauthed.
	for _, name := range []string{"anthropic", "openai", "xai"} {
		if s := by[name]; s.Authed || s.Detail != "none" {
			t.Errorf("%s should be unauthenticated, got %+v", name, s)
		}
	}
}

func TestSetAPIKeyReflectedInDescribeAndStatuses(t *testing.T) {
	svc, _ := newTestServices(t)
	if err := svc.Auth.SetAPIKey("anthropic", "sk-ant-123"); err != nil {
		t.Fatal(err)
	}
	if got := svc.Auth.Describe("anthropic"); got != "api key" {
		t.Errorf("Describe = %q, want \"api key\"", got)
	}
	s := statusByName(svc.Auth.Statuses())["anthropic"]
	if !s.Authed || s.Detail != "api key" {
		t.Errorf("anthropic status after SetAPIKey = %+v", s)
	}
}

func TestStatusesExpiresAtFromOAuthOnly(t *testing.T) {
	svc, store := newTestServices(t)
	exp := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if err := store.Set("openai", auth.Credential{
		Type:      auth.TypeOAuth,
		Access:    "access-token",
		Refresh:   "refresh-token",
		ExpiresAt: exp,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Auth.SetAPIKey("anthropic", "sk-ant-key"); err != nil {
		t.Fatal(err)
	}

	by := statusByName(svc.Auth.Statuses())
	oauth := by["openai"]
	if !oauth.Authed {
		t.Fatalf("openai should be authed after OAuth cred: %+v", oauth)
	}
	if oauth.ExpiresAt.IsZero() {
		t.Fatal("OAuth credential ExpiresAt missing from Statuses()")
	}
	if got, want := oauth.ExpiresAt.UTC(), exp; !got.Equal(want) {
		t.Errorf("openai ExpiresAt = %v, want %v", got, want)
	}

	api := by["anthropic"]
	if !api.Authed || api.Detail != "api key" {
		t.Fatalf("anthropic status = %+v", api)
	}
	if !api.ExpiresAt.IsZero() {
		t.Errorf("API key provider ExpiresAt = %v, want zero", api.ExpiresAt)
	}
}

func TestSetAPIKeyTrimsAndPersists0600(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(home, ".strike", "auth.json")
	store, err := auth.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store, nil, nil, nil, nil, nil)

	if err := svc.Auth.SetAPIKey("anthropic", "  sk-trim  "); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("auth.json perm = %o, want 600", perm)
	}
	if cred, ok := store.Get("anthropic"); !ok || cred.APIKey != "sk-trim" {
		t.Errorf("stored key = %q (ok=%v), want trimmed \"sk-trim\"", cred.APIKey, ok)
	}
}

func TestSetAPIKeyRejects(t *testing.T) {
	svc, _ := newTestServices(t)
	cases := map[string]struct{ provider, key string }{
		"empty":            {"anthropic", ""},
		"whitespace-only":  {"anthropic", "   "},
		"builtin echo":     {"echo", "sk"},
		"unknown provider": {"mystery", "sk"},
	}
	for name, c := range cases {
		if err := svc.Auth.SetAPIKey(c.provider, c.key); err == nil {
			t.Errorf("%s: expected error from SetAPIKey(%q, %q)", name, c.provider, c.key)
		}
	}
}

func TestLogout(t *testing.T) {
	svc, store := newTestServices(t)
	if err := svc.Auth.SetAPIKey("openai", "sk"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("openai"); !ok {
		t.Fatal("precondition: openai credential should exist")
	}
	if err := svc.Auth.Logout("openai"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("openai"); ok {
		t.Error("openai credential should be gone after Logout")
	}
	if got := svc.Auth.Describe("openai"); got != "none" {
		t.Errorf("Describe after logout = %q, want none", got)
	}
	// Logging out a provider with no credentials is a no-op, not an error.
	if err := svc.Auth.Logout("xai"); err != nil {
		t.Errorf("logout with no credentials errored: %v", err)
	}
}

func TestBeginOAuthUnsupported(t *testing.T) {
	svc, _ := newTestServices(t)
	// anthropic (API-key only) and echo (builtin) have no OAuth flow. These
	// must fail fast, before any network or loopback server is touched.
	for _, provider := range []string{"anthropic", "echo", "mystery"} {
		if _, err := svc.Auth.BeginOAuth(context.Background(), provider); err == nil {
			t.Errorf("BeginOAuth(%q): expected error", provider)
		}
	}
}

func TestBeginDeviceUnsupported(t *testing.T) {
	svc, _ := newTestServices(t)
	// Only xai supports the device flow; the rest fail fast before network.
	for _, provider := range []string{"openai", "anthropic", "echo"} {
		if _, err := svc.Auth.BeginDevice(context.Background(), provider); err == nil {
			t.Errorf("BeginDevice(%q): expected error", provider)
		}
	}
}

func TestSkillMappingAndFiltering(t *testing.T) {
	// store is unused by the skills path; New must not touch it eagerly.
	skills := []config.Skill{
		{Name: "withargs", Description: "takes args", Template: "do $ARGUMENTS now"},
		{Name: "noargs", Description: "no args", Template: "just do it"},
		{Name: "auth", Description: "reserved", Template: "x"},      // reserved name -> filtered
		{Name: "bad name", Description: "has space", Template: "y"}, // invalid name -> filtered
	}
	svc := New(nil, nil, nil, nil, skills, nil)

	if len(svc.Skills) != 2 {
		t.Fatalf("got %d skills, want 2: %+v", len(svc.Skills), svc.Skills)
	}
	by := map[string]host.Skill{}
	for _, s := range svc.Skills {
		by[s.Name] = s
	}

	wa, ok := by["withargs"]
	if !ok || !wa.HasArgs {
		t.Errorf("withargs = %+v ok=%v, want HasArgs", wa, ok)
	}
	if got := wa.Render("stuff"); got != "do stuff now" {
		t.Errorf("withargs Render = %q, want \"do stuff now\"", got)
	}

	na, ok := by["noargs"]
	if !ok || na.HasArgs {
		t.Errorf("noargs = %+v ok=%v, want no args", na, ok)
	}
	if got := na.Render("stuff"); got != "just do it\n\nArguments: stuff" {
		t.Errorf("noargs Render = %q", got)
	}

	if _, present := by["auth"]; present {
		t.Error("reserved skill name \"auth\" should be filtered out")
	}
	if _, present := by["bad name"]; present {
		t.Error("invalid skill name \"bad name\" should be filtered out")
	}
}

func TestSaveDefaultsWritesGlobalConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := New(nil, nil, nil, nil, nil, nil)

	if err := svc.Settings.SaveDefaults("openai", "gpt-5.5", "build", "high"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config.GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	var got config.Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.5" || got.DefaultAgent != "build" {
		t.Errorf("saved config = %+v", got)
	}
}

func TestSaveThemeWritesGlobalConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := New(nil, nil, nil, nil, nil, nil)
	if err := svc.Settings.SaveTheme("dracula"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config.GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	var got config.Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Theme != "dracula" {
		t.Errorf("theme = %q", got.Theme)
	}
}

func TestHistoryNilTolerated(t *testing.T) {
	svc := New(nil, nil, nil, nil, nil, nil)
	if svc.History != nil {
		t.Errorf("nil hist should yield nil Services.History, got %#v", svc.History)
	}
	if svc.Memory != nil {
		t.Errorf("nil mem should yield nil Services.Memory, got %#v", svc.Memory)
	}
}

func TestHistoryWiredThrough(t *testing.T) {
	hist, err := history.Open(t.TempDir(), "project-key")
	if err != nil {
		t.Fatal(err)
	}
	defer hist.Close()

	svc := New(nil, hist, nil, nil, nil, nil)
	if svc.History == nil {
		t.Fatal("Services.History should be non-nil when hist is provided")
	}
	if err := <-svc.History.Enqueue("hello world"); err != nil {
		t.Fatal(err)
	}
	if got := svc.History.Entries(); !reflect.DeepEqual(got, []string{"hello world"}) {
		t.Errorf("Entries = %v, want [hello world]", got)
	}
}

func TestMemoryWiredThrough(t *testing.T) {
	mem, err := memory.Open(t.TempDir(), "project-key")
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()

	svc := New(nil, nil, mem, nil, nil, nil)
	if svc.Memory == nil {
		t.Fatal("Services.Memory should be non-nil when mem is provided")
	}
	if err := svc.Memory.Put("k", "v", []string{"t"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := svc.Memory.Get("k")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Value != "v" || !reflect.DeepEqual(got.Tags, []string{"t"}) {
		t.Errorf("entry = %+v", got)
	}
	list, err := svc.Memory.List("t")
	if err != nil || len(list) != 1 || list[0].Key != "k" {
		t.Fatalf("List = %+v err=%v", list, err)
	}
	if err := svc.Memory.Delete("k"); err != nil {
		t.Fatal(err)
	}
}

// TestCatalogFromCache exercises host.Catalog offline by seeding the on-disk
// models cache the real loader reads, so no network is involved.
func TestCatalogFromCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cacheDir := filepath.Join(home, ".strike", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := `{"anthropic":{"id":"anthropic","name":"Anthropic","models":` +
		`{"claude-sonnet-5":{"id":"claude-sonnet-5","name":"Sonnet"},` +
		`"claude-opus-4-8":{"id":"claude-opus-4-8","name":"Opus"}}}}`
	if err := os.WriteFile(filepath.Join(cacheDir, "models.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(nil, nil, nil, nil, nil, nil)

	ids, err := svc.Catalog.ModelIDs(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude-opus-4-8", "claude-sonnet-5"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ModelIDs = %v, want %v", ids, want)
	}

	// A provider absent from the catalog yields the exact empty-list message.
	_, err = svc.Catalog.ModelIDs(context.Background(), "xai")
	if err == nil || err.Error() != "no models listed for xai on models.dev" {
		t.Errorf("empty-list error = %v, want \"no models listed for xai on models.dev\"", err)
	}
}

func TestCatalogContextWindowAndOutputLimitFromCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cacheDir := filepath.Join(home, ".strike", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := `{"openai":{"id":"openai","name":"OpenAI","models":{` +
		`"gpt-big":{"id":"gpt-big","name":"Big","limit":{"context":200000,"output":8192}},` +
		`"gpt-bare":{"id":"gpt-bare","name":"Bare"}` +
		`}}}`
	if err := os.WriteFile(filepath.Join(cacheDir, "models.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	tokens, ok, err := svc.Catalog.ContextWindow(ctx, "openai", "gpt-big")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || tokens != 200_000 {
		t.Errorf("ContextWindow(gpt-big) = %d,%v want 200000,true", tokens, ok)
	}
	tokens, ok, err = svc.Catalog.OutputLimit(ctx, "openai", "gpt-big")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || tokens != 8192 {
		t.Errorf("OutputLimit(gpt-big) = %d,%v want 8192,true", tokens, ok)
	}

	tokens, ok, err = svc.Catalog.ContextWindow(ctx, "openai", "gpt-bare")
	if err != nil {
		t.Fatal(err)
	}
	if ok || tokens != 0 {
		t.Errorf("ContextWindow(gpt-bare) = %d,%v want 0,false", tokens, ok)
	}
	tokens, ok, err = svc.Catalog.OutputLimit(ctx, "openai", "gpt-bare")
	if err != nil {
		t.Fatal(err)
	}
	if ok || tokens != 0 {
		t.Errorf("OutputLimit(gpt-bare) = %d,%v want 0,false", tokens, ok)
	}
}

func TestCatalogModelsMetadataFromCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cacheDir := filepath.Join(home, ".strike", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := `{"openai":{"id":"openai","name":"OpenAI","models":{` +
		`"gpt-full":{"id":"gpt-full","name":"Full","limit":{"context":128000,"output":16384},` +
		`"cost":{"input":2.5,"output":10},"tool_call":true,"reasoning":true,"attachment":true},` +
		`"gpt-bare":{"id":"gpt-bare","name":"Bare"}` +
		`}}}`
	if err := os.WriteFile(filepath.Join(cacheDir, "models.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(nil, nil, nil, nil, nil, nil)
	infos, err := svc.Catalog.Models(context.Background(), "openai")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[0].ID != "gpt-bare" || infos[1].ID != "gpt-full" {
		t.Fatalf("Models = %#v", infos)
	}
	full := infos[1]
	if full.Context != 128_000 || !full.HasCost || full.InputCost != 2.5 || full.OutputCost != 10 {
		t.Errorf("gpt-full meta = %+v", full)
	}
	if !full.ToolCall || !full.Reasoning || !full.Attachment {
		t.Errorf("gpt-full caps = %+v", full)
	}
	if infos[0].HasCost || infos[0].Context != 0 {
		t.Errorf("gpt-bare should lack meta: %+v", infos[0])
	}
}
