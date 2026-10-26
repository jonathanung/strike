// Package goal is a deterministic loop harness: state, budgets, hooks, and
// termination. The LLM (when used) only fills plan() and optionally judge
// evaluate slots — it never grades its own criteria or bypasses guards.
package goal

import (
	"fmt"
	"strings"
	"time"
)

// Status is the lifecycle of a stored goal.
type Status string

const (
	StatusPending Status = "pending"
	StatusActive  Status = "active"
	StatusPaused  Status = "paused"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	StatusAborted Status = "aborted"
	// StatusBlocked stops the loop and asks a person to continue.
	// Budget exhaustion stays StatusFailed: raising the cap is a new limit,
	// not a resume of the same one.
	StatusBlocked Status = "blocked"
)

// CheckKind is how a criterion is verified.
type CheckKind string

const (
	CheckCmd       CheckKind = "cmd"
	CheckPredicate CheckKind = "predicate"
	CheckJudge     CheckKind = "judge"
)

// CheckSpec is a tagged verification rule. Prefer cmd > predicate > judge.
type CheckSpec struct {
	Kind  CheckKind `json:"kind"`
	Value string    `json:"value"` // shell cmd, predicate name, or judge prompt
}

// Criterion is one falsifiable success condition.
type Criterion struct {
	Description string    `json:"description"`
	Check       CheckSpec `json:"check"`
	Satisfied   bool      `json:"satisfied"`
}

// Constraints are hard limits enforced by the harness (never the prompt).
type Constraints struct {
	MaxIterations      int     `json:"max_iterations"`
	MaxCostUSD         float64 `json:"max_cost_usd"`
	MaxWallClockS      int     `json:"max_wall_clock_s"`
	MaxNoProgressIters int     `json:"max_no_progress_iters"`
	// MaxTokens is a hard token cap. Zero means unlimited (goals stored
	// before the cap existed). New goals take DefaultConstraints.
	MaxTokens int `json:"max_tokens,omitempty"`
	// MaxVerifyFailures stops the loop after this many consecutive
	// unsatisfied critic passes, even when the state hash keeps changing.
	// Zero disables the guard (legacy records). New goals default to 3.
	MaxVerifyFailures int      `json:"max_verify_failures,omitempty"`
	AllowedTools      []string `json:"allowed_tools"`
	// DenyTools always beat AllowedTools. Autopilot does not skip them.
	DenyTools []string `json:"deny_tools,omitempty"`
}

// DefaultConstraints returns safe v1 defaults.
func DefaultConstraints() Constraints {
	return Constraints{
		MaxIterations:      25,
		MaxCostUSD:         5.0,
		MaxWallClockS:      1800,
		MaxNoProgressIters: 3,
		MaxTokens:          200_000,
		MaxVerifyFailures:  3,
		AllowedTools:       nil,
	}
}

