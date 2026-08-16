package config

import (
	"os"
	"regexp"
	"strings"
	"unicode"
)

// {env:NAME} — OpenCode-style process env reference.
var envBraceRE = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// ${NAME} shell-style brace form.
var envDollarBraceRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnv replaces {env:NAME}, ${NAME}, and $NAME with os.Getenv values.
// Unset variables expand to empty strings. Values are taken from the process
// environment (e.g. bashrc-exported vars available to the strike process).
func ExpandEnv(s string) string {
	if s == "" || !strings.ContainsAny(s, "{$") {
		return s
	}
	out := envBraceRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := envBraceRE.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		return os.Getenv(sub[1])
	})
	out = envDollarBraceRE.ReplaceAllStringFunc(out, func(m string) string {
		sub := envDollarBraceRE.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		return os.Getenv(sub[1])
	})
	return expandDollarVars(out)
}

// expandDollarVars replaces $NAME outside of incomplete forms.
func expandDollarVars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '{' {
			// Unmatched ${… left by earlier pass — keep literal.
			b.WriteByte('$')
			continue
		}
		j := i + 1
		if j < len(s) && (s[j] == '_' || unicode.IsLetter(rune(s[j]))) {
			j++
			for j < len(s) && (s[j] == '_' || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
				j++
			}
			b.WriteString(os.Getenv(s[i+1 : j]))
			i = j - 1
			continue
		}
		b.WriteByte('$')
	}
	return b.String()
}

// ContainsEnvRef reports whether s includes an env placeholder syntax.
func ContainsEnvRef(s string) bool {
	if s == "" {
		return false
	}
	if envBraceRE.MatchString(s) || envDollarBraceRE.MatchString(s) {
		return true
	}
	// bare $NAME
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			continue
		}
		c := s[i+1]
		if c == '_' || unicode.IsLetter(rune(c)) {
			return true
		}
	}
	return false
}

// EnvRefName returns the environment variable name when s is solely a
// reference ({env:NAME}, $NAME, or ${NAME}). Otherwise ok is false.
func EnvRefName(s string) (name string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if m := envBraceRE.FindStringSubmatch(s); len(m) == 2 && m[0] == s {
		return m[1], true
	}
	if m := envDollarBraceRE.FindStringSubmatch(s); len(m) == 2 && m[0] == s {
		return m[1], true
	}
	if strings.HasPrefix(s, "$") && !strings.HasPrefix(s, "${") {
		name = s[1:]
		if isEnvIdent(name) {
			return name, true
		}
	}
	return "", false
}

func isEnvIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// NormalizeAPIKeyEnv turns a plain env name or a sole env ref into the bare
// variable name used by auth.APIKeyEnv. Empty input stays empty.
func NormalizeAPIKeyEnv(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if name, ok := EnvRefName(raw); ok {
		return name
	}
	return raw
}
