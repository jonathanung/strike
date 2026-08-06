package protocol

import (
	pub "github.com/jonathanung/strike-cli/pkg/protocol"
)

// Wire schema version (see pkg/protocol).
const (
	Version       = pub.Version
	LegacyVersion = pub.LegacyVersion
)

// Effort ladder.
type Effort = pub.Effort

const (
	EffortDefault = pub.EffortDefault
	EffortOff     = pub.EffortOff
	EffortLow     = pub.EffortLow
	EffortMedium  = pub.EffortMedium
	EffortHigh    = pub.EffortHigh
	EffortXHigh   = pub.EffortXHigh
	EffortMax     = pub.EffortMax
)

// Autonomy dial.
type Autonomy = pub.Autonomy

const (
	AutonomySupervised = pub.AutonomySupervised
	AutonomyAgent      = pub.AutonomyAgent
	AutonomyChecks     = pub.AutonomyChecks
	AutonomySkipAll    = pub.AutonomySkipAll
)

// Phase resume recovery statuses.
const (
	PhaseStatusMissing  = pub.PhaseStatusMissing
	PhaseStatusMismatch = pub.PhaseStatusMismatch
)

// Permission posture dial.
type PermissionMode = pub.PermissionMode

const (
	PermissionModeDefault     = pub.PermissionModeDefault
	PermissionModePlan        = pub.PermissionModePlan
	PermissionModeSoftApprove = pub.PermissionModeSoftApprove
	PermissionModeAcceptEdits = pub.PermissionModeAcceptEdits
	PermissionModeYolo        = pub.PermissionModeYolo

	SoftApproveSeconds = pub.SoftApproveSeconds
)

// Permission decision.
type Decision = pub.Decision

const (
	DecisionOnce    = pub.DecisionOnce
	DecisionAlways  = pub.DecisionAlways
	DecisionProject = pub.DecisionProject
	DecisionReject  = pub.DecisionReject
)

// Op / Event interfaces and shared structs.
type (
	Op                      = pub.Op
	Event                   = pub.Event
	ImageAttachment         = pub.ImageAttachment
	Correlation             = pub.Correlation
	ChildStatus             = pub.ChildStatus
	CompletionHandoff       = pub.CompletionHandoff
	DelegationState         = pub.DelegationState
	VerificationReport      = pub.VerificationReport
	VerificationCheck       = pub.VerificationCheck
	VerificationEnv         = pub.VerificationEnv
	TeamMemberState         = pub.TeamMemberState
	TeamRosterMember        = pub.TeamRosterMember
	ProcessStatus           = pub.ProcessStatus
	TokenCount              = pub.TokenCount
	QuestionOption          = pub.QuestionOption
	QuestionPrompt          = pub.QuestionPrompt
	PromptLayerInfo         = pub.PromptLayerInfo
	RequestTokenAttribution = pub.RequestTokenAttribution
	RewindPoint             = pub.RewindPoint
	Envelope                = pub.Envelope
	OpEnvelope              = pub.OpEnvelope
)

// Ops.
type (
	UserInput              = pub.UserInput
	PermissionReply        = pub.PermissionReply
	QuestionReply          = pub.QuestionReply
	Interrupt              = pub.Interrupt
	SelectModel            = pub.SelectModel
	SelectAgent            = pub.SelectAgent
	SetEffort              = pub.SetEffort
	SetAutonomy            = pub.SetAutonomy
	SetPermissionMode      = pub.SetPermissionMode
	SetFast                = pub.SetFast
	StartWorkflow          = pub.StartWorkflow
	StopWorkflow           = pub.StopWorkflow
	FilesChanged           = pub.FilesChanged
	Compact                = pub.Compact
	InspectEffectivePrompt = pub.InspectEffectivePrompt
	Rewind                 = pub.Rewind
)

