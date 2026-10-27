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

#[derive(Clone, Debug, Deserialize)]
pub struct Bootstrap {
    #[serde(default)]
    pub status: Option<LiveStatus>,
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
    pub output: String,
    pub elapsed_s: i64,
    pub started_ms: f64,
    pub expanded: bool,
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

pub fn child_keys_match(a: &str, b: &str) -> bool {
    if a.is_empty() || b.is_empty() {
        return false;
    }
    let a = a.to_ascii_lowercase();
    let b = b.to_ascii_lowercase();
    a == b || a.starts_with(&b) || b.starts_with(&a)
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
        if !child.parent_id.is_empty() && child.parent_id != active {
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
            output: card.map(|line| line.detail.clone()).unwrap_or_default(),
            elapsed_s: 0,
            started_ms: 0.0,
            expanded: false,
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
            unread: Some(true),
        }];
        let mut seen = HashMap::new();
        seen.insert("a".into(), 10_000i64);
        let rows = merge_sessions(&roots, &[], &[], &seen, "other", false);
        assert!(rows[0].unread);
        assert!(rows[0].busy);
        assert_eq!(rows[0].status, "waiting");
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
        assert!(child_keys_match("abcd1234", "abcd"));

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
    }

    #[test]
    fn rfc3339_parses_go_envelope_times() {
        // 2026-10-26T12:51:31Z, verified against `date -u`.
        assert_eq!(rfc3339_to_ms("2026-10-26T12:51:31Z"), 1_793_019_091_000.0);
        // Fractional seconds (Go RFC3339Nano) keep millisecond precision.
        assert_eq!(
            rfc3339_to_ms("2026-10-26T12:51:31.5Z"),
            1_793_019_091_500.0
        );
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
