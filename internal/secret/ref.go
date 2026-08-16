package secret

import "github.com/jonathanung/strike-cli/harness/secretref"

// KindEnv is the only v1 secret-ref kind: resolve from the process environment
// at exec / provider-call time. Implementation lives in harness/secretref so
// kernel tools can resolve refs without importing product packages.
const KindEnv = secretref.KindEnv

// Ref is a secret reference that must not be expanded into model-visible
// strings, session JSONL, patches, or fixtures.
type Ref = secretref.Ref

func ParseRef(s string) (Ref, bool) { return secretref.ParseRef(s) }
func IsRef(s string) bool           { return secretref.IsRef(s) }
func Resolve(r Ref) (string, error) { return secretref.Resolve(r) }
func EnvPairs(refs map[string]Ref) ([]string, error) {
	return secretref.EnvPairs(refs)
}
func MergeEnv(base []string, refs map[string]Ref) ([]string, error) {
	return secretref.MergeEnv(base, refs)
}
