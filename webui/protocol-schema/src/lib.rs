// Code generated from pkg/protocol. DO NOT EDIT.
// Regenerate with `make protocol-schema`.
//
// Unknown event type strings become Event::Unknown.
// Unknown JSON fields are ignored. Missing fields are defaults.
// Unknown op type strings return Err.
// protocol.Version 1.19.0

use std::collections::HashMap;

use serde::de::DeserializeOwned;
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::Value;

pub const PROTOCOL_VERSION: &str = "1.19.0";
pub const LEGACY_VERSION: &str = "1.0.0";

/// Open string. Known writer values live in the JSON Schema.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct Autonomy(pub String);

/// Open string. Known writer values live in the JSON Schema.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct ChildStatus(pub String);

/// Open string. Known writer values live in the JSON Schema.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct Decision(pub String);

/// Open string. Known writer values live in the JSON Schema.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct DelegationState(pub String);

/// Open string. Known writer values live in the JSON Schema.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct Effort(pub String);

/// Open string. Known writer values live in the JSON Schema.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct PermissionMode(pub String);

/// Open string. Known writer values live in the JSON Schema.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct ProcessStatus(pub String);

/// Open string. Known writer values live in the JSON Schema.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct TeamMemberState(pub String);

/// AdmissionDecided is a non-blocking audit event when MCP/skill/plugin admission finishes for one subject (allow|warn|block|quarantine). Emitted at register/load time before tools bind. Reasons and evidence are redacted by timeline export.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct AdmissionDecided {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "surface")]
    pub surface: String,
    #[serde(default, rename = "target")]
    pub target: String,
    #[serde(default, rename = "action")]
    pub action: String,
    #[serde(default, rename = "reason")]
    pub reason: String,
    #[serde(default, rename = "preset")]
    pub preset: String,
    #[serde(default, rename = "findings", deserialize_with = "null_as_default")]
    pub findings: Vec<String>,
}

/// AgentBudgetView is the protocol wire shape for per-agent budget remaining. Nested under TeamRosterMember and ChildEscalated. Zero limits mean unlimited. Session-level maxSessionCostUSD (#577) is the outer envelope when configured; per-agent maxCostUSD nests inside it.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct AgentBudgetView {
    #[serde(default, rename = "maxWallClockS")]
    pub max_wall_clock_s: i64,
    #[serde(default, rename = "maxTokens")]
    pub max_tokens: i64,
    #[serde(default, rename = "maxCostUsd")]
    pub max_cost_usd: f64,
    #[serde(default, rename = "maxToolCalls")]
    pub max_tool_calls: i64,
    #[serde(default, rename = "maxDangerousTools")]
    pub max_dangerous_tools: i64,
    #[serde(default, rename = "stallAfterS")]
    pub stall_after_s: i64,
    #[serde(default, rename = "loopDetectN")]
    pub loop_detect_n: i64,
    #[serde(default, rename = "elapsedS")]
    pub elapsed_s: i64,
    #[serde(default, rename = "tokensUsed")]
    pub tokens_used: i64,
    #[serde(default, rename = "costUsdUsed")]
    pub cost_usd_used: f64,
    #[serde(default, rename = "toolCalls")]
    pub tool_calls: i64,
    #[serde(default, rename = "dangerousTools")]
    pub dangerous_tools: i64,
    #[serde(default, rename = "wallClockRemainingS")]
    pub wall_clock_remaining_s: Option<i64>,
    #[serde(default, rename = "tokensRemaining")]
    pub tokens_remaining: Option<i64>,
    #[serde(default, rename = "toolCallsRemaining")]
    pub tool_calls_remaining: Option<i64>,
    #[serde(default, rename = "dangerousRemaining")]
    pub dangerous_remaining: Option<i64>,
    #[serde(default, rename = "costUsdRemaining")]
    pub cost_usd_remaining: Option<f64>,
    #[serde(default, rename = "stall")]
    pub stall: bool,
    #[serde(default, rename = "idleS")]
    pub idle_s: i64,
    #[serde(default, rename = "lastProgressAt")]
    pub last_progress_at: String,
    #[serde(default, rename = "stallAfterSEffective")]
    pub stall_after_s_effective: i64,
    #[serde(default, rename = "loop")]
    pub r#loop: bool,
    #[serde(default, rename = "escalated")]
    pub escalated: bool,
    #[serde(default, rename = "escalateKind")]
    pub escalate_kind: String,
    #[serde(default, rename = "escalateReason")]
    pub escalate_reason: String,
}

/// AgentContractTimeout reports that a require-ack peer message was not acked before its TTL. Correlation is the original sender session. An escalation mailbox delivery to EscalateTo (default lead) is also enqueued when live.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct AgentContractTimeout {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "messageId")]
    pub message_id: String,
    #[serde(default, rename = "from")]
    pub from: String,
    #[serde(default, rename = "to")]
    pub to: String,
    #[serde(default, rename = "taskId")]
    pub task_id: String,
    #[serde(default, rename = "teamId")]
    pub team_id: String,
    #[serde(default, rename = "urgency")]
    pub urgency: String,
    #[serde(default, rename = "escalateTo")]
    pub escalate_to: String,
    #[serde(default, rename = "detail")]
    pub detail: String,
}

/// AgentMessage records a peer/team mailbox delivery for UI and debugging. Correlation is the recipient session. Body is the message text; From/To are session ids; TeamID is the lead session id (team identity). Emitted on the recipient engine at boundary injection (tool-round / idle nudge), never mid-tool-call.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct AgentMessage {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "from")]
    pub from: String,
    #[serde(default, rename = "to")]
    pub to: String,
    #[serde(default, rename = "body")]
    pub body: String,
    #[serde(default, rename = "summary")]
    pub summary: String,
    #[serde(default, rename = "teamId")]
    pub team_id: String,
    #[serde(default, rename = "messageId")]
    pub message_id: String,
    #[serde(default, rename = "taskId")]
    pub task_id: String,
    #[serde(default, rename = "urgency")]
    pub urgency: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "requireAck")]
    pub require_ack: bool,
    #[serde(default, rename = "inReplyTo")]
    pub in_reply_to: String,
    #[serde(default, rename = "escalateTo")]
    pub escalate_to: String,
    #[serde(default, rename = "ackStatus")]
    pub ack_status: String,
}

/// AgentSelected confirms the active agent.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct AgentSelected {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "name")]
    pub name: String,
}

/// ArtifactRef points at a shared typed artifact (id + optional CAS version/type). Used on completion handoffs and task/agent messages so peers can fetch "findings:v3" without inlining prose. See internal/persist/artifact.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ArtifactRef {
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "version")]
    pub version: i64,
    #[serde(default, rename = "type")]
    pub r#type: String,
}

/// ArtifactUpdated is emitted after a successful artifact_write create/update so session JSONL and attach clients observe shared typed artifact mutations. Content is omitted (fetch via tools); id+version+type identify the object.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ArtifactUpdated {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "type")]
    pub r#type: String,
    #[serde(default, rename = "version")]
    pub version: i64,
    #[serde(default, rename = "scope")]
    pub scope: String,
    #[serde(default, rename = "title")]
    pub title: String,
    #[serde(default, rename = "op")]
    pub op: String,
}

/// AutonomySelected confirms the session exit-gate policy, at startup and after each SetAutonomy.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct AutonomySelected {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "mode")]
    pub mode: Autonomy,
}

/// ChildCompleted marks the end of a foreground child/subagent session. Emitted by the parent engine with the child's correlation.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ChildCompleted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "status")]
    pub status: ChildStatus,
    #[serde(default, rename = "summary")]
    pub summary: String,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "handoff")]
    pub handoff: CompletionHandoff,
    #[serde(default, rename = "delegationId")]
    pub delegation_id: String,
    #[serde(default, rename = "verification")]
    pub verification: Option<VerificationReport>,
    #[serde(default, rename = "budgetKind")]
    pub budget_kind: String,
    #[serde(default, rename = "finalization")]
    pub finalization: String,
}

/// ChildEscalated reports a per-child budget/stall/loop trip (#774 / #879). Emitted on the parent stream before interrupt or finalization. Action is "finalizing" (soft budget: one reserved structured-handoff turn), "interrupted" (hard kill in flight), or "signaled" (soft observability only — unused in hard path). Kind is wall_clock|tokens|cost_usd|tool_calls| dangerous_tools|stall|loop. Correlation is the child session.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ChildEscalated {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "reason")]
    pub reason: String,
    #[serde(default, rename = "action")]
    pub action: String,
    #[serde(default, rename = "terminalStatus")]
    pub terminal_status: ChildStatus,
    #[serde(default, rename = "budget")]
    pub budget: Option<AgentBudgetView>,
}

/// ChildStarted marks the beginning of a foreground child/subagent session. Emitted by the parent engine with the child's correlation.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ChildStarted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "agent")]
    pub agent: String,
    #[serde(default, rename = "prompt")]
    pub prompt: String,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "routeReason")]
    pub route_reason: String,
    #[serde(default, rename = "policyReason")]
    pub policy_reason: String,
    #[serde(default, rename = "contextBundle")]
    pub context_bundle: Option<ContextBundle>,
    #[serde(default, rename = "provider")]
    pub provider: String,
    #[serde(default, rename = "model")]
    pub model: String,
    #[serde(default, rename = "isolation")]
    pub isolation: String,
    #[serde(default, rename = "worktreePath")]
    pub worktree_path: String,
    #[serde(default, rename = "baseRevision")]
    pub base_revision: String,
}

/// Compact requests model-history compaction. Rejected while a turn is running. Strategy selects trim (drop older turns, default) or summarize (model-authored summary). Empty uses the engine/config default.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct Compact {
    #[serde(default, rename = "strategy")]
    pub strategy: String,
}

/// CompactionCompleted records that model-facing history was replaced. Removed/Kept count provider messages (not transcript events). Strategy is the strategy actually applied (may fall back from summarize to trim). Summary is the model-authored text when Strategy is summarize (for restore). Residue is the structured provenance document when extraction found items (or pins/summary); nil when empty.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct CompactionCompleted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "reason")]
    pub reason: String,
    #[serde(default, rename = "strategy")]
    pub strategy: String,
    #[serde(default, rename = "removed")]
    pub removed: i64,
    #[serde(default, rename = "kept")]
    pub kept: i64,
    #[serde(default, rename = "summary")]
    pub summary: String,
    #[serde(default, rename = "residue")]
    pub residue: Option<CompactionResidue>,
}

