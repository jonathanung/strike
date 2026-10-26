package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWebUIStyleMirrorsUITokens locks the cockpit stylesheet to the TUI token
// file. Drift here means the web UI stopped using the Bubble Tea palette.
func TestWebUIStyleMirrorsUITokens(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	css, err := os.ReadFile(filepath.Join(root, "webui", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "schemas", "ui-tokens.json"))
	if err != nil {
		t.Fatalf("read ui-tokens.json: %v", err)
	}
	var doc struct {
		Roles map[string]struct {
			Light  string `json:"light"`
			Dark   string `json:"dark"`
			CSSVar string `json:"cssVar"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse tokens: %v", err)
	}
	sheet := string(css)
	if len(doc.Roles) != 21 {
		t.Fatalf("roles = %d, want 21", len(doc.Roles))
	}
	for name, role := range doc.Roles {
		for _, needle := range []string{role.Light, role.Dark, role.CSSVar} {
			if needle == "" || !strings.Contains(sheet, needle) {
				t.Errorf("%s: stylesheet missing %q", name, needle)
			}
		}
	}
	for _, needle := range []string{
		".badge-running",
		"var(--accent-alt)",
		".badge-blocked",
		"var(--warning)",
		".badge-failed",
		"var(--signal)",
		".badge-done",
		"var(--success)",
		".badge-stopped",
		"var(--muted)",
		".gutter-running",
		".diff-add",
		"var(--diff-add)",
		".diff-del",
		"var(--diff-del)",
		`[data-theme="nord"]`,
		`[data-theme="dracula"]`,
		`[data-theme="gruvbox"]`,
		`[data-theme="monokai"]`,
		`[data-theme="tokyo-night"]`,
		`[data-theme="catppuccin"]`,
	} {
		if !strings.Contains(sheet, needle) {
			t.Errorf("stylesheet missing %s", needle)
		}
	}
}
