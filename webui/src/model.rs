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

#[derive(Clone, Debug, Deserialize)]
pub struct Bootstrap {
    #[serde(default)]
    pub status: Option<LiveStatus>,
    #[serde(default)]
    pub skills: Vec<SkillInfo>,
    #[serde(default, rename = "composerCommands")]
    pub composer_commands: Vec<ComposerCommand>,
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
    /// Collapsed detail for a child or protocol card. Empty for prose.
    pub detail: String,
    /// Dedupe key for child cards (name, or session id).
    pub key: String,
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

pub fn fmt_ago(mtime: i64, now_s: i64) -> String {
    if mtime <= 0 || now_s <= 0 {
        return String::new();
    }
    let delta = (now_s - mtime).max(0);
    if delta < 60 {
        return "now".into();
    }
    if delta < 3600 {
        return format!("{}m", delta / 60);
    }
    if delta < 86_400 {
        return format!("{}h", delta / 3600);
    }
    format!("{}d", delta / 86_400)
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
        }];
        let rows = activity_panel(&[], &kids, &cards, "root");
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].name, "scribe");
        assert_eq!(rows[0].state, "blocked");
        assert_eq!(rows[0].task, "schema: handoff missing");
    }
}
