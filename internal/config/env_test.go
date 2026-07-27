package config

import "testing"

func TestExpandEnv(t *testing.T) {
	t.Setenv("STRIKE_TEST_KEY", "secret-value")
	t.Setenv("STRIKE_TEST_URL", "https://proxy.example/v1")

	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"plain", "plain"},
		{"{env:STRIKE_TEST_KEY}", "secret-value"},
		{"Bearer {env:STRIKE_TEST_KEY}", "Bearer secret-value"},
		{"$STRIKE_TEST_KEY", "secret-value"},
		{"${STRIKE_TEST_KEY}", "secret-value"},
		{"{env:STRIKE_TEST_URL}/chat", "https://proxy.example/v1/chat"},
		{"{env:STRIKE_TEST_MISSING}", ""},
		{"$STRIKE_TEST_MISSING", ""},
	}
	for _, tc := range cases {
		if got := ExpandEnv(tc.in); got != tc.want {
			t.Errorf("ExpandEnv(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnvRefName(t *testing.T) {
	cases := []struct {
		in   string
		name string
		ok   bool
	}{
		{"{env:FOO}", "FOO", true},
		{"$FOO", "FOO", true},
		{"${FOO}", "FOO", true},
		{"  $BAR  ", "BAR", true},
		{"Bearer $FOO", "", false},
		{"{env:FOO}x", "", false},
		{"plain", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		name, ok := EnvRefName(tc.in)
		if ok != tc.ok || name != tc.name {
			t.Errorf("EnvRefName(%q) = %q,%v want %q,%v", tc.in, name, ok, tc.name, tc.ok)
		}
	}
}

func TestNormalizeAPIKeyEnv(t *testing.T) {
	if got := NormalizeAPIKeyEnv("{env:KIMI_API_KEY}"); got != "KIMI_API_KEY" {
		t.Errorf("got %q", got)
	}
	if got := NormalizeAPIKeyEnv("$KIMI_API_KEY"); got != "KIMI_API_KEY" {
		t.Errorf("got %q", got)
	}
	if got := NormalizeAPIKeyEnv("KIMI_API_KEY"); got != "KIMI_API_KEY" {
		t.Errorf("got %q", got)
	}
}

func TestValidateBaseURLEnvRef(t *testing.T) {
	if err := ValidateBaseURL("{env:MY_BASE}"); err != nil {
		t.Fatalf("env ref baseURL: %v", err)
	}
	if err := ValidateBaseURL("$MY_BASE"); err != nil {
		t.Fatalf("$ baseURL: %v", err)
	}
	if err := ValidateBaseURL("https://ok.example/v1"); err != nil {
		t.Fatalf("plain: %v", err)
	}
}

func TestResolveCustom(t *testing.T) {
	t.Setenv("CP_BASE", "https://api.example/v1")
	t.Setenv("CP_HDR", "hdr-val")
	p := ResolveCustom(CustomProvider{
		Name:      "proxy",
		BaseURL:   "{env:CP_BASE}",
		API:       WireOpenAI,
		APIKeyEnv: "{env:CP_KEY}",
		Headers:   map[string]string{"X-T": "{env:CP_HDR}"},
	})
	if p.BaseURL != "https://api.example/v1" {
		t.Errorf("BaseURL = %q", p.BaseURL)
	}
	if p.APIKeyEnv != "CP_KEY" {
		t.Errorf("APIKeyEnv = %q", p.APIKeyEnv)
	}
	if p.Headers["X-T"] != "hdr-val" {
		t.Errorf("header = %q", p.Headers["X-T"])
	}
}
