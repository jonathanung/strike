package tbench

import "time"

// ReportSchemaVersion bumps when the result document shape changes.
const ReportSchemaVersion = "1.0.0"

// DefaultSubsetSize is the fixed E3.4 subset cardinality.
const DefaultSubsetSize = 25

// BenchmarkName is the report.benchmark field value.
const BenchmarkName = "terminal-bench-2-subset"

// DatasetRepo is the upstream Terminal-Bench 2 task pack (Harbor format).
const DatasetRepo = "https://github.com/harbor-framework/terminal-bench-2"

// DatasetPin documents the image tag / pack revision this subset targets.
// Prebuilt images use the 20251031 tag (see task.toml environment.docker_image).
const DatasetPin = "terminal-bench-2@20251031"

// WorkDirInContainer is the Harbor/TB agent working directory.
const WorkDirInContainer = "/app"

// Instance is one Terminal-Bench task (fields needed to run + grade).
type Instance struct {
	InstanceID    string  `json:"instance_id"`
	Instruction   string  `json:"instruction"`
	DockerImage   string  `json:"docker_image,omitempty"`
	Category      string  `json:"category,omitempty"`
	Difficulty    string  `json:"difficulty,omitempty"`
	AgentTimeout  float64 `json:"agent_timeout_sec,omitempty"`
	VerifyTimeout float64 `json:"verifier_timeout_sec,omitempty"`
	// TaskDir is the local path to the Harbor task folder (instruction.md,
	// task.toml, tests/). Required for docker grading (needs tests/).
	TaskDir string `json:"task_dir,omitempty"`
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
	InstanceID  string         `json:"instanceId"`
	Category    string         `json:"category,omitempty"`
	Difficulty  string         `json:"difficulty,omitempty"`
	Status      InstanceStatus `json:"status"`
	Resolved    *bool          `json:"resolved,omitempty"`
	Reward      float64        `json:"reward,omitempty"`
	TokensIn    int            `json:"tokensIn,omitempty"`
	TokensOut   int            `json:"tokensOut,omitempty"`
	TokensCache int            `json:"tokensCache,omitempty"`
	CostUSD     float64        `json:"costUsd,omitempty"`
	WallClockMs int64          `json:"wallClockMs"`
	AgentMs     int64          `json:"agentMs,omitempty"`
	GradeMs     int64          `json:"gradeMs,omitempty"`
	Provider    string         `json:"provider,omitempty"`
	Model       string         `json:"model,omitempty"`
	SessionID   string         `json:"sessionId,omitempty"`
	Image       string         `json:"image,omitempty"`
	Error       string         `json:"error,omitempty"`
	GradeDetail string         `json:"gradeDetail,omitempty"`
	Usage       *Usage         `json:"usage,omitempty"`
	StartedAt   time.Time      `json:"startedAt,omitempty"`
	FinishedAt  time.Time      `json:"finishedAt,omitempty"`
}

// Report is the versioned Terminal-Bench subset run artifact (#562).
type Report struct {
	SchemaVersion  string           `json:"schemaVersion"`
	Benchmark      string           `json:"benchmark"`
	DatasetPin     string           `json:"datasetPin,omitempty"`
	SubsetSize     int              `json:"subsetSize"`
	RunID          string           `json:"runId"`
	GeneratedAt    time.Time        `json:"generatedAt"`
	Provider       string           `json:"provider,omitempty"`
	Model          string           `json:"model,omitempty"`
	Grader         string           `json:"grader,omitempty"`
	StrikeVersion  string           `json:"strikeVersion,omitempty"`
	Attempted      int              `json:"attempted"`
	Resolved       int              `json:"resolved"`
	Unresolved     int              `json:"unresolved"`
	Errors         int              `json:"errors"`
	Skipped        int              `json:"skipped"`
	PassRate       float64          `json:"passRate"`
	TotalTokensIn  int              `json:"totalTokensIn"`
	TotalTokensOut int              `json:"totalTokensOut"`
	TotalCostUSD   float64          `json:"totalCostUsd"`
	TotalWallMs    int64            `json:"totalWallMs"`
	Note           string           `json:"note"`
	Results        []InstanceResult `json:"results"`
}

// ReportNote is embedded in every report document.
const ReportNote = "Internal regression signal only. Do not publish pass rates in product README."
