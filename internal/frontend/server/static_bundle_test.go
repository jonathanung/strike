package server

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// TestEmbeddedMobileCSSSegmentedScope guards #1710/#1551 at the served
// layer: the binary embeds internal/frontend/server/static/, and
// `make build` does not regenerate it, so a source-only fix in
// webui/mobile.css can ship while the served CSS still carries the old
// rule. The full bundle-vs-source divergence check lives in
// scripts/check-webui-bundle.mjs (make webui-bundle-check); this test
// pins the specific rule in the embedded copy every build serves.
func TestEmbeddedMobileCSSSegmentedScope(t *testing.T) {
	entries, err := fs.ReadDir(staticFS, "assets")
	if err != nil {
		t.Fatalf("read embedded assets: %v", err)
	}
	var mobile string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "mobile-") && strings.HasSuffix(name, ".css") {
			if mobile != "" {
				t.Fatalf("multiple embedded mobile CSS files: %s and %s", mobile, name)
			}
			mobile = name
		}
	}
	if mobile == "" {
		t.Fatal("no embedded mobile-*.css in the committed bundle")
	}
	data, err := fs.ReadFile(staticFS, "assets/"+mobile)
	if err != nil {
		t.Fatalf("read embedded %s: %v", mobile, err)
	}
	// Strip comments first: mobile.css documents the old blanket rule in
	// a comment, and that mention is not a live rule.
	rules := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(data), "")

	scoped := regexp.MustCompile(
		`body\[data-phone="1"\]\s+\.page-header\s+\.segmented\s*\{[^}]*display\s*:\s*none`)
	if !scoped.MatchString(rules) {
		t.Errorf("embedded %s: missing header-scoped phone hide "+
			`body[data-phone="1"] .page-header .segmented { display: none }`, mobile)
	}
	blanket := regexp.MustCompile(`body\[data-phone="1"\]\s+\.segmented\s*(\{|,)`)
	if blanket.MatchString(rules) {
		t.Errorf("embedded %s: still contains the blanket phone hide "+
			`body[data-phone="1"] .segmented (#1551 trap); `+
			"regenerate the bundle with 'make webui-build'", mobile)
	}
}
