package security_test

import (
	"testing"

	"github.com/jonathanung/strike-cli/internal/security"
)

func TestParseSeverity(t *testing.T) {
	cases := []struct {
		in   string
		want security.Severity
		ok   bool
	}{
		{"critical", security.SeverityCritical, true},
		{"CRIT", security.SeverityCritical, true},
		{"high", security.SeverityHigh, true},
		{"medium", security.SeverityMedium, true},
		{"med", security.SeverityMedium, true},
		{"low", security.SeverityLow, true},
		{"info", security.SeverityInfo, true},
		{"", "", false},
		{"nope", "", false},
	}
	for _, tc := range cases {
		got, ok := security.ParseSeverity(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseSeverity(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMaxSeverity(t *testing.T) {
	if got := security.MaxSeverity(nil); got != security.SeverityInfo {
		t.Fatalf("nil = %q", got)
	}
	fs := []security.Finding{
		{Severity: security.SeverityLow},
		{Severity: security.SeverityCritical},
		{Severity: security.SeverityMedium},
	}
	if got := security.MaxSeverity(fs); got != security.SeverityCritical {
		t.Fatalf("MaxSeverity = %q", got)
	}
}

func TestSeverityRankOrder(t *testing.T) {
	order := []security.Severity{
		security.SeverityInfo,
		security.SeverityLow,
		security.SeverityMedium,
		security.SeverityHigh,
		security.SeverityCritical,
	}
	for i := 1; i < len(order); i++ {
		if order[i].Rank() <= order[i-1].Rank() {
			t.Fatalf("rank not increasing: %v then %v", order[i-1], order[i])
		}
	}
}
