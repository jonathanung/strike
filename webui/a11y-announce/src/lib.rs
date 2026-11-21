//! Live-region announcements for the cockpit (#1337).
//!
//! A supervisor who cannot see the screen must still hear the events they
//! are there to act on. This crate decides *what* to say and *how
//! urgently*; `webui/src/announce.rs` owns the two `aria-live` regions.
//!
//! - New permission asks are **assertive**: the human-in-the-loop control
//!   does not exist for a screen-reader user unless it interrupts.
//! - A turn ending (finished, or ended on an error notice) is **polite**.

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
    fn live_text_alternates() {
        assert_eq!(live_text("Turn finished.", 0), "Turn finished.");
        assert_eq!(live_text("Turn finished.", 1), "Turn finished.\u{00A0}");
        assert_ne!(live_text("x", 1), live_text("x", 2));
    }
}