// Goal is a durable goal record.
type Goal struct {
	ID          string      `json:"id"`
	Description string      `json:"description"`
	Criteria    []Criterion `json:"criteria"`
	Constraints Constraints `json:"constraints"`
	Status      Status      `json:"status"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	// ActiveStartedAt is set when status becomes active (for wall-clock guard).
	ActiveStartedAt time.Time `json:"active_started_at,omitempty"`
	// CostUSD accumulates action costs across iterations.
	CostUSD float64 `json:"cost_usd"`
	// LastIteration is the highest committed iteration number (0 = none).
	LastIteration int `json:"last_iteration"`
	// AbortRequested is set by /goal abort; guards flip status on next check.
	AbortRequested bool `json:"abort_requested,omitempty"`
	// FailReason is set when status becomes failed, blocked, or aborted.
	FailReason string `json:"fail_reason,omitempty"`
	// TokensUsed accumulates EstimateTokens across committed iterations.
	TokensUsed int `json:"tokens_used,omitempty"`
	// Autopilot allows the tool allowlist to run without an ask.
	// Deny rules and an empty allowlist still block every tool.
	Autopilot bool `json:"autopilot,omitempty"`
	// Escalation is "human" when the loop stopped and is waiting for a person.
	Escalation string `json:"escalation,omitempty"`
	// GuardFloor is the last iteration a person acknowledged. Stall and
	// verification streaks count only iterations after it, so Continue
	// does not immediately re-trip the same window.
	GuardFloor int `json:"guard_floor,omitempty"`
}

// ActionRecord is one tool invocation inside an iteration.
type ActionRecord struct {
	Index       int               `json:"index"`
	Tool        string            `json:"tool"`
	Args        map[string]string `json:"args,omitempty"`
	Result      string            `json:"result,omitempty"`
	OK          bool              `json:"ok"`
	Error       string            `json:"error,omitempty"`
	CostUSD     float64           `json:"cost_usd,omitempty"`
	Duration    time.Duration     `json:"duration_ms"` // wall duration; JSON via custom if needed
	Blocked     bool              `json:"blocked,omitempty"`
	BlockReason string            `json:"block_reason,omitempty"`
	// IntentKey is (goal_id, iter_n, action_idx) for idempotent resume.
	IntentKey string `json:"intent_key"`
	Completed bool   `json:"completed"`
}

// EvalRecord is the critic's verdict for one iteration.
type EvalRecord struct {
	AllSatisfied bool              `json:"all_satisfied"`
	Criteria     []CriterionResult `json:"criteria"`
	Notes        string            `json:"notes,omitempty"`
}

// CriterionResult is one criterion after critic evaluation.
type CriterionResult struct {
	Description string `json:"description"`
	Satisfied   bool   `json:"satisfied"`
	Evidence    string `json:"evidence,omitempty"`
	Error       string `json:"error,omitempty"`
}

// IterationRecord is one committed observe→plan→act→evaluate pass.
type IterationRecord struct {
	N                 int            `json:"n"`
	ObservationDigest string         `json:"observation_digest"`
	Plan              string         `json:"plan"`
	Actions           []ActionRecord `json:"actions"`
	Evaluation        EvalRecord     `json:"evaluation"`
	StateHash         string         `json:"state_hash"`
	CostUSD           float64        `json:"cost_usd"`
	Tokens            int            `json:"tokens,omitempty"`
	CommittedAt       time.Time      `json:"committed_at"`
}

// Action is a planned tool call before execution.
type Action struct {
	Tool string
	Args map[string]string
}

// Plan is the planner output for one iteration.
type Plan struct {
	Summary string
	Actions []Action
}

// Observation is harness-built context for the planner (never self-graded).
type Observation struct {
	GoalID      string
	Description string
	Status      Status
	Iteration   int
	Criteria    []Criterion
	LastEval    *EvalRecord
	Digest      string
	Scratch     string
}

// ParseCheckSpec parses "cmd: …", "predicate: …", or "judge: …".
func ParseCheckSpec(raw string) (CheckSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CheckSpec{}, fmt.Errorf("goal: empty check spec")
	}
	kind, value, ok := strings.Cut(raw, ":")
	if !ok {
		return CheckSpec{}, fmt.Errorf("goal: check spec must be cmd:, predicate:, or judge: (got %q)", raw)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	value = strings.TrimSpace(value)
	if value == "" {
		return CheckSpec{}, fmt.Errorf("goal: check spec %q has empty value", kind)
	}
	switch CheckKind(kind) {
	case CheckCmd, CheckPredicate, CheckJudge:
		return CheckSpec{Kind: CheckKind(kind), Value: value}, nil
	default:
		return CheckSpec{}, fmt.Errorf("goal: unknown check kind %q (want cmd, predicate, judge)", kind)
	}
}

// FormatCheckSpec renders a CheckSpec as the CLI form.
func FormatCheckSpec(c CheckSpec) string {
	return string(c.Kind) + ": " + c.Value
}

// ValidateGoal rejects empty descriptions and unfalsifiable criteria.
func ValidateGoal(description string, criteria []Criterion, c Constraints) error {
	description = strings.TrimSpace(description)
	if description == "" {
		return fmt.Errorf("goal: description is required")
	}
	if len(description) > 4*1024 {
		return fmt.Errorf("goal: description exceeds 4KiB")
	}
	if len(criteria) == 0 {
		return fmt.Errorf("goal: at least one criterion with a concrete check is required")
	}
	for i, cr := range criteria {
		if strings.TrimSpace(cr.Description) == "" && strings.TrimSpace(cr.Check.Value) == "" {
			return fmt.Errorf("goal: criterion %d is empty", i+1)
		}
		if err := validateCheck(cr.Check); err != nil {
			return fmt.Errorf("goal: criterion %d: %w", i+1, err)
		}
	}
	if err := validateConstraints(c); err != nil {
		return err
	}
	return nil
}

func validateCheck(c CheckSpec) error {
	switch c.Kind {
	case CheckCmd, CheckPredicate, CheckJudge:
		// ok
	case "":
		return fmt.Errorf("missing check kind (want cmd, predicate, or judge)")
	default:
		return fmt.Errorf("unknown check kind %q", c.Kind)
	}
	if strings.TrimSpace(c.Value) == "" {
		return fmt.Errorf("check value is empty (unfalsifiable)")
	}
	// Reject vague free-text that snuck in without a kind (already handled).
	return nil
}

func validateConstraints(c Constraints) error {
	if c.MaxIterations < 1 {
		return fmt.Errorf("goal: max_iterations must be >= 1")
	}
	if c.MaxIterations > 10_000 {
		return fmt.Errorf("goal: max_iterations too large")
	}
	if c.MaxCostUSD < 0 {
		return fmt.Errorf("goal: max_cost_usd must be >= 0")
	}
	if c.MaxWallClockS < 1 {
		return fmt.Errorf("goal: max_wall_clock_s must be >= 1")
	}
	if c.MaxNoProgressIters < 1 {
		return fmt.Errorf("goal: max_no_progress_iters must be >= 1")
	}
	if c.MaxTokens < 0 {
		return fmt.Errorf("goal: max_tokens must be >= 0")
	}
	if c.MaxVerifyFailures < 0 {
		return fmt.Errorf("goal: max_verify_failures must be >= 0")
	}
	for _, t := range c.AllowedTools {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("goal: allowed_tools contains an empty name")
		}
		if ToolDenied(t, c.DenyTools) {
			return fmt.Errorf("goal: tool %q is denied and allowed; deny wins", strings.TrimSpace(t))
		}
	}
	for _, t := range c.DenyTools {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("goal: deny_tools contains an empty name")
		}
	}
	return nil
}

// AllCriteriaSatisfied reports whether every criterion is marked satisfied.
func AllCriteriaSatisfied(criteria []Criterion) bool {
	if len(criteria) == 0 {
		return false
	}
	for _, c := range criteria {
		if !c.Satisfied {
			return false
		}
	}
	return true
}

// CloneGoal returns a deep copy safe for callers.
func CloneGoal(g Goal) Goal {
	out := g
	if g.Criteria != nil {
		out.Criteria = make([]Criterion, len(g.Criteria))
		copy(out.Criteria, g.Criteria)
	}
	if g.Constraints.AllowedTools != nil {
		out.Constraints.AllowedTools = append([]string(nil), g.Constraints.AllowedTools...)
	}
	if g.Constraints.DenyTools != nil {
		out.Constraints.DenyTools = append([]string(nil), g.Constraints.DenyTools...)
	}
	return out
}
