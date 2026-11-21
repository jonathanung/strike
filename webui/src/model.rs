//! Cockpit view models: HTTP projections and UI state.
//! Op/Event wire types are generated in the `protocol-schema` crate from
//! `pkg/protocol` (`schemas/protocol/v1/schema.json`). Do not hand-copy them
//! here. Unknown JSON fields on these view models stay ignored.

use std::collections::HashMap;

use serde::Deserialize;

#[derive(Clone, Debug, Deserialize)]
pub struct Health {
    pub ok: bool,
    pub version: String,
    /// Unix milliseconds from the daemon. The client records its own receipt
    /// time for the stale check, so this field is the wire clock, not a signal.
    #[allow(dead_code)]
    #[serde(default)]
    pub now: i64,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Workspace {
    pub id: String,
    pub name: String,
    pub path: String,
    #[serde(default)]
    pub rules: String,
    #[serde(default, rename = "permissionMode")]
    pub permission_mode: String,
    #[serde(default, rename = "extraRoots")]
    pub extra_roots: Vec<String>,
    /// Present on the wire. Recents are already ordered by this field.
    #[allow(dead_code)]
    #[serde(default, rename = "lastOpened")]
    pub last_opened: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct WorkspacesResponse {
    pub workspaces: Vec<Workspace>,
    #[serde(default, rename = "activeId")]
    pub active_id: String,
    #[serde(default)]
    pub recents: Vec<Workspace>,
    /// Allowlist echoed by the daemon. The picker loads it through browse.
    #[allow(dead_code)]
    #[serde(default, rename = "browseRoots")]
    pub browse_roots: Vec<String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct BrowseEntry {
    pub name: String,
    pub path: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct BrowseResponse {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub parent: String,
    #[serde(default)]
    pub entries: Vec<BrowseEntry>,
}

#[derive(Clone, Debug, Deserialize, serde::Serialize)]
pub struct Remote {
    pub name: String,
    pub url: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct RootSummary {
    pub id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub busy: bool,
    #[serde(default, rename = "createdAt")]
    pub created_at: i64,
    /// Canonical status id. Empty when the daemon did not send one.
    #[serde(default)]
    pub status: String,
    #[serde(default, rename = "statusSince")]
    pub status_since: i64,
    #[serde(default, rename = "lastActivityAt")]
    pub last_activity_at: i64,
    #[serde(default, rename = "permissionPending")]
    pub permission_pending: bool,
    #[serde(default, rename = "questionPending")]
    pub question_pending: bool,
    /// Last approval outcome this turn (allowed | denied | expired), #1387.
    #[serde(default, rename = "askOutcome")]
    pub ask_outcome: String,
    /// `None` when the field is absent (older daemon). Present `false` is read.
    #[serde(default)]
    pub unread: Option<bool>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct RootsResponse {
    #[serde(default)]
    pub roots: Vec<RootSummary>,
    #[serde(default, rename = "activeId")]
    pub active_id: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct SessionItem {
    pub id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub mtime: i64,
    #[serde(default, rename = "workspaceId")]
    pub workspace_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default, rename = "lastActivityAt")]
    pub last_activity_at: i64,
    #[serde(default)]
    pub unread: Option<bool>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ChildSession {
    pub id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default, rename = "parentId")]
    pub parent_id: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ChildrenResponse {
    #[serde(default)]
    pub sessions: Vec<ChildSession>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct LiveStatus {
    #[serde(default)]
    pub provider: String,
    #[serde(default)]
    pub model: String,
    #[serde(default, rename = "permissionMode")]
    pub permission_mode: String,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub effort: String,
    /// The running agent (#1563), from the daemon's last `agent.selected`.
    /// Empty when not reported; the chip then says unknown, never a guess.
    #[serde(default)]
    pub agent: String,
    /// Session exit-gate policy the daemon reports on every status
    /// (#1572). Empty until the daemon reports one; the UI normalizes
    /// empty to the `supervised` default rather than inventing a mode.
    #[serde(default)]
    pub autonomy: String,
    /// Active workflow and phase the daemon reports on every status
    /// (#1574; `StatusSnapshot.Workflow` / `.Phase`). Both empty when no
    /// workflow is active. Later changes arrive as `phase.changed`.
    #[serde(default)]
    pub workflow: String,
    #[serde(default)]
    pub phase: String,
    #[serde(default)]
    pub busy: bool,
    #[serde(default, rename = "contextUsed")]
    pub context_used: i64,
    #[serde(default, rename = "contextLimit")]
    pub context_limit: i64,
    #[serde(default, rename = "contextFit")]
    pub context_fit: String,
    /// Wire fields from `GET /v1/status`. The shell classifies from roots.
    #[allow(dead_code)]
    #[serde(default, rename = "turnState")]
    pub turn_state: String,
    #[allow(dead_code)]
    #[serde(default, rename = "turnSince")]
    pub turn_since: i64,
    #[allow(dead_code)]
    #[serde(default, rename = "lastEventAt")]
    pub last_event_at: i64,
}

/// One row of `GET /v1/status/sessions`. The shell applies SSE payloads as
/// JSON and reads gateway from [`StatusBoard`], so these fields stay on the
/// type for the contract.
#[allow(dead_code)]
#[derive(Clone, Debug, Deserialize)]
pub struct BoardSession {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub unread: bool,
    #[serde(default)]
    pub since: i64,
    #[serde(default, rename = "lastActivityAt")]
    pub last_activity_at: i64,
}

#[derive(Clone, Debug, Deserialize)]
pub struct StatusBoard {
    #[serde(default)]
    pub gateway: String,
    #[allow(dead_code)]
    #[serde(default)]
    pub now: i64,
    #[allow(dead_code)]
    #[serde(default, rename = "heartbeatStaleMs")]
    pub heartbeat_stale_ms: i64,
    #[allow(dead_code)]
    #[serde(default)]
    pub sessions: Vec<BoardSession>,
}

/// One selectable agent from bootstrap `agents` (#1563). The daemon's
/// `AgentInfo`; `description` is omitted when empty.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct AgentInfo {
    pub name: String,
    #[serde(default)]
    pub description: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
pub struct SkillInfo {
    pub name: String,
    #[serde(default)]
    pub description: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
pub struct ComposerCommand {
    pub id: String,
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub detail: String,
    #[serde(default)]
    pub insert: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct FileSearchResponse {
    #[serde(default)]
    pub paths: Vec<String>,
}

/// Bootstrap capability flags the cockpit reads. Unknown flags stay
/// ignored; a missing object parses as every capability off.
#[derive(Clone, Debug, Default, Deserialize)]
pub struct BootstrapCapabilities {
    /// True when `GET /v1/workspace/trust` reports the workspace trust
    /// state and the repo config it gates (#1781).
    #[serde(default, rename = "workspaceTrust")]
    pub workspace_trust: bool,
    /// True when a live engine can build a diagnostic bundle (#1489):
    /// the daemon sets it from the same live gate as `GET /v1/diag`.
    #[serde(default)]
    pub diag: bool,
    /// True when the daemon serves the read-only decision ledger
    /// (`GET /v1/ledger`, #1485); gates the Decisions storage tab.
    #[serde(default)]
    pub ledger: bool,
    /// True when the daemon serves the project-local issues tracker
    /// (`/v1/issues*`, #1500); gates the Issues storage tab.
    #[serde(default)]
    pub issues: bool,
    /// True when `/v1/init` can write AGENTS.md (#1581): host init service
    /// present, live engine, and not `--read-only`.
    #[serde(default, rename = "projectInit")]
    pub project_init: bool,
    /// True when the daemon wires plugin lifecycle (`/v1/plugins*`, #1570).
    #[serde(default)]
    pub plugins: bool,
    /// True when the daemon serves the typed config-source list
    /// (`GET /v1/config-sources`, #1493).
    #[serde(default, rename = "configFiles")]
    pub config_files: bool,
    /// True when `GET /v1/update` can run the release probe (#1583).
    #[serde(default, rename = "updateCheck")]
    pub update_check: bool,
    /// True when `/v1/sessions/{id}/loops` can start and stop session
    /// loops (#1588): live engine and not `--read-only`.
    #[serde(default)]
    pub loops: bool,
}

/// One `GET /v1/config-sources` row (#1493). The daemon withholds absolute
/// host paths; `display` is the only location the cockpit may show.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq, Hash)]
pub struct ConfigSource {
    /// Primary slot id: config | mcp | providers | keybinds. Empty for extras.
    #[serde(default)]
    pub slot: String,
    /// Extra-file group: agents | skills | themes | workflows. Empty for primary.
    #[serde(default)]
    pub kind: String,
    /// global | project.
    #[serde(default)]
    pub scope: String,
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub display: String,
    #[serde(default)]
    pub exists: bool,
    #[serde(default, rename = "canCreate")]
    pub can_create: bool,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct ConfigSourcesReply {
    #[serde(default)]
    pub sources: Vec<ConfigSource>,
}

impl ConfigSource {
    /// "Global" / "Project", or the daemon's word for any other scope.
    pub fn scope_label(&self) -> String {
        match self.scope.as_str() {
            "global" => "Global".into(),
            "project" => "Project".into(),
            "" => "Unknown scope".into(),
            other => other.to_string(),
        }
    }

    /// What the row is: the primary slot, or the extra-file group.
    pub fn kind_label(&self) -> String {
        match (self.slot.as_str(), self.kind.as_str()) {
            ("", "") => "file".into(),
            ("", kind) => format!("{kind} file"),
            (slot, _) => format!("{slot} slot"),
        }
    }

    /// Present / not present, with the create fact stated as text only:
    /// slice 1 never creates files.
    pub fn state_label(&self) -> &'static str {
        match (self.exists, self.can_create) {
            (true, _) => "Present",
            (false, true) => "Not present (the daemon can create it; not from here)",
            (false, false) => "Not present",
        }
    }

    /// The row's name. Falls back to `display` so a row is never blank.
    pub fn title(&self) -> &str {
        if self.label.trim().is_empty() {
            &self.display
        } else {
            &self.label
        }
    }
}

/// One `/v1/init` reply (#1581). `exists` with a non-empty `error` is the
/// daemon's 409 refusal; the browser never overwrites AGENTS.md.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct ProjectInitReply {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub exists: bool,
    #[serde(default)]
    pub created: bool,
    #[serde(default)]
    pub error: String,
}

/// Reads a `/v1/init` response. 2xx and the 409 already-exists refusal are
/// both states to render; anything else is the daemon's error sentence.
pub fn project_init_outcome(status: u16, body: &str) -> Result<ProjectInitReply, String> {
    if (200..300).contains(&status) || status == 409 {
        let mut reply: ProjectInitReply = serde_json::from_str(body).unwrap_or_default();
        if status == 409 {
            reply.exists = true;
            if reply.error.trim().is_empty() {
                reply.error = "AGENTS.md already exists".to_string();
            }
        }
        return Ok(reply);
    }
    Err(diag_error_message(status, body))
}

/// One repo-supplied process or endpoint in the trust enumeration. Env,
/// headers and keys are never sent by the daemon.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct TrustExecutable {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default)]
    pub url: String,
}

impl TrustExecutable {
    /// `name: command args…`, or `name: url` for an endpoint-only entry.
    pub fn line(&self) -> String {
        let target = if self.command.is_empty() && !self.url.is_empty() {
            self.url.clone()
        } else {
            let mut parts = vec![self.command.clone()];
            parts.extend(self.args.iter().cloned());
            parts.join(" ").trim().to_string()
        };
        if target.is_empty() {
            self.name.clone()
        } else {
            format!("{}: {}", self.name, target)
        }
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct TrustHook {
    #[serde(default)]
    pub event: String,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    pub action: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct TrustFile {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub path: String,
}

/// `config.ProjectSurfaces` as the daemon serializes it: what the repo
/// would load once trusted, enumerated without loading any of it.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct TrustSurfaces {
    #[serde(default, rename = "configFiles")]
    pub config_files: Vec<String>,
    #[serde(default, rename = "configKeys")]
    pub config_keys: Vec<String>,
    #[serde(default, rename = "permissionRules")]
    pub permission_rules: u32,
    #[serde(default)]
    pub mcp: Vec<TrustExecutable>,
    #[serde(default)]
    pub lsp: Vec<TrustExecutable>,
    #[serde(default)]
    pub harnesses: Vec<TrustExecutable>,
    #[serde(default)]
    pub providers: Vec<TrustExecutable>,
    #[serde(default)]
    pub hooks: Vec<TrustHook>,
    #[serde(default)]
    pub agents: Vec<TrustFile>,
    #[serde(default)]
    pub skills: Vec<TrustFile>,
    #[serde(default)]
    pub workflows: Vec<TrustFile>,
    #[serde(default)]
    pub plugins: Vec<String>,
    #[serde(default)]
    pub steering: Vec<String>,
    #[serde(default)]
    pub errors: Vec<String>,
}

/// One titled group of lines on the trust card.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TrustSection {
    pub title: &'static str,
    pub lines: Vec<String>,
}

impl TrustSurfaces {
    /// The card's groups in the same order and wording as
    /// `strike trust status`; empty groups are dropped.
    pub fn sections(&self) -> Vec<TrustSection> {
        let execs = |items: &[TrustExecutable]| items.iter().map(|e| e.line()).collect::<Vec<_>>();
        let files = |items: &[TrustFile]| {
            items
                .iter()
                .map(|f| format!("{} ({})", f.name, f.path))
                .collect::<Vec<_>>()
        };
        let mut config = Vec::new();
        if !self.config_keys.is_empty() {
            config.push(format!("overrides: {}", self.config_keys.join(", ")));
        }
        if self.permission_rules > 0 {
            config.push(format!("permission rules: {}", self.permission_rules));
        }
        config.extend(self.config_files.iter().cloned());
        let hooks = self
            .hooks
            .iter()
            .map(|h| {
                if h.command.is_empty() {
                    format!("{}: {} (rule)", h.event, h.action)
                } else {
                    format!("{}: {}", h.event, h.command)
                }
            })
            .collect::<Vec<_>>();
        let groups = vec![
            TrustSection { title: "Config", lines: config },
            TrustSection { title: "MCP servers", lines: execs(&self.mcp[..]) },
            TrustSection { title: "LSP servers", lines: execs(&self.lsp[..]) },
            TrustSection { title: "Harnesses", lines: execs(&self.harnesses[..]) },
            TrustSection { title: "Providers", lines: execs(&self.providers[..]) },
            TrustSection { title: "Hooks", lines: hooks },
            TrustSection { title: "Agents", lines: files(&self.agents[..]) },
            TrustSection { title: "Skills", lines: files(&self.skills[..]) },
            TrustSection { title: "Workflows", lines: files(&self.workflows[..]) },
            TrustSection { title: "Plugins", lines: self.plugins.clone() },
            TrustSection { title: "Steering", lines: self.steering.clone() },
            TrustSection { title: "Unreadable", lines: self.errors.clone() },
        ];
        groups.into_iter().filter(|g| !g.lines.is_empty()).collect()
    }
}

/// One `GET/POST /v1/workspace/trust` reply (#1781).
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct WorkspaceTrustReply {
    #[serde(default)]
    pub root: String,
    #[serde(default)]
    pub trusted: bool,
    #[serde(default, rename = "trustedAt")]
    pub trusted_at: String,
    #[serde(default)]
    pub untrustable: bool,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub surfaces: TrustSurfaces,
    #[serde(default)]
    pub mutable: bool,
    #[serde(default, rename = "restartRequired")]
    pub restart_required: bool,
    #[serde(default)]
    pub error: String,
}

impl WorkspaceTrustReply {
    /// The persistent "Untrusted workspace" badge shows only when the repo
    /// actually ships config that trust would enable.
    pub fn badge_visible(&self) -> bool {
        !self.trusted && !self.surfaces.sections().is_empty()
    }
}

/// Reads a `/v1/workspace/trust` response. 2xx and the 422 untrustable
/// refusal (which still carries the status body) are states to render;
/// anything else is the daemon's error sentence.
pub fn workspace_trust_outcome(status: u16, body: &str) -> Result<WorkspaceTrustReply, String> {
    if (200..300).contains(&status) || status == 422 {
        let mut reply: WorkspaceTrustReply =
            serde_json::from_str(body).map_err(|e| e.to_string())?;
        if status == 422 && reply.error.trim().is_empty() {
            reply.error = "workspace root cannot be trusted".to_string();
        }
        return Ok(reply);
    }
    Err(diag_error_message(status, body))
}

#[derive(Clone, Debug, Deserialize)]
pub struct Bootstrap {
    #[serde(default)]
    pub status: Option<LiveStatus>,
    #[serde(default)]
    pub skills: Vec<SkillInfo>,
    /// Selectable agents for the live session (#1563), default first.
    #[serde(default)]
    pub agents: Vec<AgentInfo>,
    #[serde(default, rename = "composerCommands")]
    pub composer_commands: Vec<ComposerCommand>,
    #[serde(default)]
    pub capabilities: BootstrapCapabilities,
}

/// The filename a `/v1/diag` download is saved under: the server's
/// `Content-Disposition` filename when it sends one (#1489 — the daemon
/// chooses `strike-diag-<session>-<timestamp>.json`), never a name the
/// client invents while a server name exists. Falls back to a generic
/// Strike name only when the header is absent or unparseable.
pub fn diag_filename(content_disposition: Option<&str>) -> String {
    if let Some(header) = content_disposition {
        for part in header.split(';') {
            let part = part.trim();
            let Some(rest) = part
                .strip_prefix("filename=")
                .or_else(|| part.strip_prefix("Filename="))
            else {
                continue;
            };
            let name = rest.trim().trim_matches('"').trim();
            // A header value must never become a path on the client.
            let name = name.rsplit(['/', '\\']).next().unwrap_or("").trim();
            if !name.is_empty() {
                return name.to_string();
            }
        }
    }
    "strike-diag.json".to_string()
}

/// The daemon's own error wording from a `/v1/diag` error body
/// (`{"error": "…"}`), verbatim when present — the 503 capability message
/// is the daemon's sentence, not the cockpit's paraphrase (#1489).
pub fn diag_error_message(status: u16, body: &str) -> String {
    if let Ok(v) = serde_json::from_str::<serde_json::Value>(body) {
        if let Some(msg) = v.get("error").and_then(|e| e.as_str()) {
            if !msg.trim().is_empty() {
                return msg.to_string();
            }
        }
    }
    let body = body.trim();
    if body.is_empty() {
        format!("HTTP {status}")
    } else {
        format!("HTTP {status} {body}")
    }
}

#[derive(Clone, Debug, Deserialize)]
pub struct ModelInfo {
    pub id: String,
    #[serde(default)]
    pub provider: String,
    #[serde(default)]
    pub name: String,
    /// Context window tokens from the catalog. 0 means not reported.
    #[serde(default)]
    pub context: i64,
    #[serde(default)]
    pub output: i64,
    #[serde(default, rename = "inputCost")]
    pub input_cost: f64,
    #[serde(default, rename = "outputCost")]
    pub output_cost: f64,
    #[serde(default, rename = "hasCost")]
    pub has_cost: bool,
    /// True only when the catalog reports the capability. False means
    /// unknown or unsupported (#1054); the menu omits it.
    #[serde(default, rename = "toolCall")]
    pub tool_call: bool,
    #[serde(default)]
    pub reasoning: bool,
    #[serde(default)]
    pub attachment: bool,
    #[serde(default, rename = "variantIds")]
    pub variant_ids: Vec<String>,
    /// catalog | config | merge. Deserialized so the wire field is not dropped.
    #[serde(default)]
    #[allow(dead_code)]
    pub source: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ModelList {
    #[serde(default)]
    pub models: Vec<ModelInfo>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct DirEntry {
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "IsDir")]
    pub is_dir: bool,
}

#[derive(Clone, Debug, Deserialize)]
pub struct FilesResponse {
    #[serde(default)]
    pub entries: Vec<DirEntry>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ChangedFile {
    pub path: String,
    #[serde(default)]
    pub added: i64,
    #[serde(default)]
    pub deleted: i64,
    /// Unified diff from GET /v1/changed-files. The Git panel reuses it.
    #[serde(default)]
    pub diff: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ChangedFilesResponse {
    #[serde(default)]
    pub files: Vec<ChangedFile>,
}

/// One row in the session list. Pins stay in the browser. Unread prefers the
/// daemon field when the payload includes it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SessionRow {
    pub id: String,
    pub title: String,
    pub mtime: i64,
    pub busy: bool,
    pub live: bool,
    pub pinned: bool,
    pub unread: bool,
    /// Registry id of the workspace this chat is bound to. Empty when unbound.
    pub workspace_id: String,
    /// Canonical status id. Empty only before classification.
    pub status: String,
    /// Live root is waiting on an approval (#1451). Carried from
    /// [`RootSummary`] so the session list can label the waiting kind.
    pub permission_pending: bool,
    /// Live root is waiting on an answer to an agent question (#1451).
    pub question_pending: bool,
    /// Last approval outcome this turn (allowed | denied | expired), #1387.
    pub ask_outcome: String,
    /// Unix milliseconds when `status` began.
    pub status_since: i64,
    /// Unix milliseconds of the last user-visible activity. 0 when unknown.
    pub last_activity_at: i64,
}

#[derive(Clone, Copy, Debug)]
pub struct ModeChoice {
    pub id: &'static str,
    pub label: &'static str,
    pub detail: &'static str,
}

#[derive(Clone, Debug, Deserialize)]
pub struct SessionsResponse {
    #[serde(default)]
    pub sessions: Vec<SessionItem>,
    #[serde(default, rename = "liveId")]
    #[allow(dead_code)]
    pub live_id: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct TeamMember {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub agent: String,
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub role: String,
    #[serde(default, rename = "terminalSummary")]
    pub terminal_summary: String,
    #[serde(default)]
    pub objective: String,
    #[serde(default, rename = "blockReason")]
    pub block_reason: String,
    #[serde(default)]
    pub budget: Option<serde_json::Map<String, serde_json::Value>>,
    #[serde(default)]
    pub terminal: bool,
    /// Latest `child.escalated` the daemon recorded for this member (#1375).
    #[serde(default)]
    pub escalation: Option<TeamEscalation>,
}

/// One child budget/stall/loop trip, as the daemon's team snapshot keeps it.
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct TeamEscalation {
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub action: String,
    #[serde(default, rename = "terminalStatus")]
    #[allow(dead_code)]
    pub terminal_status: String,
}

/// One inter-agent message from the daemon's bounded team snapshot
/// (`TeamSnapshot.messages`, already redacted daemon-side).
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct TeamMessage {
    #[serde(default, rename = "messageId")]
    pub message_id: String,
    #[serde(default)]
    pub from: String,
    #[serde(default)]
    pub to: String,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub urgency: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default, rename = "taskId")]
    #[allow(dead_code)]
    pub task_id: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct TeamSnapshot {
    #[serde(default)]
    pub available: bool,
    #[serde(default, rename = "unavailableReason")]
    pub unavailable_reason: String,
    #[serde(default, rename = "leadId")]
    pub lead_id: String,
    #[serde(default)]
    pub members: HashMap<String, TeamMember>,
    /// Recent inter-agent mail, oldest first (#1375). The daemon sends
    /// `null` for an empty list, which reads as empty here.
    #[serde(default, deserialize_with = "null_as_empty")]
    pub messages: Vec<TeamMessage>,
}

fn null_as_empty<'de, D, T>(d: D) -> Result<Vec<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    Ok(Option::<Vec<T>>::deserialize(d)?.unwrap_or_default())
}

#[derive(Clone, Debug)]
pub struct ChatLine {
    #[allow(dead_code)]
    pub id: u32,
    pub role: String,
    pub title: String,
    pub body: String,
    pub badge: String,
    /// Collapsed detail for a child or protocol card. Empty for prose.
    pub detail: String,
    /// Dedupe key for child cards (name, or session id).
    pub key: String,
    /// Envelope time of the event that created this line, epoch ms.
    /// 0 means unknown (old log line without a parseable `time`).
    /// Carried for transcript consumers; the readable pane stamps its own
    /// `Message::at_ms`.
    #[allow(dead_code)]
    pub at_ms: f64,
}

#[derive(Clone, Debug)]
pub struct ActivityRow {
    pub session_id: String,
    pub name: String,
    pub agent: String,
    pub task: String,
    pub state: String,
    pub elapsed_s: i64,
    pub started_ms: f64,
    pub expanded: bool,
    /// Agent-written terminal summary, kept apart for the status card (#1397).
    pub summary: String,
    /// Agent-written block reason, kept apart for the status card (#1397).
    pub block_reason: String,
    /// Engine budget counters; `None` when the daemon sent no budget.
    pub budget: Option<status_card::Budget>,
}

pub fn display_state(m: &TeamMember) -> String {
    if !m.terminal && budget_flag(m, "stall") {
        return "stalled".into();
    }
    match m.state.to_ascii_lowercase().as_str() {
        "completed" | "done" => "done".into(),
        "failed" => "failed".into(),
        "canceled" | "cancelled" => "stopped".into(),
        "blocked" => "blocked".into(),
        // starting: spawned, tool or turn in flight, roster not yet "working".
        "running" | "working" | "active" | "starting" | "" => "running".into(),
        other => other.to_string(),
    }
}

fn budget_flag(m: &TeamMember, key: &str) -> bool {
    m.budget
        .as_ref()
        .and_then(|b| b.get(key))
        .and_then(|v| v.as_bool())
        .unwrap_or(false)
}

fn budget_i64(b: &serde_json::Map<String, serde_json::Value>, key: &str) -> Option<i64> {
    let v = b.get(key)?;
    v.as_i64().or_else(|| v.as_f64().filter(|f| f.is_finite()).map(|f| f as i64))
}

/// Engine counters for the status card (#1397). Keys match the daemon's
/// `TeamMemberSnapshot.Budget` map (`internal/frontend/server/live.go`).
/// A missing key stays `None` so the card shows a dash, never `0`.
pub fn card_budget(m: &TeamMember) -> Option<status_card::Budget> {
    let b = m.budget.as_ref()?;
    Some(status_card::Budget {
        elapsed_s: budget_i64(b, "elapsedS"),
        tokens: budget_i64(b, "tokensUsed"),
        tool_calls: budget_i64(b, "toolCalls"),
        cost_usd: b.get("costUsdUsed").and_then(|v| v.as_f64()),
    })
}

pub fn budget_elapsed(m: &TeamMember) -> i64 {
    let Some(b) = m.budget.as_ref() else {
        return 0;
    };
    let Some(v) = b.get("elapsedS") else {
        return 0;
    };
    if let Some(n) = v.as_i64() {
        return n.max(0);
    }
    if let Some(n) = v.as_f64() {
        return n.max(0.0) as i64;
    }
    0
}

pub fn is_running(state: &str) -> bool {
    matches!(state, "running" | "stalled")
}

/// True only when the daemon has reported a status for `root` and that
/// status is not busy: the evidence the orphan sweep (#1506) needs before
/// settling a replayed log that ends mid-tool. An unknown root or a row
/// without a status is not evidence of idleness.
pub fn root_reported_idle(rows: &[RootSummary], root: &str) -> bool {
    !root.is_empty()
        && rows
            .iter()
            .any(|r| r.id == root && !r.busy && !r.status.is_empty())
}

pub fn can_retry(state: &str) -> bool {
    matches!(state, "failed" | "blocked" | "stopped")
}

/// Real permission postures, safest first. Labels are Strike's. The detail
/// says how each one lines up with a four-way ask / reads / trust / skip
/// menu: there is no read-only-shell mode, and yolo is this session only.
pub fn permission_modes() -> &'static [ModeChoice] {
    &[
        ModeChoice {
            id: "default",
            label: "Default",
            detail: "Asks before tools the rules mark as ask.",
        },
        ModeChoice {
            id: "plan",
            label: "Plan",
            detail: "Writes and edits are denied. Stricter than read-only shell.",
        },
        ModeChoice {
            id: "soft-approve",
            label: "Soft",
            detail: "Shows the ask, then allows once after 15 seconds.",
        },
        ModeChoice {
            id: "accept-edits",
            label: "Edits",
            detail: "File edits are allowed. Shell and network still ask.",
        },
        ModeChoice {
            id: "yolo",
            label: "Yolo",
            detail:
                "This session skips asks that are not hard-denied. Other sessions stay as they are.",
        },
    ]
}

pub fn mode_label(id: &str) -> &'static str {
    permission_modes()
        .iter()
        .find(|m| m.id == id)
        .map(|m| m.label)
        .unwrap_or("Default")
}

/// The four protocol autonomy values (#1572), most to least human
/// oversight — the same order as `protocol.Autonomies()`. Labels are the
/// protocol values themselves (the TUI picker renders the raw mode), and
/// each detail is the protocol `Autonomy.Describe()` wording verbatim —
/// the cockpit does not invent its own description of gate semantics.
pub fn autonomies() -> &'static [ModeChoice] {
    &[
        ModeChoice {
            id: "supervised",
            label: "supervised",
            detail: "you approve phase gates — safest default",
        },
        ModeChoice {
            id: "agent",
            label: "agent",
            detail: "agent clears phase gates itself — less interruption",
        },
        ModeChoice {
            id: "checks",
            label: "checks",
            detail: "commands must pass before a phase advances",
        },
        ModeChoice {
            id: "skip-all",
            label: "skip-all",
            detail: "skip workflow/plan approval — tool perms unchanged",
        },
    ]
}

pub fn autonomy_label(id: &str) -> &'static str {
    autonomies()
        .iter()
        .find(|m| m.id == id)
        .map(|m| m.label)
        .unwrap_or("supervised")
}

/// The autonomy a status line should claim: the daemon's reported value
/// when it is one of the four protocol modes, otherwise the protocol
/// default (`supervised` — `Autonomy.Normalize` maps empty there too).
pub fn autonomy_or_default(id: &str) -> &'static str {
    autonomies()
        .iter()
        .find(|m| m.id == id)
        .map(|m| m.id)
        .unwrap_or("supervised")
}

fn unix_seconds(created_at: i64) -> i64 {
    if created_at > 10_000_000_000 {
        created_at / 1000
    } else {
        created_at.max(0)
    }
}

fn blank_row(id: &str, title: &str, mtime: i64, pinned: bool) -> SessionRow {
    SessionRow {
        id: id.to_string(),
        title: title.to_string(),
        mtime,
        busy: false,
        live: false,
        pinned,
        unread: false,
        workspace_id: String::new(),
        status: String::new(),
        permission_pending: false,
        question_pending: false,
        ask_outcome: String::new(),
        status_since: 0,
        last_activity_at: 0,
    }
}

/// Merge live roots with saved sessions. `active_running` upgrades an idle
/// active root to running when a delegated child is still going.
/// Server `unread` wins when the field was present. Otherwise the browser
/// seen map is the fallback for an older daemon.
pub fn merge_sessions(
    roots: &[RootSummary],
    sessions: &[SessionItem],
    pins: &[String],
    seen: &HashMap<String, i64>,
    active: &str,
    active_running: bool,
) -> Vec<SessionRow> {
    let mut by_id: HashMap<String, SessionRow> = HashMap::new();
    let mut server_unread: HashMap<String, bool> = HashMap::new();
    for s in sessions {
        let pinned = pins.iter().any(|p| p == &s.id);
        let mut row = blank_row(&s.id, &s.title, s.mtime.max(0), pinned);
        row.workspace_id = s.workspace_id.clone();
        row.status = s.status.clone();
        if s.last_activity_at > 0 {
            row.last_activity_at = s.last_activity_at;
        } else if s.mtime > 0 {
            row.last_activity_at = s.mtime.saturating_mul(1000);
        }
        if let Some(flag) = s.unread {
            server_unread.insert(s.id.clone(), flag);
        }
        by_id.insert(s.id.clone(), row);
    }
    for r in roots {
        let created = unix_seconds(r.created_at);
        let pinned = pins.iter().any(|p| p == &r.id);
        let entry = by_id
            .entry(r.id.clone())
            .or_insert_with(|| blank_row(&r.id, &r.title, created, pinned));
        entry.live = true;
        entry.busy = r.busy;
        entry.pinned = pinned;
        if entry.title.is_empty() {
            entry.title = r.title.clone();
        }
        if entry.mtime == 0 {
            entry.mtime = created;
        }
        if !r.status.is_empty() {
            entry.status = r.status.clone();
        }
        entry.permission_pending = r.permission_pending;
        entry.question_pending = r.question_pending;
        entry.ask_outcome = r.ask_outcome.clone();
        if r.status_since > 0 {
            entry.status_since = r.status_since;
        }
        if r.last_activity_at > 0 {
            entry.last_activity_at = r.last_activity_at;
        }
        if let Some(flag) = r.unread {
            server_unread.insert(r.id.clone(), flag);
        }
    }
    for row in by_id.values_mut() {
        if row.id == active && active_running && matches!(row.status.as_str(), "" | "idle" | "done")
        {
            row.status = "running".into();
            row.busy = true;
        }
        if matches!(row.status.as_str(), "running" | "waiting") {
            row.busy = true;
        }
        let local = {
            let seen_at = seen.get(&row.id).copied().unwrap_or(0);
            row.id != active && row.mtime > seen_at && row.mtime > 0
        };
        row.unread = if row.id == active {
            false
        } else if let Some(flag) = server_unread.get(&row.id) {
            *flag
        } else {
            local
        };
    }
    let mut rows: Vec<_> = by_id.into_values().collect();
    rows.sort_by(|a, b| b.mtime.cmp(&a.mtime).then(a.id.cmp(&b.id)));
    rows
}

pub fn filter_sessions(
    rows: &[SessionRow],
    filter: &str,
    query: &str,
    active: &str,
    child_titles: &[String],
) -> Vec<SessionRow> {
    let q = query.trim().to_ascii_lowercase();
    rows.iter()
        .filter(|row| {
            let mode_ok = match filter {
                "unread" => row.unread,
                "needs-you" => {
                    crate::status::session_kind(
                        &row.status,
                        row.busy,
                        row.permission_pending,
                        row.question_pending,
                    ) == crate::status::StatusKind::Waiting
                }
                "progress" => row.busy,
                "pinned" => row.pinned,
                _ => true,
            };
            if !mode_ok {
                return false;
            }
            if q.is_empty() {
                return true;
            }
            let title = row.title.to_ascii_lowercase();
            let id = row.id.to_ascii_lowercase();
            if title.contains(&q) || id.contains(&q) {
                return true;
            }
            row.id == active
                && child_titles
                    .iter()
                    .any(|t| t.to_ascii_lowercase().contains(&q))
        })
        .cloned()
        .collect()
}

/// Keep the newest `keep` rows, plus anything pinned, in progress, or active.
/// A non-empty query, or `show_older`, returns every row. The second value
/// is how many rows were folded.
pub fn visible_sessions(
    rows: &[SessionRow],
    query: &str,
    active: &str,
    keep: usize,
    show_older: bool,
) -> (Vec<SessionRow>, usize) {
    if show_older || !query.trim().is_empty() {
        return (rows.to_vec(), 0);
    }
    let mut shown = Vec::new();
    let mut hidden = 0usize;
    for (i, row) in rows.iter().enumerate() {
        if i < keep || row.pinned || row.busy || row.id == active {
            shown.push(row.clone());
        } else {
            hidden += 1;
        }
    }
    (shown, hidden)
}

pub use crate::status::{fmt_ago, fmt_elapsed};

/// Parse an RFC 3339 timestamp — the protocol envelope's `time` field, as
/// emitted by Go's `time.Time` JSON encoding — into epoch milliseconds.
/// Returns 0.0 for anything unparseable, the "unknown time" convention
/// used by `ChatLine::at_ms`.
pub fn rfc3339_to_ms(s: &str) -> f64 {
    let b = s.as_bytes();
    // Minimum shape: YYYY-MM-DDTHH:MM:SS plus a zone designator.
    if b.len() < 20 {
        return 0.0;
    }
    let num = |from: usize, len: usize| -> Option<i64> {
        let mut v: i64 = 0;
        for i in from..from + len {
            let c = *b.get(i)?;
            if !c.is_ascii_digit() {
                return None;
            }
            v = v * 10 + (c - b'0') as i64;
        }
        Some(v)
    };
    if b[4] != b'-'
        || b[7] != b'-'
        || (b[10] != b'T' && b[10] != b't')
        || b[13] != b':'
        || b[16] != b':'
    {
        return 0.0;
    }
    let (year, month, day) = match (num(0, 4), num(5, 2), num(8, 2)) {
        (Some(y), Some(m), Some(d)) => (y, m, d),
        _ => return 0.0,
    };
    let (hour, min, sec) = match (num(11, 2), num(14, 2), num(17, 2)) {
        (Some(h), Some(m), Some(s)) => (h, m, s),
        _ => return 0.0,
    };
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) || hour > 23 || min > 59 || sec > 60 {
        return 0.0;
    }
    // Fractional seconds (Go emits up to nanoseconds; ms is enough here).
    let mut i = 19;
    let mut frac_ms: i64 = 0;
    if b.get(i) == Some(&b'.') {
        i += 1;
        let mut scale: i64 = 100;
        while let Some(c) = b.get(i) {
            if !c.is_ascii_digit() {
                break;
            }
            if scale > 0 {
                frac_ms += (*c - b'0') as i64 * scale;
                scale /= 10;
            }
            i += 1;
        }
    }
    let offset_s: i64 = match b.get(i) {
        Some(b'Z') | Some(b'z') => 0,
        Some(sign) if *sign == b'+' || *sign == b'-' => {
            if b.get(i + 3) != Some(&b':') {
                return 0.0;
            }
            match (num(i + 1, 2), num(i + 4, 2)) {
                (Some(hh), Some(mm)) => {
                    let v = hh * 3600 + mm * 60;
                    if *sign == b'-' {
                        -v
                    } else {
                        v
                    }
                }
                _ => return 0.0,
            }
        }
        _ => return 0.0,
    };
    // Days from civil (Howard Hinnant's algorithm), days since 1970-01-01.
    let y = if month <= 2 { year - 1 } else { year };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = (month + 9) % 12;
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    ((((days * 24 + hour) * 60 + min) * 60 + sec) * 1000 + frac_ms - offset_s * 1000) as f64
}

/// Badge class suffix. Wire status `completed` renders as done.
pub fn status_kind(status: &str) -> String {
    match status.to_ascii_lowercase().as_str() {
        "completed" => "done".into(),
        "canceled" | "cancelled" => "stopped".into(),
        "failed" => "failed".into(),
        "blocked" => "blocked".into(),
        other => other.to_string(),
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct ChildCard {
    pub key: String,
    pub name: String,
    pub status: String,
    pub summary: String,
    pub details: String,
    /// Envelope time of the originating event, epoch ms. 0 = unknown.
    pub at_ms: f64,
}

/// Exact identity, ignoring ASCII case. A prefix is a different child:
/// `scribe` and `scribe-2` must not share a card. No in-repo producer
/// truncates the key, so a short prefix is never a stand-in for the full id.
pub fn child_keys_match(a: &str, b: &str) -> bool {
    if a.is_empty() || b.is_empty() {
        return false;
    }
    a.eq_ignore_ascii_case(b)
}

/// Summary text worth merging onto an existing card. The bare status word
/// is not one: the badge already shows it, and treating it as a summary
/// replaces the handoff with `completed`.
pub fn summary_for_merge(summary: &str, status: &str) -> String {
    let summary = summary.trim();
    let status = status.trim();
    if summary.is_empty() || (!status.is_empty() && summary.eq_ignore_ascii_case(status)) {
        String::new()
    } else {
        summary.to_string()
    }
}

pub fn child_card_from_parts(
    name: &str,
    session: &str,
    status: &str,
    summary: &str,
    details: &str,
) -> ChildCard {
    let name = name.trim().to_string();
    let session = session.trim().to_string();
    let key = if !name.is_empty() {
        name.to_ascii_lowercase()
    } else {
        session.to_ascii_lowercase()
    };
    let picked = prefer_summary("", summary);
    let summary = if useful_summary(&picked) {
        picked
    } else {
        status.trim().to_string()
    };
    ChildCard {
        key,
        name: if name.is_empty() { session } else { name },
        status: status.trim().to_string(),
        summary,
        details: details.trim().to_string(),
        at_ms: 0.0,
    }
}

/// Engine-injected `[child.completed …]` user text, including the handoff JSON.
pub fn child_card_from_text(text: &str) -> Option<ChildCard> {
    let start = text.find("[child.completed")?;
    let rest = &text[start..];
    let header_end = rest.find(']')?;
    let header = &rest[..=header_end];
    let session = header_field(header, "session");
    let name = header_field(header, "name");
    let status = header_field(header, "status");
    if session.is_empty() && name.is_empty() && status.is_empty() {
        return None;
    }
    let body = rest[header_end + 1..].trim();
    let mut summary = handoff_summary(body).unwrap_or_default();
    if !useful_summary(&summary) {
        summary = first_blocker(body).unwrap_or_default();
    }
    if !useful_summary(&summary) {
        summary = status.clone();
    }
    Some(child_card_from_parts(
        &name,
        &session,
        &status,
        &summary,
        rest.trim(),
    ))
}

/// Sidebar title. A stored `[child.completed]` blob becomes the child name.
pub fn session_label(title: &str, fallback: &str) -> String {
    let title = title.trim();
    if let Some(card) = child_card_from_text(title) {
        if !card.name.is_empty() {
            return card.name;
        }
    }
    if title.is_empty() {
        return fallback.to_string();
    }
    if title.chars().count() > 72 {
        let mut short: String = title.chars().take(72).collect();
        short.push('…');
        return short;
    }
    title.to_string()
}

const ECHO_TIP_MARK: &str = "Tip: start a message with `run <command>`";

pub fn strip_echo_tip(text: &str) -> String {
    let Some(i) = text.find(ECHO_TIP_MARK) else {
        return text.to_string();
    };
    let mut end = i + ECHO_TIP_MARK.len();
    if let Some(rel) = text[end..].find('.') {
        end += rel + 1;
    } else {
        end = text.len();
    }
    let mut out = text[..i].trim_end().to_string();
    let rest = text[end..].trim_start();
    if !rest.is_empty() {
        if !out.is_empty() {
            out.push_str("\n\n");
        }
        out.push_str(rest);
    }
    out
}

/// Long JSON or handoff/verification dumps that should not sit in the transcript as a wall.
pub fn protocol_dump(text: &str) -> Option<(String, String)> {
    let t = text.trim();
    if t.chars().count() < 80 {
        return None;
    }
    let jsonish = t.starts_with('{')
        || t.starts_with('[')
        || t.contains("\nhandoff:")
        || t.contains("handoff: {")
        || t.contains("\nverification:")
        || t.contains("verification: {");
    if !jsonish {
        return None;
    }
    let summary = one_line(t);
    Some((summary, t.to_string()))
}

fn header_field(header: &str, key: &str) -> String {
    let pat = format!("{key}=");
    let Some(i) = header.find(&pat) else {
        return String::new();
    };
    header[i + pat.len()..]
        .split(|c: char| c.is_whitespace() || c == ']')
        .next()
        .unwrap_or("")
        .trim()
        .to_string()
}

fn handoff_summary(body: &str) -> Option<String> {
    let idx = body.find("handoff:")?;
    let after = body[idx + "handoff:".len()..].trim_start();
    let json = json_object(after)?;
    let v: serde_json::Value = serde_json::from_str(json).ok()?;
    let summary = v.get("summary")?.as_str()?.trim();
    if summary.is_empty() {
        return None;
    }
    Some(one_line(summary))
}

fn json_object(s: &str) -> Option<&str> {
    let bytes = s.as_bytes();
    if bytes.first() != Some(&b'{') {
        return None;
    }
    let mut depth = 0i32;
    let mut in_str = false;
    let mut esc = false;
    for (i, &b) in bytes.iter().enumerate() {
        if in_str {
            if esc {
                esc = false;
                continue;
            }
            if b == b'\\' {
                esc = true;
                continue;
            }
            if b == b'"' {
                in_str = false;
            }
            continue;
        }
        match b {
            b'"' => in_str = true,
            b'{' => depth += 1,
            b'}' => {
                depth -= 1;
                if depth == 0 {
                    return Some(&s[..=i]);
                }
            }
            _ => {}
        }
    }
    None
}

fn useful_summary(summary: &str) -> bool {
    let cleaned = strip_echo_tip(summary);
    let t = cleaned.trim();
    !t.is_empty() && !t.starts_with("You said:") && !t.contains(ECHO_TIP_MARK)
}

/// Keep a real status line. Echo "You said" text and the run-command tip lose.
pub fn prefer_summary(current: &str, incoming: &str) -> String {
    let current = one_line(&strip_echo_tip(current));
    let incoming = one_line(&strip_echo_tip(incoming));
    match (useful_summary(&current), useful_summary(&incoming)) {
        (false, true) => incoming,
        (true, false) => current,
        (true, true) => {
            if incoming.split_whitespace().count() > current.split_whitespace().count() {
                incoming
            } else {
                current
            }
        }
        (false, false) => String::new(),
    }
}

fn first_blocker(body: &str) -> Option<String> {
    let idx = body.find("handoff:")?;
    let json = json_object(body[idx + "handoff:".len()..].trim_start())?;
    let v: serde_json::Value = serde_json::from_str(json).ok()?;
    let arr = v.get("blockers")?.as_array()?;
    arr.iter().find_map(|item| {
        let s = item.as_str()?.trim();
        if s.is_empty() {
            None
        } else {
            Some(s.to_string())
        }
    })
}

/// When the live team snapshot is empty, show saved children so the panel
/// matches the session list. Status comes from the transcript card.
pub fn activity_panel(
    activity: &[ActivityRow],
    kids: &[ChildSession],
    cards: &[ChatLine],
    active: &str,
) -> Vec<ActivityRow> {
    if !activity.is_empty() {
        return activity.to_vec();
    }
    let mut rows = Vec::new();
    for child in kids {
        // An empty parent id cannot be attributed. The children list is
        // loaded for one root, but this fallback is also called with a
        // captured root that may no longer be that list, so a parentless
        // row would show up under every session. Rows that name their
        // parent still appear for that root only.
        if child.parent_id.is_empty() || child.parent_id != active {
            continue;
        }
        let name = if child.title.is_empty() {
            child.id.clone()
        } else {
            child.title.clone()
        };
        let card = cards.iter().rev().find(|line| {
            line.role == "child"
                && (child_keys_match(&line.key, &name) || child_keys_match(&line.key, &child.id))
        });
        rows.push(ActivityRow {
            session_id: child.id.clone(),
            name,
            agent: String::new(),
            task: card.map(|line| line.body.clone()).unwrap_or_default(),
            state: card.map(|line| line.badge.clone()).unwrap_or_default(),
            elapsed_s: 0,
            started_ms: 0.0,
            expanded: false,
            summary: card.map(|line| line.detail.clone()).unwrap_or_default(),
            block_reason: String::new(),
            budget: None,
        });
    }
    rows
}

/// Short label for an activity row. The transcript card keeps the full summary.
pub fn task_headline(task: &str) -> String {
    let flat = one_line(task);
    if flat.is_empty() {
        return String::new();
    }
    if let Some((head, _)) = flat.split_once(": ") {
        let n = head.chars().count();
        if (8..=42).contains(&n) {
            return head.to_string();
        }
    }
    if flat.chars().count() <= 42 {
        return flat;
    }
    let mut short: String = flat.chars().take(41).collect();
    short.push('…');
    short
}

fn one_line(text: &str) -> String {
    let flat = text.split_whitespace().collect::<Vec<_>>().join(" ");
    let flat = flat.trim();
    if flat.chars().count() <= 140 {
        return flat.to_string();
    }
    let mut short: String = flat.chars().take(139).collect();
    short.push('…');
    short
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bootstrap_plugins_capability() {
        let on: Bootstrap = serde_json::from_str(r#"{"capabilities":{"plugins":true,"panes":true}}"#).unwrap();
        assert!(on.capabilities.plugins);
        let off: Bootstrap = serde_json::from_str(r#"{"capabilities":{}}"#).unwrap();
        assert!(!off.capabilities.plugins);
    }

    #[test]
    fn config_sources_decode_in_daemon_order() {
        let reply: ConfigSourcesReply = serde_json::from_str(
            r#"{"sources":[
                {"slot":"config","kind":"","scope":"global","label":"Main config","display":"~/.strike/config","exists":true,"canCreate":true},
                {"slot":"mcp","kind":"","scope":"project","label":"MCP servers","display":"./.strike/mcp.json","exists":false,"canCreate":true},
                {"slot":"","kind":"agents","scope":"project","label":"agents/review.md","display":"./.strike/agents/review.md","exists":true,"canCreate":false},
                {"display":"./odd","exists":false,"futureField":1}
            ]}"#,
        )
        .unwrap();
        let titles: Vec<&str> = reply.sources.iter().map(ConfigSource::title).collect();
        assert_eq!(titles, ["Main config", "MCP servers", "agents/review.md", "./odd"]);
        let cases = [
            (0, "Global", "config slot", "Present"),
            (1, "Project", "mcp slot", "Not present (the daemon can create it; not from here)"),
            (2, "Project", "agents file", "Present"),
            (3, "Unknown scope", "file", "Not present"),
        ];
        for (i, scope, kind, state) in cases {
            let row = &reply.sources[i];
            assert_eq!(row.scope_label(), scope, "row {i}");
            assert_eq!(row.kind_label(), kind, "row {i}");
            assert_eq!(row.state_label(), state, "row {i}");
        }
        let empty: ConfigSourcesReply = serde_json::from_str("{}").unwrap();
        assert!(empty.sources.is_empty());
    }

    #[test]
    fn bootstrap_config_files_capability() {
        let on: Bootstrap = serde_json::from_str(r#"{"capabilities":{"configFiles":true}}"#).unwrap();
        assert!(on.capabilities.config_files);
        let off: Bootstrap = serde_json::from_str(r#"{"capabilities":{}}"#).unwrap();
        assert!(!off.capabilities.config_files);
        let old: Bootstrap = serde_json::from_str("{}").unwrap();
        assert!(!old.capabilities.config_files);
    }

    #[test]
    fn card_budget_keeps_missing_counters_unmeasured() {
        let mk = |v: serde_json::Value| -> TeamMember { serde_json::from_value(v).unwrap() };
        assert_eq!(card_budget(&mk(serde_json::json!({"sessionId": "a"}))), None);
        let b = card_budget(&mk(serde_json::json!({
            "sessionId": "a",
            "budget": {"elapsedS": 90, "tokensUsed": 1200.0, "costUsdUsed": 0.0}
        })))
        .expect("budget sent");
        assert_eq!(b.elapsed_s, Some(90));
        assert_eq!(b.tokens, Some(1200));
        assert_eq!(b.tool_calls, None, "key not sent stays unmeasured");
        assert_eq!(b.cost_usd, Some(0.0));
    }

    #[test]
    fn root_reported_idle_needs_a_daemon_status() {
        let mk = |id: &str, busy: bool, status: &str| -> RootSummary {
            serde_json::from_value(serde_json::json!({"id": id, "busy": busy, "status": status}))
                .unwrap()
        };
        let rows = vec![mk("a", false, "idle"), mk("b", true, "running"), mk("c", false, "")];
        assert!(root_reported_idle(&rows, "a"));
        assert!(!root_reported_idle(&rows, "b"), "busy root is not idle");
        assert!(!root_reported_idle(&rows, "c"), "no status is not evidence");
        assert!(!root_reported_idle(&rows, "missing"));
        assert!(!root_reported_idle(&rows, ""));
    }

    fn row(id: &str, mtime: i64, busy: bool, pinned: bool) -> SessionRow {
        SessionRow {
            id: id.into(),
            title: id.into(),
            mtime,
            busy,
            live: true,
            pinned,
            unread: false,
            workspace_id: String::new(),
            status: String::new(),
            permission_pending: false,
            question_pending: false,
            ask_outcome: String::new(),
            status_since: 0,
            last_activity_at: 0,
        }
    }

    #[test]
    fn merge_marks_unread_and_prefers_session_mtime() {
        let roots = vec![RootSummary {
            id: "a".into(),
            title: String::new(),
            busy: false,
            created_at: 1_700_000_000_000,
            status: String::new(),
            status_since: 0,
            last_activity_at: 0,
            permission_pending: false,
            question_pending: false,
            ask_outcome: String::new(),
            unread: None,
        }];
        let sessions = vec![SessionItem {
            id: "a".into(),
            title: "Alpha".into(),
            mtime: 50,
            workspace_id: String::new(),
            status: String::new(),
            last_activity_at: 0,
            unread: None,
        }];
        let mut seen = HashMap::new();
        seen.insert("a".into(), 40i64);
        let rows = merge_sessions(&roots, &sessions, &[], &seen, "other", false);
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].title, "Alpha");
        assert_eq!(rows[0].mtime, 50);
        assert!(rows[0].unread);
        assert!(rows[0].live);

        let seen_active = merge_sessions(&roots, &sessions, &[], &seen, "a", true);
        assert!(!seen_active[0].unread);
        assert!(seen_active[0].busy);
        assert_eq!(seen_active[0].status, "running");
    }

    #[test]
    fn server_unread_overrides_browser_seen() {
        let roots = vec![RootSummary {
            id: "a".into(),
            title: "Alpha".into(),
            busy: false,
            created_at: 0,
            status: "waiting".into(),
            status_since: 5_000,
            last_activity_at: 9_000,
            permission_pending: true,
            question_pending: false,
            ask_outcome: String::new(),
            unread: Some(true),
        }];
        let mut seen = HashMap::new();
        seen.insert("a".into(), 10_000i64);
        let rows = merge_sessions(&roots, &[], &[], &seen, "other", false);
        assert!(rows[0].unread);
        assert!(rows[0].busy);
        assert_eq!(rows[0].status, "waiting");
        assert!(rows[0].permission_pending);
        assert!(!rows[0].question_pending);
        assert_eq!(rows[0].last_activity_at, 9_000);

        let acked = vec![RootSummary {
            unread: Some(false),
            ..roots[0].clone()
        }];
        let rows = merge_sessions(&acked, &[], &[], &seen, "other", false);
        assert!(!rows[0].unread);
    }

    #[test]
    fn filter_and_fold_keep_pinned_and_search() {
        let mut rows = Vec::new();
        for i in 0..8 {
            rows.push(row(&format!("s{i}"), 100 - i, false, false));
        }
        rows.push(row("pin", 1, false, true));
        let (shown, hidden) = visible_sessions(&rows, "", "", 6, false);
        assert_eq!(hidden, 2);
        assert!(shown.iter().any(|r| r.id == "pin"));
        assert_eq!(shown.len(), 7);

        let filtered = filter_sessions(&rows, "pinned", "", "", &[]);
        assert_eq!(filtered.len(), 1);

        let (all, hidden_search) = visible_sessions(&rows, "s7", "", 6, false);
        assert_eq!(hidden_search, 0);
        assert_eq!(all.len(), rows.len());

        let progress = filter_sessions(&[row("run", 3, true, false)], "progress", "", "", &[]);
        assert_eq!(progress.len(), 1);
        let by_child = filter_sessions(
            &[row("parent", 3, false, false)],
            "all",
            "scribe",
            "parent",
            &["scribe".into()],
        );
        assert_eq!(by_child.len(), 1);
    }

    #[test]
    fn task_headline_keeps_the_short_clause() {
        let long = "verification failed: schema: handoff: handoff schema: structured handoff missing or incomplete";
        assert_eq!(task_headline(long), "verification failed");
        assert_eq!(task_headline("say hello"), "say hello");
        assert_eq!(task_headline(""), "");
        let wide = "a very long task line without a colon that should be cut down to one row";
        assert!(task_headline(wide).ends_with('…'));
        assert!(task_headline(wide).chars().count() <= 42);
    }

    #[test]
    fn autonomies_are_the_real_four() {
        let ids: Vec<_> = autonomies().iter().map(|m| m.id).collect();
        assert_eq!(ids, vec!["supervised", "agent", "checks", "skip-all"]);
        // Details are protocol Autonomy.Describe() verbatim.
        let details: Vec<_> = autonomies().iter().map(|m| m.detail).collect();
        assert_eq!(
            details,
            vec![
                "you approve phase gates — safest default",
                "agent clears phase gates itself — less interruption",
                "commands must pass before a phase advances",
                "skip workflow/plan approval — tool perms unchanged",
            ]
        );
        assert_eq!(autonomy_label("checks"), "checks");
        assert_eq!(autonomy_label("nope"), "supervised");
        assert_eq!(autonomy_or_default(""), "supervised");
        assert_eq!(autonomy_or_default("skip-all"), "skip-all");
        assert_eq!(autonomy_or_default("bogus"), "supervised");
    }

    #[test]
    fn live_status_keeps_the_daemons_autonomy() {
        let st: LiveStatus = serde_json::from_str(
            r#"{"provider":"echo","model":"echo","autonomy":"skip-all","busy":false}"#,
        )
        .unwrap();
        assert_eq!(st.autonomy, "skip-all");
        // Older statuses without the field parse as unreported, not as a
        // mode the daemon never claimed.
        let st: LiveStatus = serde_json::from_str(r#"{"provider":"echo"}"#).unwrap();
        assert_eq!(st.autonomy, "");
    }

    #[test]
    fn permission_modes_are_the_real_five() {
        let ids: Vec<_> = permission_modes().iter().map(|m| m.id).collect();
        assert_eq!(
            ids,
            vec!["default", "plan", "soft-approve", "accept-edits", "yolo"]
        );
        assert_eq!(mode_label("plan"), "Plan");
        assert_eq!(mode_label("nope"), "Default");
    }

    #[test]
    fn child_notice_is_one_card_and_echo_tip_is_stripped() {
        let raw = "[child.completed session=abcd1234 name=scribe status=blocked]\nhandoff: {\"summary\":\"verification failed: schema: handoff\",\"blockers\":[\"schema\"]}\nverification: {\"passed\":false,\"summary\":\"gate\"}\nDo not sleep-poll for subagents; this is the terminal result.";
        let card = child_card_from_text(raw).expect("card");
        assert_eq!(card.name, "scribe");
        assert_eq!(card.key, "scribe");
        assert_eq!(card.status, "blocked");
        assert!(card.summary.contains("schema: handoff"));
        assert!(!card.summary.contains("Tip:"));
        assert!(!card.summary.starts_with("You said:"));
        assert!(card.details.contains("verification"));
        assert!(child_keys_match(&card.key, "Scribe"));
        assert!(!child_keys_match("abcd1234", "abcd"));
        assert!(!child_keys_match("scribe", "scribe-2"));
        assert!(!child_keys_match("a", "alice"));

        let echoed = format!("You said: {raw}\n\nTip: start a message with `run <command>` to exercise the bash tool and the permission prompt.");
        let stripped = strip_echo_tip(&echoed);
        assert!(!stripped.contains("Tip:"));
        assert!(child_card_from_text(&stripped).is_some());

        let plain = strip_echo_tip("You said: hello\n\nTip: start a message with `run <command>` to exercise the bash tool and the permission prompt.");
        assert_eq!(plain, "You said: hello");
        assert!(child_card_from_text("hello from the cockpit").is_none());
        assert!(protocol_dump("hello").is_none());
        let dump = protocol_dump(&format!("handoff: {}", "{\"summary\":\"x\",\"blockers\":[\"a\",\"b\",\"c\",\"d\",\"e\"]} and more text to pass the length gate for a protocol wall"));
        assert!(dump.is_some());

        assert_eq!(session_label(raw, "fallback"), "scribe");
        assert_eq!(session_label("", "short"), "short");
        assert_eq!(fmt_ago(100, 160), "1m");
        assert_eq!(status_kind("completed"), "done");
        assert_eq!(status_kind("blocked"), "blocked");
        assert_eq!(
            prefer_summary(
                "You said: say hello",
                "verification failed: schema: handoff"
            ),
            "verification failed: schema: handoff"
        );

        let kids = vec![ChildSession {
            id: "child-1".into(),
            title: "scribe".into(),
            parent_id: "root".into(),
        }];
        let cards = vec![ChatLine {
            id: 1,
            role: "child".into(),
            title: "scribe".into(),
            body: "schema: handoff missing".into(),
            badge: "blocked".into(),
            detail: "handoff json".into(),
            key: "scribe".into(),
            at_ms: 0.0,
        }];
        let rows = activity_panel(&[], &kids, &cards, "root");
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].name, "scribe");
        assert_eq!(rows[0].state, "blocked");
        assert_eq!(rows[0].task, "schema: handoff missing");
        assert_eq!(rows[0].summary, "handoff json", "transcript detail feeds the card as agent text");
        assert!(rows[0].budget.is_none(), "saved children carry no engine counters");

        let siblings = vec![
            ChildSession {
                id: "id-scribe".into(),
                title: "scribe".into(),
                parent_id: "root".into(),
            },
            ChildSession {
                id: "id-scribe-2".into(),
                title: "scribe-2".into(),
                parent_id: "root".into(),
            },
            ChildSession {
                id: "orphan".into(),
                title: "orphan".into(),
                parent_id: String::new(),
            },
        ];
        let sibling_cards = vec![
            ChatLine {
                id: 1,
                role: "child".into(),
                title: "scribe".into(),
                body: "wrote the index".into(),
                badge: "done".into(),
                detail: String::new(),
                key: "scribe".into(),
                at_ms: 0.0,
            },
            ChatLine {
                id: 2,
                role: "child".into(),
                title: "scribe-2".into(),
                body: "still reading".into(),
                badge: "blocked".into(),
                detail: String::new(),
                key: "scribe-2".into(),
                at_ms: 0.0,
            },
        ];
        let rows = activity_panel(&[], &siblings, &sibling_cards, "root");
        assert_eq!(
            rows.len(),
            2,
            "parentless child must not leak into the panel"
        );
        assert_eq!(rows[0].name, "scribe");
        assert_eq!(rows[0].task, "wrote the index");
        assert_eq!(rows[0].state, "done");
        assert_eq!(rows[1].name, "scribe-2");
        assert_eq!(rows[1].task, "still reading");
        assert_eq!(rows[1].state, "blocked");
        assert!(activity_panel(&[], &siblings, &sibling_cards, "other").is_empty());
        assert_eq!(summary_for_merge("completed", "completed"), "");
        assert_eq!(
            summary_for_merge("wrote the migration", "completed"),
            "wrote the migration"
        );
    }

    #[test]
    fn bootstrap_reads_the_diag_capability() {
        let boot: Bootstrap = serde_json::from_str(
            r#"{"capabilities":{"diag":true},"skills":[],"composerCommands":[]}"#,
        )
        .unwrap();
        assert!(boot.capabilities.diag);
        assert!(!boot.capabilities.ledger);
        // Older daemons without a capabilities object read as off, so the
        // Download diagnostics control stays disabled rather than lying.
        let boot: Bootstrap = serde_json::from_str(r#"{"skills":[]}"#).unwrap();
        assert!(!boot.capabilities.diag);
    }

    #[test]
    fn bootstrap_reads_the_workspace_trust_capability() {
        let boot: Bootstrap = serde_json::from_str(
            r#"{"capabilities":{"workspaceTrust":true},"skills":[],"composerCommands":[]}"#,
        )
        .unwrap();
        assert!(boot.capabilities.workspace_trust);
        let off: Bootstrap = serde_json::from_str(r#"{"skills":[]}"#).unwrap();
        assert!(!off.capabilities.workspace_trust);
    }

    #[test]
    fn workspace_trust_sections_match_cli_wording() {
        let reply = workspace_trust_outcome(
            200,
            r#"{"root":"/r","trusted":false,"summary":"1 MCP server","mutable":true,
               "surfaces":{"root":"/r","configFiles":["/r/.strike/config"],"configKeys":["model"],
               "permissionRules":2,"mcp":[{"name":"evil","command":"npx","args":["-y","evil"]},
               {"name":"remote","url":"https://mcp.example"}],
               "providers":[{"name":"openai","url":""}],
               "hooks":[{"event":"pre_tool_use","command":"echo hi"},{"event":"stop","action":"log"}],
               "agents":[{"name":"a","path":"/r/.strike/agents/a.md"}],"steering":["/r/AGENTS.md"]}}"#,
        )
        .unwrap();
        assert!(reply.badge_visible());
        assert!(reply.mutable);
        let sections = reply.surfaces.sections();
        let titles: Vec<&str> = sections.iter().map(|s| s.title).collect();
        assert_eq!(
            titles,
            vec!["Config", "MCP servers", "Providers", "Hooks", "Agents", "Steering"]
        );
        assert_eq!(
            sections[0].lines,
            vec!["overrides: model", "permission rules: 2", "/r/.strike/config"]
        );
        assert_eq!(
            sections[1].lines,
            vec!["evil: npx -y evil", "remote: https://mcp.example"]
        );
        assert_eq!(sections[2].lines, vec!["openai"], "blank URL row shows the name only");
        assert_eq!(sections[3].lines, vec!["pre_tool_use: echo hi", "stop: log (rule)"]);
        assert_eq!(sections[4].lines, vec!["a (/r/.strike/agents/a.md)"]);
    }

    #[test]
    fn workspace_trust_badge_hides_when_trusted_or_empty() {
        let trusted = workspace_trust_outcome(
            200,
            r#"{"root":"/r","trusted":true,"surfaces":{"steering":["/r/AGENTS.md"]}}"#,
        )
        .unwrap();
        assert!(!trusted.badge_visible());
        let empty = workspace_trust_outcome(200, r#"{"root":"/r","surfaces":{}}"#).unwrap();
        assert!(!empty.badge_visible());
    }

    #[test]
    fn workspace_trust_outcome_reads_refusals() {
        let home = workspace_trust_outcome(
            422,
            r#"{"root":"/home/u","untrustable":true,"error":"workspace root cannot be trusted: /home/u"}"#,
        )
        .unwrap();
        assert!(home.untrustable);
        assert!(home.error.contains("cannot be trusted"));
        assert_eq!(
            workspace_trust_outcome(403, r#"{"error":"daemon is --read-only"}"#).unwrap_err(),
            "daemon is --read-only"
        );
        assert_eq!(workspace_trust_outcome(500, "").unwrap_err(), "HTTP 500");
    }

    #[test]
    fn bootstrap_reads_the_project_init_capability() {
        let boot: Bootstrap = serde_json::from_str(
            r#"{"capabilities":{"projectInit":true},"skills":[],"composerCommands":[]}"#,
        )
        .unwrap();
        assert!(boot.capabilities.project_init);
        let boot: Bootstrap = serde_json::from_str(r#"{"capabilities":{}}"#).unwrap();
        assert!(!boot.capabilities.project_init);
    }

    #[test]
    fn bootstrap_reads_agents_and_the_current_agent() {
        let boot: Bootstrap = serde_json::from_str(
            r#"{"status":{"agent":"build"},"agents":[{"name":"build"},{"name":"plan","description":"Read-only"}]}"#,
        )
        .unwrap();
        assert_eq!(boot.status.map(|s| s.agent), Some("build".to_string()));
        assert_eq!(
            boot.agents,
            vec![
                AgentInfo { name: "build".into(), description: String::new() },
                AgentInfo { name: "plan".into(), description: "Read-only".into() },
            ]
        );
        // Older daemons: no agents and no status agent read as empty.
        let boot: Bootstrap = serde_json::from_str(r#"{"status":{}}"#).unwrap();
        assert!(boot.agents.is_empty());
        assert_eq!(boot.status.map(|s| s.agent), Some(String::new()));
    }

    #[test]
    fn team_snapshot_reads_messages_and_escalation() {
        let snap: TeamSnapshot = serde_json::from_str(
            r#"{"available":true,"leadId":"lead","members":{"c1":{"sessionId":"c1","name":"scout","state":"running","escalation":{"kind":"stall","reason":"no progress","action":"finalizing","terminalStatus":"blocked"}}},"messages":[{"messageId":"m1","from":"c1","to":"lead","body":"stuck on schema","summary":"blocked","urgency":"high","kind":"request"}]}"#,
        )
        .unwrap();
        let esc = snap.members["c1"].escalation.clone().unwrap();
        assert_eq!((esc.kind.as_str(), esc.action.as_str()), ("stall", "finalizing"));
        assert_eq!(snap.messages.len(), 1);
        assert_eq!(snap.messages[0].summary, "blocked");
        assert_eq!(snap.messages[0].urgency, "high");

        // Go marshals an empty slice as null; older daemons omit it.
        for raw in [
            r#"{"available":true,"members":{},"messages":null}"#,
            r#"{"available":true,"members":{}}"#,
        ] {
            let snap: TeamSnapshot = serde_json::from_str(raw).unwrap();
            assert!(snap.messages.is_empty(), "{raw}");
        }
    }

    #[test]
    fn project_init_outcome_reads_each_state() {
        let ok = project_init_outcome(
            201,
            r#"{"path":"/w/AGENTS.md","exists":true,"created":true}"#,
        )
        .unwrap();
        assert!(ok.created && ok.exists && ok.error.is_empty());
        assert_eq!(ok.path, "/w/AGENTS.md");

        let preview = project_init_outcome(200, r#"{"path":"/w/AGENTS.md","exists":false}"#).unwrap();
        assert!(!preview.exists && !preview.created);

        let refused = project_init_outcome(
            409,
            r#"{"path":"/w/AGENTS.md","exists":true,"error":"AGENTS.md already exists"}"#,
        )
        .unwrap();
        assert!(refused.exists && !refused.created);
        assert_eq!(refused.error, "AGENTS.md already exists");
        // A 409 without a body still renders as the refusal state.
        let bare = project_init_outcome(409, "").unwrap();
        assert!(bare.exists && !bare.error.is_empty());

        assert_eq!(
            project_init_outcome(
                501,
                r#"{"error":"projectInit capability unavailable on this host"}"#
            )
            .unwrap_err(),
            "projectInit capability unavailable on this host"
        );
        assert_eq!(project_init_outcome(403, "").unwrap_err(), "HTTP 403");
    }

    #[test]
    fn diag_filename_honours_the_server_name() {
        assert_eq!(
            diag_filename(Some(
                "attachment; filename=\"strike-diag-abcd1234-20261030-120000.json\""
            )),
            "strike-diag-abcd1234-20261030-120000.json"
        );
        assert_eq!(
            diag_filename(Some("attachment; filename=strike-diag-x.json")),
            "strike-diag-x.json"
        );
        // A hostile header cannot smuggle a path into the download name.
        assert_eq!(
            diag_filename(Some("attachment; filename=\"../../evil.json\"")),
            "evil.json"
        );
        assert_eq!(diag_filename(None), "strike-diag.json");
        assert_eq!(diag_filename(Some("attachment")), "strike-diag.json");
    }

    #[test]
    fn diag_error_keeps_the_daemons_words() {
        assert_eq!(
            diag_error_message(
                503,
                r#"{"error":"diag capability unavailable on this host"}"#
            ),
            "diag capability unavailable on this host"
        );
        assert_eq!(diag_error_message(504, ""), "HTTP 504");
        assert_eq!(diag_error_message(500, "boom"), "HTTP 500 boom");
    }

    #[test]
    fn rfc3339_parses_go_envelope_times() {
        // 2026-10-26T12:51:31Z, verified against `date -u`.
        assert_eq!(rfc3339_to_ms("2026-10-26T12:51:31Z"), 1_793_019_091_000.0);
        // Fractional seconds (Go RFC3339Nano) keep millisecond precision.
        assert_eq!(rfc3339_to_ms("2026-10-26T12:51:31.5Z"), 1_793_019_091_500.0);
        assert_eq!(
            rfc3339_to_ms("2026-10-26T12:51:31.123456789Z"),
            1_793_019_091_123.0
        );
        // Numeric zones denote the same instant.
        assert_eq!(
            rfc3339_to_ms("2026-10-26T14:51:31+02:00"),
            1_793_019_091_000.0
        );
        assert_eq!(
            rfc3339_to_ms("2026-10-26T10:21:31-02:30"),
            1_793_019_091_000.0
        );
    }

    #[test]
    fn rfc3339_rejects_non_timestamps() {
        assert_eq!(rfc3339_to_ms(""), 0.0);
        assert_eq!(rfc3339_to_ms("2026-10-26"), 0.0);
        assert_eq!(rfc3339_to_ms("not a time at all........"), 0.0);
        assert_eq!(rfc3339_to_ms("2026-13-26T12:51:31Z"), 0.0);
        assert_eq!(rfc3339_to_ms("2026-10-26T25:51:31Z"), 0.0);
        // Missing zone: ambiguous, treat as unknown rather than guessing.
        assert_eq!(rfc3339_to_ms("2026-10-26T12:51:31"), 0.0);
    }
}
