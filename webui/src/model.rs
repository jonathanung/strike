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
    #[serde(default)]
    pub rules: String,
    #[serde(default, rename = "permissionMode")]
    pub permission_mode: String,
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
    pub mtime: i64,
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
    pub busy: bool,
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

/// One row in the session list. Unread and pinned are client state.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SessionRow {
    pub id: String,
    pub title: String,
    pub mtime: i64,
    pub busy: bool,
    pub live: bool,
    pub pinned: bool,
    pub unread: bool,
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

/// Merge live roots with saved sessions. `active_running` marks the active
/// root in progress when a delegated child is still running.
pub fn merge_sessions(
    roots: &[RootSummary],
    sessions: &[SessionItem],
    pins: &[String],
    seen: &HashMap<String, i64>,
    active: &str,
    active_running: bool,
) -> Vec<SessionRow> {
    let mut by_id: HashMap<String, SessionRow> = HashMap::new();
    for s in sessions {
        by_id.insert(
            s.id.clone(),
            SessionRow {
                id: s.id.clone(),
                title: s.title.clone(),
                mtime: s.mtime.max(0),
                busy: false,
                live: false,
                pinned: pins.iter().any(|p| p == &s.id),
                unread: false,
            },
        );
    }
    for r in roots {
        let created = unix_seconds(r.created_at);
        let entry = by_id.entry(r.id.clone()).or_insert_with(|| SessionRow {
            id: r.id.clone(),
            title: r.title.clone(),
            mtime: created,
            busy: false,
            live: false,
            pinned: pins.iter().any(|p| p == &r.id),
            unread: false,
        });
        entry.live = true;
        entry.busy = r.busy;
        entry.pinned = pins.iter().any(|p| p == &r.id);
        if entry.title.is_empty() {
            entry.title = r.title.clone();
        }
        if entry.mtime == 0 {
            entry.mtime = created;
        }
    }
    for row in by_id.values_mut() {
        if row.id == active && active_running {
            row.busy = true;
        }
        let seen_at = seen.get(&row.id).copied().unwrap_or(0);
        row.unread = row.id != active && row.mtime > seen_at && row.mtime > 0;
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
        }
    }

    #[test]
    fn merge_marks_unread_and_prefers_session_mtime() {
        let roots = vec![RootSummary {
            id: "a".into(),
            title: String::new(),
            busy: false,
            created_at: 1_700_000_000_000,
        }];
        let sessions = vec![SessionItem {
            id: "a".into(),
            title: "Alpha".into(),
            mtime: 50,
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
    fn permission_modes_are_the_real_five() {
        let ids: Vec<_> = permission_modes().iter().map(|m| m.id).collect();
        assert_eq!(
            ids,
            vec!["default", "plan", "soft-approve", "accept-edits", "yolo"]
        );
        assert_eq!(mode_label("plan"), "Plan");
        assert_eq!(mode_label("nope"), "Default");
    }
}
