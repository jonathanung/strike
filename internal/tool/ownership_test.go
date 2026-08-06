package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNormalizeOverlapPolicy(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", OverlapWarn},
		{"warn", OverlapWarn},
		{"WARN", OverlapWarn},
		{"off", OverlapOff},
		{"block", OverlapBlock},
		{"nope", OverlapWarn},
	}
	for _, tc := range cases {
		if got := NormalizeOverlapPolicy(tc.in); got != tc.want {
			t.Errorf("NormalizeOverlapPolicy(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPathOwnershipNilReceiver(t *testing.T) {
	var o *PathOwnership
	if res := o.Touch("a", "", "/x", "x"); res.Overlap {
		t.Fatalf("nil touch overlap: %+v", res)
	}
	o.DeactivateSession("a")
	o.ReleaseLease("a", "/x")
	o.ReleaseAllLeases("a")
	o.Clear()
	o.SetPolicy(OverlapBlock)
	o.SetNotify(nil)
	if o.Policy() != OverlapWarn {
		t.Fatalf("nil Policy = %q", o.Policy())
	}
	snap := o.Snapshot()
	if len(snap.Claims) != 0 {
		t.Fatalf("nil snapshot claims = %+v", snap.Claims)
	}
}

func TestPathOwnershipDisjointNoOverlap(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	a := o.Touch("s1", "alice", "/proj/a.go", "a.go")
	b := o.Touch("s2", "bob", "/proj/b.go", "b.go")
	if a.Overlap || b.Overlap {
		t.Fatalf("disjoint overlap: a=%+v b=%+v", a, b)
	}
	snap := o.Snapshot()
	if len(snap.Overlaps) != 0 {
		t.Fatalf("overlaps = %v", snap.Overlaps)
	}
	if len(snap.Claims) != 2 {
		t.Fatalf("claims = %d, want 2", len(snap.Claims))
	}
}

func TestPathOwnershipDetectsActiveOverlap(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	var notified []TouchResult
	o.SetNotify(func(r TouchResult) { notified = append(notified, r) })

	if res := o.Touch("s1", "alice", "/proj/shared.go", "shared.go"); res.Overlap {
		t.Fatalf("first touch: %+v", res)
	}
	res := o.Touch("s2", "bob", "/proj/shared.go", "shared.go")
	if !res.Overlap || res.Blocked {
		t.Fatalf("second touch: %+v", res)
	}
	if !strings.Contains(res.Warning, "shared.go") || !strings.Contains(res.Warning, "alice") {
		t.Fatalf("warning = %q", res.Warning)
	}
	if len(res.Holders) != 1 || res.Holders[0].SessionID != "s1" {
		t.Fatalf("holders = %+v", res.Holders)
	}
	if len(notified) != 1 {
		t.Fatalf("notify count = %d", len(notified))
	}
	snap := o.Snapshot()
	if len(snap.Overlaps) != 1 || snap.Overlaps[0] != "/proj/shared.go" {
		t.Fatalf("snap overlaps = %v", snap.Overlaps)
	}
}

func TestPathOwnershipSelfRetouchNoOverlap(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	o.Touch("s1", "alice", "/proj/a.go", "a.go")
	res := o.Touch("s1", "alice", "/proj/a.go", "a.go")
	if res.Overlap {
		t.Fatalf("self retouch: %+v", res)
	}
}

func TestPathOwnershipBlockPolicy(t *testing.T) {
	o := NewPathOwnership(OverlapBlock)
	o.Touch("s1", "", "/proj/a.go", "a.go")
	res := o.Touch("s2", "", "/proj/a.go", "a.go")
	if !res.Blocked || !res.Overlap {
		t.Fatalf("block result: %+v", res)
	}
	// Blocked writer must not appear as a holder.
	snap := o.Snapshot()
	for _, c := range snap.Claims {
		if c.Path != "/proj/a.go" {
			continue
		}
		for _, h := range c.Holders {
			if h.SessionID == "s2" {
				t.Fatalf("blocked session recorded: %+v", c.Holders)
			}
		}
	}
}

func TestPathOwnershipOffPolicySilent(t *testing.T) {
	o := NewPathOwnership(OverlapOff)
	var n int
	o.SetNotify(func(TouchResult) { n++ })
	o.Touch("s1", "", "/p/a", "a")
	res := o.Touch("s2", "", "/p/a", "a")
	if res.Overlap || res.Warning != "" {
		t.Fatalf("off policy: %+v", res)
	}
	if n != 0 {
		t.Fatalf("notify called %d times", n)
	}
	// Still tracked for query.
	if len(o.Snapshot().Claims) != 1 {
		t.Fatalf("claims = %+v", o.Snapshot().Claims)
	}
}

func TestPathOwnershipDeactivateStopsOverlap(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	o.Touch("s1", "alice", "/proj/a.go", "a.go")
	o.DeactivateSession("s1")
	res := o.Touch("s2", "bob", "/proj/a.go", "a.go")
	if res.Overlap {
		t.Fatalf("after deactivate: %+v", res)
	}
	// History retained.
	snap := o.Snapshot()
	var foundInactive bool
	for _, c := range snap.Claims {
		for _, h := range c.Holders {
			if h.SessionID == "s1" && !h.Active {
				foundInactive = true
			}
		}
	}
	if !foundInactive {
		t.Fatalf("inactive history missing: %+v", snap)
	}
}

func TestPathOwnershipLeaseExclusive(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	res := o.AcquireLease("s1", "alice", "/proj/pkg", "pkg", true)
	if res.Overlap {
		t.Fatalf("first lease: %+v", res)
	}
	// Touch under leased prefix by another session.
	touch := o.Touch("s2", "bob", "/proj/pkg/a.go", "pkg/a.go")
	if !touch.Overlap {
		t.Fatalf("touch under exclusive lease: %+v", touch)
	}
	// Second exclusive lease on same prefix.
	lease2 := o.AcquireLease("s2", "bob", "/proj/pkg", "pkg", true)
	if !lease2.Overlap {
		t.Fatalf("second exclusive: %+v", lease2)
	}
	o.ReleaseLease("s1", "/proj/pkg")
	ok := o.AcquireLease("s2", "bob", "/proj/pkg", "pkg", true)
	if ok.Overlap {
		t.Fatalf("after release: %+v", ok)
	}
}

func TestPathOwnershipLeaseSharedCompatible(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	a := o.AcquireLease("s1", "", "/proj/pkg", "pkg", false)
	b := o.AcquireLease("s2", "", "/proj/pkg", "pkg", false)
	if a.Overlap || b.Overlap {
		t.Fatalf("shared leases: a=%+v b=%+v", a, b)
	}
	// Exclusive conflicts with shared.
	ex := o.AcquireLease("s3", "", "/proj/pkg", "pkg", true)
	if !ex.Overlap {
		t.Fatalf("exclusive vs shared: %+v", ex)
	}
}

func TestPathOwnershipLeaseBlock(t *testing.T) {
	o := NewPathOwnership(OverlapBlock)
	o.AcquireLease("s1", "", "/proj/pkg", "pkg", true)
	res := o.AcquireLease("s2", "", "/proj/pkg", "pkg", true)
	if !res.Blocked {
		t.Fatalf("want blocked lease: %+v", res)
	}
	// Not recorded.
	for _, c := range o.Snapshot().Claims {
		for _, h := range c.Holders {
			if h.SessionID == "s2" {
				t.Fatalf("blocked lease recorded: %+v", c)
			}
		}
	}
}

func TestPathOwnershipReleaseAllLeases(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	o.AcquireLease("s1", "", "/a", "a", true)
	o.AcquireLease("s1", "", "/b", "b", true)
	o.ReleaseAllLeases("s1")
	if len(o.Snapshot().Claims) != 0 {
		t.Fatalf("after release all: %+v", o.Snapshot())
	}
}

func TestPathOwnershipRecordFilesChanged(t *testing.T) {
	dir := t.TempDir()
	o := NewPathOwnership(OverlapWarn)
	o.Touch("s1", "alice", filepath.Join(dir, "x.go"), "x.go")
	results := o.RecordFilesChanged("s2", "bob", dir, []string{"x.go", "y.go", "", "  "})
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	if !results[0].Overlap {
		t.Fatalf("files_changed overlap on x.go: %+v", results[0])
	}
	if results[1].Overlap {
		t.Fatalf("y.go should be clean: %+v", results[1])
	}
}

func TestPathOwnershipClear(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	o.Touch("s1", "", "/a", "a")
	o.AcquireLease("s1", "", "/b", "b", true)
	o.Clear()
	if len(o.Snapshot().Claims) != 0 {
		t.Fatalf("clear: %+v", o.Snapshot())
	}
}

func TestClaimWriteAndEditIntegration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	own := NewPathOwnership(OverlapWarn)
	tc1 := allowAll(dir)
	tc1.Ownership = own
	tc1.SessionID = "child-a"
	tc1.MemberName = "alice"

	tc2 := allowAll(dir)
	tc2.Ownership = own
	tc2.SessionID = "child-b"
	tc2.MemberName = "bob"

	res, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "note.txt",
		"oldString": "hello",
		"newString": "hi",
	}), tc1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "warning:") {
		t.Fatalf("first edit should be clean: %q", res.Output)
	}

	// Restore content for second edit.
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res2, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "note.txt",
		"oldString": "hello",
		"newString": "hey",
	}), tc2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res2.Output, "warning:") || !strings.Contains(res2.Output, "overlap") {
		t.Fatalf("second edit want overlap warning, got %q", res2.Output)
	}
}

