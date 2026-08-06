package timeline_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

func TestBoundPayloadSpillAndTruncate(t *testing.T) {
	dir := t.TempDir()
	blobDir := filepath.Join(dir, "blobs")
	// Large tool output with a secret that must not land in the blob.
	secret := "sk-ant-api03-SPILLSECRETVALUE99999999"
	big := strings.Repeat("line of tool output\n", 200) + secret + "\n" + strings.Repeat("z", 500)

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	corr := protocol.Correlation{SessionID: "sess-spill", TurnID: "t1"}
	events := []timeline.TimedEvent{
		{Time: base, Event: protocol.TurnStarted{Correlation: corr}},
		{Time: base.Add(time.Millisecond), Event: protocol.ToolCallBegin{
			Correlation: corr,
			CallID:      "c1",
			Name:        "bash",
			Args:        []byte(`{"command":"` + strings.Repeat("echo x;", 80) + `"}`),
		}},
		{Time: base.Add(2 * time.Millisecond), Event: protocol.ToolCallEnd{
			Correlation: corr,
			CallID:      "c1",
			Output:      big,
		}},
	}
	tr := timeline.Build(events, timeline.Options{
		SessionID:        "sess-spill",
		ArgsPreviewMax:   64,
		OutputPreviewMax: 128,
		BlobDir:          blobDir,
		MaxEntries:       -1,
	})

	var tool *timeline.Entry
	for i := range tr.Entries {
		if tr.Entries[i].Kind == timeline.KindTool {
			tool = &tr.Entries[i]
			break
		}
	}
	if tool == nil {
		t.Fatal("missing tool entry")
	}
	if !tool.Truncated {
		t.Fatalf("expected truncated: %+v", tool)
	}
	if tool.OutputRef == "" || !strings.HasPrefix(tool.OutputRef, timeline.BlobRefPrefix) {
		t.Fatalf("expected output blob ref, got %+v", tool)
	}
	if utf8.RuneCountInString(tool.OutputPreview) > 128+1 { // clip adds …
		t.Fatalf("preview too long: %d runes", utf8.RuneCountInString(tool.OutputPreview))
	}
	if strings.Contains(tool.OutputPreview, secret) {
		t.Fatalf("secret in preview: %q", tool.OutputPreview)
	}

	body, err := timeline.ReadBlob(blobDir, tool.OutputRef)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, secret) {
		t.Fatal("secret leaked into spilled blob")
	}
	if !strings.Contains(body, "REDACTED") {
		t.Fatalf("expected redacted marker in blob, got %q", body[:min(80, len(body))])
	}
	// Timeline entry count stays small (turn + tool).
	if len(tr.Entries) > 4 {
		t.Fatalf("timeline not bounded: %d entries", len(tr.Entries))
	}
}

func TestBoundPayloadTruncateWithoutBlobDir(t *testing.T) {
	base := time.Now().UTC()
	corr := protocol.Correlation{SessionID: "s", TurnID: "t"}
	out := strings.Repeat("x", 5000)
	tr := timeline.Build([]timeline.TimedEvent{
		{Time: base, Event: protocol.ToolCallBegin{Correlation: corr, CallID: "c", Name: "read", Args: []byte(`{}`)}},
		{Time: base.Add(time.Millisecond), Event: protocol.ToolCallEnd{Correlation: corr, CallID: "c", Output: out}},
	}, timeline.Options{OutputPreviewMax: 100, MaxEntries: -1})
	var tool *timeline.Entry
	for i := range tr.Entries {
		if tr.Entries[i].Kind == timeline.KindTool {
			tool = &tr.Entries[i]
		}
	}
	if tool == nil || !tool.Truncated || tool.OutputRef != "" {
		t.Fatalf("want truncate-only: %+v", tool)
	}
	if utf8.RuneCountInString(tool.OutputPreview) > 101 {
		t.Fatalf("preview len = %d", utf8.RuneCountInString(tool.OutputPreview))
	}
}

func TestMaxEntriesPrunesTerminal(t *testing.T) {
	b := timeline.NewBuilder(timeline.Options{
		SessionID:  "root",
		MaxEntries: 5,
	})
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		corr := protocol.Correlation{SessionID: "root", TurnID: "turn-" + itoa(i)}
		t0 := base.Add(time.Duration(i) * time.Second)
		b.Observe(protocol.TurnStarted{Correlation: corr}, t0)
		b.Observe(protocol.TurnCompleted{Correlation: corr}, t0.Add(10*time.Millisecond))
	}
	snap := b.Snapshot()
	if len(snap) > 5 {
		t.Fatalf("entries = %d, want <= 5", len(snap))
	}
	m := b.Metrics()
	if m.Pruned == 0 {
		t.Fatalf("expected pruned > 0: %+v", m)
	}
	if m.Entries != len(snap) {
		t.Fatalf("metrics entries %d != snap %d", m.Entries, len(snap))
	}
}

