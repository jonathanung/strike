//! Live-region announcements for the cockpit (#1337).
//!
//! A supervisor who cannot see the screen must still hear the events they
//! are there to act on. This crate decides *what* to say and *how
//! urgently*; `webui/src/announce.rs` owns the two `aria-live` regions.
//!
//! - New permission asks are **assertive**: the human-in-the-loop control
//!   does not exist for a screen-reader user unless it interrupts.
//! - A turn ending (finished, or ended on an error notice) is **polite**.
//! - New agent questions are **assertive**, like approvals: an unanswered
//!   question stalls the agent the same way (#1878).
//! - Session status changes (#1263) are **polite**.
//!
//! [`focus`] holds the focus-trap decisions for modal dialogs.

pub mod focus;

/// `aria-live` urgency.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Politeness {
    Polite,
    Assertive,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Announcement {
    pub text: String,
    pub politeness: Politeness,
}

/// Announce permission asks whose ids were not in `seen`. `current` is
/// `(request_id, permission)` in arrival order. Returns `None` when nothing
/// is new (an ask being answered or withdrawn is not announced).
pub fn new_asks(seen: &[String], current: &[(String, String)]) -> Option<Announcement> {
    let fresh: Vec<&(String, String)> = current
        .iter()
        .filter(|(id, _)| !id.is_empty() && !seen.iter().any(|s| s == id))
        .collect();
    let text = match fresh.as_slice() {
        [] => return None,
        [(_, permission)] => {
            let what = permission.trim();
            if what.is_empty() {
                "Approval requested.".to_string()
            } else {
                format!("Approval requested: {what}.")
            }
        }
        many => format!("{} approvals requested.", many.len()),
    };
    let waiting = current.len();
    let text = if waiting > fresh.len() {
        format!("{text} {waiting} waiting in total.")
    } else {
        text
    };
    Some(Announcement {
        text,
        politeness: Politeness::Assertive,
    })
}

/// Announce the active session's turn ending. `same_root` is false when
/// the user switched sessions between observations — the busy flag then
/// belongs to a different session and is not a turn ending.
/// `last_notice` is the title of the transcript's final message when that
/// message is an error notice.
pub fn turn_end(
    was_busy: bool,
    now_busy: bool,
    same_root: bool,
    last_notice: Option<&str>,
) -> Option<Announcement> {
    if !same_root || !was_busy || now_busy {
        return None;
    }
    let text = match last_notice.map(str::trim) {
        Some(title) if !title.is_empty() => format!("Turn ended with an error: {title}."),
        Some(_) => "Turn ended with an error.".to_string(),
        None => "Turn finished.".to_string(),
    };
    Some(Announcement {
        text,
        politeness: Politeness::Polite,
    })
}

/// Announce agent questions whose ids were not in `seen`. `current` is
/// `(request_id, first question text)` in arrival order. Answered or
/// dismissed questions are not announced.
pub fn new_questions(seen: &[String], current: &[(String, String)]) -> Option<Announcement> {
    let fresh: Vec<&(String, String)> = current
        .iter()
        .filter(|(id, _)| !id.is_empty() && !seen.iter().any(|s| s == id))
        .collect();
    let text = match fresh.as_slice() {
        [] => return None,
        [(_, question)] => {
            let q = question.trim();
            if q.is_empty() {
                "The agent asked a question.".to_string()
            } else if q.ends_with(|c: char| matches!(c, '?' | '.' | '!')) {
                format!("Agent question: {q}")
            } else {
                format!("Agent question: {q}.")
            }
        }
        many => format!("{} agent questions asked.", many.len()),
    };
    let waiting = current.len();
    let text = if waiting > fresh.len() {
        format!("{text} {waiting} waiting in total.")
    } else {
        text
    };
    Some(Announcement {
        text,
        politeness: Politeness::Assertive,
    })
}

/// One session row as the status strip shows it. `kind` is the status id
/// (`idle`, `running`, `waiting`, `blocked`, `failed`, `done`, `stopped`,
/// `disconnected`); `word` is its display label.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SessionState {
    pub id: String,
    pub label: String,
    pub kind: String,
    pub word: String,
}

/// Most session changes named in one announcement; the rest are counted.
pub const SESSION_CHANGES_NAMED: usize = 3;

