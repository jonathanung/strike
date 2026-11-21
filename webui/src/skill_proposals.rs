//! Skill proposals in Customize → Skills (#1611). The daemon mines this
//! workspace's sessions for repeated workflows, but only after the operator
//! opts in and asks it to look. It drafts each workflow as a skill and holds
//! the drafts pending. This module is the wire types plus the copy the cards
//! show. Accept, Edit, and Dismiss live in `customize.rs` next to the skill
//! editor that Edit opens.

use serde::Deserialize;

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(default, rename_all = "camelCase")]
pub struct Evidence {
    pub session_id: String,
    pub title: String,
    pub at: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(default, rename_all = "camelCase")]
pub struct Proposal {
    pub id: String,
    pub name: String,
    pub description: String,
    pub body: String,
    pub tools: Vec<String>,
    pub count: u32,
    pub sessions: Vec<Evidence>,
    pub drafted_by: String,
    /// The model that rewrote the local draft (#1958); empty for a local draft.
    pub model: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(default, rename_all = "camelCase")]
pub struct State {
    pub enabled: bool,
    pub threshold: u32,
    pub window_days: u32,
    pub last_scan: Option<String>,
    pub pending: Vec<Proposal>,
    /// #1958: the workspace's model-drafting opt-in, and whether the daemon
    /// has a provider to draft with.
    pub model_draft: bool,
    pub model_draft_available: bool,
}

fn day(stamp: &str) -> &str {
    let s = stamp.trim();
    if s.len() >= 10 && s.as_bytes()[4] == b'-' && s.as_bytes()[7] == b'-' && !s.starts_with("0001-") {
        &s[..10]
    } else {
        ""
    }
}

/// The rule the scan applies, stated plainly.
pub fn rule_text(st: &State) -> String {
    let n = if st.threshold == 0 { 3 } else { st.threshold };
    let days = if st.window_days == 0 { 28 } else { st.window_days };
    format!("Proposes a skill when {n} sessions in {days} days open with a similar request and run similar tools. Reads only this workspace's sessions; nothing leaves the gateway.")
}

/// Status line under the controls.
pub fn status_text(st: &State) -> String {
    if !st.enabled {
        return "Off. Turn it on to look for repeated workflows.".into();
    }
    let last = st.last_scan.as_deref().map(day).unwrap_or("");
    if last.is_empty() {
        return "On. Not scanned yet.".into();
    }
    match st.pending.len() {
        0 => format!("Last looked {last}. No repeated workflows to propose."),
        1 => format!("Last looked {last}. 1 proposal."),
        n => format!("Last looked {last}. {n} proposals."),
    }
}

/// "From 3 sessions · drafted by local pattern match (no model call)", or
/// "Drafted by <model> from 3 sessions" for a model draft (#1958).
pub fn evidence_text(p: &Proposal) -> String {
    let n = if p.count == 0 { p.sessions.len() as u32 } else { p.count };
    let sessions = if n == 1 { "1 session".to_string() } else { format!("{n} sessions") };
    if !p.model.trim().is_empty() {
        return format!("Drafted by {} from {sessions}", p.model.trim());
    }
    if p.drafted_by.is_empty() {
        format!("From {sessions}")
    } else {
        format!("From {sessions} · drafted by {}", p.drafted_by)
    }
}

/// The Skills tab badge (#1958): the pending proposal count, or None when
/// there is nothing to review.
pub fn badge_count(st: &State) -> Option<String> {
    match st.pending.len() {
        0 => None,
        n if n > 99 => Some("99+".into()),
        n => Some(n.to_string()),
    }
}

/// The model-drafting toggle label and its hint (#1958).
pub fn model_draft_label(st: &State) -> &'static str {
    if st.model_draft {
        "Draft with my model: On"
    } else {
        "Draft with my model: Off"
    }
}

pub fn model_draft_hint(st: &State) -> &'static str {
    if !st.model_draft_available {
        "No model provider is configured, so drafts are written by local pattern matching."
    } else if !st.enabled {
        "Model drafting runs only while the scan is on."
    } else {
        "When on, each draft is rewritten by your configured provider after a scan, from redacted openings and tool names. The card says which model drafted it."
    }
}

/// One evidence row: title (or id) and day.
pub fn evidence_row(e: &Evidence) -> String {
    let name = if e.title.trim().is_empty() { e.session_id.as_str() } else { e.title.as_str() };
    let d = day(&e.at);
    if d.is_empty() {
        name.to_string()
    } else {
        format!("{name} · {d}")
    }
}

/// Whether an evidence session id can be opened in the cockpit: a plain
/// session token, not empty and with no path separators or spaces.
pub fn openable(session_id: &str) -> bool {
    let s = session_id.trim();
    !s.is_empty() && s.len() <= 128 && s == session_id && !s.chars().any(|c| matches!(c, '/' | '\\' | ' ' | '\t' | '\n' | '\r'))
}

