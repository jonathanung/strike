// Package redact scrubs credential-shaped substrings from free text before
// persist, export, inspect, or display. It is the shared helper for session
// traces, timeline export (#790), and UI dumps. Detection is best-effort
// pattern matching (prefer false negatives over mangling ordinary prose).
//
// Secret refs and protocol event walking live in internal/trust/secret (#796), which
// calls String/ScrubToolOutput here. Extend patterns in this package rather
// than forking per caller. This package does not resolve secret refs or touch
// the credential store — only string scrubbing.
package redact

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Placeholder is the generic marker substituted for scrubbed secret material.
const Placeholder = "[REDACTED]"

// PlaceholderHighEntropy marks long mixed-alphabet tokens scrubbed from tool
// output (env dumps, nested echoes) that did not match a named pattern.
const PlaceholderHighEntropy = "[REDACTED_HIGH_ENTROPY]"

// Finding is one credential-shaped match. Used by write-time content guards
// (#890) and tests; redaction still uses String/ScrubToolOutput.
type Finding struct {
	// RuleID is a stable machine id (e.g. pem_private_key, aws_access_key_id).
	RuleID string
	// Kind is always "credential" for findings from this package.
	Kind string
}

// KindCredential is Finding.Kind for secret-shaped spans.
const KindCredential = "credential"

// Stable rule ids for credential patterns (write guards + docs).
const (
	RulePEMPrivateKey  = "pem_private_key"
	RuleAnthropicKey   = "anthropic_api_key"
	RuleXAIKey         = "xai_api_key"
	RuleOpenAIKey      = "openai_api_key"
	RuleGitHubToken    = "github_token"
	RuleSlackToken     = "slack_token"
	RuleAWSAccessKeyID = "aws_access_key_id"
	RuleBearerToken    = "bearer_token"
	RuleLabeledSecret  = "labeled_secret"
	RuleProviderEnvKey = "provider_env_key"
	RuleGenericToken   = "generic_token"
	RuleJSONCredential = "json_credential_field"
)

// pattern is one replace rule. When keepPrefix is true, submatch group 1 is
// preserved and group 2 (the secret) is replaced with repl (or Placeholder).
// When the match has three groups (e.g. JSON "key":"value"), group 3 is kept
// after the placeholder.
type pattern struct {
	id         string
	re         *regexp.Regexp
	repl       string
	keepPrefix bool
}