/// Announce session status changes between two observations (polite).
///
/// Not announced: sessions that are new or gone (not a status change),
/// any change *to* `running` (the user or a schedule just started it), and
/// the active session going `idle` or `waiting` — the turn-end, approval
/// and question announcements already speak for those.
pub fn session_changes(
    prev: &[SessionState],
    current: &[SessionState],
    active: &str,
) -> Option<Announcement> {
    let changed: Vec<&SessionState> = current
        .iter()
        .filter(|row| {
            let Some(before) = prev.iter().find(|p| p.id == row.id) else {
                return false;
            };
            if before.kind == row.kind || row.kind == "running" {
                return false;
            }
            !(row.id == active && matches!(row.kind.as_str(), "idle" | "waiting"))
        })
        .collect();
    if changed.is_empty() {
        return None;
    }
    let mut parts: Vec<String> = changed
        .iter()
        .take(SESSION_CHANGES_NAMED)
        .map(|row| {
            let label = row.label.trim();
            let label = if label.is_empty() { "Untitled" } else { label };
            format!("Session {label}: {}.", row.word.trim())
        })
        .collect();
    let more = changed.len().saturating_sub(SESSION_CHANGES_NAMED);
    if more == 1 {
        parts.push("1 more session changed.".to_string());
    } else if more > 1 {
        parts.push(format!("{more} more sessions changed."));
    }
    Some(Announcement {
        text: parts.join(" "),
        politeness: Politeness::Polite,
    })
}

