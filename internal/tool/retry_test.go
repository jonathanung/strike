package tool

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecideRetryPolicyTable(t *testing.T) {
	t.Parallel()
	type row struct {
		code ErrorCode
		id   Idempotency
		want RetryDecision
	}
	cases := []row{
		// transient × idempotency
		{CodeTransient, IdempotencySafeRetry, DecisionRetry},
		{CodeTransient, IdempotencyConditional, DecisionFail},
		{CodeTransient, IdempotencyUnsafe, DecisionFail},
		// timeout × idempotency
		{CodeTimeout, IdempotencySafeRetry, DecisionRetry},
		{CodeTimeout, IdempotencyConditional, DecisionFail},
		{CodeTimeout, IdempotencyUnsafe, DecisionFail},
		// precondition
		{CodePreconditionFailed, IdempotencySafeRetry, DecisionRecover},
		{CodePreconditionFailed, IdempotencyConditional, DecisionRecover},
		{CodePreconditionFailed, IdempotencyUnsafe, DecisionFail},
		// permanent / policy
		{CodePermissionDenied, IdempotencySafeRetry, DecisionFail},
		{CodeInvalidArgs, IdempotencySafeRetry, DecisionFail},
		{CodeCanceled, IdempotencySafeRetry, DecisionFail},
		{CodeBlocked, IdempotencySafeRetry, DecisionFail},
		{CodeSandboxDenied, IdempotencySafeRetry, DecisionFail},
		{CodeSandboxDenied, IdempotencyUnsafe, DecisionFail},
		{CodeInternal, IdempotencySafeRetry, DecisionFail},
		{CodePermissionDenied, IdempotencyUnsafe, DecisionFail},
		// unknown code
		{ErrorCode("mystery"), IdempotencySafeRetry, DecisionFail},
		{"", IdempotencySafeRetry, DecisionFail},
	}
	for _, tc := range cases {
		got := DecideRetry(tc.code, tc.id)
		if got != tc.want {
			t.Errorf("DecideRetry(%q, %q) = %q, want %q", tc.code, tc.id, got, tc.want)
		}
	}
}

func TestDecideRetryMutativeToolsNeverAutoRetryTransient(t *testing.T) {
	t.Parallel()
	// Acceptance: mutative tools do not auto-retry on generic/transient failure.
	for _, tool := range []Tool{NewEdit(), NewWrite(), NewApplyPatch(), NewMove(), NewDelete(), NewBash(), NewNotebookEdit()} {
		c := LookupContract(tool)
		for _, code := range []ErrorCode{CodeTransient, CodeTimeout, CodeInternal, ""} {
			if d := DecideRetry(code, c.Idempotency); d == DecisionRetry {
				t.Errorf("%s DecideRetry(%q, %q) = retry; mutative must not auto-retry",
					tool.Name(), code, c.Idempotency)
			}
		}
	}
}

func TestRecoveryHint(t *testing.T) {
	t.Parallel()
	if RecoveryHint(CodeTransient, DecisionRetry) != "" {
		t.Fatal("retry should not attach recovery hint")
	}
	hint := RecoveryHint(CodePreconditionFailed, DecisionRecover)
	if !strings.Contains(hint, "re-read") {
		t.Fatalf("hint = %q", hint)
	}
	out := AppendRecoveryHint("Error: precondition_failed: stale", CodePreconditionFailed, DecisionRecover)
	if !strings.Contains(out, "[recovery:") {
		t.Fatalf("output missing recovery: %q", out)
	}
	// Idempotent append
	out2 := AppendRecoveryHint(out, CodePreconditionFailed, DecisionRecover)
	if strings.Count(out2, "[recovery:") != 1 {
		t.Fatalf("double recovery: %q", out2)
	}
}

func TestToolRetryDelayCapsAndJitter(t *testing.T) {
	t.Parallel()
	// With tiny base, delay is in [0, base<<n] capped.
	seen := map[time.Duration]bool{}
	for i := 0; i < 40; i++ {
		d := ToolRetryDelay(2, 10*time.Millisecond, 50*time.Millisecond)
		if d < 0 || d > 50*time.Millisecond {
			t.Fatalf("delay %v out of range", d)
		}
		seen[d] = true
	}
	// Cap at max
	d := ToolRetryDelay(20, 200*time.Millisecond, 300*time.Millisecond)
	if d > 300*time.Millisecond {
		t.Fatalf("uncapped delay %v", d)
	}
	_ = seen
}

func TestArgsFingerprintStable(t *testing.T) {
	t.Parallel()
	a := json.RawMessage(`{"path":"a.go","oldString":"x"}`)
	b := json.RawMessage(`{"path":"a.go","oldString":"x"}`)
	c := json.RawMessage(`{"path":"a.go","oldString":"y"}`)
	if ArgsFingerprint(a) != ArgsFingerprint(b) {
		t.Fatal("same args different fingerprint")
	}
	if ArgsFingerprint(a) == ArgsFingerprint(c) {
		t.Fatal("different args same fingerprint")
	}
	if CallSignature("edit", a) == CallSignature("write", a) {
		t.Fatal("name ignored in signature")
	}
}