/// CompactionResidue is a schema-versioned structured residual produced by history compaction. It is not lossless; rebuild yields a usable prompt skeleton for continue, not the full dropped transcript.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct CompactionResidue {
    #[serde(default, rename = "schemaVersion")]
    pub schema_version: String,
    #[serde(default, rename = "strategy")]
    pub strategy: String,
    #[serde(default, rename = "reason")]
    pub reason: String,
    #[serde(default, rename = "removed")]
    pub removed: i64,
    #[serde(default, rename = "facts", deserialize_with = "null_as_default")]
    pub facts: Vec<ResidueItem>,
    #[serde(default, rename = "decisions", deserialize_with = "null_as_default")]
    pub decisions: Vec<ResidueItem>,
    #[serde(default, rename = "openQuestions", deserialize_with = "null_as_default")]
    pub open_questions: Vec<ResidueItem>,
    #[serde(default, rename = "pinnedKinds", deserialize_with = "null_as_default")]
    pub pinned_kinds: Vec<String>,
    #[serde(default, rename = "summary")]
    pub summary: String,
}

/// CompactionStarted announces that model-facing history compaction is about to replace older messages. Emitted before the history mutation.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct CompactionStarted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "reason")]
    pub reason: String,
    #[serde(default, rename = "strategy")]
    pub strategy: String,
}

/// CompletionHandoff is the structured work product for a delegated child at terminal status. Always present on ChildCompleted (empty slices/strings are honest). Success fills the full schema; failure/cancel may leave verification/findings empty. filesChanged may be engine-tracked and/or model-supplied (merged, de-duplicated).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct CompletionHandoff {
    #[serde(default, rename = "summary")]
    pub summary: String,
    #[serde(default, rename = "filesChanged", deserialize_with = "null_as_default")]
    pub files_changed: Vec<String>,
    #[serde(default, rename = "verification")]
    pub verification: String,
    #[serde(default, rename = "findings", deserialize_with = "null_as_default")]
    pub findings: Vec<String>,
    #[serde(default, rename = "blockers", deserialize_with = "null_as_default")]
    pub blockers: Vec<String>,
    #[serde(default, rename = "recommendedNextAction")]
    pub recommended_next_action: String,
    #[serde(default, rename = "artifactRefs", deserialize_with = "null_as_default")]
    pub artifact_refs: Vec<ArtifactRef>,
    #[serde(default, rename = "isolation")]
    pub isolation: String,
    #[serde(default, rename = "patch")]
    pub patch: String,
    #[serde(default, rename = "baseRevision")]
    pub base_revision: String,
    #[serde(default, rename = "worktreePath")]
    pub worktree_path: String,
    #[serde(default, rename = "missingContext", deserialize_with = "null_as_default")]
    pub missing_context: Vec<MissingContextEntry>,
    #[serde(default, rename = "provenance", deserialize_with = "null_as_default")]
    pub provenance: Vec<String>,
    #[serde(default, rename = "incomplete")]
    pub incomplete: bool,
    #[serde(default, rename = "quality")]
    pub quality: String,
    #[serde(default, rename = "sectionTitle")]
    pub section_title: String,
    #[serde(default, rename = "sectionBody")]
    pub section_body: String,
    #[serde(default, rename = "sectionBodySet")]
    pub section_body_set: bool,
}

/// ContextBundle is a sealed context package attached at task/delegate spawn. Children read it via the context_bundle tool and a system-prompt layer; leads attach goal, paths, artifact refs, constraints, and optional file pins so work is not "prompt-only by convention."
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ContextBundle {
    #[serde(default, rename = "goal")]
    pub goal: String,
    #[serde(default, rename = "acceptance", deserialize_with = "null_as_default")]
    pub acceptance: Vec<String>,
    #[serde(default, rename = "allowedPaths", deserialize_with = "null_as_default")]
    pub allowed_paths: Vec<String>,
    #[serde(default, rename = "requiredPaths", deserialize_with = "null_as_default")]
    pub required_paths: Vec<String>,
    #[serde(default, rename = "artifacts", deserialize_with = "null_as_default")]
    pub artifacts: Vec<ArtifactRef>,
    #[serde(default, rename = "constraints", deserialize_with = "null_as_default")]
    pub constraints: Vec<String>,
    #[serde(default, rename = "items", deserialize_with = "null_as_default")]
    pub items: Vec<ContextBundleItem>,
    #[serde(default, rename = "filePins", deserialize_with = "null_as_default")]
    pub file_pins: Vec<ContextFilePin>,
}

/// ContextBundleItem is one addressable entry inside a ContextBundle. Kind is goal|acceptance|path|artifact|constraint|note|file_pin|other.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ContextBundleItem {
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "title")]
    pub title: String,
    #[serde(default, rename = "text")]
    pub text: String,
    #[serde(default, rename = "path")]
    pub path: String,
    #[serde(default, rename = "artifact")]
    pub artifact: Option<ArtifactRef>,
    #[serde(default, rename = "hash")]
    pub hash: String,
}

/// ContextControlsSelected confirms the session pin/exclude sets after SetContextControls (and is echoed on EffectivePrompt for inspect).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ContextControlsSelected {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "excludedKinds", deserialize_with = "null_as_default")]
    pub excluded_kinds: Vec<String>,
    #[serde(default, rename = "pinnedKinds", deserialize_with = "null_as_default")]
    pub pinned_kinds: Vec<String>,
}

/// ContextFilePin seals a workspace path (optional hash and/or snapshot text).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ContextFilePin {
    #[serde(default, rename = "path")]
    pub path: String,
    #[serde(default, rename = "hash")]
    pub hash: String,
    #[serde(default, rename = "text")]
    pub text: String,
}

/// ContextFitWarning signals projected prompt occupancy is approaching or exceeding the model context window (soft budget). Emitted before Stream when possible so UIs can warn ahead of hard provider failure. Estimates use the local ~4 chars/token heuristic unless Source says otherwise.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ContextFitWarning {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "estimatedTokens")]
    pub estimated_tokens: i64,
    #[serde(default, rename = "contextLimit")]
    pub context_limit: i64,
    #[serde(default, rename = "level")]
    pub level: String,
    #[serde(default, rename = "message")]
    pub message: String,
    #[serde(default, rename = "source")]
    pub source: String,
}

/// Correlation identifies the session, turn, and provider request that produced an event. Embedded anonymously so JSON stays flat; omitempty keeps legacy envelopes without IDs decodable and re-encodable as `{}`. ParentSessionID and Depth record immutable child-session lineage when a foreground subagent is running (root sessions leave both zero-valued).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct Correlation {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
}

/// DelegationChanged reports a lifecycle transition on a delegation object. Correlation is the owner (creator) session. Emitted for UI/debug and for event-wait consumers (#775). Subscribe hooks on create may also notify the lead via mailbox when State matches a subscribed kind.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct DelegationChanged {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "state")]
    pub state: DelegationState,
    #[serde(default, rename = "prev")]
    pub prev: DelegationState,
    #[serde(default, rename = "version")]
    pub version: i64,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "reason")]
    pub reason: String,
    #[serde(default, rename = "ownerSessionId")]
    pub owner_session_id: String,
}

/// DiagnosticBundle is the inspectable prompt/config diagnostic document for the last Stream (or current composition). Field layout matches pkg/diag.Bundle JSON so frontends can export the event payload directly. Never carries raw API keys — engine redacts previews and paths before emit.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct DiagnosticBundle {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "schemaVersion")]
    pub schema_version: String,
    #[serde(default, rename = "protocolVersion")]
    pub protocol_version: String,
    #[serde(default, rename = "strikeVersion")]
    pub strike_version: String,
    #[serde(default, rename = "exportedAt")]
    pub exported_at: String,
    #[serde(default, rename = "redacted")]
    pub redacted: bool,
    #[serde(default, rename = "note")]
    pub note: String,
    #[serde(default, rename = "session")]
    pub session: DiagnosticSession,
    #[serde(default, rename = "prompt")]
    pub prompt: DiagnosticPrompt,
    #[serde(default, rename = "config")]
    pub config: DiagnosticConfig,
    #[serde(default, rename = "warnings", deserialize_with = "null_as_default")]
    pub warnings: Vec<String>,
}

/// DiagnosticCompaction holds effective compaction/prune dials on a bundle.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct DiagnosticCompaction {
    #[serde(default, rename = "strategy")]
    pub strategy: String,
    #[serde(default, rename = "model")]
    pub model: String,
    #[serde(default, rename = "threshold")]
    pub threshold: f64,
    #[serde(default, rename = "buffer")]
    pub buffer: i64,
    #[serde(default, rename = "keepUserTurns")]
    pub keep_user_turns: i64,
    #[serde(default, rename = "pruneProtectTokens")]
    pub prune_protect_tokens: i64,
    #[serde(default, rename = "pruneMinimumTokens")]
    pub prune_minimum_tokens: i64,
    #[serde(default, rename = "pruneKeepUserTurns")]
    pub prune_keep_user_turns: i64,
    #[serde(default, rename = "pruneProtectTools", deserialize_with = "null_as_default")]
    pub prune_protect_tools: Vec<String>,
}

