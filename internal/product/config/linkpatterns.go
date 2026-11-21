package config

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// WebUIConfig is the JSON "webui" object: cockpit display preferences kept on
// the gateway so every device paired to it shares them (#1879). Host-scoped
// like keepAwake: only the global file is read and written.
type WebUIConfig struct {
	// LinkPatterns are Text Link Patterns (#1383): regex → URL rules the
	// cockpit applies to transcript text at display time. Order matters (the
	// first matching rule wins). See ValidateLinkPatterns for the rules.
	LinkPatterns []LinkPattern `json:"linkPatterns,omitempty"`
}

// LinkPattern is one Text Link Patterns rule.
type LinkPattern struct {
	Name     string `json:"name"`
	Pattern  string `json:"pattern"`
	Template string `json:"template"`
}

// Limits shared with the cockpit's webui/link-patterns crate.
const (
	MaxLinkPatterns            = 50
	MaxLinkPatternLen          = 512 // bytes
	MaxLinkPatternTemplateLen  = 2048
	MaxLinkPatternNameLen      = 80 // characters
	LinkPatternPlaceholder     = "{match}"
	maxLinkPatternProgramInsts = 10000
)

// ErrTooManyLinkPatterns is returned when a list exceeds MaxLinkPatterns.
var ErrTooManyLinkPatterns = fmt.Errorf("at most %d link patterns", MaxLinkPatterns)

// NormalizeLinkPattern trims the name and template. The pattern is kept
// verbatim: whitespace is significant in a regex.
func NormalizeLinkPattern(p LinkPattern) LinkPattern {
	return LinkPattern{
		Name:     strings.TrimSpace(p.Name),
		Pattern:  p.Pattern,
		Template: strings.TrimSpace(p.Template),
	}
}

// ValidateLinkPattern checks one (normalized) rule with the same rules as the
// cockpit: non-empty name of at most MaxLinkPatternNameLen characters; a
// pattern of at most MaxLinkPatternLen bytes that compiles (RE2: linear-time
// matching, no backtracking), stays under a compiled-program size cap, and
// does not match empty text; and an absolute http/https template containing
// {match} whose host is fixed.
func ValidateLinkPattern(p LinkPattern) error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	if utf8.RuneCountInString(p.Name) > MaxLinkPatternNameLen {
		return fmt.Errorf("name is longer than %d characters", MaxLinkPatternNameLen)
	}
	if err := validateLinkRegex(p.Pattern); err != nil {
		return err
	}
	return validateLinkTemplate(p.Template)
}

func validateLinkRegex(pattern string) error {
	if pattern == "" {
		return errors.New("pattern is required")
	}
	if len(pattern) > MaxLinkPatternLen {
		return fmt.Errorf("pattern is longer than %d bytes", MaxLinkPatternLen)
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("invalid regular expression: %w", err)
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return fmt.Errorf("invalid regular expression: %w", err)
	}
	if len(prog.Inst) > maxLinkPatternProgramInsts {
		return errors.New("regular expression is too complex")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid regular expression: %w", err)
	}
	if re.MatchString("") {
		return errors.New("pattern matches empty text; make it match at least one character")
	}
	return nil
}

func validateLinkTemplate(template string) error {
	if template == "" {
		return errors.New("URL template is required")
	}
	if len(template) > MaxLinkPatternTemplateLen {
		return fmt.Errorf("URL template is longer than %d bytes", MaxLinkPatternTemplateLen)
	}
	if !utf8.ValidString(template) {
		return errors.New("URL template is not valid UTF-8")
	}
	for _, r := range template {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("\"<>`", r) {
			return errors.New("URL template has spaces, control characters or one of \" < > `")
		}
	}
	lower := strings.ToLower(template)
	var rest string
	switch {
	case strings.HasPrefix(lower, "https://"):
		rest = template[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		rest = template[len("http://"):]
	default:
		return errors.New("URL template must start with https:// or http://")
	}
	if !strings.Contains(template, LinkPatternPlaceholder) {
		return errors.New("URL template must contain {match}")
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	if strings.Contains(authority, LinkPatternPlaceholder) || strings.Contains(authority, "@") {
		return errors.New("put {match} in the path or query; the host must be fixed and may not contain @")
	}
	if authority == "" || strings.ContainsAny(authority, "{}") {
		return errors.New("URL template needs a host after the scheme")
	}
	return nil
}

// ValidateLinkPatterns normalizes and validates a whole list. It returns the
// normalized list, or an error naming the first bad rule (1-based).
func ValidateLinkPatterns(rules []LinkPattern) ([]LinkPattern, error) {
	if len(rules) > MaxLinkPatterns {
		return nil, ErrTooManyLinkPatterns
	}
	out := make([]LinkPattern, 0, len(rules))
	for i, r := range rules {
		n := NormalizeLinkPattern(r)
		if err := ValidateLinkPattern(n); err != nil {
			return nil, fmt.Errorf("link pattern %d: %w", i+1, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// SetGlobalLinkPatterns validates rules and replaces webui.linkPatterns in
// ~/.strike/config. An invalid list is refused and nothing is written. An
// empty list clears the key.
func SetGlobalLinkPatterns(rules []LinkPattern) error {
	normalized, err := ValidateLinkPatterns(rules)
	if err != nil {
		return err
	}
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	if len(normalized) == 0 {
		normalized = nil
	}
	cfg.WebUI.LinkPatterns = normalized
	return writeGlobal(cfg, unlock)
}
