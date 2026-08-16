package secret_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/trust/secret"
)

func TestRedact(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		banned string
	}{
		{"anthropic", "key sk-ant-api03-abcdefghijklmnopqrstuvwxyz012345", "sk-ant-api03-abcdefghijklmnopqrstuvwxyz012345"},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123", "abcdefghijklmnopqrstuvwxyz0123"},
		{"github", "export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"assignment", `api_key=supersecretvalue123`, "supersecretvalue123"},
		{"json apiKey", `{"apiKey":"super-secret-value-here"}`, "super-secret-value-here"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := secret.Redact(tt.in)
			if strings.Contains(got, tt.banned) {
				t.Fatalf("Redact still contains %q → %q", tt.banned, got)
			}
			if !strings.Contains(got, "REDACTED") {
				t.Fatalf("missing placeholder → %q", got)
			}
		})
	}
	if got := secret.Redact("please review the auth package"); got != "please review the auth package" {
		t.Fatalf("prose mangled: %q", got)
	}
}

func TestScrubToolOutput(t *testing.T) {
	tok := "a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0u1v2"
	got := secret.ScrubToolOutput("dump: " + tok)
	if strings.Contains(got, tok) {
		t.Fatalf("high-entropy leaked: %q", got)
	}
	sha := "a1b2c3d4e5f6789012345678abcdef0123456789"
	in := "HEAD " + sha
	if got := secret.ScrubToolOutput(in); got != in {
		t.Fatalf("git SHA redacted: %q", got)
	}
	nested := `outer {"inner":"sk-abcdefghijklmnopqrstuvwxyz0123456789"}`
	got = secret.ScrubToolOutput(nested)
	if strings.Contains(got, "sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatalf("nested key leaked: %q", got)
	}
}

func TestRedactError(t *testing.T) {
	if got := secret.RedactError(errors.New("Authorization: Bearer super-secret-token")); got != "start failed" {
		t.Fatalf("bearer = %q", got)
	}
	if got := secret.RedactError(errors.New("connection refused")); got != "connection refused" {
		t.Fatalf("plain = %q", got)
	}
}