/// DiagnosticConfig is the effective dial snapshot + digests (no secrets).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct DiagnosticConfig {
    #[serde(default, rename = "provider")]
    pub provider: String,
    #[serde(default, rename = "model")]
    pub model: String,
    #[serde(default, rename = "agent")]
    pub agent: String,
    #[serde(default, rename = "effort")]
    pub effort: String,
    #[serde(default, rename = "autonomy")]
    pub autonomy: String,
    #[serde(default, rename = "permissionMode")]
    pub permission_mode: String,
    #[serde(default, rename = "sandbox")]
    pub sandbox: String,
    #[serde(default, rename = "leanCode")]
    pub lean_code: String,
    #[serde(default, rename = "fast")]
    pub fast: bool,
    #[serde(default, rename = "maxTokens")]
    pub max_tokens: i64,
    #[serde(default, rename = "maxChildDepth")]
    pub max_child_depth: i64,
    #[serde(default, rename = "contextWindow")]
    pub context_window: i64,
    #[serde(default, rename = "turnTimeoutS")]
    pub turn_timeout_s: i64,
    #[serde(default, rename = "workDir")]
    pub work_dir: String,
    #[serde(default, rename = "projectRoot")]
    pub project_root: String,
    #[serde(default, rename = "compaction")]
    pub compaction: DiagnosticCompaction,
    #[serde(default, rename = "scheduler")]
    pub scheduler: DiagnosticScheduler,
    #[serde(default, rename = "digests", deserialize_with = "null_as_default")]
    pub digests: HashMap<String, String>,
}

/// DiagnosticPrompt is the ordered layer map section of a DiagnosticBundle.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct DiagnosticPrompt {
    #[serde(default, rename = "precedence", deserialize_with = "null_as_default")]
    pub precedence: Vec<String>,
    #[serde(default, rename = "layers", deserialize_with = "null_as_default")]
    pub layers: Vec<PromptLayerInfo>,
    #[serde(default, rename = "layerCount")]
    pub layer_count: i64,
    #[serde(default, rename = "systemChars")]
    pub system_chars: i64,
    #[serde(default, rename = "messageCount")]
    pub message_count: i64,
    #[serde(default, rename = "fromLastStream")]
    pub from_last_stream: bool,
    #[serde(default, rename = "attribution")]
    pub attribution: RequestTokenAttribution,
}

/// DiagnosticScheduler holds scheduler pool limits on a bundle.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct DiagnosticScheduler {
    #[serde(default, rename = "limits", deserialize_with = "null_as_default")]
    pub limits: HashMap<String, i64>,
}

/// DiagnosticSession is session lineage on a DiagnosticBundle (solo + child).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct DiagnosticSession {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "isChild")]
    pub is_child: bool,
}

/// EffectivePrompt is the inspectable composition of the system prompt for the last Stream (or the current composition when no stream has run yet).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct EffectivePrompt {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "layers", deserialize_with = "null_as_default")]
    pub layers: Vec<PromptLayerInfo>,
    #[serde(default, rename = "systemChars")]
    pub system_chars: i64,
    #[serde(default, rename = "messageCount")]
    pub message_count: i64,
    #[serde(default, rename = "fromLastStream")]
    pub from_last_stream: bool,
    #[serde(default, rename = "attribution")]
    pub attribution: RequestTokenAttribution,
    #[serde(default, rename = "excludedKinds", deserialize_with = "null_as_default")]
    pub excluded_kinds: Vec<String>,
    #[serde(default, rename = "pinnedKinds", deserialize_with = "null_as_default")]
    pub pinned_kinds: Vec<String>,
    #[serde(default, rename = "shedKinds", deserialize_with = "null_as_default")]
    pub shed_kinds: Vec<String>,
}

/// EffortSelected confirms the active reasoning level, at startup and after each SetEffort.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct EffortSelected {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "level")]
    pub level: Effort,
}

#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct EngineError {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "message")]
    pub message: String,
    #[serde(default, rename = "code")]
    pub code: String,
}

/// Envelope wraps an Event with a type tag and timestamp for JSONL persistence and replay.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct Envelope {
    #[serde(default, rename = "type")]
    pub r#type: String,
    #[serde(default, rename = "time")]
    pub time: String,
    #[serde(default, rename = "v")]
    pub v: String,
    #[serde(default, rename = "data")]
    pub data: Value,
}

/// FastSelected confirms the session priority-tier preference after SetFast.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct FastSelected {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "enabled")]
    pub enabled: bool,
}

/// FilesChanged reports paths the user edited outside the agent (for example via /vim). The engine invalidates any prior read snapshots for those paths so a subsequent edit/write fails until the model re-reads.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct FilesChanged {
    #[serde(default, rename = "paths", deserialize_with = "null_as_default")]
    pub paths: Vec<String>,
    #[serde(default, rename = "reason")]
    pub reason: String,
}

/// FilesInvalidated confirms the engine dropped read snapshots for paths reported by a FilesChanged op (external editor, post-edit review, …).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct FilesInvalidated {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "paths", deserialize_with = "null_as_default")]
    pub paths: Vec<String>,
    #[serde(default, rename = "reason")]
    pub reason: String,
}

/// HarnessProgress is emitted by a custom harness to report intermediate state (search tree, scores, iteration count, etc.) during a turn. The Name identifies the harness; Payload is harness-specific JSON.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct HarnessProgress {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "payload")]
    pub payload: Value,
}

/// HookMatched records that a declarative config hook rule fired (log/block/ notify). Persisted in the session JSONL for review and notify sinks.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct HookMatched {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "event")]
    pub event: String,
    #[serde(default, rename = "action")]
    pub action: String,
    #[serde(default, rename = "matcher")]
    pub matcher: String,
    #[serde(default, rename = "tool")]
    pub tool: String,
    #[serde(default, rename = "message")]
    pub message: String,
    #[serde(default, rename = "callId")]
    pub call_id: String,
}

/// ImageAttachment is one typed attachment on a user message (paste/drop). Data is standard base64 (no data: URI prefix) and is omitted from persisted history when Ref is set. MIME is a full media type such as image/png. Ref is att:sha256:<hex> for content-addressed storage.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ImageAttachment {
    #[serde(default, rename = "mime")]
    pub mime: String,
    #[serde(default, rename = "data")]
    pub data: String,
    #[serde(default, rename = "ref")]
    pub r#ref: String,
    #[serde(default, rename = "sha256")]
    pub sha256: String,
    #[serde(default, rename = "bytes")]
    pub bytes: i64,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "name")]
    pub name: String,
}

/// InspectDiagnosticBundle requests a versioned prompt/config diagnostic bundle (layer map + effective dials + digests). Prefer the last Stream composition when one exists; otherwise the current composition. Never carries raw API keys — previews and paths are redacted by the engine. See pkg/diag for the export document schema.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct InspectDiagnosticBundle {
}

/// InspectEffectivePrompt requests a snapshot of the composed system-prompt layers (provenance + sizes). Prefer the last Stream composition when one exists; otherwise the current composition for the next request. Never carries raw API keys — previews are redacted by the engine.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct InspectEffectivePrompt {
}

/// Interrupt cancels the running turn, if any.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct Interrupt {
}

/// LedgerUpdated is emitted after a successful ledger_write append/invalidate/ supersede so session JSONL and wait/subscribe consumers observe decision trail mutations. Full statement is included (redacted on persist); tools remain the source of truth for history queries.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct LedgerUpdated {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "status")]
    pub status: String,
    #[serde(default, rename = "op")]
    pub op: String,
    #[serde(default, rename = "statement")]
    pub statement: String,
    #[serde(default, rename = "reason")]
    pub reason: String,
    #[serde(default, rename = "supersedes")]
    pub supersedes: String,
    #[serde(default, rename = "supersededBy")]
    pub superseded_by: String,
    #[serde(default, rename = "authorSession")]
    pub author_session: String,
}

/// LifecycleCapabilities advertises which host-level session operations a frontend supports. Engine-level Rewind remains a protocol op ("rewind") and is listed separately under EngineRewind when the live session accepts ops.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct LifecycleCapabilities {
    #[serde(default, rename = "list")]
    pub list: bool,
    #[serde(default, rename = "get")]
    pub get: bool,
    #[serde(default, rename = "fork")]
    pub fork: bool,
    #[serde(default, rename = "forkAt")]
    pub fork_at: bool,
    #[serde(default, rename = "load")]
    pub load: bool,
    #[serde(default, rename = "rewindPoints")]
    pub rewind_points: bool,
    #[serde(default, rename = "replay")]
    pub replay: bool,
    #[serde(default, rename = "engineRewind")]
    pub engine_rewind: bool,
    #[serde(default, rename = "activeSessionId")]
    pub active_session_id: String,
}

/// LifecycleError is a structured host-level session lifecycle failure. Frontends should surface Code to automation and Message to humans.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct LifecycleError {
    #[serde(default, rename = "code")]
    pub code: String,
    #[serde(default, rename = "message")]
    pub message: String,
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
}

/// MissingContextEntry reports context the child needed but did not have. Used on blocked completions so the lead can supply paths, answers, or artifact ids instead of the child hallucinating.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct MissingContextEntry {
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "path")]
    pub path: String,
    #[serde(default, rename = "question")]
    pub question: String,
    #[serde(default, rename = "artifactId")]
    pub artifact_id: String,
    #[serde(default, rename = "itemId")]
    pub item_id: String,
    #[serde(default, rename = "detail")]
    pub detail: String,
}

/// ModelSelected confirms the active provider/model, at startup (if an initial selection succeeded) and after each SelectModel.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ModelSelected {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "provider")]
    pub provider: String,
    #[serde(default, rename = "model")]
    pub model: String,
}

/// OpEnvelope wraps an Op with a type tag for WebSocket/HTTP JSON transport.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct OpEnvelope {
    #[serde(default, rename = "type")]
    pub r#type: String,
    #[serde(default, rename = "v")]
    pub v: String,
    #[serde(default, rename = "data")]
    pub data: Value,
}

/// PathOverlap reports concurrent multi-agent claims on the same path. Emitted when a write touch or lease hits another active holder under session.overlapPolicy warn|block. Correlation is the claiming session.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PathOverlap {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "path")]
    pub path: String,
    #[serde(default, rename = "policy")]
    pub policy: String,
    #[serde(default, rename = "blocked")]
    pub blocked: bool,
    #[serde(default, rename = "holders", deserialize_with = "null_as_default")]
    pub holders: Vec<PathOverlapHolder>,
    #[serde(default, rename = "warning")]
    pub warning: String,
}

/// PathOverlapHolder is one other active claimant on a PathOverlap event.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PathOverlapHolder {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "source")]
    pub source: String,
    #[serde(default, rename = "mode")]
    pub mode: String,
}

