package scheduler

import (
	"fmt"
	"regexp"
	"strings"
)

// Class is how a shell command is categorized for pool admission.
//
// Every bash invocation still consumes the process pool (see later SCHED
// wiring); build/test classes acquire those pools in addition. general means
// process only.
type Class string

const (
	ClassGeneral Class = "general"
	ClassBuild   Class = "build"
	ClassTest    Class = "test"
)

// ValidClass reports whether c is a known command class.
func ValidClass(c Class) bool {
	switch c {
	case ClassGeneral, ClassBuild, ClassTest:
		return true
	}
	return false
}

// CommandRule is one ordered classification rule from config.
//
// Pattern is a full-string glob over the submitted command (`*` any run of
// runes, `?` one rune). Matching is case-sensitive. Class is build, test, or
// general. Source is provenance (config path or layer label), not JSON.
type CommandRule struct {
	Pattern string `json:"pattern"`
	Class   Class  `json:"class"`
	// Source identifies where the rule came from (e.g. config file path).
	// Set by config load; omitted from JSON.
	Source string `json:"-"`
}

// CompiledRule is a validated rule with a ready matcher and provenance.
type CompiledRule struct {
	Pattern string
	Class   Class
	Source  string
	re      *regexp.Regexp
}

// Match reports whether command matches this rule's pattern.
func (r CompiledRule) Match(command string) bool {
	if r.re == nil {
		return false
	}
	return r.re.MatchString(command)
}

// CloneCommandRules returns a deep-enough copy of rules (nil stays nil).
func CloneCommandRules(rules []CommandRule) []CommandRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]CommandRule, len(rules))
	copy(out, rules)
	return out
}

// ValidateCommandRule checks pattern/class without compiling a matcher.
// Prefer CompileRules for startup paths so malformed globs fail the same way.
func ValidateCommandRule(r CommandRule, index int, source string) error {
	_, err := compileCommandRule(r, index, source)
	return err
}

func compileCommandRule(r CommandRule, index int, source string) (CompiledRule, error) {
	src := strings.TrimSpace(r.Source)
	if src == "" {
		src = strings.TrimSpace(source)
	}
	pat := r.Pattern
	if strings.TrimSpace(pat) == "" {
		return CompiledRule{}, ruleErr(src, index, "empty pattern")
	}
	class := Class(strings.TrimSpace(string(r.Class)))
	if !ValidClass(class) {
		return CompiledRule{}, ruleErr(src, index, fmt.Sprintf("invalid class %q (want general|build|test)", r.Class))
	}
	re, err := compileCommandGlob(pat)
	if err != nil {
		return CompiledRule{}, ruleErr(src, index, fmt.Sprintf("invalid pattern %q: %v", pat, err))
	}
	return CompiledRule{
		Pattern: pat,
		Class:   class,
		Source:  src,
		re:      re,
	}, nil
}

func ruleErr(source string, index int, msg string) error {
	loc := fmt.Sprintf("commands[%d]", index)
	if source == "" {
		return fmt.Errorf("scheduler: %s: %s", loc, msg)
	}
	return fmt.Errorf("%s: scheduler: %s: %s", source, loc, msg)
}

// CompileRules validates and compiles ordered command rules.
// index in errors is the position within rules (0-based).
func CompileRules(rules []CommandRule) ([]CompiledRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	out := make([]CompiledRule, 0, len(rules))
	for i, r := range rules {
		cr, err := compileCommandRule(r, i, r.Source)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, nil
}

// Classify returns the command class using last-match-wins over rules.
// When no rule matches (or rules is empty), the result is ClassGeneral.
//
// Matching is deterministic: rules are tried in order; each match updates the
// winner; the last matching rule's class is returned. Multiple matches are
// therefore resolved by the latest matching rule in the effective list
// (project rules append after global, so project can override).
func Classify(command string, rules []CompiledRule) Class {
	class, _ := ClassifyDetail(command, rules)
	return class
}

// ClassifyDetail is Classify plus the winning rule (nil when default general).
func ClassifyDetail(command string, rules []CompiledRule) (Class, *CompiledRule) {
	var (
		class Class = ClassGeneral
		win   *CompiledRule
	)
	for i := range rules {
		r := &rules[i]
		if r.Match(command) {
			class = r.Class
			win = r
		}
	}
	return class, win
}

// PoolsForClass returns the named pools a bash-style admission should acquire
// for class, always including process. general → [process]; build →
// [build, process]; test → [process, test] (sorted for Acquire).
func PoolsForClass(class Class) []string {
	switch class {
	case ClassBuild:
		return []string{PoolBuild, PoolProcess}
	case ClassTest:
		return []string{PoolProcess, PoolTest}
	default:
		return []string{PoolProcess}
	}
}

// PoolsForCommand classifies command then returns PoolsForClass.
func PoolsForCommand(command string, rules []CompiledRule) []string {
	return PoolsForClass(Classify(command, rules))
}

// compileCommandGlob turns a shell-style glob into an anchored regexp.
// Supported metacharacters: * (any run of runes), ? (one rune). Other runes
// match literally. The full command string must match.
func compileCommandGlob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.Grow(len(pattern)*2 + 4)
	b.WriteByte('^')
	for i := 0; i < len(pattern); {
		r := pattern[i]
		switch r {
		case '*':
			b.WriteString(".*")
			i++
		case '?':
			b.WriteByte('.')
			i++
		case '\\':
			// Allow escaping the next byte as literal (including * ? \).
			if i+1 >= len(pattern) {
				return nil, fmt.Errorf("trailing backslash")
			}
			b.WriteString(regexp.QuoteMeta(string(pattern[i+1])))
			i += 2
		default:
			// QuoteMeta one byte at a time keeps UTF-8 sequences intact as
			// literal bytes (command strings are matched as Go strings).
			b.WriteString(regexp.QuoteMeta(string(r)))
			i++
		}
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
}