func TestMetricsObserveLatency(t *testing.T) {
	b := timeline.NewBuilder(timeline.Options{SessionID: "s", MaxEntries: -1})
	base := time.Now().UTC()
	for i := 0; i < 100; i++ {
		corr := protocol.Correlation{SessionID: "s", TurnID: "t" + itoa(i)}
		b.Observe(protocol.TurnStarted{Correlation: corr}, base.Add(time.Duration(i)*time.Millisecond))
		b.Observe(protocol.TurnCompleted{Correlation: corr}, base.Add(time.Duration(i)*time.Millisecond+time.Millisecond))
	}
	m := b.Metrics()
	if m.Observes != 200 {
		t.Fatalf("observes = %d", m.Observes)
	}
	if m.ObserveNanos <= 0 || m.LastObserveNs <= 0 {
		t.Fatalf("expected positive latency: %+v", m)
	}
	if m.AvgObserveNs() <= 0 {
		t.Fatalf("avg = %d", m.AvgObserveNs())
	}
}

func TestRetentionMaxFilesAgeBytes(t *testing.T) {
	root := t.TempDir()
	// Create 5 session-shaped dirs with distinct mtimes and sizes.
	var names []string
	for i := 0; i < 5; i++ {
		name := "sess-" + itoa(i)
		names = append(names, name)
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		payload := strings.Repeat("x", 100*(i+1))
		if err := os.WriteFile(filepath.Join(d, "blob"), []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-time.Duration(5-i) * time.Hour)
		if err := os.Chtimes(d, mt, mt); err != nil {
			t.Fatal(err)
		}
		// Also touch the file so dirBytes is stable.
		if err := os.Chtimes(filepath.Join(d, "blob"), mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	// MaxFiles = 2 keeps the two newest.
	res, err := timeline.ApplyRetention(root, timeline.RetentionPolicy{MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 3 {
		t.Fatalf("deleted = %v, want 3", res.Deleted)
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 {
		t.Fatalf("remain = %d", len(ents))
	}

	// Rebuild for age test.
	root2 := t.TempDir()
	old := filepath.Join(root2, "old")
	fresh := filepath.Join(root2, "fresh")
	_ = os.MkdirAll(old, 0o755)
	_ = os.MkdirAll(fresh, 0o755)
	_ = os.WriteFile(filepath.Join(old, "f"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(fresh, "f"), []byte("b"), 0o644)
	oldT := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(old, oldT, oldT)
	res, err = timeline.ApplyRetention(root2, timeline.RetentionPolicy{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "old" {
		t.Fatalf("age deleted = %v", res.Deleted)
	}

	// MaxBytes: three equal files, cap forces eviction.
	root3 := t.TempDir()
	for i := 0; i < 3; i++ {
		name := "b" + itoa(i)
		p := filepath.Join(root3, name)
		if err := os.WriteFile(p, []byte(strings.Repeat("y", 1000)), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-time.Duration(3-i) * time.Minute)
		_ = os.Chtimes(p, mt, mt)
	}
	res, err = timeline.ApplyRetention(root3, timeline.RetentionPolicy{MaxBytes: 1500})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) == 0 {
		t.Fatal("expected MaxBytes deletions")
	}
}

func TestRetentionFromConfig(t *testing.T) {
	p := timeline.RetentionFromConfig(10, 7, 1024)
	if p.MaxFiles != 10 || p.MaxBytes != 1024 || p.MaxAge != 7*24*time.Hour {
		t.Fatalf("%+v", p)
	}
	if !p.Active() {
		t.Fatal("active")
	}
	if timeline.RetentionFromConfig(0, 0, 0).Active() {
		t.Fatal("zero inactive")
	}
}

func TestSessionBlobDirAndDefaultTracesDir(t *testing.T) {
	d := timeline.SessionBlobDir("/tmp/traces", "sess/../evil")
	// Sanitized to a single path segment (slashes → _); must not nest via separators.
	base := filepath.Base(filepath.Dir(d)) // session id segment
	if strings.Contains(base, string(filepath.Separator)) || base == ".." || base == "." {
		t.Fatalf("unsafe session segment in %s (base=%q)", d, base)
	}
	if !strings.HasSuffix(d, "blobs") {
		t.Fatalf("blob dir = %s", d)
	}
	if timeline.DefaultTracesDir() == "" {
		t.Fatal("empty default traces dir")
	}
}

func TestObserveBudgetUnderNEvents(t *testing.T) {
	// Acceptance: timeline append stays within budget under N events.
	// Structural bounds always apply; wall budgets are generous enough for
	// -race / loaded CI while still catching pathological regressions
	// (accidental fsync-per-observe or O(n^2) prune).
	const n = 2000
	maxAvgNs := int64(5_000_000)        // 5ms average per Observe
	maxTotalNs := int64(30_000_000_000) // 30s total (race detector headroom)
	maxWall := 45 * time.Second

	b := timeline.NewBuilder(timeline.Options{
		SessionID:        "bench-sess",
		MaxEntries:       500, // exercise prune path
		ArgsPreviewMax:   128,
		OutputPreviewMax: 256,
		// No BlobDir: measure pure in-memory path (export/spill is async/off-loop).
	})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	start := time.Now()
	for i := 0; i < n; i++ {
		corr := protocol.Correlation{SessionID: "bench-sess", TurnID: "t-" + itoa(i)}
		t0 := base.Add(time.Duration(i) * time.Millisecond)
		b.Observe(protocol.TurnStarted{Correlation: corr}, t0)
		b.Observe(protocol.ToolCallBegin{
			Correlation: corr,
			CallID:      "c-" + itoa(i),
			Name:        "bash",
			Args:        []byte(`{"command":"echo ` + strings.Repeat("a", 400) + `"}`),
		}, t0.Add(time.Microsecond))
		b.Observe(protocol.ToolCallEnd{
			Correlation: corr,
			CallID:      "c-" + itoa(i),
			Output:      strings.Repeat("out-", 200),
		}, t0.Add(2*time.Microsecond))
		b.Observe(protocol.TurnCompleted{Correlation: corr}, t0.Add(3*time.Microsecond))
	}
	wall := time.Since(start)
	m := b.Metrics()
	if m.Observes != int64(n*4) {
		t.Fatalf("observes = %d, want %d", m.Observes, n*4)
	}
	if m.Entries > 500 {
		t.Fatalf("entries = %d, want <= 500 (bounded)", m.Entries)
	}
	if m.Truncations == 0 {
		t.Fatal("expected truncations for oversized previews")
	}
	if m.Pruned == 0 {
		t.Fatal("expected prune under MaxEntries")
	}
	if m.ObserveNanos <= 0 || m.LastObserveNs <= 0 {
		t.Fatalf("expected latency metrics: %+v", m)
	}
	if m.AvgObserveNs() > maxAvgNs {
		t.Fatalf("avg Observe %dns > budget %dns (total metrics %dns, wall %s)",
			m.AvgObserveNs(), maxAvgNs, m.ObserveNanos, wall)
	}
	if m.ObserveNanos > maxTotalNs {
		t.Fatalf("total Observe %dns > budget %dns", m.ObserveNanos, maxTotalNs)
	}
	if wall > maxWall {
		t.Fatalf("wall %s > budget %s", wall, maxWall)
	}
	t.Logf("N=%d observes=%d entries=%d pruned=%d trunc=%d avgNs=%d wall=%s",
		n, m.Observes, m.Entries, m.Pruned, m.Truncations, m.AvgObserveNs(), wall)
}

func BenchmarkObserve(b *testing.B) {
	builder := timeline.NewBuilder(timeline.Options{
		SessionID:  "bench",
		MaxEntries: 1000,
	})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		corr := protocol.Correlation{SessionID: "bench", TurnID: "t-" + itoa(i)}
		t0 := base.Add(time.Duration(i) * time.Microsecond)
		builder.Observe(protocol.TurnStarted{Correlation: corr}, t0)
		builder.Observe(protocol.ToolCallBegin{
			Correlation: corr, CallID: "c-" + itoa(i), Name: "read",
			Args: []byte(`{}`),
		}, t0)
		builder.Observe(protocol.ToolCallEnd{
			Correlation: corr, CallID: "c-" + itoa(i), Output: "ok",
		}, t0)
		builder.Observe(protocol.TurnCompleted{Correlation: corr}, t0)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