func TestClaimWriteBlockIntegration(t *testing.T) {
	dir := t.TempDir()
	own := NewPathOwnership(OverlapBlock)
	tc1 := allowAll(dir)
	tc1.Ownership = own
	tc1.SessionID = "a"
	tc2 := allowAll(dir)
	tc2.Ownership = own
	tc2.SessionID = "b"

	if _, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "f.txt",
		"content":  "one",
	}), tc1); err != nil {
		t.Fatal(err)
	}
	_, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "f.txt",
		"content":  "two",
	}), tc2)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("want block error, got %v", err)
	}
	// File still first writer's content.
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "one" {
		t.Fatalf("content = %q", data)
	}
}

func TestClaimWriteNilOwnershipNoop(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	tc.SessionID = "solo"
	// No Ownership — single-agent path.
	if _, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "solo.txt",
		"content":  "ok",
	}), tc); err != nil {
		t.Fatal(err)
	}
}

func TestPathOwnershipConcurrentTouches(t *testing.T) {
	o := NewPathOwnership(OverlapWarn)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "s" + string(rune('a'+i%5))
			path := "/proj/f" + string(rune('0'+i%3)) + ".go"
			o.Touch(id, "", path, path)
		}(i)
	}
	wg.Wait()
	// Must not panic; snapshot is well-formed.
	snap := o.Snapshot()
	if snap.Policy != OverlapWarn {
		t.Fatalf("policy = %q", snap.Policy)
	}
}
