package tool

import (
	"strings"
	"testing"
)

func TestNormalizeTUIFrameRedactsAndBounds(t *testing.T) {
	secret := "sk-ant-" + strings.Repeat("b", 16)
	var b strings.Builder
	for i := 0; i < MaxTUISnapshotLines+10; i++ {
		b.WriteString("line token ")
		b.WriteString(secret)
		b.WriteByte('\n')
	}
	got := NormalizeTUIFrame(b.String(), 40, 100)
	if !got.Truncated {
		t.Fatal("expected truncation")
	}
	if strings.Contains(got.Text, secret) {
		t.Fatal("secret leaked")
	}
	if !got.Redacted {
		t.Fatal("expected Redacted")
	}
	if !strings.Contains(got.Text, "... (truncated)") {
		t.Fatalf("missing trunc marker: %q", got.Text)
	}
}