/// Live regions only speak when their text changes. Alternate a trailing
/// no-break space by sequence number so the same sentence twice in a row
/// ("Turn finished.") is announced both times.
pub fn live_text(text: &str, seq: u32) -> String {
    if seq % 2 == 0 {
        text.to_string()
    } else {
        format!("{text}\u{00A0}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ask(id: &str, perm: &str) -> (String, String) {
        (id.to_string(), perm.to_string())
    }

    #[test]
    fn new_asks_cases() {
        let cases: &[(&[&str], &[(&str, &str)], Option<&str>)] = &[
            (&[], &[], None),
            (&[], &[("a", "bash")], Some("Approval requested: bash.")),
            (&[], &[("a", "")], Some("Approval requested.")),
            (&["a"], &[("a", "bash")], None),
            (&["a"], &[("a", "bash"), ("b", "edit")], Some("Approval requested: edit. 2 waiting in total.")),
            (&[], &[("a", "bash"), ("b", "edit")], Some("2 approvals requested.")),
            (&["a", "b"], &[("b", "edit")], None),
            (&[], &[("", "bash")], None),
        ];
        for (seen, current, want) in cases {
            let seen: Vec<String> = seen.iter().map(|s| s.to_string()).collect();
            let current: Vec<(String, String)> = current.iter().map(|(i, p)| ask(i, p)).collect();
            let got = new_asks(&seen, &current);
            assert_eq!(got.as_ref().map(|a| a.text.as_str()), *want, "seen={seen:?} current={current:?}");
            if let Some(a) = got {
                assert_eq!(a.politeness, Politeness::Assertive);
            }
        }
    }

    #[test]
    fn turn_end_cases() {
        let cases: &[(bool, bool, bool, Option<&str>, Option<&str>)] = &[
            (true, false, true, None, Some("Turn finished.")),
            (true, false, true, Some("Provider error"), Some("Turn ended with an error: Provider error.")),
            (true, false, true, Some("  "), Some("Turn ended with an error.")),
            (false, false, true, None, None),
            (false, true, true, None, None),
            (true, true, true, None, None),
            (true, false, false, None, None),
        ];
        for (was, now, same, notice, want) in cases {
            let got = turn_end(*was, *now, *same, *notice);
            assert_eq!(got.as_ref().map(|a| a.text.as_str()), *want, "{was} {now} {same} {notice:?}");
            if let Some(a) = got {
                assert_eq!(a.politeness, Politeness::Polite);
            }
        }
    }

    #[test]
    fn new_questions_cases() {
        let cases: &[(&[&str], &[(&str, &str)], Option<&str>)] = &[
            (&[], &[], None),
            (&[], &[("q1", "Use Postgres?")], Some("Agent question: Use Postgres?")),
            (&[], &[("q1", "Pick a name")], Some("Agent question: Pick a name.")),
            (&[], &[("q1", "  ")], Some("The agent asked a question.")),
            (&["q1"], &[("q1", "Use Postgres?")], None),
            (&["q1"], &[("q1", "A?"), ("q2", "B?")], Some("Agent question: B? 2 waiting in total.")),
            (&[], &[("q1", "A?"), ("q2", "B?")], Some("2 agent questions asked.")),
            (&["q1", "q2"], &[("q2", "B?")], None),
            (&[], &[("", "A?")], None),
        ];
        for (seen, current, want) in cases {
            let seen: Vec<String> = seen.iter().map(|s| s.to_string()).collect();
            let current: Vec<(String, String)> = current.iter().map(|(i, q)| ask(i, q)).collect();
            let got = new_questions(&seen, &current);
            assert_eq!(got.as_ref().map(|a| a.text.as_str()), *want, "seen={seen:?} current={current:?}");
            if let Some(a) = got {
                assert_eq!(a.politeness, Politeness::Assertive);
            }
        }
    }

    fn row(id: &str, label: &str, kind: &str) -> SessionState {
        let word = match kind {
            "" => String::new(),
            k => {
                let mut c = k.chars();
                c.next().map(|f| f.to_uppercase().chain(c).collect::<String>()).unwrap_or_default()
            }
        };
        SessionState { id: id.into(), label: label.into(), kind: kind.into(), word }
    }

    #[test]
    fn session_changes_cases() {
        let cases: Vec<(&str, Vec<SessionState>, Vec<SessionState>, &str, Option<&str>)> = vec![
            ("first observation", vec![], vec![row("a", "Fix login", "idle")], "a", None),
            ("unchanged", vec![row("a", "A", "idle")], vec![row("a", "A", "idle")], "", None),
            ("background done", vec![row("b", "Docs", "running")], vec![row("b", "Docs", "done")], "a", Some("Session Docs: Done.")),
            ("background waiting", vec![row("b", "Docs", "running")], vec![row("b", "Docs", "waiting")], "a", Some("Session Docs: Waiting.")),
            ("background idle", vec![row("b", "Docs", "running")], vec![row("b", "Docs", "idle")], "a", Some("Session Docs: Idle.")),
            ("to running is silent", vec![row("b", "Docs", "idle")], vec![row("b", "Docs", "running")], "a", None),
            ("active idle is the turn-end's job", vec![row("a", "A", "running")], vec![row("a", "A", "idle")], "a", None),
            ("active waiting is the approval's job", vec![row("a", "A", "running")], vec![row("a", "A", "waiting")], "a", None),
            ("active failed is announced", vec![row("a", "A", "running")], vec![row("a", "A", "failed")], "a", Some("Session A: Failed.")),
            ("new session is not a change", vec![row("a", "A", "idle")], vec![row("a", "A", "idle"), row("b", "B", "done")], "a", None),
            ("blank label", vec![row("b", " ", "running")], vec![row("b", " ", "blocked")], "a", Some("Session Untitled: Blocked.")),
            (
                "many changes are capped",
                vec![row("1", "One", "running"), row("2", "Two", "running"), row("3", "Three", "running"), row("4", "Four", "running"), row("5", "Five", "running")],
                vec![row("1", "One", "done"), row("2", "Two", "done"), row("3", "Three", "failed"), row("4", "Four", "stopped"), row("5", "Five", "done")],
                "",
                Some("Session One: Done. Session Two: Done. Session Three: Failed. 2 more sessions changed."),
            ),
            (
                "one more",
                vec![row("1", "One", "running"), row("2", "Two", "running"), row("3", "Three", "running"), row("4", "Four", "running")],
                vec![row("1", "One", "done"), row("2", "Two", "done"), row("3", "Three", "done"), row("4", "Four", "done")],
                "",
                Some("Session One: Done. Session Two: Done. Session Three: Done. 1 more session changed."),
            ),
        ];
        for (name, prev, current, active, want) in cases {
            let got = session_changes(&prev, &current, active);
            assert_eq!(got.as_ref().map(|a| a.text.as_str()), want, "{name}");
            if let Some(a) = got {
                assert_eq!(a.politeness, Politeness::Polite, "{name}");
            }
        }
    }

    #[test]
    fn live_text_alternates() {
        assert_eq!(live_text("Turn finished.", 0), "Turn finished.");
        assert_eq!(live_text("Turn finished.", 1), "Turn finished.\u{00A0}");
        assert_ne!(live_text("x", 1), live_text("x", 2));
    }
}
