// Package redact scrubs credential-shaped substrings from free text before
// persist, export, inspect, or display. It is the shared helper for session
// traces, timeline export, and UI dumps. Detection is best-effort pattern
// matching (prefer false negatives over mangling ordinary prose).
//
// Coordinate with secret-handling work (#796): extend patterns here rather
// than forking per package. This package does not resolve secret refs or
// touch the credential store — only string scrubbing.
package redact

import (
	"regexp"
	"strings"
)

// Placeholder is the generic marker substituted for scrubbed secret material.
const Placeholder = "[REDACTED]"

// pattern is one replace rule. When keepPrefix is true, submatch group 1 is
// preserved and group 2 (the secret) is replaced with repl (or Placeholder).
type pattern struct {
	re         *regexp.Regexp
	repl       string
	keepPrefix bool
}

// patterns cover auth env assignment shapes used by internal/auth plus common
// API token forms. Order matters for overlapping prefixes (more specific first).
var patterns = []pattern{
	// PEM private keys (multiline).
	{re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), repl: "[REDACTED_PRIVATE_KEY]"},
	// Anthropic keys (sk-ant-…).
	{re: regexp.MustCompile(`(?i)\bsk-ant-[a-z0-9_-]{8,}\b`), repl: "[REDACTED_ANTHROPIC_KEY]"},
	// xAI keys.
	{re: regexp.MustCompile(`(?i)\bxai-[a-z0-9_-]{8,}\b`), repl: "[REDACTED_XAI_KEY]"},
	// OpenAI-ish sk-… (after sk-ant so ant keys are not double-matched).
	{re: regexp.MustCompile(`(?i)\bsk-[a-z0-9_-]{8,}\b`), repl: "[REDACTED_API_KEY]"},
	// GitHub tokens.
	{re: regexp.MustCompile(`(?i)\b(?:ghp|gho|ghu|ghs|ghr)_[a-z0-9_]{20,}\b`), repl: "[REDACTED_GITHUB_TOKEN]"},
	{re: regexp.MustCompile(`(?i)\bgithub_pat_[a-z0-9_]{20,}\b`), repl: "[REDACTED_GITHUB_TOKEN]"},
	// Slack tokens.
	{re: regexp.MustCompile(`(?i)\bxox[baprs]-[a-z0-9-]{10,}\b`), repl: "[REDACTED_SLACK_TOKEN]"},
	// AWS access key ids.
	{re: regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`), repl: "[REDACTED_AWS_KEY]"},
	// Bearer tokens (keep scheme).
	{re: regexp.MustCompile(`(?i)\b(Bearer\s+)([a-z0-9._\-+/=]{12,})`), repl: Placeholder, keepPrefix: true},
	// Labeled assignments: api_key=, password:, etc.
	{re: regexp.MustCompile(`(?i)\b((?:api[_-]?key|api[_-]?secret|access[_-]?token|refresh[_-]?token|secret[_-]?key|client[_-]?secret|password|passwd)\s*[=:]\s*["']?)(\S+)`), repl: Placeholder, keepPrefix: true},
	// Provider env keys used by internal/auth (and common aliases).
	{re: regexp.MustCompile(`(?i)\b((?:ANTHROPIC|OPENAI|XAI|OPENROUTER|GEMINI|GOOGLE|KIMI|DEEPSEEK)_(?:API_)?KEY\s*[=:]\s*)(\S+)`), repl: Placeholder, keepPrefix: true},
	// Generic TOKEN= / SECRET= env-style (high entropy values only via length).
	{re: regexp.MustCompile(`(?i)\b((?:AUTH_)?TOKEN\s*[=:]\s*)([^\s"'\\]{12,})`), repl: Placeholder, keepPrefix: true},
}

// String replaces credential-shaped substrings with placeholders.
// Empty input is returned unchanged.
func String(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, p := range patterns {
		if p.keepPrefix {
			out = p.re.ReplaceAllStringFunc(out, func(match string) string {
				sub := p.re.FindStringSubmatch(match)
				if len(sub) >= 3 {
					repl := p.repl
					if repl == "" {
						repl = Placeholder
					}
					return sub[1] + repl
				}
				return Placeholder
			})
			continue
		}
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}

// ContainsSecret reports whether s still appears to hold a credential-shaped
// span after a no-op check against the same patterns (pre-redaction).
// Useful in tests asserting scrub coverage.
func ContainsSecret(s string) bool {
	if s == "" {
		return false
	}
	// If redaction changes the string, a secret-shaped span was present.
	return String(s) != s
}

// Bytes is String for byte slices; nil/empty input returns the input unchanged.
func Bytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return []byte(String(string(b)))
}

// JoinRedacted joins parts with sep after redacting each non-empty part.
// Convenience for multi-field previews.
func JoinRedacted(sep string, parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, String(p))
	}
	return strings.Join(out, sep)
}
