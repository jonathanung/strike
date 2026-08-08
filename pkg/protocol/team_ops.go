package protocol

// Human orchestration Ops (WEBUI.18 / docs/human-orchestration-ops.md).
//
// Public wire contract for browser/RPC/SDK team controls. Outcomes reuse
// existing events (child.*, delegation.changed, agent.message, team.roster).
// Optional Reply channels are in-process only (json:"-") for serve HTTP
// request/response; wire codecs never serialize them.

// Team control Op type strings (frozen capability advertisement names).
const (
	OpTeamSpawn          = "team.spawn"
	OpTeamMessage        = "team.message"
	OpTeamBroadcast      = "team.broadcast"
	OpTeamChildInterrupt = "team.child_interrupt"
	OpTeamTaskTransition = "team.task_transition"
	OpTeamBoardCreate    = "team.board_create"
	OpTeamBoardClaim     = "team.board_claim"
	OpTeamBoardComplete  = "team.board_complete"
)

// TeamControlOpNames is the ordered v1 protocolOps list for capability hello.
func TeamControlOpNames() []string {
	return []string{
		OpTeamSpawn,
		OpTeamMessage,
		OpTeamBroadcast,
		OpTeamChildInterrupt,
		OpTeamTaskTransition,
		OpTeamBoardCreate,
		OpTeamBoardClaim,
		OpTeamBoardComplete,
	}
}

// Stable team-control error codes (RFC §13).
const (
	ErrTeamCapabilityUnavailable = "capability_unavailable: teamControl"
	ErrTeamAttachOnly            = "attach_only"
	ErrTeamReadOnly              = "read_only"
	ErrTeamCrossRoot             = "cross_root_denied"
	ErrTeamNotLead               = "not_lead"
	ErrTeamUnavailable           = "team_unavailable"
	ErrTeamConflict              = "conflict"
	ErrTeamIdempotencyConflict   = "idempotency_conflict"
	ErrTeamValidation            = "validation"
	ErrTeamPermissionDenied      = "permission_denied"
)

// TeamOpOutcome is the in-process result of a team-control Op (serve HTTP).
// Not an Event — never written to session JSONL.
type TeamOpOutcome struct {
	OK              bool   `json:"ok"`
	Error           string `json:"error,omitempty"`
	Code            string `json:"code,omitempty"`
	ChildSessionID  string `json:"childSessionId,omitempty"`
	Name            string `json:"name,omitempty"`
	DelegationID    string `json:"delegationId,omitempty"`
	TaskID          string `json:"taskId,omitempty"`
	MessageID       string `json:"messageId,omitempty"`
	Version         int    `json:"version,omitempty"`
	CurrentVersion  int    `json:"currentVersion,omitempty"`
	AlreadyTerminal bool   `json:"alreadyTerminal,omitempty"`
}

// TeamSpawnBudget is optional spawn resource bounds (wire subset).
type TeamSpawnBudget struct {
	// MaxTurns is accepted for wire compatibility; engine maps loosely when
	// no native turn ceiling exists (currently ignored beyond validation ≥0).
	MaxTurns int `json:"maxTurns,omitempty"`
	// MaxToolCalls maps to AgentBudgetLimits.MaxToolCalls when > 0.
	MaxToolCalls int `json:"maxToolCalls,omitempty"`
	// MaxTokens maps to AgentBudgetLimits.MaxTokens when > 0.
	MaxTokens int `json:"maxTokens,omitempty"`
}

// teamControlFields are shared JSON fields on every team-control Op.
// Embedded anonymously so wire shape stays flat.
type teamControlFields struct {
	RootSessionID    string `json:"rootSessionId,omitempty"`
	IdempotencyKey   string `json:"idempotencyKey"`
	ClientMutationID string `json:"clientMutationId,omitempty"`
}

// TeamSpawn starts a child agent (task/delegate semantics) as the human lead.
type TeamSpawn struct {
	teamControlFields
	Objective    string           `json:"objective"`
	Agent        string           `json:"agent,omitempty"`
	Name         string           `json:"name,omitempty"`
	Isolation    string           `json:"isolation,omitempty"` // shared|worktree
	Budget       *TeamSpawnBudget `json:"budget,omitempty"`
	DelegationID string           `json:"delegationId,omitempty"`

	// Reply is optional in-process only; set by serve for synchronous HTTP.
	Reply chan<- TeamOpOutcome `json:"-"`
}

// TeamMessage delivers one peer mailbox message (agent_message semantics).
type TeamMessage struct {
	teamControlFields
	To      string `json:"to"`
	Body    string `json:"body"`
	Kind    string `json:"kind,omitempty"`    // message|request
	Urgency string `json:"urgency,omitempty"` // normal|high|blocker
	TaskID  string `json:"taskId,omitempty"`

	Reply chan<- TeamOpOutcome `json:"-"`
}

// TeamBroadcast delivers lead broadcast mail (agent_broadcast semantics).
type TeamBroadcast struct {
	teamControlFields
	Body    string `json:"body"`
	Urgency string `json:"urgency,omitempty"`
	TaskID  string `json:"taskId,omitempty"`

	Reply chan<- TeamOpOutcome `json:"-"`
}

