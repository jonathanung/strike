package tool

// SteerAck is the agent's acknowledgement of a steer it just received
// (steer_ack tool → turn.steer_ack, #1860).
type SteerAck struct {
	// Summary is one line: what the agent is doing now.
	Summary string
	// Conflict is true when the steer contradicts the in-flight plan.
	Conflict bool
	// Reason explains the conflict; required when Conflict is true.
	Reason string
}
