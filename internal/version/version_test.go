package version

import "testing"

func TestString(t *testing.T) {
	origV, origC := Version, Commit
	t.Cleanup(func() {
		Version, Commit = origV, origC
	})

	tests := []struct {
		v, c, want string
	}{
		{"v1.2.3", "abcdef0", "v1.2.3 (abcdef0)"},
		{"v1.2.3", "abcdef012345", "v1.2.3 (abcdef0)"},
		{"", "", "dev (none)"},
		{"dev", "none", "dev (none)"},
	}
	for _, tt := range tests {
		Version, Commit = tt.v, tt.c
		if got := String(); got != tt.want {
			t.Errorf("Version=%q Commit=%q: String()=%q, want %q", tt.v, tt.c, got, tt.want)
		}
	}
}
