package protocol

// Debug mode actions carried by DebugControl.Action and echoed on
// DebugUpdated.Action (#1345).
const (
	DebugActionStart      = "start"
	DebugActionHypotheses = "hypotheses"
	DebugActionEvidence   = "evidence"
	DebugActionMark       = "mark"
	DebugActionUnlock     = "unlock"
	DebugActionFixed      = "fixed"
	DebugActionEnd        = "end"
	// DebugActionScan only refreshes the live instrumentation list.
	DebugActionScan = "scan"
)

// DebugControl drives the session's hypothesis-first debug mode. Action is
// start, hypotheses, evidence, mark, unlock, fixed, end or scan. Hypotheses
// is the ranked list for hypotheses; HypothesisID, Status, Source and Text
// carry evidence and verdicts; Text is the override reason for unlock.
type DebugControl struct {
	Action       string            `json:"action"`
	Hypotheses   []DebugHypothesis `json:"hypotheses,omitempty"`
	HypothesisID string            `json:"hypothesisId,omitempty"`
	Status       string            `json:"status,omitempty"`
	Source       string            `json:"source,omitempty"`
	Text         string            `json:"text,omitempty"`
}

// DebugHypothesis is one ranked candidate cause in a debug session.
type DebugHypothesis struct {
	ID        string          `json:"id"`
	Rank      int             `json:"rank,omitempty"`
	Statement string          `json:"statement"`
	Confirms  string          `json:"confirms,omitempty"`
	Refutes   string          `json:"refutes,omitempty"`
	Status    string          `json:"status,omitempty"`
	Evidence  []DebugEvidence `json:"evidence,omitempty"`
}

// DebugEvidence is captured runtime output attached to a hypothesis.
type DebugEvidence struct {
	Source string `json:"source,omitempty"`
	Text   string `json:"text"`
}

// DebugHit is one tagged instrumentation line on disk.
type DebugHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// DebugUpdated reports debug-mode state after a DebugControl op. Phase is
// hypothesis, fix or closed; empty with no Error means no debug session.
// Error is set (and state unchanged) when the action was refused.
// Instrumentation lists tagged lines still on disk; Removed lists the lines
// the end sweep deleted.
type DebugUpdated struct {
	Correlation
	Action          string            `json:"action"`
	Phase           string            `json:"phase,omitempty"`
	Hypotheses      []DebugHypothesis `json:"hypotheses,omitempty"`
	Override        string            `json:"override,omitempty"`
	Fixed           bool              `json:"fixed,omitempty"`
	Instrumentation []DebugHit        `json:"instrumentation,omitempty"`
	Removed         []DebugHit        `json:"removed,omitempty"`
	Error           string            `json:"error,omitempty"`
}

func (DebugControl) isOp()    {}
func (DebugUpdated) isEvent() {}
