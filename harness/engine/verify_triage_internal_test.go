package engine

import (
	"reflect"
	"testing"

	"github.com/jonathanung/strike/harness/verify"
)

func TestVerificationTriageRoundTrip(t *testing.T) {
	tri := &verify.TriageReport{
		Failures:        []verify.Failure{{Package: "example.com/a", Dir: "./a", Tests: []string{"TestA"}, Source: "a_test.go:3"}},
		FocusedCommand:  "go test './a' -run '^(TestA)$' -count=1",
		OriginalOutput:  "FAIL example.com/a",
		Attempts:        []verify.TriageAttempt{{Command: "go test './a' -count=1", ExitCode: 1, Output: "boom", Error: "exit 1"}},
		FullGateSkipped: true,
		Exhausted:       true,
	}
	res := verify.Result{Checks: []verify.CheckResult{{Kind: verify.KindCmd, Value: "make test", Triage: tri}, {Kind: verify.KindPath, Value: "x"}}}
	rep := verifyResultToProtocol(res)
	if rep.Checks[1].Triage != nil {
		t.Fatalf("nil triage must stay nil: %#v", rep.Checks[1].Triage)
	}
	back := protocolReportToVerifyResult(&rep)
	if !reflect.DeepEqual(back.Checks[0].Triage, tri) {
		t.Fatalf("protocol round trip:\n got %#v\nwant %#v", back.Checks[0].Triage, tri)
	}
	tv := toolVerification(rep)
	if !reflect.DeepEqual(tv.Checks[0].Triage, tri) || tv.Checks[1].Triage != nil {
		t.Fatalf("tool view: %#v", tv.Checks)
	}
}
