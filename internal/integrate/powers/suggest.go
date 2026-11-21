package powers

import (
	"strings"
	"unicode"
)

// Suggest returns the names of packs whose keywords appear in prompt as
// whole words (case-insensitive; multi-word keywords match as a phrase).
// Packs already active or muted are skipped. Suggestion never activates:
// callers offer a one-tap "Activate <name>?" chip.
func Suggest(prompt string, packs []Power, active, muted map[string]bool) []string {
	words := tokenize(prompt)
	if len(words) == 0 {
		return nil
	}
	var out []string
	for _, p := range packs {
		if active[p.Name] || muted[p.Name] {
			continue
		}
		for _, k := range p.Keywords {
			if containsPhrase(words, tokenize(k)) {
				out = append(out, p.Name)
				break
			}
		}
	}
	return out
}

// tokenize lowercases s and splits it on anything that is not a letter,
// digit, '-', '_' or '.' inside a word (so "k8s", "next.js", "pre-commit"
// stay whole). Trailing dots are trimmed so a sentence end still matches.
func tokenize(s string) []string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.')
	})
	out := f[:0]
	for _, w := range f {
		if w = strings.Trim(w, ".-_"); w != "" {
			out = append(out, w)
		}
	}
	return out
}

func containsPhrase(words, phrase []string) bool {
	if len(phrase) == 0 || len(phrase) > len(words) {
		return false
	}
outer:
	for i := 0; i+len(phrase) <= len(words); i++ {
		for j, w := range phrase {
			if words[i+j] != w {
				continue outer
			}
		}
		return true
	}
	return false
}
