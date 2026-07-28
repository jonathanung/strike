package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
	if e.Theme.Chrome != ChromeSolid {
		t.Errorf("default chrome = %v, want solid", e.Theme.Chrome)
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
