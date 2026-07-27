package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/issue"
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
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(store, nil, nil, nil, []string{"build", "plan"}, nil, nil, ""), store
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

	wantOrder := []string{"anthropic", "openai", "xai", "gemini", "kimi", "deepseek", "echo"}
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
	if s := by["gemini"]; !s.APIKey || !s.OAuth || s.Device || s.Builtin {
		t.Errorf("gemini flags = %+v, want OAuth+APIKey", s)
	}
	if s := by["kimi"]; !s.APIKey || s.OAuth || s.Device || s.Builtin {
		t.Errorf("kimi flags = %+v, want APIKey-only", s)
	}
	if s := by["deepseek"]; !s.APIKey || s.OAuth || s.Device || s.Builtin {
		t.Errorf("deepseek flags = %+v, want APIKey-only", s)
	}

	echo := by["echo"]
	if !echo.Builtin || !echo.Authed || echo.Detail != "offline dev provider" {
		t.Errorf("echo status = %+v", echo)
	}

	// With an empty store and no env keys, credential providers are unauthed.
	for _, name := range []string{"anthropic", "openai", "xai", "gemini", "kimi", "deepseek"} {
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
	svc := New(store, nil, nil, nil, nil, nil, nil, "")

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

func TestBeginOAuthGemini(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "test-client-id.apps.googleusercontent.com")
	svc, _ := newTestServices(t)
	login, err := svc.Auth.BeginOAuth(context.Background(), "gemini")
	if err != nil {
		t.Fatalf("BeginOAuth(gemini): %v", err)
	}
	if login == nil {
		t.Fatal("BeginOAuth(gemini) returned nil login")
	}
	if login.URL == "" {
		t.Error("URL is empty")
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
	svc := New(nil, nil, nil, nil, nil, skills, nil, "")

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
	svc := New(nil, nil, nil, nil, nil, nil, nil, "")

	if err := svc.Settings.SaveDefaults("openai", "gpt-5.5", "build", "high", "accept-edits"); err != nil {
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
	if got.PermissionMode != "accept-edits" {
		t.Errorf("permissionMode = %q, want accept-edits", got.PermissionMode)
	}
}

func TestSaveThemeWritesGlobalConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := New(nil, nil, nil, nil, nil, nil, nil, "")
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

func TestDefaultsAndSavePresentation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := New(nil, nil, nil, nil, nil, nil, nil, "")

	if d := svc.Settings.Defaults(); d.Theme != "" || d.VimMode != "" {
		t.Fatalf("empty defaults = %#v", d)
	}
	if err := svc.Settings.SaveTheme("nord"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Settings.SavePresentation("overlay", "pane", "modal"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Settings.SaveDefaults("openai", "gpt-x", "build", "high", "yolo"); err != nil {
		t.Fatal(err)
	}
	d := svc.Settings.Defaults()
	if d.Theme != "nord" || d.VimMode != "overlay" || d.NanoMode != "pane" || d.MdReadMode != "modal" {
		t.Errorf("presentation defaults = %#v", d)
	}
	if d.Provider != "openai" || d.Model != "gpt-x" || d.Agent != "build" || d.Effort != "high" || d.PermissionMode != "yolo" {
		t.Errorf("session defaults = %#v", d)
	}
}

func TestHistoryNilTolerated(t *testing.T) {
	svc := New(nil, nil, nil, nil, nil, nil, nil, "")
	if svc.History != nil {
		t.Errorf("nil hist should yield nil Services.History, got %#v", svc.History)
	}
	if svc.Memory != nil {
		t.Errorf("nil mem should yield nil Services.Memory, got %#v", svc.Memory)
	}
	if svc.Issues != nil {
		t.Errorf("nil issues should yield nil Services.Issues, got %#v", svc.Issues)
	}
}

func TestHistoryWiredThrough(t *testing.T) {
	hist, err := history.Open(t.TempDir(), "project-key")
	if err != nil {
		t.Fatal(err)
	}
	defer hist.Close()

	svc := New(nil, hist, nil, nil, nil, nil, nil, "")
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

	svc := New(nil, nil, mem, nil, nil, nil, nil, "")
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

func TestMemoryListFilterAndAll(t *testing.T) {
	mem, err := memory.Open(t.TempDir(), "project-key")
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	svc := New(nil, nil, mem, nil, nil, nil, nil, "")

	empty, err := svc.Memory.List("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("List empty store = %+v err=%v", empty, err)
	}

	if err := svc.Memory.Put("a", "1", []string{"alpha", "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Memory.Put("b", "2", []string{"beta"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Memory.Put("c", "3", []string{"shared"}); err != nil {
		t.Fatal(err)
	}

	all, err := svc.Memory.List("")
	if err != nil || len(all) != 3 {
		t.Fatalf("List all = %+v err=%v", all, err)
	}
	if all[0].Key != "a" || all[1].Key != "b" || all[2].Key != "c" {
		t.Fatalf("List all order = %+v, want a,b,c", all)
	}

	shared, err := svc.Memory.List("shared")
	if err != nil || len(shared) != 2 {
		t.Fatalf("List(shared) = %+v err=%v", shared, err)
	}
	if shared[0].Key != "a" || shared[1].Key != "c" {
		t.Fatalf("List(shared) keys = %+v", shared)
	}

	none, err := svc.Memory.List("missing")
	if err != nil || len(none) != 0 {
		t.Fatalf("List(missing) = %+v err=%v", none, err)
	}
}

func TestMemoryExportImportPathSafe(t *testing.T) {
	work := t.TempDir()
	mem, err := memory.Open(t.TempDir(), "project-key")
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	svc := New(nil, nil, mem, nil, nil, nil, nil, work)
	if err := svc.Memory.Put("k", "v", nil); err != nil {
		t.Fatal(err)
	}

	if err := svc.Memory.Export("../escape.json"); err == nil {
		t.Fatal("expected path escape error for relative ..")
	}
	if err := svc.Memory.Export("backup/memory.json"); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(work, "backup", "memory.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("export file missing: %v", err)
	}

	// Absolute path outside work dir is intentional and allowed.
	abs := filepath.Join(t.TempDir(), "out.json")
	if err := svc.Memory.Export(abs); err != nil {
		t.Fatal(err)
	}

	// Wipe and re-import from relative path.
	if _, err := svc.Memory.Import("backup/memory.json", true); err != nil {
		t.Fatal(err)
	}
	got, ok, err := svc.Memory.Get("k")
	if err != nil || !ok || got.Value != "v" {
		t.Fatalf("after import = %+v ok=%v err=%v", got, ok, err)
	}
	if _, err := svc.Memory.Import("../escape.json", true); err == nil {
		t.Fatal("expected import path escape error")
	}
}

func TestIssuesWiredThrough(t *testing.T) {
	issStore, err := issue.Open(t.TempDir(), "project-key")
	if err != nil {
		t.Fatal(err)
	}
	defer issStore.Close()

	svc := New(nil, nil, nil, issStore, nil, nil, nil, "")
	if svc.Issues == nil {
		t.Fatal("Services.Issues should be non-nil when issues is provided")
	}
	created, err := svc.Issues.Create("fix", "body")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 1 || created.Status != "open" {
		t.Fatalf("created = %+v", created)
	}
	got, ok, err := svc.Issues.Get(1)
	if err != nil || !ok || got.Title != "fix" {
		t.Fatalf("Get: %+v ok=%v err=%v", got, ok, err)
	}
	closed, err := svc.Issues.Close(1)
	if err != nil || closed.Status != "closed" {
		t.Fatalf("Close: %+v err=%v", closed, err)
	}
	list, err := svc.Issues.List("closed")
	if err != nil || len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("List = %+v err=%v", list, err)
	}
}

func TestIssuesExportImportPathSafe(t *testing.T) {
	work := t.TempDir()
	issStore, err := issue.Open(t.TempDir(), "project-key")
	if err != nil {
		t.Fatal(err)
	}
	defer issStore.Close()
	svc := New(nil, nil, nil, issStore, nil, nil, nil, work)
	if _, err := svc.Issues.Create("fix", "body"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Issues.Export("../escape.json"); err == nil {
		t.Fatal("expected path escape error")
	}
	if err := svc.Issues.Export("backup/issues.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "backup", "issues.json")); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Issues.Import("backup/issues.json", true)
	if err != nil || n != 1 {
		t.Fatalf("import = %d err=%v", n, err)
	}
}

func TestIssuesUpdateAndList(t *testing.T) {
	issStore, err := issue.Open(t.TempDir(), "project-key")
	if err != nil {
		t.Fatal(err)
	}
	defer issStore.Close()
	svc := New(nil, nil, nil, issStore, nil, nil, nil, "")

	a, err := svc.Issues.Create("one", "body-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Issues.Create("two", "body-b")
	if err != nil {
		t.Fatal(err)
	}

	title := "one-renamed"
	body := "updated-body"
	status := "closed"
	updated, err := svc.Issues.Update(a.ID, &title, &body, &status)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != title || updated.Body != body || updated.Status != status {
		t.Fatalf("Update = %+v", updated)
	}

	// Partial update leaves unspecified fields alone.
	onlyTitle := "two-renamed"
	partial, err := svc.Issues.Update(b.ID, &onlyTitle, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Title != onlyTitle || partial.Body != "body-b" || partial.Status != "open" {
		t.Fatalf("partial Update = %+v", partial)
	}

	all, err := svc.Issues.List("")
	if err != nil || len(all) != 2 {
		t.Fatalf("List all = %+v err=%v", all, err)
	}
	openOnly, err := svc.Issues.List("open")
	if err != nil || len(openOnly) != 1 || openOnly[0].ID != b.ID {
		t.Fatalf("List open = %+v err=%v", openOnly, err)
	}
	closedOnly, err := svc.Issues.List("closed")
	if err != nil || len(closedOnly) != 1 || closedOnly[0].ID != a.ID {
		t.Fatalf("List closed = %+v err=%v", closedOnly, err)
	}

	if _, err := svc.Issues.Update(99, &title, nil, nil); err == nil {
		t.Fatal("Update missing id should error")
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
	svc := New(nil, nil, nil, nil, nil, nil, nil, "")

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
	svc := New(nil, nil, nil, nil, nil, nil, nil, "")
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
	svc := New(nil, nil, nil, nil, nil, nil, nil, "")
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
	if full.Name != "Full" || full.Source != host.ModelSourceCatalog {
		t.Errorf("gpt-full name/source = %q/%q", full.Name, full.Source)
	}
}

func TestCatalogOverlayMergesWithoutDroppingCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cacheDir := filepath.Join(home, ".strike", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := `{"openai":{"id":"openai","name":"OpenAI","models":{` +
		`"gpt-a":{"id":"gpt-a","name":"A","limit":{"context":100000,"output":1000}},` +
		`"gpt-b":{"id":"gpt-b","name":"B","limit":{"context":50000,"output":500}}` +
		`}}}`
	if err := os.WriteFile(filepath.Join(cacheDir, "models.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	overlays := map[string][]config.ModelDef{
		"openai": {
			{
				ID:    "gpt-a",
				Name:  "A Overlay",
				Limit: &config.ModelLimit{Context: 272000},
				Variants: map[string]map[string]any{
					"high": {"reasoningEffort": "high"},
					"low":  {"reasoningEffort": "low"},
				},
			},
			{ID: "gpt-custom", Name: "Custom Only"},
		},
	}
	customs := config.NewCustomStoreWithOverlays(nil, overlays, nil, "")
	svc := New(nil, nil, nil, nil, nil, nil, customs, "")
	infos, err := svc.Catalog.Models(context.Background(), "openai")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 3 {
		t.Fatalf("want 3 models (2 catalog + 1 config), got %#v", infos)
	}
	byID := map[string]host.ModelInfo{}
	for _, info := range infos {
		byID[info.ID] = info
	}
	a := byID["gpt-a"]
	if a.Name != "A Overlay" || a.Context != 272000 || a.Source != host.ModelSourceMerge {
		t.Errorf("gpt-a overlay = %+v", a)
	}
	if len(a.VariantIDs) != 2 || a.VariantIDs[0] != "high" {
		t.Errorf("variants = %v", a.VariantIDs)
	}
	b := byID["gpt-b"]
	if b.Name != "B" || b.Context != 50000 || b.Source != host.ModelSourceCatalog {
		t.Errorf("gpt-b must stay catalog-only: %+v", b)
	}
	c := byID["gpt-custom"]
	if c.Name != "Custom Only" || c.Source != host.ModelSourceConfig {
		t.Errorf("gpt-custom = %+v", c)
	}

	// Context limit from config drives meter.
	tokens, ok, err := svc.Catalog.ContextWindow(context.Background(), "openai", "gpt-a")
	if err != nil || !ok || tokens != 272000 {
		t.Errorf("ContextWindow overlay = %d,%v,%v", tokens, ok, err)
	}
	// Unoverlaid model keeps catalog limit.
	tokens, ok, err = svc.Catalog.ContextWindow(context.Background(), "openai", "gpt-b")
	if err != nil || !ok || tokens != 50000 {
		t.Errorf("ContextWindow catalog = %d,%v,%v", tokens, ok, err)
	}

	// Omitted overlay → full catalog still listed.
	svcBare := New(nil, nil, nil, nil, nil, nil, config.NewCustomStore(nil, ""), "")
	bare, err := svcBare.Catalog.Models(context.Background(), "openai")
	if err != nil || len(bare) != 2 {
		t.Fatalf("bare catalog = %#v err=%v", bare, err)
	}

	effort, ok, err := svc.Catalog.ResolveVariant(context.Background(), "openai", "gpt-a", "high")
	if err != nil || !ok || effort != "high" {
		t.Errorf("ResolveVariant = %q ok=%v err=%v", effort, ok, err)
	}
}

// TestCatalogBuiltinEndpointKeepsModelsDev lists catalog models when anthropic
// has only options (no models) — endpoint overlay must not drop registration.
func TestCatalogBuiltinEndpointKeepsModelsDev(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROXY_KEY", "from-env")
	cacheDir := filepath.Join(home, ".strike", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := `{"anthropic":{"id":"anthropic","name":"Anthropic","models":{` +
		`"claude-test":{"id":"claude-test","name":"Claude Test","limit":{"context":200000,"output":8192}}` +
		`}}}`
	if err := os.WriteFile(filepath.Join(cacheDir, "models.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	endpoints := map[string]config.ProviderEndpoint{
		"anthropic": {
			BaseURL:   "https://proxy.example/anthropic",
			APIKeyEnv: "PROXY_KEY",
		},
	}
	customs := config.NewCustomStoreWithOverlays(nil, nil, endpoints, "")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store, nil, nil, nil, nil, nil, customs, "")

	// Must not be treated as a custom provider.
	if _, ok := svc.Providers.Get("anthropic"); ok {
		t.Fatal("anthropic endpoint overlay must not create a custom provider row")
	}
	infos, err := svc.Catalog.Models(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != "claude-test" {
		t.Fatalf("catalog models = %#v", infos)
	}
	by := statusByName(svc.Auth.Statuses())
	ant := by["anthropic"]
	if !ant.Authed {
		t.Fatalf("anthropic should be authed via PROXY_KEY: %+v", ant)
	}
	if ant.Detail != "env" && !strings.Contains(ant.Detail, "env") {
		t.Errorf("detail = %q, want env (+ optional host)", ant.Detail)
	}
	if ant.BaseURL != "https://proxy.example/anthropic" {
		t.Errorf("BaseURL = %q", ant.BaseURL)
	}
	if ant.Custom {
		t.Error("anthropic must remain non-custom")
	}
}

func TestCatalogCustomNestedModelsDTO(t *testing.T) {
	cp := config.NormalizeCustomProvider(config.CustomProvider{
		Name:    "acme",
		BaseURL: "https://a.example/v1",
		API:     config.WireOpenAI,
		ModelDefs: []config.ModelDef{
			{
				ID:    "k2",
				Name:  "Acme K2",
				Limit: &config.ModelLimit{Context: 128000, Output: 8192},
				Variants: map[string]map[string]any{
					"medium": {"reasoningEffort": "medium"},
				},
			},
		},
	})
	customs := config.NewCustomStore([]config.CustomProvider{cp}, "")
	svc := New(nil, nil, nil, nil, nil, nil, customs, "")
	infos, err := svc.Catalog.Models(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("infos = %#v", infos)
	}
	got := infos[0]
	if got.ID != "k2" || got.Name != "Acme K2" || got.Context != 128000 || got.Output != 8192 {
		t.Errorf("dto = %+v", got)
	}
	if got.Source != host.ModelSourceConfig || len(got.VariantIDs) != 1 {
		t.Errorf("source/variants = %+v", got)
	}
}