/// PermissionAsked suspends a tool call until a PermissionReply arrives.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PermissionAsked {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "permission")]
    pub permission: String,
    #[serde(default, rename = "patterns", deserialize_with = "null_as_default")]
    pub patterns: Vec<String>,
    #[serde(default, rename = "always", deserialize_with = "null_as_default")]
    pub always: Vec<String>,
    #[serde(default, rename = "metadata")]
    pub metadata: Value,
}

/// PermissionDecided is a non-blocking audit event for every permission evaluation outcome (auto-allow, auto-deny, ask suspended, or user reply). Patterns and rule fields are redacted by timeline export; session JSONL consumers should treat them as potentially sensitive paths/commands.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PermissionDecided {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "permission")]
    pub permission: String,
    #[serde(default, rename = "patterns", deserialize_with = "null_as_default")]
    pub patterns: Vec<String>,
    #[serde(default, rename = "action")]
    pub action: String,
    #[serde(default, rename = "decision")]
    pub decision: Decision,
    #[serde(default, rename = "layer")]
    pub layer: String,
    #[serde(default, rename = "rulePermission")]
    pub rule_permission: String,
    #[serde(default, rename = "rulePattern")]
    pub rule_pattern: String,
    #[serde(default, rename = "ruleAction")]
    pub rule_action: String,
    #[serde(default, rename = "evalPath")]
    pub eval_path: String,
    #[serde(default, rename = "factSummary")]
    pub fact_summary: String,
    #[serde(default, rename = "chainId")]
    pub chain_id: String,
    #[serde(default, rename = "chainRule")]
    pub chain_rule: String,
    #[serde(default, rename = "chainSummary")]
    pub chain_summary: String,
}

/// PermissionModeSelected confirms the session tool-permission posture, at startup and after each SetPermissionMode.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PermissionModeSelected {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "mode")]
    pub mode: PermissionMode,
}

/// PermissionReply resolves a pending PermissionAsked event.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PermissionReply {
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "decision")]
    pub decision: Decision,
    #[serde(default, rename = "message")]
    pub message: String,
}

#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PermissionResolved {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "decision")]
    pub decision: Decision,
}

/// PhaseChanged reports the active workflow phase (permission profile + context). Empty Phase means no workflow phase is active.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PhaseChanged {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "workflow")]
    pub workflow: String,
    #[serde(default, rename = "phase")]
    pub phase: String,
    #[serde(default, rename = "index")]
    pub index: i64,
    #[serde(default, rename = "gate")]
    pub gate: String,
    #[serde(default, rename = "source")]
    pub source: String,
    #[serde(default, rename = "fingerprint")]
    pub fingerprint: String,
    #[serde(default, rename = "status")]
    pub status: String,
}

/// PhaseGrantApproved records acceptance of workflow phase permission widenings (user review or --auto / --dangerously-skip-permissions). Resume skips re-prompt when Fingerprint and Grants still match.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PhaseGrantApproved {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "workflow")]
    pub workflow: String,
    #[serde(default, rename = "phase")]
    pub phase: String,
    #[serde(default, rename = "index")]
    pub index: i64,
    #[serde(default, rename = "fingerprint")]
    pub fingerprint: String,
    #[serde(default, rename = "grants", deserialize_with = "null_as_default")]
    pub grants: Vec<PhaseGrantRule>,
    #[serde(default, rename = "auto")]
    pub auto: bool,
}

/// PhaseGrantRule is one effective permission widening approved for a phase.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PhaseGrantRule {
    #[serde(default, rename = "permission")]
    pub permission: String,
    #[serde(default, rename = "pattern")]
    pub pattern: String,
    #[serde(default, rename = "action")]
    pub action: String,
}

/// PlanHandoff records that plan mode handed an exact plan identity (or a bounded legacy text plan) to the implementer. Emitted once per successful unified approval+handoff. Resume restores this so the implementer still sees the approved artifact after --continue.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PlanHandoff {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "planId")]
    pub plan_id: String,
    #[serde(default, rename = "planVersion")]
    pub plan_version: i64,
    #[serde(default, rename = "approvalSource")]
    pub approval_source: String,
    #[serde(default, rename = "title")]
    pub title: String,
    #[serde(default, rename = "agent")]
    pub agent: String,
    #[serde(default, rename = "legacyText")]
    pub legacy_text: String,
}

/// ProcessExited marks the end of a managed subprocess.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ProcessExited {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "processId")]
    pub process_id: String,
    #[serde(default, rename = "exitCode")]
    pub exit_code: i64,
    #[serde(default, rename = "status")]
    pub status: ProcessStatus,
}

/// ProcessOutput is a chunk of subprocess stdout or stderr.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ProcessOutput {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "processId")]
    pub process_id: String,
    #[serde(default, rename = "stream")]
    pub stream: String,
    #[serde(default, rename = "data")]
    pub data: String,
}

/// ProcessStarted marks the beginning of a managed subprocess (bash, hooks, …). CallID is set when the process belongs to a tool call.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ProcessStarted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "processId")]
    pub process_id: String,
    #[serde(default, rename = "callId")]
    pub call_id: String,
    #[serde(default, rename = "argv", deserialize_with = "null_as_default")]
    pub argv: Vec<String>,
    #[serde(default, rename = "cwd")]
    pub cwd: String,
}

/// PromptLayerInfo is one ordered system-prompt segment with provenance. Text content is not included — only kind/source/mode/size and an optional redacted preview suitable for logs and the TUI.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct PromptLayerInfo {
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "source")]
    pub source: String,
    #[serde(default, rename = "mode")]
    pub mode: String,
    #[serde(default, rename = "chars")]
    pub chars: i64,
    #[serde(default, rename = "estTokens")]
    pub est_tokens: i64,
    #[serde(default, rename = "pinned")]
    pub pinned: bool,
    #[serde(default, rename = "preview")]
    pub preview: String,
}

/// ProviderRetrying announces that a transient provider stream failure will be retried with a new attempt identity. Correlation identifies the failed attempt; NextAttempt is the 1-based number of the upcoming Stream call. Retries only happen at the model boundary — never after tool side effects from the failed attempt have committed. FromProvider is true when DelayMs came from a valid provider Retry-After header rather than local backoff.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ProviderRetrying {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "nextAttempt")]
    pub next_attempt: i64,
    #[serde(default, rename = "delayMs")]
    pub delay_ms: i64,
    #[serde(default, rename = "fromProvider")]
    pub from_provider: bool,
    #[serde(default, rename = "message")]
    pub message: String,
}

/// QuestionAsked suspends a tool call until a QuestionReply arrives.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct QuestionAsked {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "questions", deserialize_with = "null_as_default")]
    pub questions: Vec<QuestionPrompt>,
}

/// QuestionOption is one selectable choice on a QuestionPrompt.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct QuestionOption {
    #[serde(default, rename = "label")]
    pub label: String,
    #[serde(default, rename = "description")]
    pub description: String,
}

/// QuestionPrompt is one question in a QuestionAsked batch.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct QuestionPrompt {
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "header")]
    pub header: String,
    #[serde(default, rename = "question")]
    pub question: String,
    #[serde(default, rename = "options", deserialize_with = "null_as_default")]
    pub options: Vec<QuestionOption>,
}

/// QuestionReply resolves a pending QuestionAsked event with one answer string per prompt (same order as Questions).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct QuestionReply {
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "answers", deserialize_with = "null_as_default")]
    pub answers: Vec<String>,
}

/// QuestionResolved closes a pending QuestionAsked (answer, reject, or cancel).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct QuestionResolved {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "requestId")]
    pub request_id: String,
}

/// ReasoningDelta is a chunk of model chain-of-thought / reasoning text. Distinct from TextDelta (the final answer). Frontends may hide it behind a toggle; it must never be treated as assistant reply content.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ReasoningDelta {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "text")]
    pub text: String,
}

/// RequestTokenAttribution breaks model-facing input into slices for one stream request (or the composition that would be sent next).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct RequestTokenAttribution {
    #[serde(default, rename = "system")]
    pub system: TokenCount,
    #[serde(default, rename = "tools")]
    pub tools: TokenCount,
    #[serde(default, rename = "messages")]
    pub messages: TokenCount,
    #[serde(default, rename = "toolResults")]
    pub tool_results: TokenCount,
    #[serde(default, rename = "total")]
    pub total: TokenCount,
    #[serde(default, rename = "source")]
    pub source: String,
}

/// ResidueItem is one structured residual fact, decision, or open question retained when older history is compacted. SourceIDs cite pre-compaction history indices (hist:N), tool call ids (tool:ID), ledger entry ids (ledger:ID), or file paths (file:path).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ResidueItem {
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "text")]
    pub text: String,
    #[serde(default, rename = "confidence")]
    pub confidence: String,
    #[serde(default, rename = "freshness")]
    pub freshness: String,
    #[serde(default, rename = "sourceIds", deserialize_with = "null_as_default")]
    pub source_ids: Vec<String>,
    #[serde(default, rename = "fileRefs", deserialize_with = "null_as_default")]
    pub file_refs: Vec<String>,
    #[serde(default, rename = "ledgerId")]
    pub ledger_id: String,
}

/// Rewind removes the last completed user↔assistant turn from model-facing history. When RestoreFiles is true, also restores per-file checkpoints captured before mutating tools in that turn (never git reset --hard). Rejected while a turn is running.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct Rewind {
    #[serde(default, rename = "restoreFiles")]
    pub restore_files: bool,
}

/// RewindPoint is a fork-at-turn candidate: keep the first KeepEvents of the source log (0-based exclusive end index = keep count).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct RewindPoint {
    #[serde(default, rename = "KeepEvents")]
    pub keep_events: i64,
    #[serde(default, rename = "Turn")]
    pub turn: i64,
    #[serde(default, rename = "Preview")]
    pub preview: String,
}

/// SchedulerAdmitted marks that capacity was granted after a SchedulerQueued wait. WaitMs is time spent waiting. Engine emitters skip admitted when the acquire never blocked (unlimited/free capacity). After SchedulerCanceled for the same RequestID, Admitted must not appear.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SchedulerAdmitted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "pools", deserialize_with = "null_as_default")]
    pub pools: Vec<String>,
    #[serde(default, rename = "label")]
    pub label: String,
    #[serde(default, rename = "waitMs")]
    pub wait_ms: i64,
}

