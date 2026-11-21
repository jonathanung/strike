package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestValidateLinkPattern(t *testing.T) {
	const tmpl = "https://tracker.example.com/browse/{match}"
	tests := []struct {
		name    string
		rule    LinkPattern
		wantErr string // substring; empty means valid
	}{
		{"jira", LinkPattern{"Jira", `\b[A-Z]{2,}-\d+\b`, tmpl}, ""},
		{"http template", LinkPattern{"Build", `build-\d+`, "http://ci.example/{match}?x=1"}, ""},
		{"placeholder in query", LinkPattern{"Q", `x`, "https://h.example/search?q={match}"}, ""},
		{"uppercase scheme", LinkPattern{"U", `x`, "HTTPS://h.example/{match}"}, ""},
		{"empty name", LinkPattern{"", `x`, tmpl}, "name is required"},
		{"long name", LinkPattern{strings.Repeat("n", MaxLinkPatternNameLen+1), `x`, tmpl}, "name is longer"},
		{"name at limit in runes", LinkPattern{strings.Repeat("é", MaxLinkPatternNameLen), `x`, tmpl}, ""},
		{"empty pattern", LinkPattern{"A", "", tmpl}, "pattern is required"},
		{"long pattern", LinkPattern{"A", strings.Repeat("a", MaxLinkPatternLen+1), tmpl}, "longer than 512 bytes"},
		{"bad regex", LinkPattern{"A", `(`, tmpl}, "invalid regular expression"},
		{"backreference unsupported", LinkPattern{"A", `(a)\1`, tmpl}, "invalid regular expression"},
		{"repeat over parser limit", LinkPattern{"A", `a{1001}`, tmpl}, "invalid regular expression"},
		{"nested repeat over parser limit", LinkPattern{"A", `(?:[a-z]{100}){100}`, tmpl}, "invalid regular expression"},
		{"program too large", LinkPattern{"A", strings.Repeat(`[a-z]{1000}`, 11), tmpl}, "too complex"},
		{"matches empty", LinkPattern{"A", `a*`, tmpl}, "matches empty text"},
		{"optional matches empty", LinkPattern{"A", `(x)?`, tmpl}, "matches empty text"},
		{"empty template", LinkPattern{"A", `x`, ""}, "URL template is required"},
		{"long template", LinkPattern{"A", `x`, "https://h.example/{match}" + strings.Repeat("a", MaxLinkPatternTemplateLen)}, "URL template is longer"},
		{"javascript scheme", LinkPattern{"A", `x`, "javascript:alert({match})"}, "must start with https://"},
		{"relative template", LinkPattern{"A", `x`, "/browse/{match}"}, "must start with https://"},
		{"no placeholder", LinkPattern{"A", `x`, "https://h.example/browse"}, "must contain {match}"},
		{"space", LinkPattern{"A", `x`, "https://h.example/a b/{match}"}, "spaces"},
		{"quote", LinkPattern{"A", `x`, `https://h.example/"{match}`}, "spaces"},
		{"control char", LinkPattern{"A", `x`, "https://h.example/\x01{match}"}, "spaces"},
		{"placeholder in host", LinkPattern{"A", `x`, "https://{match}.example/"}, "host must be fixed"},
		{"userinfo", LinkPattern{"A", `x`, "https://user@h.example/{match}"}, "host must be fixed"},
		{"no host", LinkPattern{"A", `x`, "https:///{match}"}, "needs a host"},
		{"braces in host", LinkPattern{"A", `x`, "https://{x}/{match}"}, "needs a host"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLinkPattern(tc.rule)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateLinkPattern(%+v) = %v, want nil", tc.rule, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateLinkPattern(%+v) = %v, want error containing %q", tc.rule, err, tc.wantErr)
			}
		})
	}
}

func TestValidateLinkPatternsNormalizesAndCaps(t *testing.T) {
	good := LinkPattern{"A", `x`, "https://h.example/{match}"}
	tests := []struct {
		name    string
		in      []LinkPattern
		want    []LinkPattern
		wantErr string
	}{
		{"empty", nil, []LinkPattern{}, ""},
		{"trims name and template, keeps pattern", []LinkPattern{{"  A ", " x ", " https://h.example/{match} "}},
			[]LinkPattern{{"A", " x ", "https://h.example/{match}"}}, ""},
		{"at cap", make50(good), make50(good), ""},
		{"over cap", append(make50(good), good), nil, "at most 50"},
		{"names the bad rule", []LinkPattern{good, {"B", `(`, "https://h.example/{match}"}}, nil, "link pattern 2:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateLinkPatterns(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rules, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("rule %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
	if _, err := ValidateLinkPatterns(append(make50(good), good)); !errors.Is(err, ErrTooManyLinkPatterns) {
		t.Fatalf("over cap err = %v, want ErrTooManyLinkPatterns", err)
	}
}

func make50(r LinkPattern) []LinkPattern {
	out := make([]LinkPattern, MaxLinkPatterns)
	for i := range out {
		out[i] = r
	}
	return out
}

func TestSetGlobalLinkPatterns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SetGlobalTheme("dracula"); err != nil {
		t.Fatal(err)
	}
	rules := []LinkPattern{
		{" Jira ", `\b[A-Z]{2,}-\d+\b`, "https://tracker.example.com/browse/{match}"},
		{"Build", `build-\d+`, "https://ci.example/{match}"},
	}
	if err := SetGlobalLinkPatterns(rules); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "dracula" || len(got.WebUI.LinkPatterns) != 2 || got.WebUI.LinkPatterns[0].Name != "Jira" ||
		got.WebUI.LinkPatterns[1].Pattern != `build-\d+` {
		t.Fatalf("after save: theme=%q rules=%+v", got.Theme, got.WebUI.LinkPatterns)
	}

	// Invalid input is refused and the stored list is unchanged.
	if err := SetGlobalLinkPatterns([]LinkPattern{{"Bad", `a*`, "https://h.example/{match}"}}); err == nil {
		t.Fatal("accepted an empty-matching pattern")
	}
	got, err = ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WebUI.LinkPatterns) != 2 {
		t.Fatalf("refused save changed the list: %+v", got.WebUI.LinkPatterns)
	}

	// An empty list clears the key from the file.
	if err := SetGlobalLinkPatterns(nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "webui") || strings.Contains(string(data), "linkPatterns") {
		t.Fatalf("cleared list still written: %s", data)
	}
}

func TestLinkPatternsNotMergedFromProjectLayer(t *testing.T) {
	layer := Config{WebUI: WebUIConfig{LinkPatterns: []LinkPattern{{"A", "x", "https://h.example/{match}"}}}}
	if got := merge(Default(), layer); len(got.WebUI.LinkPatterns) != 0 {
		t.Fatalf("merge copied webui.linkPatterns: %+v", got.WebUI.LinkPatterns)
	}
}
