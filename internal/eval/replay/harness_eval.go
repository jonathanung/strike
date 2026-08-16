package replay

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

// Harness eval themes (#807). Compose with E3 runners (#459); do not replace
// SWE-bench / Terminal-Bench ownership (#561/#562). Soft-related: failure
// injection (#808) is out of scope here.
const (
	ThemeCorrectness = "correctness"
	ThemeSafety      = "safety"
	ThemeRecovery    = "recovery"
	ThemeLatencyCost = "latency_cost"
	ThemeRecording   = "recording" // #791/#782 consumption
)

// EvalStatus is the outcome of one harness regression scenario.
type EvalStatus string

const (
	EvalPass EvalStatus = "pass"
	EvalFail EvalStatus = "fail"
	EvalSkip EvalStatus = "skip"
)

// EvalResult is one scenario row in the harness regression report.
type EvalResult struct {
	Name     string        `json:"name"`
	Theme    string        `json:"theme"`
	Status   EvalStatus    `json:"status"`
	Detail   string        `json:"detail,omitempty"`
	Duration time.Duration `json:"durationNs"`
}

// EvalReport is the versioned harness regression artifact (#807).
// Written under testdata/ or a CI path for non-blocking tracking.
type EvalReport struct {
	SchemaVersion string       `json:"schemaVersion"`
	GeneratedAt   time.Time    `json:"generatedAt"`
	Passed        int          `json:"passed"`
	Failed        int          `json:"failed"`
	Skipped       int          `json:"skipped"`
	Results       []EvalResult `json:"results"`
}

// EvalReportSchemaVersion bumps when the report document shape changes.
const EvalReportSchemaVersion = "1.0.0"

// BuildEvalReport aggregates scenario results into a stable report document.
func BuildEvalReport(results []EvalResult, now time.Time) EvalReport {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rep := EvalReport{
		SchemaVersion: EvalReportSchemaVersion,
		GeneratedAt:   now.UTC(),
		Results:       append([]EvalResult(nil), results...),
	}
	// Stable order: theme then name.
	sort.SliceStable(rep.Results, func(i, j int) bool {
		if rep.Results[i].Theme != rep.Results[j].Theme {
			return rep.Results[i].Theme < rep.Results[j].Theme
		}
		return rep.Results[i].Name < rep.Results[j].Name
	})
	for _, r := range rep.Results {
		switch r.Status {
		case EvalPass:
			rep.Passed++
		case EvalFail:
			rep.Failed++
		case EvalSkip:
			rep.Skipped++
		}
	}
	return rep
}

// FormatEvalReport renders a human-readable table for CI logs / make harness-eval.
func FormatEvalReport(rep EvalReport) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "harness eval report (#807) schema=%s generated=%s\n",
		rep.SchemaVersion, rep.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&buf, "summary: pass=%d fail=%d skip=%d total=%d\n",
		rep.Passed, rep.Failed, rep.Skipped, len(rep.Results))
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "theme\tstatus\tname\tduration\tdetail")
	for _, r := range rep.Results {
		detail := strings.ReplaceAll(r.Detail, "\n", " ")
		if len(detail) > 80 {
			detail = detail[:77] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.Theme, r.Status, r.Name, r.Duration.Round(time.Microsecond), detail)
	}
	_ = w.Flush()
	return buf.String()
}

// WriteEvalReport persists the report as indented JSON (CI artifact path).
func WriteEvalReport(path string, rep EvalReport) error {
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

// LoadEvalReport reads a previously written harness eval report.
func LoadEvalReport(path string) (EvalReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return EvalReport{}, err
	}
	var rep EvalReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return EvalReport{}, fmt.Errorf("replay: eval report %s: %w", path, err)
	}
	return rep, nil
}

// ThemesCovered returns the sorted unique themes present in results.
func ThemesCovered(results []EvalResult) []string {
	seen := map[string]struct{}{}
	for _, r := range results {
		if r.Theme != "" {
			seen[r.Theme] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
