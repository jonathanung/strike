package progressive

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
)

// Schema version for progressive disclosure comparison reports.
const ReportSchemaVersion = "1.0.0"

// Rollback thresholds governing the permanent default (#992).
// Exceeding either criterion warrants deferTools=off until investigated.
const (
	// RollbackCompletionDelta is the max allowed absolute drop in completion
	// rate (progressive − full). Values are 0–1 fractions.
	RollbackCompletionDelta = 0.05
	// RollbackWallTimeRatio is the max allowed relative wall-time increase
	// (progressive/full − 1). 0.25 = +25%.
	RollbackWallTimeRatio = 0.25
	// MinSchemaReductionRatio is the expected minimum first-turn schema
	// token reduction (1 − progressive/full). Below this is a soft warning.
	MinSchemaReductionRatio = 0.30
)

// Mode labels for A/B points.
const (
	ModeFull        = "full"        // deferTools=off
	ModeProgressive = "progressive" // deferTools=on (default)
)

// FixtureKind classifies representative workloads.
type FixtureKind string

const (
	FixtureSolo       FixtureKind = "solo"
	FixturePlan       FixtureKind = "plan"
	FixtureMultiAgent FixtureKind = "multi-agent"
)

// PointMetrics is one mode's measured surface for a fixture.
type PointMetrics struct {
	Mode string `json:"mode"`

	// FirstTurnToolCount is how many tools appear on the first provider request.
	FirstTurnToolCount int `json:"firstTurnToolCount"`
	// FirstTurnSchemaChars estimates schema payload size (name+desc+schema JSON).
	FirstTurnSchemaChars int `json:"firstTurnSchemaChars"`
	// FirstTurnSchemaTokens is a rough token estimate (chars/4).
	FirstTurnSchemaTokens int `json:"firstTurnSchemaTokens"`

	// ToolSearchCalls counts toolsearch invocations during the fixture.
	ToolSearchCalls int `json:"toolSearchCalls"`
	// InvalidToolCalls counts tool results settled as errors.
	InvalidToolCalls int `json:"invalidToolCalls"`
	// RedundantToolCalls counts repeated identical failing calls (loop-ish).
	RedundantToolCalls int `json:"redundantToolCalls"`
	// TotalToolCalls is all tool invocations observed.
	TotalToolCalls int `json:"totalToolCalls"`

	// Completed is true when the fixture reached its success predicate.
	Completed bool `json:"completed"`
	// WallTimeMs is fixture wall clock.
	WallTimeMs int64 `json:"wallTimeMs"`

	// TaskSchemaAdvanced is whether task exposed the advanced schema on first turn.
	TaskSchemaAdvanced bool `json:"taskSchemaAdvanced,omitempty"`
	// CompatToolsCallable is true when legacy shims remain registered+executable.
	CompatToolsCallable bool `json:"compatToolsCallable,omitempty"`
}

// FixtureResult is one fixture under both modes.
type FixtureResult struct {
	Name        string       `json:"name"`
	Kind        FixtureKind  `json:"kind"`
	Full        PointMetrics `json:"full"`
	Progressive PointMetrics `json:"progressive"`
	// SchemaReduction is 1 - progressive.tokens/full.tokens (first-turn).
	SchemaReduction float64 `json:"schemaReduction"`
	// Notes hold human-readable observations.
	Notes []string `json:"notes,omitempty"`
	Error string   `json:"error,omitempty"`
}

// Report is the versioned progressive disclosure comparison artifact.
type Report struct {
	SchemaVersion string          `json:"schemaVersion"`
	GeneratedAt   time.Time       `json:"generatedAt"`
	Note          string          `json:"note"`
	Rollback      RollbackPolicy  `json:"rollback"`
	Fixtures      []FixtureResult `json:"fixtures"`
	// Pass is true when all fixtures completed under progressive and rollback
	// thresholds are not breached.
	Pass bool `json:"pass"`
}

// RollbackPolicy documents the permanent-default gate.
type RollbackPolicy struct {
	CompletionDelta    float64 `json:"completionDelta"`
	WallTimeRatio      float64 `json:"wallTimeRatio"`
	MinSchemaReduction float64 `json:"minSchemaReduction"`
	Guidance           string  `json:"guidance"`
}

// DefaultRollbackPolicy returns the shipped thresholds.
func DefaultRollbackPolicy() RollbackPolicy {
	return RollbackPolicy{
		CompletionDelta:    RollbackCompletionDelta,
		WallTimeRatio:      RollbackWallTimeRatio,
		MinSchemaReduction: MinSchemaReductionRatio,
		Guidance: "If progressive completion drops by more than completionDelta " +
			"absolute, or median wall time rises by more than wallTimeRatio relative " +
			"vs full exposure, set deferTools=off until investigated. Schema reduction " +
			"below minSchemaReduction is a soft warning, not an automatic rollback.",
	}
}

// EstimateSchemaChars sums name + description + input schema JSON lengths.
func EstimateSchemaChars(schemas []provider.ToolSchema) int {
	n := 0
	for _, s := range schemas {
		n += utf8.RuneCountInString(s.Name)
		n += utf8.RuneCountInString(s.Description)
		n += len(s.InputSchema)
	}
	return n
}