/// SchedulerCanceled marks that a waiter left without capacity (context cancel or scheduler Close). Clears any queued UI state for RequestID.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SchedulerCanceled {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "pools", deserialize_with = "null_as_default")]
    pub pools: Vec<String>,
    #[serde(default, rename = "label")]
    pub label: String,
    #[serde(default, rename = "waitMs")]
    pub wait_ms: i64,
    #[serde(default, rename = "reason")]
    pub reason: String,
}

/// SchedulerQueued marks that work is waiting for named pool capacity inside this Strike process. RequestID correlates queued → admitted|canceled. Pools lists the constrained pool names (no exact queue position — FIFO order is internal and not a stable wire guarantee). Label is a short human tag (e.g. "model", "bash", "bash:build").
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SchedulerQueued {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "requestId")]
    pub request_id: String,
    #[serde(default, rename = "pools", deserialize_with = "null_as_default")]
    pub pools: Vec<String>,
    #[serde(default, rename = "label")]
    pub label: String,
}

/// SelectAgent switches the active agent (system prompt + optional provider/model pins). Rejected while a turn is running.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SelectAgent {
    #[serde(default, rename = "name")]
    pub name: String,
}

/// SelectModel switches the active provider (and optionally model). An empty Model selects the provider's default. Rejected while a turn is running.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SelectModel {
    #[serde(default, rename = "provider")]
    pub provider: String,
    #[serde(default, rename = "model")]
    pub model: String,
}

/// SessionBudgetWarning reports session cost / per-turn token envelope progress at 50% / 80% / 100% thresholds (#577 / #542). Level 100 with Exhausted=true is the hard stop signal frontends render (paired with EngineError code budget_exhausted and TurnCompleted stopReason budget_exhausted).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionBudgetWarning {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "level")]
    pub level: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "exhausted")]
    pub exhausted: bool,
    #[serde(default, rename = "costUsd")]
    pub cost_usd: f64,
    #[serde(default, rename = "maxCostUsd")]
    pub max_cost_usd: f64,
    #[serde(default, rename = "tokensUsed")]
    pub tokens_used: i64,
    #[serde(default, rename = "maxTokens")]
    pub max_tokens: i64,
    #[serde(default, rename = "ratio")]
    pub ratio: f64,
    #[serde(default, rename = "message")]
    pub message: String,
}

/// SessionForkAtParams forks a prefix of a session log.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionForkAtParams {
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "keepEvents")]
    pub keep_events: i64,
}

/// SessionIDParams is a single-id request (get/load/replay/fork).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionIDParams {
    #[serde(default, rename = "id")]
    pub id: String,
}

/// SessionListParams is the body for session.list.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionListParams {
    #[serde(default, rename = "rootsOnly")]
    pub roots_only: bool,
}

/// SessionListResult is the body for session.list.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionListResult {
    #[serde(default, rename = "sessions", deserialize_with = "null_as_default")]
    pub sessions: Vec<SessionSummary>,
}

/// SessionLoadResult confirms a load/resume binding.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionLoadResult {
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "active")]
    pub active: bool,
    #[serde(default, rename = "note")]
    pub note: String,
}

/// SessionMeta records durable session-level metadata (e.g. a PR opened while shipping). Also written to the session sidecar by the host; the event keeps the JSONL transcript self-describing.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionMeta {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "prUrl")]
    pub pr_url: String,
    #[serde(default, rename = "prNumber")]
    pub pr_number: i64,
    #[serde(default, rename = "prState")]
    pub pr_state: String,
    #[serde(default, rename = "isolation")]
    pub isolation: String,
}

/// SessionReplayResult returns the raw JSONL event log.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionReplayResult {
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "jsonl")]
    pub jsonl: String,
}

/// SessionRewindPointsResult lists fork-at-turn candidates for a session.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionRewindPointsResult {
    #[serde(default, rename = "points", deserialize_with = "null_as_default")]
    pub points: Vec<RewindPoint>,
}

/// SessionRewound records that the last completed user↔assistant turn was dropped from model-facing history. Restore applies the same drop so --continue stays consistent. File restore is best-effort per path and is recorded here; it is not re-applied on JSONL resume (disk already changed).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionRewound {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "removed")]
    pub removed: i64,
    #[serde(default, rename = "restoreFiles")]
    pub restore_files: bool,
    #[serde(default, rename = "filesRestored")]
    pub files_restored: i64,
    #[serde(default, rename = "filesSkipped")]
    pub files_skipped: i64,
    #[serde(default, rename = "files", deserialize_with = "null_as_default")]
    pub files: Vec<String>,
    #[serde(default, rename = "uncovered", deserialize_with = "null_as_default")]
    pub uncovered: Vec<String>,
}

/// SessionSummary is the public inspect/list row for one durable session. Mirrors host.Session / HTTP session payloads without importing internal/.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionSummary {
    #[serde(default, rename = "id")]
    pub id: String,
    #[serde(default, rename = "parentId")]
    pub parent_id: String,
    #[serde(default, rename = "title")]
    pub title: String,
    #[serde(default, rename = "forkedFrom")]
    pub forked_from: String,
    #[serde(default, rename = "projectKey")]
    pub project_key: String,
    #[serde(default, rename = "open")]
    pub open: bool,
    #[serde(default, rename = "updatedAt")]
    pub updated_at: String,
    #[serde(default, rename = "eventCount")]
    pub event_count: i64,
}

/// SessionTitled records the human-readable session title. Emitted once when the first user message is accepted (derived from that text). Later emits may rename; consumers should take the last title in the log.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SessionTitled {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "title")]
    pub title: String,
}

/// SetAutonomy changes the session exit-gate policy. Rejected while a turn is running, like the other selection ops.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SetAutonomy {
    #[serde(default, rename = "mode")]
    pub mode: Autonomy,
}

/// SetContextControls updates session pin/exclude sets for system-prompt layer kinds. Empty slices clear that set when the corresponding Clear* flag is set; otherwise empty means no change for that set.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SetContextControls {
    #[serde(default, rename = "excludeKinds", deserialize_with = "null_as_default")]
    pub exclude_kinds: Vec<String>,
    #[serde(default, rename = "setExclude")]
    pub set_exclude: bool,
    #[serde(default, rename = "pinKinds", deserialize_with = "null_as_default")]
    pub pin_kinds: Vec<String>,
    #[serde(default, rename = "setPin")]
    pub set_pin: bool,
}

/// SetEffort changes the reasoning dial for subsequent turns. Rejected while a turn is running, like the other selection ops.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SetEffort {
    #[serde(default, rename = "level")]
    pub level: Effort,
}

/// SetFast toggles OpenAI priority (fast) service tier for subsequent turns. Rejected while a turn is running. Providers and models that do not support priority tier ignore the flag silently.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SetFast {
    #[serde(default, rename = "enabled")]
    pub enabled: bool,
}

/// SetPermissionMode changes the session tool-permission posture. Accepted while a turn is running so Shift+Tab / /mode can tighten or loosen asks mid-turn; the permission service applies the new posture to subsequent tool checks and rejects any in-flight permission asks.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct SetPermissionMode {
    #[serde(default, rename = "mode")]
    pub mode: PermissionMode,
}

/// StartWorkflow activates a loaded workflow at phase index 0. Exactly one workflow may be active per root session; starting replaces any prior active workflow after the target is validated. Rejected while a turn is running.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct StartWorkflow {
    #[serde(default, rename = "name")]
    pub name: String,
}

/// Steer redirects an active root turn with additional user guidance at the next safe request boundary (before the next Provider.Stream). Distinct from UserInput (which queues a later turn) and Interrupt (which cancels).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct Steer {
    #[serde(default, rename = "text")]
    pub text: String,
    #[serde(default, rename = "images", deserialize_with = "null_as_default")]
    pub images: Vec<ImageAttachment>,
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
}

/// StopWorkflow clears the active workflow phase, context, and phase permission profile. No-op when none is active. Rejected while a turn is running.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct StopWorkflow {
}

/// TeamBoardClaim claims a board task with CAS.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamBoardClaim {
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "idempotencyKey")]
    pub idempotency_key: String,
    #[serde(default, rename = "clientMutationId")]
    pub client_mutation_id: String,
    #[serde(default, rename = "taskId")]
    pub task_id: String,
    #[serde(default, rename = "expectedVersion")]
    pub expected_version: i64,
}

/// TeamBoardComplete completes a board task with CAS.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamBoardComplete {
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "idempotencyKey")]
    pub idempotency_key: String,
    #[serde(default, rename = "clientMutationId")]
    pub client_mutation_id: String,
    #[serde(default, rename = "taskId")]
    pub task_id: String,
    #[serde(default, rename = "expectedVersion")]
    pub expected_version: i64,
    #[serde(default, rename = "summary")]
    pub summary: String,
}

/// TeamBoardCreate adds a shared board task (team_task create).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamBoardCreate {
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "idempotencyKey")]
    pub idempotency_key: String,
    #[serde(default, rename = "clientMutationId")]
    pub client_mutation_id: String,
    #[serde(default, rename = "title")]
    pub title: String,
    #[serde(default, rename = "body")]
    pub body: String,
    #[serde(default, rename = "assignee")]
    pub assignee: String,
}

/// TeamBroadcast delivers lead broadcast mail (agent_broadcast semantics).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamBroadcast {
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "idempotencyKey")]
    pub idempotency_key: String,
    #[serde(default, rename = "clientMutationId")]
    pub client_mutation_id: String,
    #[serde(default, rename = "body")]
    pub body: String,
    #[serde(default, rename = "urgency")]
    pub urgency: String,
    #[serde(default, rename = "taskId")]
    pub task_id: String,
}

/// TeamChildInterrupt cancels an owned child (task_interrupt path).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamChildInterrupt {
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "idempotencyKey")]
    pub idempotency_key: String,
    #[serde(default, rename = "clientMutationId")]
    pub client_mutation_id: String,
    #[serde(default, rename = "childSessionId")]
    pub child_session_id: String,
    #[serde(default, rename = "reason")]
    pub reason: String,
}

