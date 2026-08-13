package swebench

import (
	"encoding/json"
	"time"
)

// ReportSchemaVersion bumps when the result document shape changes.
const ReportSchemaVersion = "1.0.0"

// DefaultSubsetSize is the fixed E3.3 subset cardinality.
const DefaultSubsetSize = 50

// DatasetName is the HuggingFace dataset id for SWE-bench Verified.
const DatasetName = "SWE-bench/SWE-bench_Verified"

// Instance is one SWE-bench task (fields needed to run + grade).
type Instance struct {
	InstanceID       string `json:"instance_id"`
	Repo             string `json:"repo"`
	BaseCommit       string `json:"base_commit"`
	Version          string `json:"version,omitempty"`
	ProblemStatement string `json:"problem_statement"`
	// TestPatch adds the FAIL_TO_PASS tests (and related fixtures). Must be
	// applied before grading; the agent does not see it during the run.
	TestPatch  string   `json:"test_patch,omitempty"`
	// EvalScript is the official SWE-bench eval.sh (conda, test_patch, test cmd).
	// When present the docker grader runs it instead of a reconstructed command.
	EvalScript string   `json:"eval_script,omitempty"`
	FailToPass []string `json:"FAIL_TO_PASS"`
	PassToPass []string `json:"PASS_TO_PASS"`
}

// UnmarshalJSON accepts FAIL_TO_PASS / PASS_TO_PASS as JSON arrays or as
// JSON-encoded strings (HuggingFace parquet / datasets-server shapes).
func (in *Instance) UnmarshalJSON(data []byte) error {
	type raw struct {
		InstanceID       string          `json:"instance_id"`
		Repo             string          `json:"repo"`
		BaseCommit       string          `json:"base_commit"`
		Version          string          `json:"version"`
		ProblemStatement string          `json:"problem_statement"`
		TestPatch        string          `json:"test_patch"`
		EvalScript       string          `json:"eval_script"`
		FailToPass       json.RawMessage `json:"FAIL_TO_PASS"`
		PassToPass       json.RawMessage `json:"PASS_TO_PASS"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	in.InstanceID = r.InstanceID
	in.Repo = r.Repo
	in.BaseCommit = r.BaseCommit
	in.Version = r.Version
	in.ProblemStatement = r.ProblemStatement
	in.TestPatch = r.TestPatch
	in.EvalScript = r.EvalScript
	var err error
	if in.FailToPass, err = parseStringList(r.FailToPass); err != nil {
		return err
	}
	if in.PassToPass, err = parseStringList(r.PassToPass); err != nil {
		return err
	}
	return nil
}

func parseStringList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var direct []string
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}
	if encoded == "" {
		return nil, nil
	}
	var nested []string
	if err := json.Unmarshal([]byte(encoded), &nested); err != nil {
		return nil, err
	}
	return nested, nil
}

// Usage is token usage from strike exec --json.
type Usage struct {
	Input         int `json:"input,omitempty"`
	Output        int `json:"output,omitempty"`
	CacheRead     int `json:"cacheRead,omitempty"`
	CacheCreation int `json:"cacheCreation,omitempty"`
}

// InstanceStatus is the outcome of one instance attempt.
type InstanceStatus string

const (
	StatusResolved   InstanceStatus = "resolved"
	StatusUnresolved InstanceStatus = "unresolved"
	StatusError      InstanceStatus = "error"
	StatusSkipped    InstanceStatus = "skipped"
)

// InstanceResult is one row in the run report.
type InstanceResult struct {
	InstanceID string         `json:"instanceId"`
	Repo       string         `json:"repo,omitempty"`
	Status     InstanceStatus `json:"status"`
	Resolved   *bool          `json:"resolved,omitempty"` // nil when not graded
	PatchBytes int            `json:"patchBytes"`
	// Patch is omitted from the aggregate report by default (large); written
	// beside the report as predictions JSONL instead.
	TokensIn     int       `json:"tokensIn,omitempty"`
	TokensOut    int       `json:"tokensOut,omitempty"`
	TokensCache  int       `json:"tokensCache,omitempty"`
	CostUSD      float64   `json:"costUsd,omitempty"`
	WallClockMs  int64     `json:"wallClockMs"`
	AgentMs      int64     `json:"agentMs,omitempty"`
	GradeMs      int64     `json:"gradeMs,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Model        string    `json:"model,omitempty"`
	SessionID    string    `json:"sessionId,omitempty"`
	Image        string    `json:"image,omitempty"`
	Error        string    `json:"error,omitempty"`
	GradeDetail  string    `json:"gradeDetail,omitempty"`
	FailToPassOK int       `json:"failToPassOk,omitempty"`
	FailToPassN  int       `json:"failToPassN,omitempty"`
	PassToPassOK int       `json:"passToPassOk,omitempty"`
	PassToPassN  int       `json:"passToPassN,omitempty"`
	Usage        *Usage    `json:"usage,omitempty"`
	StartedAt    time.Time `json:"startedAt,omitempty"`
	FinishedAt   time.Time `json:"finishedAt,omitempty"`
}

// Report is the versioned SWE-bench subset run artifact (#561).
type Report struct {
	SchemaVersion string    `json:"schemaVersion"`
	Benchmark     string    `json:"benchmark"` // "swe-bench-verified-subset"
	SubsetSize    int       `json:"subsetSize"`
	RunID         string    `json:"runId"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Provider      string    `json:"provider,omitempty"`
	Model         string    `json:"model,omitempty"`
	Grader        string    `json:"grader,omitempty"`
	StrikeVersion string    `json:"strikeVersion,omitempty"`
	// Summary aggregates.
	Attempted      int     `json:"attempted"`
	Resolved       int     `json:"resolved"`
	Unresolved     int     `json:"unresolved"`
	Errors         int     `json:"errors"`
	Skipped        int     `json:"skipped"`
	PassRate       float64 `json:"passRate"` // resolved/attempted graded; 0 if none graded
	TotalTokensIn  int     `json:"totalTokensIn"`
	TotalTokensOut int     `json:"totalTokensOut"`
	TotalCostUSD   float64 `json:"totalCostUsd"`
	TotalWallMs    int64   `json:"totalWallMs"`
	// Note is a fixed caveat for consumers (not for README marketing).
	Note    string           `json:"note"`
	Results []InstanceResult `json:"results"`
}

// ReportNote is embedded in every report document.
const ReportNote = "Internal regression signal only. Do not publish pass rates in product README (SWE-ABS caveat)."

// Prediction is one SWE-bench harness prediction row.
type Prediction struct {
	InstanceID      string `json:"instance_id"`
	ModelNameOrPath string `json:"model_name_or_path"`
	ModelPatch      string `json:"model_patch"`
}