// EstimateTokens is a rough chars/4 estimate used for A/B comparison only.
func EstimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// MeasureFirstTurn captures first-turn provider tool surface from a registry.
func MeasureFirstTurn(reg *tool.Registry, mode string) PointMetrics {
	m := PointMetrics{Mode: mode, CompatToolsCallable: true}
	if reg == nil {
		return m
	}
	schemas := reg.SchemasForProvider()
	m.FirstTurnToolCount = len(schemas)
	m.FirstTurnSchemaChars = EstimateSchemaChars(schemas)
	m.FirstTurnSchemaTokens = EstimateTokens(m.FirstTurnSchemaChars)
	for _, s := range schemas {
		if s.Name == "task" {
			m.TaskSchemaAdvanced = strings.Contains(string(s.InputSchema), `"transition"`)
		}
	}
	return m
}

// SchemaReductionRatio returns 1 - prog/full for first-turn schema tokens.
func SchemaReductionRatio(full, prog PointMetrics) float64 {
	if full.FirstTurnSchemaTokens <= 0 {
		return 0
	}
	r := 1 - float64(prog.FirstTurnSchemaTokens)/float64(full.FirstTurnSchemaTokens)
	if r < 0 {
		return 0
	}
	return r
}

// EvaluateRollback returns notes and whether progressive should roll back.
func EvaluateRollback(fixtures []FixtureResult, pol RollbackPolicy) (pass bool, notes []string) {
	pass = true
	var fullDone, progDone int
	var fullWall, progWall int64
	var n int
	for _, f := range fixtures {
		if f.Error != "" {
			pass = false
			notes = append(notes, fmt.Sprintf("%s: error %s", f.Name, f.Error))
			continue
		}
		if f.Full.Completed {
			fullDone++
		}
		if f.Progressive.Completed {
			progDone++
		} else {
			pass = false
			notes = append(notes, fmt.Sprintf("%s: progressive did not complete", f.Name))
		}
		fullWall += f.Full.WallTimeMs
		progWall += f.Progressive.WallTimeMs
		n++
		if f.SchemaReduction < pol.MinSchemaReduction {
			notes = append(notes, fmt.Sprintf("%s: schema reduction %.2f below soft min %.2f",
				f.Name, f.SchemaReduction, pol.MinSchemaReduction))
		}
	}
	if n == 0 {
		return false, append(notes, "no fixtures")
	}
	fullRate := float64(fullDone) / float64(n)
	progRate := float64(progDone) / float64(n)
	if fullRate-progRate > pol.CompletionDelta {
		pass = false
		notes = append(notes, fmt.Sprintf("completion regression: full=%.2f progressive=%.2f delta=%.2f > %.2f",
			fullRate, progRate, fullRate-progRate, pol.CompletionDelta))
	}
	if fullWall > 0 {
		ratio := float64(progWall)/float64(fullWall) - 1
		if ratio > pol.WallTimeRatio {
			pass = false
			notes = append(notes, fmt.Sprintf("wall-time regression: ratio=+%.2f > %.2f", ratio, pol.WallTimeRatio))
		}
	}
	return pass, notes
}

// CompatToolNames are legacy shims that must remain registered.
var CompatToolNames = []string{
	"delegate", "task_status", "task_read", "task_message", "task_interrupt", "wait",
}

// AssertCompatRegistered checks legacy tools exist on the registry.
func AssertCompatRegistered(reg *tool.Registry) error {
	if reg == nil {
		return fmt.Errorf("nil registry")
	}
	var missing []string
	for _, name := range CompatToolNames {
		if _, ok := reg.Get(name); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("compat tools missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

// FormatReport renders a human-readable comparison table.
func FormatReport(rep Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "progressive disclosure report schema=%s pass=%v\n", rep.SchemaVersion, rep.Pass)
	fmt.Fprintf(&b, "rollback: completionΔ≤%.2f wall≤+%.0f%% schema soft≥%.0f%%\n",
		rep.Rollback.CompletionDelta, rep.Rollback.WallTimeRatio*100, rep.Rollback.MinSchemaReduction*100)
	for _, f := range rep.Fixtures {
		fmt.Fprintf(&b, "\n[%s] %s kind=%s\n", f.Kind, f.Name, f.Kind)
		fmt.Fprintf(&b, "  full:        tools=%d schemaTok=%d completed=%v wallMs=%d\n",
			f.Full.FirstTurnToolCount, f.Full.FirstTurnSchemaTokens, f.Full.Completed, f.Full.WallTimeMs)
		fmt.Fprintf(&b, "  progressive: tools=%d schemaTok=%d completed=%v wallMs=%d toolsearch=%d\n",
			f.Progressive.FirstTurnToolCount, f.Progressive.FirstTurnSchemaTokens,
			f.Progressive.Completed, f.Progressive.WallTimeMs, f.Progressive.ToolSearchCalls)
		fmt.Fprintf(&b, "  schemaReduction=%.1f%%\n", f.SchemaReduction*100)
		for _, n := range f.Notes {
			fmt.Fprintf(&b, "  note: %s\n", n)
		}
		if f.Error != "" {
			fmt.Fprintf(&b, "  ERROR: %s\n", f.Error)
		}
	}
	fmt.Fprintf(&b, "\n%s\n", rep.Note)
	return b.String()
}

// MustJSON is a test helper for stable marshaling checks.
func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
