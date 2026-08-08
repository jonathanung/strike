package main

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestThemesAdapterListsBuiltinsWithPortableColors(t *testing.T) {
	var a themesAdapter
	list := a.List("")
	if len(list) < 2 {
		t.Fatalf("expected builtins, got %d", len(list))
	}
	var strikeOK, nordOK bool
	for _, th := range list {
		switch th.ID {
		case theme.BuiltinID:
			strikeOK = true
			if th.Provenance != "builtin" {
				t.Errorf("strike provenance = %q", th.Provenance)
			}
			if th.Appearance != "adaptive" {
				t.Errorf("appearance = %q", th.Appearance)
			}
			if th.Colors.Text.Dark == "" || th.Colors.Accent.Dark == "" {
				t.Errorf("strike missing portable colors: %+v", th.Colors)
			}
			if !strings.EqualFold(th.Colors.Text.Dark, "#f3f1fa") {
				t.Errorf("strike text.dark = %q", th.Colors.Text.Dark)
			}
		case "nord":
			nordOK = true
			if th.Colors.Text.Dark == "" {
				t.Errorf("nord missing text: %+v", th.Colors)
			}
		}
	}
	if !strikeOK || !nordOK {
		t.Fatalf("missing strike/nord in %v", themeIDs(list))
	}
	got, ok := a.Get("", "nord")
	if !ok || got.ID != "nord" {
		t.Fatalf("Get nord = %+v ok=%v", got, ok)
	}
	if _, ok := a.Get("", "no-such-theme"); ok {
		t.Fatal("expected missing theme")
	}
}

func TestThemesAdapterGetEmpty(t *testing.T) {
	var a themesAdapter
	if _, ok := a.Get("", ""); ok {
		t.Fatal("empty id should miss")
	}
}

func themeIDs(list []host.ThemeInfo) []string {
	out := make([]string, len(list))
	for i, th := range list {
		out[i] = th.ID
	}
	return out
}
