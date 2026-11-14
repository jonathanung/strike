package provider

import (
	"errors"
	"fmt"
	"testing"
)

type fakeAuthErr struct {
	name string
	ok   bool
}

func (e fakeAuthErr) Error() string               { return "fake" }
func (e fakeAuthErr) AuthFailure() (string, bool) { return e.name, e.ok }

func TestAuthFailure(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantName string
		wantOK   bool
	}{
		{"nil", nil, "", false},
		{"plain text never classified", errors.New("401 Unauthorized: invalid api key"), "", false},
		{"marker true", fakeAuthErr{name: "anthropic", ok: true}, "anthropic", true},
		{"marker false", fakeAuthErr{name: "anthropic", ok: false}, "anthropic", false},
		{"wrapped marker", fmt.Errorf("turn: %w", fakeAuthErr{name: "google", ok: true}), "google", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, ok := AuthFailure(tt.err)
			if ok != tt.wantOK || (ok && name != tt.wantName) {
				t.Fatalf("AuthFailure() = (%q, %v), want (%q, %v)", name, ok, tt.wantName, tt.wantOK)
			}
		})
	}
}
