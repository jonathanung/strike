package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSettingsLinkPatternsRoundTrip exercises PATCH → ~/.strike/config → GET
// for webui.linkPatterns through the real local settings adapter (#1879).
func TestSettingsLinkPatternsRoundTrip(t *testing.T) {
	srv := newGuardrailServer(t)

	// A gateway that stores rules always reports the field, even when empty.
	res := getSettingsBody(t, srv)
	if !strings.Contains(res, `"linkPatterns":[]`) {
		t.Fatalf("initial GET missing empty linkPatterns: %s", res)
	}

	save := `{"linkPatterns":[` +
		`{"name":" Jira ","pattern":"\\b[A-Z]{2,}-\\d+\\b","template":"https://tracker.example.com/browse/{match}"},` +
		`{"name":"Build","pattern":"build-\\d+","template":"https://ci.example/{match}"}]}`
	if r := patchSettings(t, srv, save); r.Code != http.StatusOK {
		t.Fatalf("save = %d %s", r.Code, r.Body.String())
	}
	got := getSettingsJSON(t, srv).LinkPatterns
	want := []linkPatternJSON{
		{Name: "Jira", Pattern: `\b[A-Z]{2,}-\d+\b`, Template: "https://tracker.example.com/browse/{match}"},
		{Name: "Build", Pattern: `build-\d+`, Template: "https://ci.example/{match}"},
	}
	if len(got) != len(want) {
		t.Fatalf("GET linkPatterns = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rule %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Other patches leave the list alone.
	if r := patchSettings(t, srv, `{"theme":"dracula"}`); r.Code != http.StatusOK {
		t.Fatalf("theme patch = %d %s", r.Code, r.Body.String())
	}
	if n := len(getSettingsJSON(t, srv).LinkPatterns); n != 2 {
		t.Fatalf("theme patch changed linkPatterns: %d rules", n)
	}

	// Invalid lists are refused with 400 and nothing is stored.
	tooMany := `{"linkPatterns":[` + strings.TrimSuffix(strings.Repeat(
		`{"name":"A","pattern":"x","template":"https://h.example/{match}"},`, 51), ",") + `]}`
	refused := []struct {
		name string
		body string
		want string
	}{
		{"bad regex", `{"linkPatterns":[{"name":"A","pattern":"(","template":"https://h.example/{match}"}]}`, "invalid regular expression"},
		{"matches empty", `{"linkPatterns":[{"name":"A","pattern":"a*","template":"https://h.example/{match}"}]}`, "matches empty text"},
		{"too complex", `{"linkPatterns":[{"name":"A","pattern":"` + strings.Repeat(`[a-z]{1000}`, 11) + `","template":"https://h.example/{match}"}]}`, "too complex"},
		{"script template", `{"linkPatterns":[{"name":"A","pattern":"x","template":"javascript:alert({match})"}]}`, "https://"},
		{"host from match", `{"linkPatterns":[{"name":"A","pattern":"x","template":"https://{match}.example/"}]}`, "host must be fixed"},
		{"too many", tooMany, "at most 50"},
		{"unknown field", `{"linkPatterns":[{"name":"A","pattern":"x","template":"https://h.example/{match}","extra":1}]}`, "unknown field"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			r := patchSettings(t, srv, tc.body)
			if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), tc.want) {
				t.Fatalf("PATCH = %d %s, want 400 containing %q", r.Code, r.Body.String(), tc.want)
			}
			if n := len(getSettingsJSON(t, srv).LinkPatterns); n != 2 {
				t.Fatalf("refused PATCH changed linkPatterns: %d rules", n)
			}
		})
	}

	// An empty list clears the rules.
	if r := patchSettings(t, srv, `{"linkPatterns":[]}`); r.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", r.Code, r.Body.String())
	}
	if body := getSettingsBody(t, srv); !strings.Contains(body, `"linkPatterns":[]`) {
		t.Fatalf("after clear: %s", body)
	}
}

func TestSettingsPatchFieldsAuditsLinkPatterns(t *testing.T) {
	empty := []linkPatternJSON{}
	tests := []struct {
		name string
		body settingsPatchBody
		want bool
	}{
		{"absent", settingsPatchBody{Theme: "x"}, false},
		{"cleared", settingsPatchBody{LinkPatterns: &empty}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := false
			for _, f := range settingsPatchFields(tc.body) {
				if f == "linkPatterns" {
					got = true
				}
			}
			if got != tc.want {
				t.Fatalf("linkPatterns audited = %v, want %v", got, tc.want)
			}
		})
	}
}

func getSettingsBody(t *testing.T, srv *Server) string {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET settings = %d %s", res.Code, res.Body.String())
	}
	return res.Body.String()
}
