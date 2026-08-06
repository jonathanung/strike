package secret_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/secret"
)

func TestRedact(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		banned string
		want   string // empty = only check banned gone + placeholder present
	}{
		{
			name:   "anthropic",
			in:     "key sk-ant-api03-abcdefghijklmnopqrstuvwxyz012345",
			banned: "sk-ant-api03-abcdefghijklmnopqrstuvwxyz012345",
			want:   "key [REDACTED_ANTHROPIC_KEY]",
		},
		{
			name:   "openai-ish",
			in:     "token sk-abcdefghijklmnopqrstuvwxyz0123456789",
			banned: "sk-abcdefghijklmnopqrstuvwxyz0123456789",
			want:   "token [REDACTED_API_KEY]",
		},
		{
			name:   "bearer",
			in:     "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123",
			banned: "abcdefghijklmnopqrstuvwxyz0123",
			want:   "Authorization: Bearer [REDACTED]",
		},
		{
			name:   "github",
			in:     "export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789",
			banned: "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		},
		{
			name:   "assignment",
			in:     `api_key=supersecretvalue123`,
			banned: "supersecretvalue123",
			want:   "api_key=[REDACTED]",
		},
		{
			name:   "env key assignment",
			in:     "OPENAI_API_KEY=sk-proj-hello-world-99-extra",
			banned: "sk-proj-hello-world-99-extra",
		},
		{
			name:   "json apiKey field",
			in:     `{"apiKey":"super-secret-value-here","provider":"openai"}`,
			banned: "super-secret-value-here",
		},
		{
			name:   "aws",
			in:     "id AKIAIOSFODNN7EXAMPLE rest",
			banned: "AKIAIOSFODNN7EXAMPLE",
			want:   "id [REDACTED_AWS_KEY] rest",
		},
		{
			name: "plain prose",
			in:   "please review the auth package",
			want: "please review the auth package",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := secret.Redact(tt.in)
			if tt.want != "" && got != tt.want {
				t.Fatalf("Redact = %q, want %q", got, tt.want)
			}
			if tt.banned != "" && strings.Contains(got, tt.banned) {
				t.Fatalf("Redact still contains %q → %q", tt.banned, got)
			}
			if tt.banned != "" && !strings.Contains(got, "[REDACTED") {
				t.Fatalf("Redact missing placeholder → %q", got)
			}
		})
	}
}

func TestContains(t *testing.T) {
	if !secret.Contains("Bearer tok_abc1234567890") {
		t.Fatal("expected Contains true for bearer token")
	}
	if secret.Contains("ordinary prose about keys") {
		t.Fatal("expected Contains false for prose")
	}
}

func TestScrubToolOutputHighEntropy(t *testing.T) {
	// Mixed letters+digits, length >= 40.
	tok := "a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0u1v2"
	if len(tok) < 40 {
		t.Fatal("fixture too short")
	}
	in := "dump: " + tok + " done"
	got := secret.ScrubToolOutput(in)
	if strings.Contains(got, tok) {
		t.Fatalf("high-entropy token leaked: %q", got)
	}
	if !strings.Contains(got, secret.PlaceholderHighEntropy) {
		t.Fatalf("expected high-entropy placeholder, got %q", got)
	}
	// Nested tool output with known key shape.
	nested := `outer {"inner":"sk-abcdefghijklmnopqrstuvwxyz0123456789"}`
	got = secret.ScrubToolOutput(nested)
	if strings.Contains(got, "sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatalf("nested key leaked: %q", got)
	}
}

func TestScrubToolOutputPreservesShortIDs(t *testing.T) {
	in := "commit abcdef1234567890 and path src/foo.go"
	got := secret.ScrubToolOutput(in)
	if got != in {
		t.Fatalf("over-redacted: %q → %q", in, got)
	}
}

func TestRedactError(t *testing.T) {
	if got := secret.RedactError(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := secret.RedactError(errors.New("Authorization: Bearer super-secret-token")); got != "start failed" {
		t.Fatalf("bearer = %q", got)
	}
	if got := secret.RedactError(errors.New("TOKEN=abc123")); got != "start failed" {
		t.Fatalf("TOKEN= = %q", got)
	}
	if got := secret.RedactError(errors.New("connection refused")); got != "connection refused" {
		t.Fatalf("plain = %q", got)
	}
}
