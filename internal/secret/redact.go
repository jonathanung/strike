// Package secret detects and redacts credentials and provides secret-ref
// indirection for exec-time resolution.
//
// Redact is the shared helper for session JSONL persist, timeline/trace export
// (#790), TUI /export, diagnostic bundles, and engine tool-result scrubbing.
// Resolve secret refs only at process exec or provider call time — never embed
// resolved values in model-facing tool output, apply_patch hunks, or fixtures.
package secret

import (
	"regexp"
	"strings"
	"unicode"
)

// Placeholder is the generic stand-in for a redacted secret span.
const Placeholder = "[REDACTED]"

// Named placeholders preserve a hint about the secret kind when useful for
// debugging without revealing material.
const (
	PlaceholderAPIKey      = "[REDACTED_API_KEY]"
	PlaceholderAnthropic   = "[REDACTED_ANTHROPIC_KEY]"
	PlaceholderXAI         = "[REDACTED_XAI_KEY]"
	PlaceholderGitHub      = "[REDACTED_GITHUB_TOKEN]"
	PlaceholderAWS         = "[REDACTED_AWS_KEY]"
	PlaceholderPrivateKey  = "[REDACTED_PRIVATE_KEY]"
	PlaceholderHighEntropy = "[REDACTED_HIGH_ENTROPY]"
)

// pattern is one redaction rule. When groups >= 2, group 1 (prefix/label) is
// kept and the remainder is replaced; otherwise the whole match is replaced.
type pattern struct {
	re   *regexp.Regexp
	repl string // used when keepPrefix is false, or as suffix after prefix
	// keepPrefix: if the regex has a capturing group 1 for a label/Bearer
	// prefix, preserve it and append Placeholder (or repl when set).
	keepPrefix bool
}

// patterns prefer false negatives over mangling ordinary prose. Longer /
// more-specific token shapes are listed first.
var patterns = []pattern{
	{
		re:   regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		repl: PlaceholderPrivateKey,
	},
	{
		re:   regexp.MustCompile(`(?i)\bsk-ant-api\d{2}-[A-Za-z0-9_-]{16,}`),
		repl: PlaceholderAnthropic,
	},
	{
		re:   regexp.MustCompile(`(?i)\bsk-ant-[A-Za-z0-9_-]{8,}`),
		repl: PlaceholderAnthropic,
	},
	{
		re:   regexp.MustCompile(`(?i)\bxai-[A-Za-z0-9_-]{16,}`),
		repl: PlaceholderXAI,
	},
	{
		re:   regexp.MustCompile(`(?i)\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{20,}`),
		repl: PlaceholderGitHub,
	},
	{
		re:   regexp.MustCompile(`(?i)\bgithub_pat_[A-Za-z0-9_]{20,}`),
		repl: PlaceholderGitHub,
	},
	{
		re:   regexp.MustCompile(`(?i)\bxox[baprs]-[A-Za-z0-9-]{10,}`),
		repl: PlaceholderAPIKey,
	},
	{
		re:   regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`),
		repl: PlaceholderAWS,
	},
	// OpenAI-ish sk-… after more specific sk-ant- / sk-proj handled above via
	// the generic sk- rule (sk-proj- and sk-live- included).
	{
		re:   regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_-]{16,}`),
		repl: PlaceholderAPIKey,
	},
	{
		re:         regexp.MustCompile(`(?i)\b(Bearer\s+)([A-Za-z0-9._\-+/=]{12,})`),
		keepPrefix: true,
	},
	{
		re:         regexp.MustCompile(`(?i)\b((?:api[_-]?key|api[_-]?secret|access[_-]?token|refresh[_-]?token|secret[_-]?key|client[_-]?secret|password|passwd)\s*[=:]\s*["']?)([^\s"'\\]{6,})`),
		keepPrefix: true,
	},
	{
		re:         regexp.MustCompile(`(?i)\b((?:ANTHROPIC|OPENAI|XAI|OPENROUTER|GEMINI|GOOGLE|KIMI|DEEPSEEK|GITHUB)_(?:API_)?KEY\s*[=:]\s*["']?)(\S+)`),
		keepPrefix: true,
	},
	// Auth store / JSON credential field shapes (apiKey, access, refresh, …).
	{
		re:         regexp.MustCompile(`(?i)("(?:apiKey|access|refresh|idToken|clientSecret|token)"\s*:\s*")([^"]{6,})(")`),
		keepPrefix: true, // special-cased in Redact via 3 groups
	},
}

// highEntropyRE matches long URL-safe / base64-ish tokens that often carry
// credentials when not already covered by a named pattern.
var highEntropyRE = regexp.MustCompile(`\b[A-Za-z0-9_-]{40,}\b`)

// Redact replaces credential-shaped substrings with placeholders.
// Safe on empty input. Prefer this over ad-hoc regex at every egress path.
func Redact(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, p := range patterns {
		if p.keepPrefix {
			out = p.re.ReplaceAllStringFunc(out, func(match string) string {
				sub := p.re.FindStringSubmatch(match)
				if len(sub) >= 4 {
					// JSON field: "apiKey":"…value…" → keep quotes/key, redact value.
					return sub[1] + Placeholder + sub[3]
				}
				if len(sub) >= 3 {
					return sub[1] + Placeholder
				}
				return Placeholder
			})
			continue
		}
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}

// Contains reports whether s matches a known secret pattern (before redaction).
func Contains(s string) bool {
	if s == "" {
		return false
	}
	for _, p := range patterns {
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}

// ScrubToolOutput redacts known secret patterns and replaces high-entropy
// token-like spans that often leak from env dumps or nested tool output.
// Intended for model-facing tool results and streaming tool tails.
func ScrubToolOutput(s string) string {
	if s == "" {
		return s
	}
	out := Redact(s)
	out = highEntropyRE.ReplaceAllStringFunc(out, func(tok string) string {
		// Skip placeholders and ordinary identifiers with low entropy.
		if strings.HasPrefix(tok, "[REDACTED") {
			return tok
		}
		if !looksHighEntropy(tok) {
			return tok
		}
		return PlaceholderHighEntropy
	})
	return out
}

// looksHighEntropy is a cheap digit/letter mix check — not cryptographic.
// Requires both letters and digits (or mixed case + symbols already in class)
// so prose words and pure hex short ids are less likely to trip.
func looksHighEntropy(tok string) bool {
	if len(tok) < 40 {
		return false
	}
	var letters, digits, upper, lower int
	for _, r := range tok {
		switch {
		case unicode.IsLetter(r):
			letters++
			if unicode.IsUpper(r) {
				upper++
			} else {
				lower++
			}
		case unicode.IsDigit(r):
			digits++
		}
	}
	if letters == 0 || digits == 0 {
		// Pure alpha or pure numeric long runs are usually not API keys.
		// Exception: base64 often has both; JWT-like segments may be pure.
		// Require mixed case for pure-alpha long tokens.
		if digits == 0 && upper > 0 && lower > 0 && letters >= 40 {
			return true
		}
		return false
	}
	return true
}

// RedactError returns a short, log-safe error string. Messages that appear to
// carry headers, tokens, or KEY=/TOKEN= assignments collapse to a generic
// failure so env values never reach status UIs.
func RedactError(err error) string {
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
		Contains(msg) {
		return "start failed"
	}
	return Redact(msg)
}