/// The daemon's `{"error": …}` text from an `HTTP <code> <body>` failure,
/// or the raw text when it is not that shape.
pub fn error_text(raw: &str) -> String {
    let body = raw
        .strip_prefix("HTTP ")
        .and_then(|rest| rest.split_once(' ').map(|(_, b)| b))
        .unwrap_or(raw);
    serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .unwrap_or_else(|| raw.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn state(raw: &str) -> State {
        serde_json::from_str(raw).unwrap()
    }

    #[test]
    fn parses_daemon_state() {
        let st = state(
            r#"{"enabled":true,"threshold":3,"windowDays":28,"lastScan":"2026-11-10T09:00:00Z","pending":[{"id":"abc","name":"bump-version-update","description":"Repeated workflow: Bump","body":"Steps","tools":["bash","edit"],"count":3,"sessions":[{"sessionId":"s1","title":"Release 1","at":"2026-11-01T09:00:00Z"}],"draftedBy":"local pattern match (no model call)","createdAt":"2026-11-10T09:00:00Z"}]}"#,
        );
        assert!(st.enabled);
        assert_eq!(st.pending.len(), 1);
        assert_eq!(st.pending[0].sessions[0].session_id, "s1");
        assert_eq!(state("{}"), State::default());
    }

    #[test]
    fn status_lines() {
        let mut st = State::default();
        assert_eq!(status_text(&st), "Off. Turn it on to look for repeated workflows.");
        st.enabled = true;
        assert_eq!(status_text(&st), "On. Not scanned yet.");
        st.last_scan = Some("2026-11-10T09:00:00Z".into());
        assert_eq!(status_text(&st), "Last looked 2026-11-10. No repeated workflows to propose.");
        st.pending = vec![Proposal::default()];
        assert_eq!(status_text(&st), "Last looked 2026-11-10. 1 proposal.");
        st.pending.push(Proposal::default());
        assert_eq!(status_text(&st), "Last looked 2026-11-10. 2 proposals.");
        assert!(rule_text(&State::default()).starts_with("Proposes a skill when 3 sessions in 28 days"));
    }

    #[test]
    fn evidence_lines() {
        let p = Proposal { count: 3, drafted_by: "local pattern match (no model call)".into(), ..Default::default() };
        assert_eq!(evidence_text(&p), "From 3 sessions · drafted by local pattern match (no model call)");
        let one = Proposal { sessions: vec![Evidence::default()], ..Default::default() };
        assert_eq!(evidence_text(&one), "From 1 session");
        let e = Evidence { session_id: "s1".into(), title: String::new(), at: "2026-11-01T09:00:00Z".into() };
        assert_eq!(evidence_row(&e), "s1 · 2026-11-01");
        let e = Evidence { session_id: "s1".into(), title: "Release".into(), at: "bad".into() };
        assert_eq!(evidence_row(&e), "Release");
    }

    #[test]
    fn model_draft_disclosure() {
        let p = Proposal { count: 3, drafted_by: "claude-test".into(), model: "claude-test".into(), ..Default::default() };
        assert_eq!(evidence_text(&p), "Drafted by claude-test from 3 sessions");
        let st = state(r#"{"enabled":true,"modelDraft":true,"modelDraftAvailable":true,"pending":[{"id":"a","model":"m"}]}"#);
        assert!(st.model_draft && st.model_draft_available);
        assert_eq!(st.pending[0].model, "m");
        assert_eq!(model_draft_label(&st), "Draft with my model: On");
        assert!(model_draft_hint(&st).contains("configured provider"));
        assert!(model_draft_hint(&State { model_draft_available: true, ..Default::default() }).contains("only while the scan is on"));
        assert!(model_draft_hint(&State::default()).contains("No model provider"));
        assert_eq!(model_draft_label(&State::default()), "Draft with my model: Off");
    }

    #[test]
    fn badge_counts_pending() {
        let mut st = State::default();
        assert_eq!(badge_count(&st), None);
        st.pending = vec![Proposal::default(); 2];
        assert_eq!(badge_count(&st).as_deref(), Some("2"));
        st.pending = vec![Proposal::default(); 120];
        assert_eq!(badge_count(&st).as_deref(), Some("99+"));
    }

    #[test]
    fn openable_session_ids() {
        for (id, want) in [
            ("20260102T000000.000000000Z-ABC", true),
            ("s1", true),
            ("", false),
            (" s1", false),
            ("a/b", false),
            ("a\\b", false),
            ("a b", false),
        ] {
            assert_eq!(openable(id), want, "{id:?}");
        }
        assert!(!openable(&"x".repeat(129)));
    }

    #[test]
    fn error_text_reads_daemon_errors() {
        assert_eq!(
            error_text(r#"HTTP 409 {"error":"skill proposals are off for this workspace"}"#),
            "skill proposals are off for this workspace"
        );
        assert_eq!(error_text("network down"), "network down");
        assert_eq!(error_text("HTTP 500 oops"), "HTTP 500 oops");
    }
}
