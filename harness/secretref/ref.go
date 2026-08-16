// Package secretref parses secret://env/NAME refs for process injection.
// Product code should import internal/secret, which re-exports this package
// and adds session-event redaction.
package secretref

import (
	"fmt"
	"os"
	"strings"
)

// KindEnv is the only v1 secret-ref kind: resolve from the process environment
// at exec / provider-call time. No enterprise secret-manager integration.
const KindEnv = "env"

// Ref is a secret reference that must not be expanded into model-visible
// strings, session JSONL, patches, or fixtures. Resolve only when building
// an OS process environment or an outbound provider HTTP request.
//
// Wire forms (all equivalent for KindEnv):
//
//	secret://env/VAR_NAME
//	{secret:env:VAR_NAME}
//
// Config already supports {env:NAME} for provider options; secret refs are the
// explicit "never embed the resolved value in egress" form for tools and
// harness code that inject env into bash without showing values to the model.
type Ref struct {
	Kind string // KindEnv
	Name string // environment variable name
}

// String returns the canonical wire form secret://env/NAME.
func (r Ref) String() string {
	if r.Kind == "" && r.Name == "" {
		return ""
	}
	kind := r.Kind
	if kind == "" {
		kind = KindEnv
	}
	return "secret://" + kind + "/" + r.Name
}

// ParseRef parses a sole secret-ref string. ok is false when s is not a ref
// (including bare env names and {env:NAME} config placeholders).
func ParseRef(s string) (Ref, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, false
	}
	// secret://env/NAME
	const scheme = "secret://"
	if strings.HasPrefix(s, scheme) {
		rest := s[len(scheme):]
		kind, name, ok := strings.Cut(rest, "/")
		if !ok || kind == "" || name == "" {
			return Ref{}, false
		}
		if strings.Contains(name, "/") {
			return Ref{}, false
		}
		if kind != KindEnv || !isIdent(name) {
			return Ref{}, false
		}
		return Ref{Kind: kind, Name: name}, true
	}
	// {secret:env:NAME}
	if strings.HasPrefix(s, "{secret:") && strings.HasSuffix(s, "}") {
		inner := s[len("{secret:") : len(s)-1]
		kind, name, ok := strings.Cut(inner, ":")
		if !ok || kind != KindEnv || !isIdent(name) {
			return Ref{}, false
		}
		return Ref{Kind: kind, Name: name}, true
	}
	return Ref{}, false
}

// IsRef reports whether s is solely a secret ref (not a literal secret).
func IsRef(s string) bool {
	_, ok := ParseRef(s)
	return ok
}

// Resolve returns the secret value for r. Only call at process exec or
// provider construction — never log, persist, or return the result to the
// model. Missing env vars yield an error (fail closed).
func Resolve(r Ref) (string, error) {
	if r.Kind != KindEnv {
		return "", fmt.Errorf("secret: unsupported kind %q", r.Kind)
	}
	if !isIdent(r.Name) {
		return "", fmt.Errorf("secret: invalid env name %q", r.Name)
	}
	v, ok := os.LookupEnv(r.Name)
	if !ok {
		return "", fmt.Errorf("secret: env %q is not set", r.Name)
	}
	if v == "" {
		return "", fmt.Errorf("secret: env %q is empty", r.Name)
	}
	return v, nil
}

// EnvPairs resolves refs into KEY=value entries suitable for os/exec Cmd.Env
// append/replace. Map keys are the destination environment variable names
// (often the same as Ref.Name). Values are resolved; do not log the result.
func EnvPairs(refs map[string]Ref) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(refs))
	for key, ref := range refs {
		if !isIdent(key) {
			return nil, fmt.Errorf("secret: invalid env destination %q", key)
		}
		val, err := Resolve(ref)
		if err != nil {
			return nil, err
		}
		out = append(out, key+"="+val)
	}
	return out, nil
}

// MergeEnv returns a copy of base with KEY=value pairs from EnvPairs(refs)
// applied (last-wins per key). base nil means "start from os.Environ()".
// Intended for bash/process injection without model-visible values.
func MergeEnv(base []string, refs map[string]Ref) ([]string, error) {
	pairs, err := EnvPairs(refs)
	if err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		if base == nil {
			return os.Environ(), nil
		}
		out := make([]string, len(base))
		copy(out, base)
		return out, nil
	}
	if base == nil {
		base = os.Environ()
	}
	// Index existing keys for replacement.
	idx := make(map[string]int, len(base))
	out := make([]string, len(base))
	copy(out, base)
	for i, kv := range out {
		if k, _, ok := strings.Cut(kv, "="); ok {
			idx[k] = i
		}
	}
	for _, kv := range pairs {
		k, _, _ := strings.Cut(kv, "=")
		if i, ok := idx[k]; ok {
			out[i] = kv
			continue
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	return out, nil
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
