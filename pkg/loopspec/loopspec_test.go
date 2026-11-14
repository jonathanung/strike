package loopspec

import (
	"strings"
	"testing"
	"time"
)

func TestParseInterval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw     string
		want    time.Duration
		wantErr string
	}{
		{raw: "15m", want: 15 * time.Minute},
		{raw: " 2h ", want: 2 * time.Hour},
		{raw: "30s", want: 30 * time.Second},
		{raw: "1s", want: time.Second},
		{raw: "1h30m", want: 90 * time.Minute},
		{raw: "15", want: 15 * time.Minute},
		{raw: "168h", want: 168 * time.Hour},
		{raw: "", wantErr: "empty"},
		{raw: "0", wantErr: "invalid"},
		{raw: "500ms", wantErr: "too short"},
		{raw: "999ms", wantErr: "too short"},
		{raw: "169h", wantErr: "too long"},
		{raw: "168h1s", wantErr: "too long"},
		{raw: "-5m", wantErr: "too short"},
		{raw: "soon", wantErr: "invalid"},
	}
	for _, tt := range tests {
		got, err := ParseInterval(tt.raw)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseInterval(%q) err=%v, want containing %q", tt.raw, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseInterval(%q)=%v,%v want %v", tt.raw, got, err, tt.want)
		}
	}
}

func TestValidateJob(t *testing.T) {
	t.Parallel()
	if got, err := ValidateJob("  check CI\tnow\nplease "); err != nil || got != "check CI\tnow\nplease" {
		t.Fatalf("ValidateJob trims and keeps tab/newline: %q %v", got, err)
	}
	for _, bad := range []string{"", "   ", "bell\x07", "nul\x00", strings.Repeat("x", MaxJobBytes+1)} {
		if _, err := ValidateJob(bad); err == nil {
			t.Errorf("ValidateJob(%q) accepted", bad)
		}
	}
}

func TestFormatInterval(t *testing.T) {
	t.Parallel()
	cases := map[time.Duration]string{
		15 * time.Minute:        "15m",
		2 * time.Hour:           "2h",
		30 * time.Second:        "30s",
		90 * time.Minute:        "90m",
		1500 * time.Millisecond: "1.5s",
		0:                       "0s",
	}
	for d, want := range cases {
		if got := FormatInterval(d); got != want {
			t.Errorf("FormatInterval(%v)=%q want %q", d, got, want)
		}
	}
}
