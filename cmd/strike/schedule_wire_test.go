package main

import "testing"

func TestProcessBypassDisabledForUnattended(t *testing.T) {
	cases := []struct {
		processSkip bool
		unattended  bool
		want        bool
	}{
		{true, false, true},
		{true, true, false},
		{false, false, false},
		{false, true, false},
	}
	for _, tc := range cases {
		if got := processBypassEnabled(tc.processSkip, tc.unattended); got != tc.want {
			t.Errorf("processSkip=%v unattended=%v got %v want %v", tc.processSkip, tc.unattended, got, tc.want)
		}
	}
}
