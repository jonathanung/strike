package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentOwnershipList(t *testing.T) {
	own := NewPathOwnership(OverlapWarn)
	own.Touch("s1", "alice", "/proj/a.go", "a.go")
	own.Touch("s2", "bob", "/proj/a.go", "a.go")

	tc := allowAll(t.TempDir())
	tc.Ownership = own
	tc.SessionID = "lead"
	tc.OwnershipQuery = func(context.Context) (OwnershipSnapshot, error) {
		return own.Snapshot(), nil
	}

	res, err := NewAgentOwnership().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var snap OwnershipSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Overlaps) != 1 {
		t.Fatalf("overlaps = %v", snap.Overlaps)
	}
	if !strings.Contains(res.Title, "overlap") {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestAgentOwnershipLeaseRelease(t *testing.T) {
	dir := t.TempDir()
	own := NewPathOwnership(OverlapWarn)
	tc := allowAll(dir)
	tc.Ownership = own
	tc.SessionID = "s1"
	tc.MemberName = "alice"

	res, err := NewAgentOwnership().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "lease",
		"path":   "pkg",
		"mode":   "exclusive",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"status":"ok"`) && !strings.Contains(res.Output, `"status": "ok"`) {
		// compact JSON
		if !strings.Contains(res.Output, "ok") {
			t.Fatalf("lease output = %q", res.Output)
		}
	}

	_, err = NewAgentOwnership().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "release",
		"path":   "pkg",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(own.Snapshot().Claims) != 0 {
		t.Fatalf("after release: %+v", own.Snapshot())
	}
}

func TestAgentOwnershipUnavailable(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewAgentOwnership().Execute(context.Background(), nil, tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("want unavailable, got %v", err)
	}
}

func TestAgentOwnershipLeaseRequiresPath(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Ownership = NewPathOwnership(OverlapWarn)
	tc.SessionID = "s1"
	_, err := NewAgentOwnership().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "lease",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("want path required, got %v", err)
	}
}
