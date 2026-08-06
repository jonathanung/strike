package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestParseFullTheme(t *testing.T) {
	raw := `{
	  "name": "Test",
	  "id": "test",
	  "defs": { "pink": "#ff00aa" },
	  "colors": {
	    "text": { "light": "#111111", "dark": "#eeeeee" },
	    "accent": "pink",
	    "background": "none"
	  },
	  "border": "heavy"
	}`
	e, err := Parse([]byte(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "test" || e.Name != "Test" {
		t.Fatalf("entry = %+v", e)
	}
	if e.Theme.Text.Light != "#111111" || e.Theme.Text.Dark != "#eeeeee" {
		t.Errorf("text = %+v", e.Theme.Text)
	}
	if e.Theme.Accent.Light != "#ff00aa" || e.Theme.Accent.Dark != "#ff00aa" {
		t.Errorf("accent = %+v", e.Theme.Accent)
	}
	if _, ok := e.Theme.Background.(lipgloss.NoColor); !ok {
		t.Errorf("background type = %T, want NoColor", e.Theme.Background)
	}
	if e.Theme.BorderStyle.Weight != BorderWeightHeavy {
		t.Errorf("border weight = %v", e.Theme.BorderStyle.Weight)
	}
	if e.Theme.Chrome != ChromeSoft {
		t.Errorf("default chrome = %v, want soft", e.Theme.Chrome)
	}
	// Unset roles inherit Default via Resolve inside Parse.
	if e.Theme.Success.Dark == "" {
		t.Error("success should resolve from default")
	}
}

func TestParseRejectsBadIDAndUnknownRole(t *testing.T) {
	if _, err := Parse([]byte(`{"id":"Bad_ID","colors":{}}`), ""); err == nil {
		t.Error("expected invalid id error")
	}
	if _, err := Parse([]byte(`{"id":"ok","colors":{"nope":"#fff"}}`), ""); err == nil || !strings.Contains(err.Error(), "unknown color role") {
		t.Errorf("unknown role err = %v", err)
	}
	if _, err := Parse([]byte(`{"id":"ok","colors":{"text":"not-a-color"}}`), ""); err == nil {
		t.Error("expected bad color error")
	}
}

func TestParseUsesIDHint(t *testing.T) {
	e, err := Parse([]byte(`{"name":"Hinted","colors":{"accent":"#abcdef"}}`), "from-file")
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "from-file" || e.Name != "Hinted" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestParseChromeAndSurfaceRoles(t *testing.T) {
	raw := `{
	  "id": "surf",
	  "chrome": "bordered",
	  "colors": {
	    "surface": {"light": "#f0f0f0", "dark": "#222222"},
	    "surfaceFocus": "#abcdef",
	    "surfaceMuted": "#111111"
	  }
	}`
	e, err := Parse([]byte(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Theme.Chrome != ChromeBordered {
		t.Errorf("chrome = %v, want bordered", e.Theme.Chrome)
	}
	if e.Theme.Surface.Light != "#f0f0f0" || e.Theme.Surface.Dark != "#222222" {
		t.Errorf("surface = %+v", e.Theme.Surface)
	}
	if e.Theme.SurfaceFocus.Light != "#abcdef" {
		t.Errorf("surfaceFocus = %+v", e.Theme.SurfaceFocus)
	}
	if _, err := Parse([]byte(`{"id":"x","chrome":"neon"}`), ""); err == nil {
		t.Error("expected unknown chrome error")
	}
}

func TestBuiltinCatalogContainsStrikeAndJSONThemes(t *testing.T) {
	list := Builtin()
	if len(list) < 2 {
		t.Fatalf("builtin count = %d", len(list))
	}
	if list[0].ID != BuiltinID || list[0].Source != "builtin" {
		t.Fatalf("first = %+v", list[0])
	}
	ids := map[string]bool{}
	for _, e := range list {
		ids[e.ID] = true
		if e.Theme.Text.Dark == "" {
			t.Errorf("%s missing text.dark", e.ID)
		}
	}
	for _, want := range []string{"dracula", "nord", "catppuccin", "gruvbox", "monokai", "tokyo-night"} {
		if !ids[want] {
			t.Errorf("missing builtin %s", want)
		}
	}
}

func TestCatalogProjectOverridesUserAndBuiltin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	userDir := filepath.Join(home, ".strike", "themes")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(work, ".strike", "themes")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(userDir, "dracula.json", `{"name":"User Dracula","id":"dracula","colors":{"accent":{"dark":"#111111","light":"#111111"}}}`)
	write(projectDir, "dracula.json", `{"name":"Project Dracula","id":"dracula","colors":{"accent":{"dark":"#222222","light":"#222222"}}}`)
	write(projectDir, "custom.json", `{"name":"Custom","id":"custom","colors":{"accent":"#abcdef"}}`)
	write(userDir, "broken.json", `{not json`)

	cat := Catalog(work)
	e, ok := Lookup(cat, "dracula")
	if !ok {
		t.Fatal("dracula missing")
	}
	if e.Source != "project" || e.Name != "Project Dracula" || e.Theme.Accent.Dark != "#222222" {
		t.Fatalf("dracula override = %+v accent=%+v", e, e.Theme.Accent)
	}
	custom, ok := Lookup(cat, "custom")
	if !ok || custom.Source != "project" {
		t.Fatalf("custom = %+v ok=%v", custom, ok)
	}
	if _, ok := Lookup(cat, "broken"); ok {
		t.Error("broken theme should be skipped")
	}
	// strike remains first.
	if cat[0].ID != BuiltinID {
		t.Errorf("first id = %s", cat[0].ID)
	}
}

func TestLookupMiss(t *testing.T) {
	if _, ok := Lookup(Builtin(), "nope"); ok {
		t.Error("expected miss")
	}
}

func TestCatalogPluginThemes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	plug := filepath.Join(work, ".strike", "plugins", "acme.themes")
	if err := os.MkdirAll(filepath.Join(plug, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "schemaVersion": 1,
  "id": "acme.themes",
  "version": "1.0.0",
  "name": "Themes",
  "strike": { "min": "0.1.0" },
  "contributions": { "themes": [{ "path": "themes/plug.json" }] }
}`
	if err := os.WriteFile(filepath.Join(plug, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	themeBody := `{"name":"Plug Theme","id":"plug-theme","colors":{"accent":"#123456"}}`
	if err := os.WriteFile(filepath.Join(plug, "themes", "plug.json"), []byte(themeBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cat := Catalog(work)
	e, ok := Lookup(cat, "plug-theme")
	if !ok {
		t.Fatal("plugin theme missing from catalog")
	}
	if e.Source != SourcePlugin || e.Name != "Plug Theme" || e.PluginID != "acme.themes" {
		t.Fatalf("entry=%+v", e)
	}
	if e.Provenance() != "plugin:acme.themes" {
		t.Fatalf("provenance=%q", e.Provenance())
	}

	// Disable and ensure it disappears on next catalog (no process cache in theme).
	lock := `{"schemaVersion":1,"plugins":{"acme.themes":{"enabled":false}}}`
	if err := os.WriteFile(filepath.Join(work, ".strike", "plugins.lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	cat2 := Catalog(work)
	if _, ok := Lookup(cat2, "plug-theme"); ok {
		t.Fatal("disabled plugin theme must not appear")
	}
}

func TestCatalogPluginThemeCollisionAndInvalidSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	// Builtin-ish id "dracula" contributed by plugin should win over builtin
	// when loaded later (project plugin layer is highest).
	plug := filepath.Join(work, ".strike", "plugins", "acme.override")
	if err := os.MkdirAll(filepath.Join(plug, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "schemaVersion": 1,
  "id": "acme.override",
  "version": "1.0.0",
  "name": "Override",
  "strike": { "min": "0.1.0" },
  "contributions": { "themes": [
    { "path": "themes/dracula.json" },
    { "path": "themes/bad.json" }
  ] }
}`
	if err := os.WriteFile(filepath.Join(plug, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	good := `{"name":"Plugin Dracula","id":"dracula","colors":{"accent":{"dark":"#abcdef","light":"#abcdef"}}}`
	if err := os.WriteFile(filepath.Join(plug, "themes", "dracula.json"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plug, "themes", "bad.json"), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Staging dir must be ignored.
	staging := filepath.Join(work, ".strike", "plugins", ".staging-install-xyz")
	if err := os.MkdirAll(filepath.Join(staging, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	cat := Catalog(work)
	// Startup still has builtins.
	if cat[0].ID != BuiltinID {
		t.Fatalf("first = %s", cat[0].ID)
	}
	e, ok := Lookup(cat, "dracula")
	if !ok {
		t.Fatal("dracula missing")
	}
	if e.Source != SourcePlugin || e.PluginID != "acme.override" {
		t.Fatalf("dracula = %+v", e)
	}
	if e.Overrode == "" {
		t.Fatal("expected Overrode provenance for collision")
	}
	if e.Theme.Accent.Dark != "#abcdef" {
		t.Fatalf("accent = %q", e.Theme.Accent.Dark)
	}
	// Invalid theme path must not appear as a separate id.
	if _, ok := Lookup(cat, "bad"); ok {
		t.Fatal("invalid theme must be skipped")
	}
}

func TestCatalogInvalidPluginDoesNotBreakStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	// Malformed plugin.json
	bad := filepath.Join(work, ".strike", "plugins", "broken.pack")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "plugin.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cat := Catalog(work)
	if len(cat) < 2 || cat[0].ID != BuiltinID {
		t.Fatalf("catalog broken by bad plugin: n=%d first=%v", len(cat), cat)
	}
}

func TestUserThemesDirResolvesStrikeDirSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := UserThemesDir()
	// macOS TempDir is often /var/... while EvalSymlinks yields /private/var/...
	// Resolve the existing target dir (themes/ may not exist yet).
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realTarget, "themes")
	if got != want {
		t.Errorf("UserThemesDir() = %q, want %q", got, want)
	}
}

func TestProjectThemesDirResolvesStrikeDirSymlink(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(t.TempDir(), "project-state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(work, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := ProjectThemesDir(work)
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realTarget, "themes")
	if got != want {
		t.Errorf("ProjectThemesDir() = %q, want %q", got, want)
	}
}
