package sweep

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// ReportSchemaVersion bumps when the summary document shape changes.
const ReportSchemaVersion = "1.0.0"

// Benchmark names accepted by the sweep CLI / summary.
const (
	BenchmarkSWEBench = "swebench"
	BenchmarkTBench   = "tbench"
)

// ReportNote is embedded in every summary document.
const ReportNote = "Internal regression signal only. Do not publish pass rates in product README."

// PointMetrics is the comparable slice of an underlying eval report.
type PointMetrics struct {
	Attempted      int     `json:"attempted"`
	Resolved       int     `json:"resolved"`
	Unresolved     int     `json:"unresolved"`
	Errors         int     `json:"errors"`
	Skipped        int     `json:"skipped"`
	PassRate       float64 `json:"passRate"`
	TotalTokensIn  int     `json:"totalTokensIn"`
	TotalTokensOut int     `json:"totalTokensOut"`
	TotalCostUSD   float64 `json:"totalCostUsd"`
	TotalWallMs    int64   `json:"totalWallMs"`
}

// PointResult is one matrix point after a subset run.
type PointResult struct {
	Point      Point        `json:"point"`
	Metrics    PointMetrics `json:"metrics"`
	ReportPath string       `json:"reportPath,omitempty"`
	OutDir     string       `json:"outDir,omitempty"`
	Error      string       `json:"error,omitempty"`
	// DurationMs is wall time for this point's full subset run.
	DurationMs int64 `json:"durationMs,omitempty"`
}

// Summary is the versioned sweep comparison artifact (#563).
type Summary struct {
	SchemaVersion string        `json:"schemaVersion"`
	Benchmark     string        `json:"benchmark"`
	Matrix        string        `json:"matrix"`
	RunID         string        `json:"runId"`
	GeneratedAt   time.Time     `json:"generatedAt"`
	Provider      string        `json:"provider,omitempty"`
	Model         string        `json:"model,omitempty"`
	Grader        string        `json:"grader,omitempty"`
	StrikeVersion string        `json:"strikeVersion,omitempty"`
	Limit         int           `json:"limit,omitempty"`
	DryRun        bool          `json:"dryRun,omitempty"`
	Note          string        `json:"note"`
	Points        []PointResult `json:"points"`
}

// SummaryMeta is run-level metadata for BuildSummary.
type SummaryMeta struct {
	Benchmark     string
	Matrix        string
	Provider      string
	Model         string
	Grader        string
	StrikeVersion string
	Limit         int
	DryRun        bool
}

// BuildSummary assembles a stable summary document.
func BuildSummary(runID string, points []PointResult, meta SummaryMeta, now time.Time) Summary {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := Summary{
		SchemaVersion: ReportSchemaVersion,
		Benchmark:     meta.Benchmark,
		Matrix:        meta.Matrix,
		RunID:         runID,
		GeneratedAt:   now.UTC(),
		Provider:      meta.Provider,
		Model:         meta.Model,
		Grader:        meta.Grader,
		StrikeVersion: meta.StrikeVersion,
		Limit:         meta.Limit,
		DryRun:        meta.DryRun,
		Note:          ReportNote,
		Points:        append([]PointResult(nil), points...),
	}
	sort.SliceStable(out.Points, func(i, j int) bool {
		return out.Points[i].Point.ID < out.Points[j].Point.ID
	})
	return out
}

// WriteSummary persists the summary as indented JSON.
func WriteSummary(path string, sum Summary) error {
	data, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.Write(data)
	buf.WriteByte('\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// LoadSummary reads a previously written summary.
func LoadSummary(path string) (Summary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Summary{}, err
	}
	var sum Summary
	if err := json.Unmarshal(data, &sum); err != nil {
		return Summary{}, fmt.Errorf("sweep: summary %s: %w", path, err)
	}
	return sum, nil
}

// FormatSummary returns a compact human-readable comparison table.
func FormatSummary(sum Summary) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "Parameter sweep %s  matrix=%s  benchmark=%s\n", sum.RunID, sum.Matrix, sum.Benchmark)
	if sum.Provider != "" || sum.Model != "" {
		fmt.Fprintf(&buf, "provider=%s model=%s grader=%s\n", sum.Provider, sum.Model, sum.Grader)
	}
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "POINT\tPASS\tRES/ATT\tTOK_IN\tTOK_OUT\tCOST_USD\tWALL_MS\tERR")
	for _, p := range sum.Points {
		errMark := ""
		if p.Error != "" {
			errMark = "yes"
		}
		m := p.Metrics
		fmt.Fprintf(tw, "%s\t%.3f\t%d/%d\t%d\t%d\t%.4f\t%d\t%s\n",
			p.Point.ID,
			m.PassRate,
			m.Resolved, m.Attempted,
			m.TotalTokensIn, m.TotalTokensOut,
			m.TotalCostUSD,
			m.TotalWallMs,
			errMark,
		)
	}
	_ = tw.Flush()
	fmt.Fprintf(&buf, "\n%s\n", sum.Note)
	return buf.String()
}

// DefaultRunID formats a UTC timestamp run label.
func DefaultRunID(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format("20060102T150405Z")
}

// MetricsFromAggregate maps common eval report fields onto PointMetrics.
func MetricsFromAggregate(attempted, resolved, unresolved, errors, skipped int, passRate float64, tokIn, tokOut int, cost float64, wallMs int64) PointMetrics {
	return PointMetrics{
		Attempted:      attempted,
		Resolved:       resolved,
		Unresolved:     unresolved,
		Errors:         errors,
		Skipped:        skipped,
		PassRate:       passRate,
		TotalTokensIn:  tokIn,
		TotalTokensOut: tokOut,
		TotalCostUSD:   cost,
		TotalWallMs:    wallMs,
	}
}
