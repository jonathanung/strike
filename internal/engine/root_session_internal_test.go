package engine

import "testing"

func TestRootSessionIDOnNewAndChild(t *testing.T) {
	root := New(Options{SessionID: "root-xyz"})
	if got := root.rootSessionID(); got != "root-xyz" {
		t.Fatalf("root.rootSessionID = %q", got)
	}
	if root.opts.RootSessionID != "root-xyz" {
		t.Fatalf("opts.RootSessionID = %q", root.opts.RootSessionID)
	}

	// Explicit RootSessionID wins over parent fallback.
	child := New(Options{
		SessionID:       "child-1",
		ParentSessionID: "parent-mid",
		RootSessionID:   "root-xyz",
		Depth:           2,
		Team:            root.team,
	})
	if got := child.rootSessionID(); got != "root-xyz" {
		t.Fatalf("nested child rootSessionID = %q", got)
	}

	// Depth-1 without explicit root falls back to ParentSessionID.
	d1 := New(Options{
		SessionID:       "child-d1",
		ParentSessionID: "root-only",
		Depth:           1,
	})
	if got := d1.rootSessionID(); got != "root-only" {
		t.Fatalf("depth-1 fallback = %q", got)
	}
}