// TeamChildInterrupt cancels an owned child (task_interrupt path).
type TeamChildInterrupt struct {
	teamControlFields
	ChildSessionID string `json:"childSessionId"`
	Reason         string `json:"reason,omitempty"`

	Reply chan<- TeamOpOutcome `json:"-"`
}

// TeamTaskTransition applies a delegation lifecycle CAS transition.
type TeamTaskTransition struct {
	teamControlFields
	DelegationID    string `json:"delegationId"`
	ExpectedVersion int    `json:"expectedVersion"`
	// ToState accepts engine states plus completed→done alias.
	ToState string `json:"toState"`
	Reason  string `json:"reason,omitempty"`

	Reply chan<- TeamOpOutcome `json:"-"`
}

// TeamBoardCreate adds a shared board task (team_task create).
type TeamBoardCreate struct {
	teamControlFields
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Assignee string `json:"assignee,omitempty"`

	Reply chan<- TeamOpOutcome `json:"-"`
}

// TeamBoardClaim claims a board task with CAS.
type TeamBoardClaim struct {
	teamControlFields
	TaskID          string `json:"taskId"`
	ExpectedVersion int    `json:"expectedVersion"`

	Reply chan<- TeamOpOutcome `json:"-"`
}

// TeamBoardComplete completes a board task with CAS.
type TeamBoardComplete struct {
	teamControlFields
	TaskID          string `json:"taskId"`
	ExpectedVersion int    `json:"expectedVersion"`
	Summary         string `json:"summary,omitempty"`

	Reply chan<- TeamOpOutcome `json:"-"`
}

func (TeamSpawn) isOp()          {}
func (TeamMessage) isOp()        {}
func (TeamBroadcast) isOp()      {}
func (TeamChildInterrupt) isOp() {}
func (TeamTaskTransition) isOp() {}
func (TeamBoardCreate) isOp()    {}
func (TeamBoardClaim) isOp()     {}
func (TeamBoardComplete) isOp()  {}

// IsTeamControlOp reports whether op is a v1 human orchestration Op.
func IsTeamControlOp(op Op) bool {
	switch op.(type) {
	case TeamSpawn, TeamMessage, TeamBroadcast, TeamChildInterrupt,
		TeamTaskTransition, TeamBoardCreate, TeamBoardClaim, TeamBoardComplete:
		return true
	default:
		return false
	}
}

// TeamControlReply extracts the optional in-process reply channel.
func TeamControlReply(op Op) chan<- TeamOpOutcome {
	switch v := op.(type) {
	case TeamSpawn:
		return v.Reply
	case TeamMessage:
		return v.Reply
	case TeamBroadcast:
		return v.Reply
	case TeamChildInterrupt:
		return v.Reply
	case TeamTaskTransition:
		return v.Reply
	case TeamBoardCreate:
		return v.Reply
	case TeamBoardClaim:
		return v.Reply
	case TeamBoardComplete:
		return v.Reply
	default:
		return nil
	}
}

// WithTeamControlReply returns a copy of op with Reply set (in-process).
func WithTeamControlReply(op Op, reply chan<- TeamOpOutcome) Op {
	switch v := op.(type) {
	case TeamSpawn:
		v.Reply = reply
		return v
	case TeamMessage:
		v.Reply = reply
		return v
	case TeamBroadcast:
		v.Reply = reply
		return v
	case TeamChildInterrupt:
		v.Reply = reply
		return v
	case TeamTaskTransition:
		v.Reply = reply
		return v
	case TeamBoardCreate:
		v.Reply = reply
		return v
	case TeamBoardClaim:
		v.Reply = reply
		return v
	case TeamBoardComplete:
		v.Reply = reply
		return v
	default:
		return op
	}
}

// TeamControlRootSessionID returns the optional root binding from a team Op.
func TeamControlRootSessionID(op Op) string {
	switch v := op.(type) {
	case TeamSpawn:
		return v.RootSessionID
	case TeamMessage:
		return v.RootSessionID
	case TeamBroadcast:
		return v.RootSessionID
	case TeamChildInterrupt:
		return v.RootSessionID
	case TeamTaskTransition:
		return v.RootSessionID
	case TeamBoardCreate:
		return v.RootSessionID
	case TeamBoardClaim:
		return v.RootSessionID
	case TeamBoardComplete:
		return v.RootSessionID
	default:
		return ""
	}
}

// TeamControlIdempotencyKey returns the idempotency key from a team Op.
func TeamControlIdempotencyKey(op Op) string {
	switch v := op.(type) {
	case TeamSpawn:
		return v.IdempotencyKey
	case TeamMessage:
		return v.IdempotencyKey
	case TeamBroadcast:
		return v.IdempotencyKey
	case TeamChildInterrupt:
		return v.IdempotencyKey
	case TeamTaskTransition:
		return v.IdempotencyKey
	case TeamBoardCreate:
		return v.IdempotencyKey
	case TeamBoardClaim:
		return v.IdempotencyKey
	case TeamBoardComplete:
		return v.IdempotencyKey
	default:
		return ""
	}
}
