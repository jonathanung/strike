//go:build darwin

package sandbox

import "testing"

func TestSeatbeltEscape(t *testing.T) {
	got := seatbeltEscape(`path\with"quotes`)
	if got != `path\\with\"quotes` {
		t.Fatalf("escape = %q", got)
	}
}
