package tool

import (
	"fmt"
	"os"
	"strings"

	"github.com/jonathanung/strike-cli/internal/secret"
)

// bashMinimalEnvKeys is the documented default environment inherited by bash
// tool invocations (#1030). Host credentials and unrelated process env are
// not passed through; supply secrets via Context.BashSecrets (secret refs).
var bashMinimalEnvKeys = []string{
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"SHELL",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"LC_MESSAGES",
	"TERM",
	"TERMINFO",
	"TMPDIR",
	"TMP",
	"TEMP",
	"XDG_RUNTIME_DIR",
	"XDG_CACHE_HOME",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	// Common toolchain roots (values only; no secret-bearing vars).
	"GOPATH",
	"GOROOT",
	"GOCACHE",
	"GOMODCACHE",
	"GOTOOLCHAIN",
	"CARGO_HOME",
	"RUSTUP_HOME",
	"JAVA_HOME",
	"NODE_PATH",
	"NPM_CONFIG_CACHE",
	"PWD",
	// SSH agent socket path only (not keys); needed for git+ssh offline auth.
	"SSH_AUTH_SOCK",
	"SSH_AGENT_PID",
}

// bashEnv builds the process environment for a bash tool invocation.
// Starts from the minimal key set, then merges resolved secret refs.
// Never logs or returns secret values to callers beyond the env slice
// intended for os/exec (do not put env into events/metadata).
func bashEnv(tc *Context) ([]string, error) {
	base := minimalEnvFromHost(bashMinimalEnvKeys)
	if tc == nil || len(tc.BashSecrets) == 0 {
		return base, nil
	}
	refs := make(map[string]secret.Ref, len(tc.BashSecrets))
	for dest, raw := range tc.BashSecrets {
		dest = strings.TrimSpace(dest)
		raw = strings.TrimSpace(raw)
		if dest == "" || raw == "" {
			continue
		}
		ref, ok := secret.ParseRef(raw)
		if !ok {
			return nil, fmt.Errorf("bash secret %q: not a secret ref (want secret://env/NAME)", dest)
		}
		refs[dest] = ref
	}
	if len(refs) == 0 {
		return base, nil
	}
	return secret.MergeEnv(base, refs)
}

// minimalEnvFromHost copies selected keys from the host environment.
func minimalEnvFromHost(keys []string) []string {
	out := make([]string, 0, len(keys)+2)
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// BashMinimalEnvKeys returns a copy of the documented minimal env key list
// (for docs/tests).
func BashMinimalEnvKeys() []string {
	out := make([]string, len(bashMinimalEnvKeys))
	copy(out, bashMinimalEnvKeys)
	return out
}
