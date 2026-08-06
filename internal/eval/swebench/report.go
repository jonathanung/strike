package swebench

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

// BuildReport aggregates instance results into a stable report document.
func BuildReport(runID string, results []InstanceResult, meta ReportMeta, now time.Time) Report {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rep := Report{
		SchemaVersion: ReportSchemaVersion,
		Benchmark:     "swe-bench-verified-subset",
		SubsetSize:    DefaultSubsetSize,
		RunID:         runID,
		GeneratedAt:   now.UTC(),
		Provider:      meta.Provider,
		Model:         meta.Model,
		Grader:        meta.Grader,
		StrikeVersion: meta.StrikeVersion,
		Note:          ReportNote,
		Results:       append([]InstanceResult(nil), results...),
	}
	sort.SliceStable(rep.Results, func(i, j int) bool {
		return rep.Results[i].InstanceID < rep.Results[j].InstanceID
	})

	var graded int
	for _, r := range rep.Results {
		rep.TotalTokensIn += r.TokensIn
		rep.TotalTokensOut += r.TokensOut
		rep.TotalCostUSD += r.CostUSD
		rep.TotalWallMs += r.WallClockMs
		switch r.Status {
		case StatusResolved:
			rep.Resolved++
			rep.Attempted++
			graded++
		case StatusUnresolved:
			rep.Unresolved++
			rep.Attempted++
			graded++
		case StatusError:
			rep.Errors++
			rep.Attempted++
		case StatusSkipped:
			rep.Skipped++
		default:
			rep.Attempted++
		}
	}
	if graded > 0 {
		rep.PassRate = float64(rep.Resolved) / float64(graded)
	}
	return rep
}

// ReportMeta is run-level metadata for BuildReport.
type ReportMeta struct {
	Provider      string
	Model         string
	Grader        string
	StrikeVersion string
}

// WriteReport persists the report as indented JSON.
func WriteReport(path string, rep Report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
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

// LoadReport reads a previously written report.
func LoadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return Report{}, fmt.Errorf("swebench: report %s: %w", path, err)
	}
	return rep, nil
}

// WritePredictionsJSONL writes SWE-bench harness predictions (one JSON object per line).
func WritePredictionsJSONL(path string, preds []Prediction) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, p := range preds {
		if err := enc.Encode(p); err != nil {
			return err
		}
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// FormatReport renders a human-readable summary table.
func FormatReport(rep Report) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "SWE-bench Verified subset report (#561) schema=%s run=%s\n",
		rep.SchemaVersion, rep.RunID)
	fmt.Fprintf(&buf, "provider=%s model=%s grader=%s generated=%s\n",
		rep.Provider, rep.Model, rep.Grader, rep.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&buf, "summary: resolved=%d unresolved=%d error=%d skip=%d attempted=%d pass_rate=%.1f%%\n",
		rep.Resolved, rep.Unresolved, rep.Errors, rep.Skipped, rep.Attempted, rep.PassRate*100)
	fmt.Fprintf(&buf, "tokens: in=%d out=%d cost_usd=%.4f wall_ms=%d\n",
		rep.TotalTokensIn, rep.TotalTokensOut, rep.TotalCostUSD, rep.TotalWallMs)
	fmt.Fprintf(&buf, "note: %s\n", rep.Note)
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "status\tinstance\twall_ms\ttok_in\ttok_out\tcost\tdetail")
	for _, r := range rep.Results {
		detail := r.Error
		if detail == "" {
			detail = r.GradeDetail
		}
		detail = strings.ReplaceAll(detail, "\n", " ")
		if len(detail) > 60 {
			detail = detail[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%.4f\t%s\n",
			r.Status, r.InstanceID, r.WallClockMs, r.TokensIn, r.TokensOut, r.CostUSD, detail)
	}
	_ = w.Flush()
	return buf.String()
}