// patterns cover auth env assignment shapes used by internal/auth plus common
// API token forms. Order matters for overlapping prefixes (more specific first).
var patterns = []pattern{
	// PEM private keys (multiline).
	{id: RulePEMPrivateKey, re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), repl: "[REDACTED_PRIVATE_KEY]"},
	// Anthropic keys (sk-ant-…).
	{id: RuleAnthropicKey, re: regexp.MustCompile(`(?i)\bsk-ant-[a-z0-9_-]{8,}\b`), repl: "[REDACTED_ANTHROPIC_KEY]"},
	// xAI keys.
	{id: RuleXAIKey, re: regexp.MustCompile(`(?i)\bxai-[a-z0-9_-]{8,}\b`), repl: "[REDACTED_XAI_KEY]"},
	// OpenAI-ish sk-… (after sk-ant so ant keys are not double-matched).
	{id: RuleOpenAIKey, re: regexp.MustCompile(`(?i)\bsk-[a-z0-9_-]{8,}\b`), repl: "[REDACTED_API_KEY]"},
	// GitHub tokens.
	{id: RuleGitHubToken, re: regexp.MustCompile(`(?i)\b(?:ghp|gho|ghu|ghs|ghr)_[a-z0-9_]{20,}\b`), repl: "[REDACTED_GITHUB_TOKEN]"},
	{id: RuleGitHubToken, re: regexp.MustCompile(`(?i)\bgithub_pat_[a-z0-9_]{20,}\b`), repl: "[REDACTED_GITHUB_TOKEN]"},
	// Slack tokens.
	{id: RuleSlackToken, re: regexp.MustCompile(`(?i)\bxox[baprs]-[a-z0-9-]{10,}\b`), repl: "[REDACTED_SLACK_TOKEN]"},
	// AWS access key ids.
	{id: RuleAWSAccessKeyID, re: regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`), repl: "[REDACTED_AWS_KEY]"},
	// Bearer tokens (keep scheme).
	{id: RuleBearerToken, re: regexp.MustCompile(`(?i)\b(Bearer\s+)([a-z0-9._\-+/=]{12,})`), repl: Placeholder, keepPrefix: true},
	// Labeled assignments: api_key=, password:, etc.
	{id: RuleLabeledSecret, re: regexp.MustCompile(`(?i)\b((?:api[_-]?key|api[_-]?secret|access[_-]?token|refresh[_-]?token|secret[_-]?key|client[_-]?secret|password|passwd)\s*[=:]\s*["']?)(\S+)`), repl: Placeholder, keepPrefix: true},
	// Provider env keys used by internal/auth (and common aliases).
	{id: RuleProviderEnvKey, re: regexp.MustCompile(`(?i)\b((?:ANTHROPIC|OPENAI|XAI|OPENROUTER|GEMINI|GOOGLE|KIMI|DEEPSEEK|GITHUB)_(?:API_)?KEY\s*[=:]\s*)(\S+)`), repl: Placeholder, keepPrefix: true},
	// Generic TOKEN= / SECRET= env-style (high entropy values only via length).
	{id: RuleGenericToken, re: regexp.MustCompile(`(?i)\b((?:AUTH_)?TOKEN\s*[=:]\s*)([^\s"'\\]{12,})`), repl: Placeholder, keepPrefix: true},
	// Auth-store JSON credential fields.
	{id: RuleJSONCredential, re: regexp.MustCompile(`(?i)("(?:apiKey|access|refresh|idToken|clientSecret|token)"\s*:\s*")([^"]{6,})(")`), repl: Placeholder, keepPrefix: true},
}

var highEntropyRE = regexp.MustCompile(`\b[A-Za-z0-9_-]{40,}\b`)

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
				repl := p.repl
				if repl == "" {
					repl = Placeholder
				}
				if len(sub) >= 4 {
					return sub[1] + repl + sub[3]
				}
				if len(sub) >= 3 {
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

// ScrubToolOutput runs String then replaces long high-entropy tokens often
// leaked from env dumps or nested tool echoes. Pure hex (git SHAs) is kept.
func ScrubToolOutput(s string) string {
	if s == "" {
		return s
	}
	out := String(s)
	return highEntropyRE.ReplaceAllStringFunc(out, func(tok string) string {
		if strings.HasPrefix(tok, "[REDACTED") {
			return tok
		}
		if !looksHighEntropy(tok) {
			return tok
		}
		return PlaceholderHighEntropy
	})
}

func looksHighEntropy(tok string) bool {
	if len(tok) < 40 {
		return false
	}
	var letters, digits, upper, lower int
	hexOnly := true
	for _, r := range tok {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'f':
			letters++
			lower++
		case r >= 'A' && r <= 'F':
			letters++
			upper++
		case r >= 'g' && r <= 'z':
			letters++
			lower++
			hexOnly = false
		case r >= 'G' && r <= 'Z':
			letters++
			upper++
			hexOnly = false
		case r == '_' || r == '-':
			hexOnly = false
		default:
			hexOnly = false
		}
	}
	if hexOnly {
		return false
	}
	if letters == 0 || digits == 0 {
		if digits == 0 && upper > 0 && lower > 0 && letters >= 40 {
			return true
		}
		return false
	}
	return true
}

// Error returns a short log-safe error string. Messages that appear to carry
// headers, tokens, or KEY=/TOKEN= assignments collapse to a generic failure.
func Error(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	upper := strings.ToUpper(msg)
	lower := strings.ToLower(msg)
	if strings.Contains(upper, "KEY=") || strings.Contains(upper, "TOKEN=") ||
		strings.Contains(lower, "bearer ") || strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "api-key") || strings.Contains(lower, "api_key") ||
		ContainsSecret(msg) {
		return "start failed"
	}
	return String(msg)
}

// JSON walks a JSON value and redacts string leaves plus known credential
// object fields. Invalid JSON is redacted as a text blob.
func JSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		b, err := json.Marshal(String(string(raw)))
		if err != nil {
			return json.RawMessage(`"[REDACTED]"`)
		}
		return b
	}
	v = redactJSONValue(v)
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`"[REDACTED]"`)
	}
	return b
}

func redactJSONValue(v any) any {
	switch t := v.(type) {
	case string:
		return String(t)
	case map[string]any:
		for k, child := range t {
			if isCredentialField(k) {
				if s, ok := child.(string); ok && s != "" {
					t[k] = Placeholder
					continue
				}
			}
			t[k] = redactJSONValue(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = redactJSONValue(child)
		}
		return t
	default:
		return v
	}
}

func isCredentialField(k string) bool {
	switch k {
	case "apiKey", "access", "refresh", "idToken", "clientSecret", "token",
		"password", "secret", "authorization", "Authorization":
		return true
	default:
		return false
	}
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

// Findings returns credential-shaped matches in s (deduped by RuleID, pattern
// order). Empty input or no matches returns nil. Does not include high-entropy
// ScrubToolOutput heuristics — those are egress-only and too noisy for write
// guards. Callers that need a boolean can use ContainsSecret or len(Findings)>0.
func Findings(s string) []Finding {
	if s == "" {
		return nil
	}
	seen := make(map[string]struct{}, 8)
	var out []Finding
	for _, p := range patterns {
		if p.id == "" || !p.re.MatchString(s) {
			continue
		}
		if _, ok := seen[p.id]; ok {
			continue
		}
		seen[p.id] = struct{}{}
		out = append(out, Finding{RuleID: p.id, Kind: KindCredential})
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
