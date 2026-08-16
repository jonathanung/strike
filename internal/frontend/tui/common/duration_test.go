package common

import (
	"testing"
	"time"
)

func TestFormatCompactDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "zero", d: 0, want: "0s"},
		{name: "five seconds", d: 5 * time.Second, want: "5s"},
		{name: "fifty nine seconds", d: 59 * time.Second, want: "59s"},
		{name: "one minute", d: 60 * time.Second, want: "1m 0s"},
		{name: "two minutes five", d: 125 * time.Second, want: "2m 5s"},
		{name: "negative", d: -3 * time.Second, want: "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatCompactDuration(tt.d); got != tt.want {
				t.Errorf("FormatCompactDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
