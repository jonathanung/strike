package actionfacts

import (
	"strings"
	"testing"
)

func TestAnalyzeNoPanicAdversarial(t *testing.T) {
	inputs := []string{
		"",
		strings.Repeat("a", maxCommandBytes+10),
		strings.Repeat("echo x | ", 200) + "true",
		"bash -c '" + strings.Repeat("bash -c '", 20) + "true" + strings.Repeat("'", 20),
		"echo " + strings.Repeat(`"`, 100),
		"echo " + strings.Repeat(`'`, 100),
		"$((1+2))",
		"${HOME:-x}",
		"curl " + strings.Repeat("a", 9000) + ".com",
		string([]byte{0xff, 0xfe, 0xfd}),
		"echo hi >",
		" | ",
		"&&&",
		"<<<foo",
		"(echo hi)",
		"function foo { echo; }",
		"rm -- -rf /",
		"curl -H 'A: " + strings.Repeat("b", 5000) + "' http://x",
	}
	for i, cmd := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("case %d panic: %v cmd=%q", i, r, truncate(cmd, 80))
				}
			}()
			_ = Analyze(Input{Tool: "bash", Command: cmd})
		}()
	}
}

func FuzzAnalyze(f *testing.F) {
	seeds := []string{
		"ls",
		"curl https://example.com",
		"cat a | grep b > c",
		`bash -c 'rm -rf /tmp'`,
		"echo $HOME",
		"eval true",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		// Bound fuzz input to avoid multi-second pathological cases.
		if len(cmd) > 4096 {
			cmd = cmd[:4096]
		}
		facts := Analyze(Input{Tool: "bash", Command: cmd})
		// Invariants
		if facts.Authoritative() != (facts.Parse.Status == StatusComplete) {
			t.Fatalf("Authoritative mismatch status=%s", facts.Parse.Status)
		}
		if facts.EnforcementEligible() && !facts.Authoritative() {
			t.Fatal("enforcement without authoritative")
		}
		if !facts.EnforcementEligible() && len(MatchKeys(facts)) != 0 {
			t.Fatalf("keys on non-eligible: %v", MatchKeys(facts))
		}
		_ = Summary(facts)
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
