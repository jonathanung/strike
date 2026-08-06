package redact_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

func TestStringRedactsCommonSecrets(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		banned string
	}{
		{"anthropic", "key sk-ant-api03-ABCDEFGHIJKLMNOP secret", "sk-ant-api03-ABCDEFGHIJKLMNOP"},
		{"openai-ish", "token sk-abcdefghijklmnopqrstuvwxyz0123456789", "sk-abcdefghijklmnopqrstuvwxyz0123456789"},
		{"xai", "xai-abcdefghijklmnopqrstuv", "xai-abcdefghijklmnopqrstuv"},
		{"bearer", "Authorization: Bearer tok_abc1234567890", "tok_abc1234567890"},
		{"openai-env", "OPENAI_API_KEY=sk-proj-hello-world-99", "sk-proj-hello-world-99"},
		{"anthropic-env", "ANTHROPIC_API_KEY=super-secret-value-here", "super-secret-value-here"},
		{"gemini-env", "GEMINI_API_KEY=AIzaSyFakeKeyValue123456", "AIzaSyFakeKeyValue123456"},
		{"api_key_assign", "api_key: super-secret-value-here", "super-secret-value-here"},
		{"github", "export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"github_pat", "github_pat_abcdefghijklmnopqrstuv", "github_pat_abcdefghijklmnopqrstuv"},
		{"aws", "AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
		{"slack", "xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := redact.String(tt.in)
			if strings.Contains(got, tt.banned) {
				t.Errorf("String(%q) still contains %q → %q", tt.in, tt.banned, got)
			}
			if !strings.Contains(got, "REDACTED") {
				t.Errorf("String(%q) = %q, want REDACTED marker", tt.in, got)
			}
		})
	}
}

func TestStringPreservesProse(t *testing.T) {
	in := "please review the auth package and keybinds"
	if got := redact.String(in); got != in {
		t.Fatalf("prose mangled: %q → %q", in, got)
	}
}

func TestStringEmpty(t *testing.T) {
	if got := redact.String(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestStringNestedToolEcho(t *testing.T) {
	// Tool result that echoes a key must still scrub (bypass attempt).
	// Real newlines (not the two-char sequence \n) so env-value \S+ stops at EOL.
	nested := "{\"stdout\":\"ANTHROPIC_API_KEY=sk-ant-api03-LEAKEDVALUEHERE99\nBearer supersecrettoken12\"}"
	got := redact.String(nested)
	for _, banned := range []string{"sk-ant-api03-LEAKEDVALUEHERE99", "supersecrettoken12"} {
		if strings.Contains(got, banned) {
			t.Errorf("nested echo still contains %q → %q", banned, got)
		}
	}
}

func TestContainsSecret(t *testing.T) {
	if !redact.ContainsSecret("sk-ant-api03-ABCDEFGHIJKLMNOP") {
		t.Fatal("expected secret detection")
	}
	if redact.ContainsSecret("hello world") {
		t.Fatal("prose should not look like a secret")
	}
}

func TestBytes(t *testing.T) {
	in := []byte("Bearer abcdefghijklmnop")
	got := redact.Bytes(in)
	if strings.Contains(string(got), "abcdefghijklmnop") {
		t.Fatalf("bytes not redacted: %s", got)
	}
	if redact.Bytes(nil) != nil {
		t.Fatal("nil bytes should stay nil")
	}
}

func TestScrubToolOutputHighEntropyAndHex(t *testing.T) {
	tok := "a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0u1v2"
	got := redact.ScrubToolOutput("dump: " + tok)
	if strings.Contains(got, tok) {
		t.Fatalf("high-entropy leaked: %q", got)
	}
	sha := "a1b2c3d4e5f6789012345678abcdef0123456789"
	in := "HEAD " + sha
	if got := redact.ScrubToolOutput(in); got != in {
		t.Fatalf("git SHA redacted: %q", got)
	}
}

func TestError(t *testing.T) {
	if got := redact.Error(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := redact.Error(errBearer); got != "start failed" {
		t.Fatalf("bearer = %q", got)
	}
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

var errBearer simpleErr = "Authorization: Bearer super-secret-token-xx"

func TestJSONRedactsAPIKeyField(t *testing.T) {
	in := json.RawMessage(`{"apiKey":"super-secret-value-here","ok":true}`)
	got := string(redact.JSON(in))
	if strings.Contains(got, "super-secret-value-here") {
		t.Fatalf("apiKey leaked: %s", got)
	}
}
