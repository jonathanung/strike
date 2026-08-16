package theme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// uiTokenFile is the shared stock palette + chrome contract.
type uiTokenFile struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Chrome        struct {
		Mode        string `json:"mode"`
		Corners     string `json:"corners"`
		RadiusWebPx int    `json:"radiusWebPx"`
	} `json:"chrome"`
	Roles map[string]struct {
		Light  string `json:"light"`
		Dark   string `json:"dark"`
		CSSVar string `json:"cssVar"`
	} `json:"roles"`
}

var hexPair = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func loadUITokens(t *testing.T) uiTokenFile {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/frontend/tui/theme → repo root
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "schemas", "ui-tokens.json"))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc uiTokenFile
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

func TestUITokenFileContract(t *testing.T) {
	doc := loadUITokens(t)
	if doc.SchemaVersion != "1" {
		t.Errorf("schemaVersion = %q, want 1", doc.SchemaVersion)
	}
	if doc.ID != "strike-default" {
		t.Errorf("id = %q, want strike-default", doc.ID)
	}
	if doc.Chrome.Mode != "bordered" {
		t.Errorf("chrome.mode = %q, want bordered", doc.Chrome.Mode)
	}
	if doc.Chrome.Corners != "square" {
		t.Errorf("chrome.corners = %q, want square", doc.Chrome.Corners)
	}
	if doc.Chrome.RadiusWebPx != 2 {
		t.Errorf("chrome.radiusWebPx = %d, want 2", doc.Chrome.RadiusWebPx)
	}

	required := []string{
		"text", "textMuted", "accent", "accentAlt", "highlight",
		"success", "warning", "error", "danger", "background",
		"surface", "surfaceFocus", "surfaceMuted",
		"border", "borderFocus", "borderMuted",
		"userLabel", "toolLabel", "diffAdded", "diffRemoved", "overlayScrim",
	}
	if len(doc.Roles) != len(required) {
		t.Errorf("roles count = %d, want %d", len(doc.Roles), len(required))
	}
	seenCSS := map[string]string{}
	for _, name := range required {
		role, ok := doc.Roles[name]
		if !ok {
			t.Errorf("missing role %q", name)
			continue
		}
		if !hexPair.MatchString(strings.ToLower(role.Light)) {
			t.Errorf("roles.%s.light = %q, want #rrggbb", name, role.Light)
		}
		if !hexPair.MatchString(strings.ToLower(role.Dark)) {
			t.Errorf("roles.%s.dark = %q, want #rrggbb", name, role.Dark)
		}
		if strings.EqualFold(role.Light, role.Dark) {
			t.Errorf("roles.%s uses the same hex for light and dark (%q)", name, role.Light)
		}
		if role.CSSVar == "" || !strings.HasPrefix(role.CSSVar, "--") {
			t.Errorf("roles.%s.cssVar = %q, want --name", name, role.CSSVar)
		}
		if prev, dup := seenCSS[role.CSSVar]; dup {
			t.Errorf("cssVar %s used by %s and %s", role.CSSVar, prev, name)
		}
		seenCSS[role.CSSVar] = name
	}

	// Royal purple is the hero, not pastel violet-300.
	accent := doc.Roles["accent"]
	if strings.EqualFold(accent.Dark, "#c4b5fd") {
		t.Error("accent.dark is pastel #c4b5fd; royal hero should be #7c3aed-class")
	}
	if !strings.EqualFold(accent.Light, "#5b21b6") || !strings.EqualFold(accent.Dark, "#7c3aed") {
		t.Errorf("accent = %s/%s, want royal #5b21b6/#7c3aed", accent.Light, accent.Dark)
	}
}

func TestDefaultPaletteMatchesTokenFile(t *testing.T) {
	doc := loadUITokens(t)
	th := Default()
	got := map[string]AdaptiveColor{
		"text":         th.Text,
		"textMuted":    th.TextMuted,
		"accent":       th.Accent,
		"accentAlt":    th.AccentAlt,
		"highlight":    th.Highlight,
		"success":      th.Success,
		"warning":      th.Warning,
		"error":        th.Error,
		"danger":       th.Danger,
		"surface":      th.Surface,
		"surfaceFocus": th.SurfaceFocus,
		"surfaceMuted": th.SurfaceMuted,
		"border":       th.Border,
		"borderFocus":  th.BorderFocus,
		"borderMuted":  th.BorderMuted,
		"userLabel":    th.UserLabel,
		"toolLabel":    th.ToolLabel,
		"diffAdded":    th.DiffAdded,
		"diffRemoved":  th.DiffRemoved,
		"overlayScrim": th.OverlayScrim,
	}
	for name, role := range doc.Roles {
		if name == "background" {
			continue
		}
		want := AdaptiveColor{Light: role.Light, Dark: role.Dark}
		if g, ok := got[name]; !ok {
			t.Errorf("Default() has no role %q from token file", name)
		} else if g != want {
			t.Errorf("Default().%s = %#v, want token %#v", name, g, want)
		}
	}
	bg, ok := th.Background.(AdaptiveColor)
	if !ok {
		t.Fatalf("Default().Background type = %T, want AdaptiveColor", th.Background)
	}
	wantBG := doc.Roles["background"]
	if bg != (AdaptiveColor{Light: wantBG.Light, Dark: wantBG.Dark}) {
		t.Errorf("Default().Background = %#v, want token %#v", bg, wantBG)
	}
	// Token chrome is the north star; Default() stays soft until #1234.
	if th.Chrome != ChromeSoft {
		t.Errorf("Default().Chrome = %v, want ChromeSoft (bordered chrome lands in #1234)", th.Chrome)
	}
}

func TestBundledNamedThemesKeepOwnHexes(t *testing.T) {
	doc := loadUITokens(t)
	stock := AdaptiveColor{Light: doc.Roles["accent"].Light, Dark: doc.Roles["accent"].Dark}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "themes", "nord.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := Parse(data, "nord")
	if err != nil {
		t.Fatal(err)
	}
	th := entry.Theme.Resolve()
	if th.Accent == stock {
		t.Errorf("nord Accent collapsed onto stock token accent %#v", stock)
	}
}
