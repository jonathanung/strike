package terminal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCWD(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	sibling := root + "-other"
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })

	cases := []struct {
		name      string
		requested string
		want      string
		wantErr   bool
	}{
		{name: "workspace root", want: root},
		{name: "relative subdir", requested: "sub", want: sub},
		{name: "absolute inside", requested: sub, want: sub},
		{name: "parent", requested: "..", wantErr: true},
		{name: "absolute outside", requested: outside, wantErr: true},
		{name: "sibling prefix", requested: sibling, wantErr: true},
		{name: "symlink escape", requested: "escape", wantErr: true},
		{name: "missing", requested: "nope", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCWD(root, tc.requested)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveCWD(%q) = %q, want error", tc.requested, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ResolveCWD(%q) = %q, want %q", tc.requested, got, tc.want)
			}
		})
	}
}

func TestSameDirectory(t *testing.T) {
	root := t.TempDir()
	if SameDirectory("", root) {
		t.Fatal("empty path must not match")
	}
	if !SameDirectory(root, root) {
		t.Fatal("root should match itself")
	}
	other := t.TempDir()
	if SameDirectory(root, other) {
		t.Fatal("distinct directories matched")
	}
}
