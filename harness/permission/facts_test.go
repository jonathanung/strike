package permission

import (
	"strings"
	"testing"
)

func TestEvaluateDetailedFactBackedNestedDeny(t *testing.T) {
	rs := Ruleset{
		{Permission: "bash", Pattern: "*", Action: Ask},
		{Permission: "bash", Pattern: "rm *", Action: Deny},
	}
	// Nested shell: raw pattern is bash -c '…' which does not match "rm *",
	// but authoritative facts expose the inner rm.
	action, det := EvaluateDetailed("bash", `bash -c 'rm -rf /tmp/x'`, rs)
	if action != Deny {
		t.Fatalf("action=%s det=%+v summary=%s", action, det.Matched, det.FactSummary)
	}
	if det.EvalPath != EvalPathFacts {
		t.Fatalf("evalPath=%q want facts (summary=%s keys=%v)", det.EvalPath, det.FactSummary, det.FactKeys)
	}
	if !det.FactsEnforcement {
		t.Fatal("expected enforcement")
	}
}

func TestEvaluateDetailedPathDenyViaFacts(t *testing.T) {
	rs := Ruleset{
		{Permission: "bash", Pattern: "*", Action: Allow},
		{Permission: "bash", Pattern: "**/.env", Action: Deny},
	}
	action, det := EvaluateDetailed("bash", "cat .env", rs)
	if action != Deny {
		t.Fatalf("action=%s paths summary=%s keys=%v", action, det.FactSummary, det.FactKeys)
	}
	if det.EvalPath != EvalPathFacts {
		t.Fatalf("evalPath=%q", det.EvalPath)
	}
}

func TestEvaluateDetailedBypassStaysPatternOnly(t *testing.T) {
	rs := Ruleset{
		{Permission: "bash", Pattern: "*", Action: Ask},
		{Permission: "bash", Pattern: "rm *", Action: Deny},
	}
	// Dynamic expansion: must not deny via fabricated facts.
	action, det := EvaluateDetailed("bash", `rm $TARGET`, rs)
	if det.FactsEnforcement {
		t.Fatalf("bypass must not be enforcement-eligible: %s", det.FactSummary)
	}
	// Raw pattern "rm $TARGET" may or may not match "rm *" depending on glob.
	// doublestar: "rm *" matches "rm $TARGET"? * is one path segment-ish —
	// doublestar * matches within one segment; space-separated: "rm *" means
	// rm + space + *. Actually doublestar Match("rm *", "rm $TARGET") is true
	// because * matches $TARGET.
	// So pattern path may still deny — that's legacy behavior, OK.
	// Critical: EvalPath must not be facts.
	if det.EvalPath == EvalPathFacts {
		t.Fatalf("must not use facts path on bypass: %+v", det)
	}
	_ = action
}

func TestEvaluateDetailedNoDualEvalSameRule(t *testing.T) {
	// A single rule should not be counted twice; last-match trail has one entry
	// per matching rule application.
	rs := Ruleset{
		{Permission: "bash", Pattern: "curl *", Action: Deny},
	}
	_, det := EvaluateDetailed("bash", "curl https://example.com/x", rs)
	if det.Action != Deny {
		t.Fatalf("action=%s", det.Action)
	}
	// Exactly one trail entry for the one rule (facts OR pattern, not both).
	if len(det.Trail) != 1 {
		t.Fatalf("trail=%+v want 1 (no dual-eval)", det.Trail)
	}
}

func TestEvaluateDetailedHostKey(t *testing.T) {
	rs := Ruleset{
		{Permission: "bash", Pattern: "*", Action: Allow},
		{Permission: "bash", Pattern: "host:evil.example", Action: Deny},
	}
	action, det := EvaluateDetailed("bash", "curl https://evil.example/a", rs)
	if action != Deny {
		t.Fatalf("action=%s keys=%v summary=%s", action, det.FactKeys, det.FactSummary)
	}
	if det.EvalPath != EvalPathFacts {
		t.Fatalf("evalPath=%q", det.EvalPath)
	}
}

func TestFormatDetailedExplanation(t *testing.T) {
	_, det := EvaluateDetailed("bash", "git status", Ruleset{
		{Permission: "bash", Pattern: "git *", Action: Allow},
	})
	s := FormatDetailedExplanation(det)
	if !strings.Contains(s, "allow") {
		t.Fatalf("format=%q", s)
	}
	if !strings.Contains(s, "eval=") && !strings.Contains(s, "facts:") {
		t.Fatalf("expected eval/facts in %q", s)
	}
}

func TestExplainDetailedService(t *testing.T) {
	svc := New(nil, Defaults(), Ruleset{
		{Permission: "bash", Pattern: "rm *", Action: Deny},
	})
	det := svc.ExplainDetailed("bash", `sh -c 'rm -rf /tmp/z'`)
	if det.Action != Deny {
		t.Fatalf("action=%s summary=%s", det.Action, det.FactSummary)
	}
	if det.EvalPath != EvalPathFacts {
		t.Fatalf("evalPath=%q fact=%s", det.EvalPath, det.FactSummary)
	}
}

func TestLegacyPatternStillWorks(t *testing.T) {
	// Unchanged last-match-wins for ordinary commands.
	base := Defaults()
	project := Ruleset{
		{Permission: "bash", Pattern: "git *", Action: Allow},
		{Permission: "bash", Pattern: "git push *", Action: Deny},
	}
	if got := Evaluate("bash", "git status", base, project); got != Allow {
		t.Fatalf("git status=%s", got)
	}
	if got := Evaluate("bash", "git push origin main", base, project); got != Deny {
		t.Fatalf("git push=%s", got)
	}
}
