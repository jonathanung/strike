package sweep

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveMatrix(t *testing.T) {
	for _, name := range BuiltinMatrixNames() {
		m, err := ResolveMatrix(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("%s validate: %v", name, err)
		}
		if len(m) == 0 {
			t.Fatalf("%s empty", name)
		}
	}
	if _, err := ResolveMatrix("nope"); err == nil {
		t.Fatal("expected error")
	}
	all, _ := ResolveMatrix(MatrixAll)
	comp, _ := ResolveMatrix(MatrixCompaction)
	lean, _ := ResolveMatrix(MatrixLeanCode)
	deferT, _ := ResolveMatrix(MatrixDeferTools)
	eff, _ := ResolveMatrix(MatrixEffort)
	if len(all) != len(comp)+len(lean)+len(deferT)+len(eff) {
		t.Fatalf("all=%d want %d", len(all), len(comp)+len(lean)+len(deferT)+len(eff))
	}
}

func TestMatrixFilterByIDs(t *testing.T) {
	m, err := ResolveMatrix(MatrixLeanCode)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.FilterByIDs([]string{"leanCode-full", "leanCode-off"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "leanCode-off" || got[1].ID != "leanCode-full" {
		t.Fatalf("got %+v", got)
	}
	if _, err := m.FilterByIDs([]string{"missing"}); err == nil {
		t.Fatal("expected missing id error")
	}
}

func TestWriteProjectConfig(t *testing.T) {
	dir := t.TempDir()
	o := Overlay{
		LeanCode:            "full",
		DeferTools:          "on",
		CompactionThreshold: 0.55,
		PruneProtectTokens:  12000,
		PruneMinimumTokens:  6000,
		Effort:              "high",
	}
	if err := WriteProjectConfig(dir, o); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".strike", "config")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["leanCode"] != "full" || got["deferTools"] != "on" {
		t.Fatalf("got %v", got)
	}
	if got["compactionThreshold"].(float64) != 0.55 {
		t.Fatalf("threshold %v", got["compactionThreshold"])
	}
	// Zero overlay is a no-op.
	empty := t.TempDir()
	if err := WriteProjectConfig(empty, Overlay{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, ".strike", "config")); !os.IsNotExist(err) {
		t.Fatalf("expected no config file, err=%v", err)
	}
}

func TestWriteProjectConfigJSONRejectsInvalid(t *testing.T) {
	if err := WriteProjectConfigJSON(t.TempDir(), []byte(`not-json`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestSummaryRoundTrip(t *testing.T) {
	points := []PointResult{
		{
			Point:   Point{ID: "leanCode-full", Label: "leanCode=full", Group: "leanCode", Overlay: Overlay{LeanCode: "full"}},
			Metrics: MetricsFromAggregate(10, 4, 6, 0, 0, 0.4, 1000, 200, 1.25, 5000),
			OutDir:  "points/leanCode-full",
		},
		{
			Point:   Point{ID: "leanCode-off", Label: "leanCode=off", Group: "leanCode", Overlay: Overlay{LeanCode: "off"}},
			Metrics: MetricsFromAggregate(10, 3, 7, 0, 0, 0.3, 1200, 250, 1.5, 6000),
			Error:   "agent boom",
		},
	}
	sum := BuildSummary("run1", points, SummaryMeta{
		Benchmark: BenchmarkSWEBench,
		Matrix:    MatrixLeanCode,
		Provider:  "echo",
		Model:     "echo",
		Grader:    "none",
		DryRun:    true,
	}, time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))
	if sum.Points[0].Point.ID != "leanCode-full" && sum.Points[0].Point.ID != "leanCode-off" {
		t.Fatalf("unsorted? %+v", sum.Points)
	}
	// Sorted by id: leanCode-full before leanCode-off? "leanCode-full" < "leanCode-off"?
	// 'f' < 'o' so full first.
	if sum.Points[0].Point.ID != "leanCode-full" {
		t.Fatalf("sort: %s", sum.Points[0].Point.ID)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "summary.json")
	if err := WriteSummary(path, sum); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSummary(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "run1" || len(got.Points) != 2 {
		t.Fatalf("%+v", got)
	}
	text := FormatSummary(got)
	if !strings.Contains(text, "leanCode-full") || !strings.Contains(text, "PASS") {
		t.Fatalf("format: %s", text)
	}
}

func TestDefaultRunID(t *testing.T) {
	id := DefaultRunID(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if id != "20260102T030405Z" {
		t.Fatalf("got %s", id)
	}
}
