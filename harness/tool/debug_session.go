package tool

// DebugOp is one agent-issued debug-mode operation (debug_session tool,
// #1961). Only the hypothesis-work actions exist here: hypotheses, evidence
// and mark. Starting a session, unlocking fix, overriding the gate, marking
// the bug fixed and ending the session stay user-only; the engine refuses
// them with a reason.
type DebugOp struct {
	// Action is hypotheses, evidence or mark.
	Action string
	// Hypotheses is the ranked list (most likely first) for hypotheses.
	Hypotheses []DebugHypothesis
	// HypothesisID targets evidence and mark.
	HypothesisID string
	// Status is the verdict for mark: open, confirmed or refuted.
	Status string
	// Source labels evidence ("repro", "log", "command").
	Source string
	// Text is the captured output for evidence.
	Text string
}

// DebugHypothesis is one candidate cause set by the agent.
type DebugHypothesis struct {
	ID        string
	Statement string
	Confirms  string
	Refutes   string
}
