package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestApplyTraceRetentionMaxFiles(t *testing.T) {
	traces := t.TempDir()
	runs := t.TempDir()
	for i := 0; i < 4; i++ {
		id := "s" + string(rune('a'+i))
		if err := os.MkdirAll(filepath.Join(traces, id, "blobs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(traces, id, "blobs", "x"), []byte("blob"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(runs, id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runs, id, "snap.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-time.Duration(4-i) * time.Hour)
		_ = os.Chtimes(filepath.Join(traces, id), mt, mt)
		_ = os.Chtimes(filepath.Join(runs, id), mt, mt)
	}
	res, err := ApplyTraceRetention(traces, runs, TraceRetentionFromConfig(2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Traces.Deleted) != 2 {
		t.Fatalf("traces deleted = %v", res.Traces.Deleted)
	}
	if len(res.Runs.Deleted) != 2 {
		t.Fatalf("runs deleted = %v", res.Runs.Deleted)
	}
	tents, _ := os.ReadDir(traces)
	rents, _ := os.ReadDir(runs)
	if len(tents) != 2 || len(rents) != 2 {
		t.Fatalf("remain traces=%d runs=%d", len(tents), len(rents))
	}
}

func TestRemoveTraceSidecars(t *testing.T) {
	traces := t.TempDir()
	runs := t.TempDir()
	id := "sess-abc"
	_ = os.MkdirAll(filepath.Join(traces, id, "blobs"), 0o755)
	_ = os.WriteFile(filepath.Join(traces, id, "blobs", "h"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(runs, id), 0o755)
	_ = os.WriteFile(filepath.Join(runs, id, "r.json"), []byte("{}"), 0o644)
	// Unrelated session must survive.
	_ = os.MkdirAll(filepath.Join(traces, "other"), 0o755)

	if err := RemoveTraceSidecars(traces, runs, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(traces, id)); !os.IsNotExist(err) {
		t.Fatalf("traces sidecars remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs, id)); !os.IsNotExist(err) {
		t.Fatalf("runs sidecars remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(traces, "other")); err != nil {
		t.Fatal("unrelated session tree deleted")
	}
}

func TestApplyRetentionWithSidecars(t *testing.T) {
	sessDir := t.TempDir()
	traces := t.TempDir()
	runs := t.TempDir()
	m := NewManager(sessDir)

	var ids []string
	for i := 0; i < 3; i++ {
		info, err := m.Create(CreateOptions{Title: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Append(info.ID, protocol.UserMessage{Text: "x"}); err != nil {
			t.Fatal(err)
		}
		if err := m.Close(info.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, info.ID)
		// Sidecar trees named by session id.
		_ = os.MkdirAll(filepath.Join(traces, info.ID, "blobs"), 0o755)
		_ = os.WriteFile(filepath.Join(traces, info.ID, "blobs", "b"), []byte("data"), 0o644)
		_ = os.MkdirAll(filepath.Join(runs, info.ID), 0o755)
		_ = os.WriteFile(filepath.Join(runs, info.ID, "snap.json"), []byte("{}"), 0o644)
		time.Sleep(2 * time.Millisecond)
	}
	// Keep only newest session.
	res, err := m.ApplyRetentionWithSidecars(RetentionPolicy{MaxSessions: 1}, traces, runs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) < 2 {
		t.Fatalf("deleted sessions = %v", res.Deleted)
	}
	for _, id := range res.Deleted {
		if _, err := os.Stat(filepath.Join(traces, id)); !os.IsNotExist(err) {
			t.Fatalf("trace sidecar for deleted session %s still present", id)
		}
		if _, err := os.Stat(filepath.Join(runs, id)); !os.IsNotExist(err) {
			t.Fatalf("run sidecar for deleted session %s still present", id)
		}
	}
	// Surviving session keeps sidecars.
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("sessions left = %d", len(list))
	}
	keep := list[0].ID
	if _, err := os.Stat(filepath.Join(traces, keep)); err != nil {
		t.Fatalf("kept session traces missing: %v", err)
	}
}

func TestTraceRetentionFromConfig(t *testing.T) {
	p := TraceRetentionFromConfig(5, 3, 99)
	if !p.Active() || p.MaxFiles != 5 || p.MaxBytes != 99 {
		t.Fatalf("%+v", p)
	}
	if p.MaxAge != 3*24*time.Hour {
		t.Fatalf("age = %s", p.MaxAge)
	}
}

func TestDefaultTraceDirsNonEmpty(t *testing.T) {
	if DefaultTracesDir() == "" || DefaultRunsDir() == "" {
		t.Fatal("empty defaults")
	}
	if !strings.Contains(DefaultTracesDir(), "traces") {
		t.Fatalf("traces dir = %s", DefaultTracesDir())
	}
	if !strings.Contains(DefaultRunsDir(), "runs") {
		t.Fatalf("runs dir = %s", DefaultRunsDir())
	}
}