// Events.
type (
	UserMessage            = pub.UserMessage
	SessionTitled          = pub.SessionTitled
	TurnStarted            = pub.TurnStarted
	TextDelta              = pub.TextDelta
	ReasoningDelta         = pub.ReasoningDelta
	ToolCallBegin          = pub.ToolCallBegin
	ToolCallEnd            = pub.ToolCallEnd
	ToolCallOutput         = pub.ToolCallOutput
	ProcessStarted         = pub.ProcessStarted
	ProcessOutput          = pub.ProcessOutput
	ProcessExited          = pub.ProcessExited
	PermissionAsked        = pub.PermissionAsked
	PermissionResolved     = pub.PermissionResolved
	QuestionAsked          = pub.QuestionAsked
	QuestionResolved       = pub.QuestionResolved
	TurnCompleted          = pub.TurnCompleted
	TurnFileChange         = pub.TurnFileChange
	HarnessProgress        = pub.HarnessProgress
	ModelSelected          = pub.ModelSelected
	AgentSelected          = pub.AgentSelected
	PhaseChanged           = pub.PhaseChanged
	PhaseGrantApproved     = pub.PhaseGrantApproved
	PhaseGrantRule         = pub.PhaseGrantRule
	EffortSelected         = pub.EffortSelected
	AutonomySelected       = pub.AutonomySelected
	PermissionModeSelected = pub.PermissionModeSelected
	FastSelected           = pub.FastSelected
	FilesInvalidated       = pub.FilesInvalidated
	PathOverlap            = pub.PathOverlap
	PathOverlapHolder      = pub.PathOverlapHolder
	EngineError            = pub.EngineError
	ChildStarted           = pub.ChildStarted
	ChildCompleted         = pub.ChildCompleted
	DelegationChanged      = pub.DelegationChanged
	WaitStarted            = pub.WaitStarted
	WaitResolved           = pub.WaitResolved
	AgentMessage           = pub.AgentMessage
	TeamRoster             = pub.TeamRoster
	UsageReported          = pub.UsageReported
	ProviderRetrying       = pub.ProviderRetrying
	SchedulerQueued        = pub.SchedulerQueued
	SchedulerAdmitted      = pub.SchedulerAdmitted
	SchedulerCanceled      = pub.SchedulerCanceled
	CompactionStarted      = pub.CompactionStarted
	CompactionCompleted    = pub.CompactionCompleted
	SessionMeta            = pub.SessionMeta
	SessionRewound         = pub.SessionRewound
	HookMatched            = pub.HookMatched
	EffectivePrompt        = pub.EffectivePrompt
)

// Status / label constants.
const (
	ChildStatusCompleted = pub.ChildStatusCompleted
	ChildStatusFailed    = pub.ChildStatusFailed
	ChildStatusCanceled  = pub.ChildStatusCanceled
	ChildStatusBlocked   = pub.ChildStatusBlocked

	DelegationQueued    = pub.DelegationQueued
	DelegationWorking   = pub.DelegationWorking
	DelegationBlocked   = pub.DelegationBlocked
	DelegationReview    = pub.DelegationReview
	DelegationDone      = pub.DelegationDone
	DelegationFailed    = pub.DelegationFailed
	DelegationCanceled  = pub.DelegationCanceled
	WaitOutcomeMatched  = pub.WaitOutcomeMatched
	WaitOutcomeTimeout  = pub.WaitOutcomeTimeout
	WaitOutcomeCanceled = pub.WaitOutcomeCanceled

	TeamMemberRunning   = pub.TeamMemberRunning
	TeamMemberCompleted = pub.TeamMemberCompleted
	TeamMemberFailed    = pub.TeamMemberFailed
	TeamMemberCanceled  = pub.TeamMemberCanceled
	TeamMemberBlocked   = pub.TeamMemberBlocked

	ProcessStreamStdout = pub.ProcessStreamStdout
	ProcessStreamStderr = pub.ProcessStreamStderr

	ProcessStatusExited   = pub.ProcessStatusExited
	ProcessStatusTimeout  = pub.ProcessStatusTimeout
	ProcessStatusCanceled = pub.ProcessStatusCanceled
	ProcessStatusError    = pub.ProcessStatusError

	UsageSourceActual    = pub.UsageSourceActual
	UsageSourceEstimated = pub.UsageSourceEstimated

	SchedulerReasonCanceled = pub.SchedulerReasonCanceled
	SchedulerReasonClosed   = pub.SchedulerReasonClosed

	CompactionReasonManual    = pub.CompactionReasonManual
	CompactionReasonThreshold = pub.CompactionReasonThreshold
	CompactionReasonOverflow  = pub.CompactionReasonOverflow

	CompactionStrategyTrim      = pub.CompactionStrategyTrim
	CompactionStrategySummarize = pub.CompactionStrategySummarize

	PromptLayerShared      = pub.PromptLayerShared
	PromptLayerTools       = pub.PromptLayerTools
	PromptLayerProvider    = pub.PromptLayerProvider
	PromptLayerConfig      = pub.PromptLayerConfig
	PromptLayerPersona     = pub.PromptLayerPersona
	PromptLayerPhase       = pub.PromptLayerPhase
	PromptLayerPlan        = pub.PromptLayerPlan
	PromptLayerLean        = pub.PromptLayerLean
	PromptLayerEnvironment = pub.PromptLayerEnvironment
	PromptLayerInstruction = pub.PromptLayerInstruction
	PromptLayerMemory      = pub.PromptLayerMemory

	PromptLayerAppend  = pub.PromptLayerAppend
	PromptLayerReplace = pub.PromptLayerReplace
)