/// TeamMessage delivers one peer mailbox message (agent_message semantics).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamMessage {
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "idempotencyKey")]
    pub idempotency_key: String,
    #[serde(default, rename = "clientMutationId")]
    pub client_mutation_id: String,
    #[serde(default, rename = "to")]
    pub to: String,
    #[serde(default, rename = "body")]
    pub body: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "urgency")]
    pub urgency: String,
    #[serde(default, rename = "taskId")]
    pub task_id: String,
}

/// TeamOpOutcome is the in-process result of a team-control Op (serve HTTP). Not an Event — never written to session JSONL.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamOpOutcome {
    #[serde(default, rename = "ok")]
    pub ok: bool,
    #[serde(default, rename = "error")]
    pub error: String,
    #[serde(default, rename = "code")]
    pub code: String,
    #[serde(default, rename = "childSessionId")]
    pub child_session_id: String,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "delegationId")]
    pub delegation_id: String,
    #[serde(default, rename = "taskId")]
    pub task_id: String,
    #[serde(default, rename = "messageId")]
    pub message_id: String,
    #[serde(default, rename = "version")]
    pub version: i64,
    #[serde(default, rename = "currentVersion")]
    pub current_version: i64,
    #[serde(default, rename = "alreadyTerminal")]
    pub already_terminal: bool,
}

/// TeamRoster is a full snapshot of the implicit session team roster. Emitted when membership or member state changes so UIs can render without calling the agent_roster tool. Correlation.SessionID is the lead id.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamRoster {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "leadId")]
    pub lead_id: String,
    #[serde(default, rename = "members", deserialize_with = "null_as_default")]
    pub members: Vec<TeamRosterMember>,
}

/// TeamRosterMember is one entry in a TeamRoster snapshot event. State prefers task_status vocabulary (starting|working|needs_attention| completed|failed|canceled|unknown) when the emitter has live detail; otherwise terminal TeamMemberState values are used. QueuePools/QueueLabel identify a constrained pool while waiting on admission (no exact queue-position guarantee).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamRosterMember {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "agent")]
    pub agent: String,
    #[serde(default, rename = "state")]
    pub state: String,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "startedAt")]
    pub started_at: String,
    #[serde(default, rename = "terminalSummary")]
    pub terminal_summary: String,
    #[serde(default, rename = "role")]
    pub role: String,
    #[serde(default, rename = "queuePools", deserialize_with = "null_as_default")]
    pub queue_pools: Vec<String>,
    #[serde(default, rename = "queueLabel")]
    pub queue_label: String,
    #[serde(default, rename = "objective")]
    pub objective: String,
    #[serde(default, rename = "lastAction")]
    pub last_action: String,
    #[serde(default, rename = "blockReason")]
    pub block_reason: String,
    #[serde(default, rename = "filesTouched", deserialize_with = "null_as_default")]
    pub files_touched: Vec<String>,
    #[serde(default, rename = "budget")]
    pub budget: Option<AgentBudgetView>,
}

/// TeamSpawn starts a child agent (task/delegate semantics) as the human lead.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamSpawn {
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "idempotencyKey")]
    pub idempotency_key: String,
    #[serde(default, rename = "clientMutationId")]
    pub client_mutation_id: String,
    #[serde(default, rename = "objective")]
    pub objective: String,
    #[serde(default, rename = "agent")]
    pub agent: String,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "isolation")]
    pub isolation: String,
    #[serde(default, rename = "budget")]
    pub budget: Option<TeamSpawnBudget>,
    #[serde(default, rename = "delegationId")]
    pub delegation_id: String,
}

/// TeamSpawnBudget is optional spawn resource bounds (wire subset).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamSpawnBudget {
    #[serde(default, rename = "maxTurns")]
    pub max_turns: i64,
    #[serde(default, rename = "maxToolCalls")]
    pub max_tool_calls: i64,
    #[serde(default, rename = "maxTokens")]
    pub max_tokens: i64,
}

/// TeamTaskTransition applies a delegation lifecycle CAS transition.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TeamTaskTransition {
    #[serde(default, rename = "rootSessionId")]
    pub root_session_id: String,
    #[serde(default, rename = "idempotencyKey")]
    pub idempotency_key: String,
    #[serde(default, rename = "clientMutationId")]
    pub client_mutation_id: String,
    #[serde(default, rename = "delegationId")]
    pub delegation_id: String,
    #[serde(default, rename = "expectedVersion")]
    pub expected_version: i64,
    #[serde(default, rename = "toState")]
    pub to_state: String,
    #[serde(default, rename = "reason")]
    pub reason: String,
}

/// TextDelta is a chunk of streaming assistant text.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TextDelta {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "text")]
    pub text: String,
}

/// TokenCount distinguishes unknown from zero: Known=false means the count was not reported; Known=true with N=0 means the vendor reported zero.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TokenCount {
    #[serde(default, rename = "n")]
    pub n: i64,
    #[serde(default, rename = "known")]
    pub known: bool,
}

#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ToolCallBegin {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "callId")]
    pub call_id: String,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "args")]
    pub args: Value,
}

#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ToolCallEnd {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "callId")]
    pub call_id: String,
    #[serde(default, rename = "title")]
    pub title: String,
    #[serde(default, rename = "output")]
    pub output: String,
    #[serde(default, rename = "isError")]
    pub is_error: bool,
    #[serde(default, rename = "errorCode")]
    pub error_code: String,
    #[serde(default, rename = "metadata")]
    pub metadata: Value,
}

/// ToolCallOutput is a chunk of streaming tool stdout/stderr while a call runs. Frontends append Data in order for a live tail; ToolCallEnd still carries the final model-facing Output (including truncation/exit suffixes).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ToolCallOutput {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "callId")]
    pub call_id: String,
    #[serde(default, rename = "data")]
    pub data: String,
}

/// ToolLoopDetected marks that the harness stopped the turn/agent path because the model repeated identical failing tool+args or oscillated between two failing calls. Reason is identical_calls | oscillating_failures.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ToolLoopDetected {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "reason")]
    pub reason: String,
    #[serde(default, rename = "toolName")]
    pub tool_name: String,
    #[serde(default, rename = "count")]
    pub count: i64,
    #[serde(default, rename = "message")]
    pub message: String,
}

/// ToolRetrying announces that a transient tool failure will be retried under the harness retry policy (safe-retry + transient/timeout only). Correlation and CallID identify the in-flight tool call; NextAttempt is 1-based. Mutative/unsafe tools never emit this event for auto-retry.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct ToolRetrying {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "callId")]
    pub call_id: String,
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "nextAttempt")]
    pub next_attempt: i64,
    #[serde(default, rename = "delayMs")]
    pub delay_ms: i64,
    #[serde(default, rename = "errorCode")]
    pub error_code: String,
    #[serde(default, rename = "message")]
    pub message: String,
}

#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TurnCompleted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "stopReason")]
    pub stop_reason: String,
    #[serde(default, rename = "files", deserialize_with = "null_as_default")]
    pub files: Vec<TurnFileChange>,
    #[serde(default, rename = "checkpointSkipped")]
    pub checkpoint_skipped: i64,
    #[serde(default, rename = "uncovered", deserialize_with = "null_as_default")]
    pub uncovered: Vec<String>,
    #[serde(default, rename = "verification")]
    pub verification: Option<VerificationReport>,
}

/// TurnFileChange is one harness-touched path in a completed turn (create / update / delete). Populated on TurnCompleted for timeline/UI; empty when the turn made no file tool mutations.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TurnFileChange {
    #[serde(default, rename = "path")]
    pub path: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
}

#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TurnStarted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
}

/// TurnSteered records that active-turn steering was accepted and how it was applied. Emitted into the session log so resume/replay can observe decisions.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct TurnSteered {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "text")]
    pub text: String,
    #[serde(default, rename = "mode")]
    pub mode: String,
    #[serde(default, rename = "targetTurnId")]
    pub target_turn_id: String,
}

/// UsageReported carries token accounting for one completed provider stream. Emitted on every EventDone that has usage (including tool-loop intermediate streams), correlated to the provider request.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct UsageReported {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "input")]
    pub input: TokenCount,
    #[serde(default, rename = "output")]
    pub output: TokenCount,
    #[serde(default, rename = "cacheRead")]
    pub cache_read: TokenCount,
    #[serde(default, rename = "cacheCreation")]
    pub cache_creation: TokenCount,
    #[serde(default, rename = "used")]
    pub used: TokenCount,
    #[serde(default, rename = "source")]
    pub source: String,
}

/// UserInput submits a user message, starting a turn.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct UserInput {
    #[serde(default, rename = "text")]
    pub text: String,
    #[serde(default, rename = "images", deserialize_with = "null_as_default")]
    pub images: Vec<ImageAttachment>,
}

/// UserMessage echoes accepted user input into the event stream so the transcript is fully reconstructable from events alone.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct UserMessage {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "text")]
    pub text: String,
    #[serde(default, rename = "images", deserialize_with = "null_as_default")]
    pub images: Vec<ImageAttachment>,
}

/// VerificationCheck is one independent gate outcome inside a VerificationReport.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct VerificationCheck {
    #[serde(default, rename = "name")]
    pub name: String,
    #[serde(default, rename = "kind")]
    pub kind: String,
    #[serde(default, rename = "value")]
    pub value: String,
    #[serde(default, rename = "passed")]
    pub passed: bool,
    #[serde(default, rename = "exitCode")]
    pub exit_code: i64,
    #[serde(default, rename = "output")]
    pub output: String,
    #[serde(default, rename = "error")]
    pub error: String,
    #[serde(default, rename = "durationMs")]
    pub duration_ms: i64,
}

/// VerificationCompleted is the harness-owned outcome of independent gates. Report.Claimed vs Report.Verified is the wire distinction between claimed_done and verified_done; model self-report text is never evidence.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct VerificationCompleted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "scope")]
    pub scope: String,
    #[serde(default, rename = "report")]
    pub report: VerificationReport,
}

