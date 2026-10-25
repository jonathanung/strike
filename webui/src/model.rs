//! Wire types the UI actually reads. Unknown JSON fields are ignored.

use std::collections::HashMap;

use serde::Deserialize;

#[derive(Clone, Debug, Deserialize)]
pub struct Health {
    pub ok: bool,
    pub version: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Workspace {
    pub id: String,
    pub name: String,
    pub path: String,
}

#[derive(Clone, Debug, Deserialize)]
pub struct WorkspacesResponse {
    pub workspaces: Vec<Workspace>,
    #[serde(default, rename = "activeId")]
    pub active_id: String,
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
    #[allow(dead_code)]
    pub created_at: i64,
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
    #[allow(dead_code)]
    pub mtime: i64,
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

pub fn fmt_elapsed(secs: i64) -> String {
    let secs = secs.max(0);
    let m = secs / 60;
    let s = secs % 60;
    if m >= 60 {
        format!("{}:{:02}:{:02}", m / 60, m % 60, s)
    } else {
        format!("{m}:{s:02}")
    }
}