// Function forwards — identical behavior to pkg/protocol.

func Efforts() []Effort                           { return pub.Efforts() }
func ParseEffort(value string) (Effort, bool)     { return pub.ParseEffort(value) }
func Autonomies() []Autonomy                      { return pub.Autonomies() }
func ParseAutonomy(value string) (Autonomy, bool) { return pub.ParseAutonomy(value) }
func PermissionModes() []PermissionMode           { return pub.PermissionModes() }
func ParsePermissionMode(value string) (PermissionMode, bool) {
	return pub.ParsePermissionMode(value)
}
func TeamMemberStateFromChild(s ChildStatus) TeamMemberState {
	return pub.TeamMemberStateFromChild(s)
}
func KnownTokens(n int) TokenCount              { return pub.KnownTokens(n) }
func UnknownTokens() TokenCount                 { return pub.UnknownTokens() }
func Wrap(ev Event) (Envelope, error)           { return pub.Wrap(ev) }
func WrapOp(op Op) (OpEnvelope, error)          { return pub.WrapOp(op) }
func RewindPoints(events []Event) []RewindPoint { return pub.RewindPoints(events) }

func ToolFeedbackPermissionDenied(reason string) string {
	return pub.ToolFeedbackPermissionDenied(reason)
}
func ToolFeedbackUserRejected(feedback string) string {
	return pub.ToolFeedbackUserRejected(feedback)
}
func ToolFeedbackBlocked(reason string) string { return pub.ToolFeedbackBlocked(reason) }
func ToolFeedbackCanceled() string             { return pub.ToolFeedbackCanceled() }
func ToolFeedbackCanceledPartial(partial string) string {
	return pub.ToolFeedbackCanceledPartial(partial)
}
func ToolFeedbackTimeout(detail string) string { return pub.ToolFeedbackTimeout(detail) }
func ToolFeedbackUnstarted() string            { return pub.ToolFeedbackUnstarted() }
func ToolFeedbackError(msg string) string      { return pub.ToolFeedbackError(msg) }

const (
	ErrorCodePermissionDenied   = pub.ErrorCodePermissionDenied
	ErrorCodeInvalidArgs        = pub.ErrorCodeInvalidArgs
	ErrorCodePreconditionFailed = pub.ErrorCodePreconditionFailed
	ErrorCodeCanceled           = pub.ErrorCodeCanceled
	ErrorCodeTimeout            = pub.ErrorCodeTimeout
	ErrorCodeTransient          = pub.ErrorCodeTransient
	ErrorCodeInternal           = pub.ErrorCodeInternal
	ErrorCodeBlocked            = pub.ErrorCodeBlocked
	ErrorCodeQueueFull          = pub.ErrorCodeQueueFull
)