/// VerificationEnv is audit metadata for a verification run (cwd, session, models).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct VerificationEnv {
    #[serde(default, rename = "workDir")]
    pub work_dir: String,
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "worktreeId")]
    pub worktree_id: String,
    #[serde(default, rename = "modelId")]
    pub model_id: String,
    #[serde(default, rename = "startedAt")]
    pub started_at: String,
    #[serde(default, rename = "finishedAt")]
    pub finished_at: String,
}

/// VerificationReport is the harness-owned outcome of independent completion gates. Distinct from CompletionHandoff.Verification (model self-report text): implementer-done is Claimed; harness-verified is Verified/Passed. Present on ChildCompleted when gates were configured at spawn (or when a solo/harness path attaches verification — shared schema with #806).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct VerificationReport {
    #[serde(default, rename = "passed")]
    pub passed: bool,
    #[serde(default, rename = "claimed")]
    pub claimed: bool,
    #[serde(default, rename = "verified")]
    pub verified: bool,
    #[serde(default, rename = "checks", deserialize_with = "null_as_default")]
    pub checks: Vec<VerificationCheck>,
    #[serde(default, rename = "env")]
    pub env: VerificationEnv,
    #[serde(default, rename = "summary")]
    pub summary: String,
    #[serde(default, rename = "durationMs")]
    pub duration_ms: i64,
}

/// VerificationStarted marks the beginning of independent completion gates. Emitted before gates run so timeline/session audit can span start→end (#790). Scope is VerificationScopeTurn or VerificationScopeChild.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct VerificationStarted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "scope")]
    pub scope: String,
    #[serde(default, rename = "gateCount")]
    pub gate_count: i64,
}

/// WaitResolved is the outcome of a wait subscription. Outcome is matched|timeout|canceled. On matched, Event is the canonical kind and TargetSessionID/Status identify the child; Handoff is set for terminal task.* outcomes when available.
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct WaitResolved {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "waitId")]
    pub wait_id: String,
    #[serde(default, rename = "outcome")]
    pub outcome: String,
    #[serde(default, rename = "event")]
    pub event: String,
    #[serde(default, rename = "targetSessionId")]
    pub target_session_id: String,
    #[serde(default, rename = "targetName")]
    pub target_name: String,
    #[serde(default, rename = "status")]
    pub status: String,
    #[serde(default, rename = "summary")]
    pub summary: String,
    #[serde(default, rename = "handoff")]
    pub handoff: CompletionHandoff,
    #[serde(default, rename = "hasHandoff")]
    pub has_handoff: bool,
}

/// WaitStarted marks an orchestration wait subscription (wait tool). Correlation is the waiting agent. TargetSessionID is an optional filter (empty = any owned child). Events lists canonical kinds (task.done|task.failed|task.canceled|task.blocked).
#[derive(Clone, Debug, Default, PartialEq, Deserialize, Serialize)]
pub struct WaitStarted {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default, rename = "turnId")]
    pub turn_id: String,
    #[serde(default, rename = "providerRequestId")]
    pub provider_request_id: String,
    #[serde(default, rename = "attempt")]
    pub attempt: i64,
    #[serde(default, rename = "parentSessionId")]
    pub parent_session_id: String,
    #[serde(default, rename = "depth")]
    pub depth: i64,
    #[serde(default, rename = "waitId")]
    pub wait_id: String,
    #[serde(default, rename = "events", deserialize_with = "null_as_default")]
    pub events: Vec<String>,
    #[serde(default, rename = "targetSessionId")]
    pub target_session_id: String,
    #[serde(default, rename = "timeoutMs")]
    pub timeout_ms: i64,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Event {
    AdmissionDecided(AdmissionDecided),
    AgentContractTimeout(AgentContractTimeout),
    AgentMessage(AgentMessage),
    AgentSelected(AgentSelected),
    ArtifactUpdated(ArtifactUpdated),
    AutonomySelected(AutonomySelected),
    ChildCompleted(ChildCompleted),
    ChildEscalated(ChildEscalated),
    ChildStarted(ChildStarted),
    CompactionCompleted(CompactionCompleted),
    CompactionStarted(CompactionStarted),
    ContextControlsSelected(ContextControlsSelected),
    ContextFitWarning(ContextFitWarning),
    DelegationChanged(DelegationChanged),
    DiagnosticBundle(DiagnosticBundle),
    EffortSelected(EffortSelected),
    EngineError(EngineError),
    FastSelected(FastSelected),
    FilesInvalidated(FilesInvalidated),
    HarnessProgress(HarnessProgress),
    HookMatched(HookMatched),
    LedgerUpdated(LedgerUpdated),
    ModelSelected(ModelSelected),
    PathOverlap(PathOverlap),
    PermissionAsked(PermissionAsked),
    PermissionDecided(PermissionDecided),
    PermissionModeSelected(PermissionModeSelected),
    PermissionResolved(PermissionResolved),
    PhaseChanged(PhaseChanged),
    PhaseGrantApproved(PhaseGrantApproved),
    PlanHandoff(PlanHandoff),
    ProcessExited(ProcessExited),
    ProcessOutput(ProcessOutput),
    ProcessStarted(ProcessStarted),
    EffectivePrompt(EffectivePrompt),
    ProviderRetrying(ProviderRetrying),
    QuestionAsked(QuestionAsked),
    QuestionResolved(QuestionResolved),
    ReasoningDelta(ReasoningDelta),
    SchedulerAdmitted(SchedulerAdmitted),
    SchedulerCanceled(SchedulerCanceled),
    SchedulerQueued(SchedulerQueued),
    SessionBudgetWarning(SessionBudgetWarning),
    SessionMeta(SessionMeta),
    SessionRewound(SessionRewound),
    SessionTitled(SessionTitled),
    TeamRoster(TeamRoster),
    TextDelta(TextDelta),
    ToolCallBegin(ToolCallBegin),
    ToolCallEnd(ToolCallEnd),
    ToolLoopDetected(ToolLoopDetected),
    ToolCallOutput(ToolCallOutput),
    ToolRetrying(ToolRetrying),
    TurnCompleted(TurnCompleted),
    TurnStarted(TurnStarted),
    TurnSteered(TurnSteered),
    UsageReported(UsageReported),
    UserMessage(UserMessage),
    VerificationCompleted(VerificationCompleted),
    VerificationStarted(VerificationStarted),
    WaitResolved(WaitResolved),
    WaitStarted(WaitStarted),
    Unknown { type_name: String, data: Value },
}

#[derive(Clone, Debug, PartialEq)]
pub enum Op {
    Compact(Compact),
    SetContextControls(SetContextControls),
    FilesChanged(FilesChanged),
    InspectDiagnosticBundle(InspectDiagnosticBundle),
    InspectEffectivePrompt(InspectEffectivePrompt),
    Interrupt(Interrupt),
    PermissionReply(PermissionReply),
    QuestionReply(QuestionReply),
    Rewind(Rewind),
    SelectAgent(SelectAgent),
    SelectModel(SelectModel),
    SetAutonomy(SetAutonomy),
    SetEffort(SetEffort),
    SetFast(SetFast),
    SetPermissionMode(SetPermissionMode),
    Steer(Steer),
    TeamBoardClaim(TeamBoardClaim),
    TeamBoardComplete(TeamBoardComplete),
    TeamBoardCreate(TeamBoardCreate),
    TeamBroadcast(TeamBroadcast),
    TeamChildInterrupt(TeamChildInterrupt),
    TeamMessage(TeamMessage),
    TeamSpawn(TeamSpawn),
    TeamTaskTransition(TeamTaskTransition),
    UserInput(UserInput),
    StartWorkflow(StartWorkflow),
    StopWorkflow(StopWorkflow),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DecodeError {
    message: String,
}

impl DecodeError {
    fn new(message: impl Into<String>) -> Self {
        Self { message: message.into() }
    }
}

impl std::fmt::Display for DecodeError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for DecodeError {}

fn null_as_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    let opt = Option::<T>::deserialize(deserializer)?;
    Ok(opt.unwrap_or_default())
}

/// Decode one event envelope. Unknown type strings succeed as
/// [`Event::Unknown`]. An empty type string, malformed JSON, or a
/// known type whose data does not match the generated struct is an error.
pub fn decode_event(raw: &str) -> Result<Event, DecodeError> {
    let env: Envelope = serde_json::from_str(raw).map_err(|err| DecodeError::new(err.to_string()))?;
    if env.r#type.is_empty() {
        return Err(DecodeError::new("empty event type"));
    }
    match env.r#type.as_str() {
        "admission.decided" => map_event::<AdmissionDecided>("admission.decided", &env.data, Event::AdmissionDecided),
        "agent.contract.timeout" => map_event::<AgentContractTimeout>("agent.contract.timeout", &env.data, Event::AgentContractTimeout),
        "agent.message" => map_event::<AgentMessage>("agent.message", &env.data, Event::AgentMessage),
        "agent.selected" => map_event::<AgentSelected>("agent.selected", &env.data, Event::AgentSelected),
        "artifact.updated" => map_event::<ArtifactUpdated>("artifact.updated", &env.data, Event::ArtifactUpdated),
        "autonomy.selected" => map_event::<AutonomySelected>("autonomy.selected", &env.data, Event::AutonomySelected),
        "child.completed" => map_event::<ChildCompleted>("child.completed", &env.data, Event::ChildCompleted),
        "child.escalated" => map_event::<ChildEscalated>("child.escalated", &env.data, Event::ChildEscalated),
        "child.started" => map_event::<ChildStarted>("child.started", &env.data, Event::ChildStarted),
        "compaction.completed" => map_event::<CompactionCompleted>("compaction.completed", &env.data, Event::CompactionCompleted),
        "compaction.started" => map_event::<CompactionStarted>("compaction.started", &env.data, Event::CompactionStarted),
        "context.controls" => map_event::<ContextControlsSelected>("context.controls", &env.data, Event::ContextControlsSelected),
        "context.fit_warning" => map_event::<ContextFitWarning>("context.fit_warning", &env.data, Event::ContextFitWarning),
        "delegation.changed" => map_event::<DelegationChanged>("delegation.changed", &env.data, Event::DelegationChanged),
        "diagnostic.bundle" => map_event::<DiagnosticBundle>("diagnostic.bundle", &env.data, Event::DiagnosticBundle),
        "effort.selected" => map_event::<EffortSelected>("effort.selected", &env.data, Event::EffortSelected),
        "engine.error" => map_event::<EngineError>("engine.error", &env.data, Event::EngineError),
        "fast.selected" => map_event::<FastSelected>("fast.selected", &env.data, Event::FastSelected),
        "files.invalidated" => map_event::<FilesInvalidated>("files.invalidated", &env.data, Event::FilesInvalidated),
        "harness.progress" => map_event::<HarnessProgress>("harness.progress", &env.data, Event::HarnessProgress),
        "hook.matched" => map_event::<HookMatched>("hook.matched", &env.data, Event::HookMatched),
        "ledger.updated" => map_event::<LedgerUpdated>("ledger.updated", &env.data, Event::LedgerUpdated),
        "model.selected" => map_event::<ModelSelected>("model.selected", &env.data, Event::ModelSelected),
        "path.overlap" => map_event::<PathOverlap>("path.overlap", &env.data, Event::PathOverlap),
        "permission.asked" => map_event::<PermissionAsked>("permission.asked", &env.data, Event::PermissionAsked),
        "permission.decided" => map_event::<PermissionDecided>("permission.decided", &env.data, Event::PermissionDecided),
        "permission.mode" => map_event::<PermissionModeSelected>("permission.mode", &env.data, Event::PermissionModeSelected),
        "permission.resolved" => map_event::<PermissionResolved>("permission.resolved", &env.data, Event::PermissionResolved),
        "phase.changed" => map_event::<PhaseChanged>("phase.changed", &env.data, Event::PhaseChanged),
        "phase.grant_approved" => map_event::<PhaseGrantApproved>("phase.grant_approved", &env.data, Event::PhaseGrantApproved),
        "plan.handoff" => map_event::<PlanHandoff>("plan.handoff", &env.data, Event::PlanHandoff),
        "process.exited" => map_event::<ProcessExited>("process.exited", &env.data, Event::ProcessExited),
        "process.output" => map_event::<ProcessOutput>("process.output", &env.data, Event::ProcessOutput),
        "process.started" => map_event::<ProcessStarted>("process.started", &env.data, Event::ProcessStarted),
        "prompt.effective" => map_event::<EffectivePrompt>("prompt.effective", &env.data, Event::EffectivePrompt),
        "provider.retrying" => map_event::<ProviderRetrying>("provider.retrying", &env.data, Event::ProviderRetrying),
        "question.asked" => map_event::<QuestionAsked>("question.asked", &env.data, Event::QuestionAsked),
        "question.resolved" => map_event::<QuestionResolved>("question.resolved", &env.data, Event::QuestionResolved),
        "reasoning.delta" => map_event::<ReasoningDelta>("reasoning.delta", &env.data, Event::ReasoningDelta),
        "scheduler.admitted" => map_event::<SchedulerAdmitted>("scheduler.admitted", &env.data, Event::SchedulerAdmitted),
        "scheduler.canceled" => map_event::<SchedulerCanceled>("scheduler.canceled", &env.data, Event::SchedulerCanceled),
        "scheduler.queued" => map_event::<SchedulerQueued>("scheduler.queued", &env.data, Event::SchedulerQueued),
        "session.budget_warning" => map_event::<SessionBudgetWarning>("session.budget_warning", &env.data, Event::SessionBudgetWarning),
        "session.meta" => map_event::<SessionMeta>("session.meta", &env.data, Event::SessionMeta),
        "session.rewound" => map_event::<SessionRewound>("session.rewound", &env.data, Event::SessionRewound),
        "session.titled" => map_event::<SessionTitled>("session.titled", &env.data, Event::SessionTitled),
        "team.roster" => map_event::<TeamRoster>("team.roster", &env.data, Event::TeamRoster),
        "text.delta" => map_event::<TextDelta>("text.delta", &env.data, Event::TextDelta),
        "tool.begin" => map_event::<ToolCallBegin>("tool.begin", &env.data, Event::ToolCallBegin),
        "tool.end" => map_event::<ToolCallEnd>("tool.end", &env.data, Event::ToolCallEnd),
        "tool.loop_detected" => map_event::<ToolLoopDetected>("tool.loop_detected", &env.data, Event::ToolLoopDetected),
        "tool.output" => map_event::<ToolCallOutput>("tool.output", &env.data, Event::ToolCallOutput),
        "tool.retrying" => map_event::<ToolRetrying>("tool.retrying", &env.data, Event::ToolRetrying),
        "turn.completed" => map_event::<TurnCompleted>("turn.completed", &env.data, Event::TurnCompleted),
        "turn.started" => map_event::<TurnStarted>("turn.started", &env.data, Event::TurnStarted),
        "turn.steered" => map_event::<TurnSteered>("turn.steered", &env.data, Event::TurnSteered),
        "usage.reported" => map_event::<UsageReported>("usage.reported", &env.data, Event::UsageReported),
        "user.message" => map_event::<UserMessage>("user.message", &env.data, Event::UserMessage),
        "verification.completed" => map_event::<VerificationCompleted>("verification.completed", &env.data, Event::VerificationCompleted),
        "verification.started" => map_event::<VerificationStarted>("verification.started", &env.data, Event::VerificationStarted),
        "wait.resolved" => map_event::<WaitResolved>("wait.resolved", &env.data, Event::WaitResolved),
        "wait.started" => map_event::<WaitStarted>("wait.started", &env.data, Event::WaitStarted),
        other => {
            let data = if env.data.is_null() {
                serde_json::json!({})
            } else {
                env.data
            };
            Ok(Event::Unknown { type_name: other.to_string(), data })
        }
    }
}

fn map_event<T>(wire: &str, data: &Value, wrap: fn(T) -> Event) -> Result<Event, DecodeError>
where
    T: DeserializeOwned,
{
    if data.is_null() {
        return Err(DecodeError::new(format!("event {wire} missing data")));
    }
    let value: T = serde_json::from_value(data.clone()).map_err(|err| DecodeError::new(format!("event {wire}: {err}")))?;
    Ok(wrap(value))
}

/// Decode one op envelope. Unknown type strings are errors.
/// Missing `data` decodes as an empty object so dataless ops
/// (`interrupt`, inspect, workflow.stop) still parse.
pub fn decode_op(raw: &str) -> Result<Op, DecodeError> {
    let env: OpEnvelope = serde_json::from_str(raw).map_err(|err| DecodeError::new(err.to_string()))?;
    if env.r#type.is_empty() {
        return Err(DecodeError::new("empty op type"));
    }
    let data = if env.data.is_null() {
        serde_json::json!({})
    } else {
        env.data
    };
    match env.r#type.as_str() {
        "compact" => map_op::<Compact>("compact", data.clone(), Op::Compact),
        "context.controls" => map_op::<SetContextControls>("context.controls", data.clone(), Op::SetContextControls),
        "files.changed" => map_op::<FilesChanged>("files.changed", data.clone(), Op::FilesChanged),
        "inspect.diagnostic" => map_op::<InspectDiagnosticBundle>("inspect.diagnostic", data.clone(), Op::InspectDiagnosticBundle),
        "inspect.prompt" => map_op::<InspectEffectivePrompt>("inspect.prompt", data.clone(), Op::InspectEffectivePrompt),
        "interrupt" => map_op::<Interrupt>("interrupt", data.clone(), Op::Interrupt),
        "permission.reply" => map_op::<PermissionReply>("permission.reply", data.clone(), Op::PermissionReply),
        "question.reply" => map_op::<QuestionReply>("question.reply", data.clone(), Op::QuestionReply),
        "rewind" => map_op::<Rewind>("rewind", data.clone(), Op::Rewind),
        "select.agent" => map_op::<SelectAgent>("select.agent", data.clone(), Op::SelectAgent),
        "select.model" => map_op::<SelectModel>("select.model", data.clone(), Op::SelectModel),
        "set.autonomy" => map_op::<SetAutonomy>("set.autonomy", data.clone(), Op::SetAutonomy),
        "set.effort" => map_op::<SetEffort>("set.effort", data.clone(), Op::SetEffort),
        "set.fast" => map_op::<SetFast>("set.fast", data.clone(), Op::SetFast),
        "set.permission_mode" => map_op::<SetPermissionMode>("set.permission_mode", data.clone(), Op::SetPermissionMode),
        "steer" => map_op::<Steer>("steer", data.clone(), Op::Steer),
        "team.board_claim" => map_op::<TeamBoardClaim>("team.board_claim", data.clone(), Op::TeamBoardClaim),
        "team.board_complete" => map_op::<TeamBoardComplete>("team.board_complete", data.clone(), Op::TeamBoardComplete),
        "team.board_create" => map_op::<TeamBoardCreate>("team.board_create", data.clone(), Op::TeamBoardCreate),
        "team.broadcast" => map_op::<TeamBroadcast>("team.broadcast", data.clone(), Op::TeamBroadcast),
        "team.child_interrupt" => map_op::<TeamChildInterrupt>("team.child_interrupt", data.clone(), Op::TeamChildInterrupt),
        "team.message" => map_op::<TeamMessage>("team.message", data.clone(), Op::TeamMessage),
        "team.spawn" => map_op::<TeamSpawn>("team.spawn", data.clone(), Op::TeamSpawn),
        "team.task_transition" => map_op::<TeamTaskTransition>("team.task_transition", data.clone(), Op::TeamTaskTransition),
        "user.input" => map_op::<UserInput>("user.input", data.clone(), Op::UserInput),
        "workflow.start" => map_op::<StartWorkflow>("workflow.start", data.clone(), Op::StartWorkflow),
        "workflow.stop" => map_op::<StopWorkflow>("workflow.stop", data.clone(), Op::StopWorkflow),
        other => Err(DecodeError::new(format!("unknown op type {other}"))),
    }
}

fn map_op<T>(wire: &str, data: Value, wrap: fn(T) -> Op) -> Result<Op, DecodeError>
where
    T: DeserializeOwned,
{
    let value: T = serde_json::from_value(data).map_err(|err| DecodeError::new(format!("op {wire}: {err}")))?;
    Ok(wrap(value))
}
