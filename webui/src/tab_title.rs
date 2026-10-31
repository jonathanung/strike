//! Dynamic browser tab title (#1405).
//!
//! The tab is the ambient signal for an always-on harness sitting in a
//! background tab. The title is derived — as a pure function of counts
//! only — from state the cockpit already holds client-side: roots with a
//! pending permission ask or question, unread sessions, and the
//! notification centre's unread count. No new fetches, no daemon or
//! protocol changes.
//!
//! Generic text only, by the same rule Jonathan set for OS notification
//! banners (2026-10-26): counts and fixed words, never a session name,
//! child name, prompt text, or handoff summary. That is guaranteed by
//! construction here — [`derive_title`] takes two `usize` counts and
//! nothing else, so there is no string input that could leak into the
//! title.
//!
//! Precedence:
//! 1. Any blocking ask (permission or question) →
//!    `"(N) Strike — waiting on you"`, N = combined blocking count.
//! 2. Otherwise any unread (finished sessions + notifications) →
//!    `"(N) Strike"`.
//! 3. Otherwise plain `"Strike"`.
//!
//! The favicon is not touched by this module: the status module
//! (`crate::status`) keeps owning the favicon via
//! `apply_document_favicon`.

use std::collections::HashSet;

use crate::model::{RootSummary, SessionItem};

/// Pure title derivation: precedence + counts, nothing else.
pub fn derive_title(blocking: usize, unread: usize) -> String {
    if blocking > 0 {
        format!("({blocking}) Strike \u{2014} waiting on you")
    } else if unread > 0 {
        format!("({unread}) Strike")
    } else {
        "Strike".to_string()
    }
}

/// Combined blocking count: one per root session with a pending
/// permission ask or a pending question. A session blocked on both is
/// one session waiting on you, so it counts once — the count answers
/// "how many sessions need me", and permission + question asks are
/// combined into that single waiting count (issue's recommended
/// default).
pub fn blocking_count(roots: &[RootSummary]) -> usize {
    roots
        .iter()
        .filter(|r| r.permission_pending || r.question_pending)
        .count()
}

/// Unread session count, matching the session list / status tabs: a
/// live root counts when it is unread and not the active session; a
/// saved session counts when it is unread and not already counted as a
/// live root.
pub fn unread_session_count(roots: &[RootSummary], saved: &[SessionItem], active: &str) -> usize {
    let mut live_ids = HashSet::new();
    let mut n = 0usize;
    for row in roots {
        live_ids.insert(row.id.as_str());
        if row.unread == Some(true) && row.id != active {
            n += 1;
        }
    }
    for row in saved {
        if row.unread == Some(true) && !live_ids.contains(row.id.as_str()) {
            n += 1;
        }
    }
    n
}

/// Set `document.title` from the derived title. No-op without a window
/// (tests / SSR-less contexts never call this; the pure functions above
/// are the tested surface).
pub fn apply_title(blocking: usize, unread: usize) {
    let Some(window) = web_sys::window() else {
        return;
    };
    let Some(doc) = window.document() else {
        return;
    };
    doc.set_title(&derive_title(blocking, unread));
}

#[cfg(test)]
mod tests {
    use super::*;

    fn root(id: &str, permission: bool, question: bool, unread: Option<bool>) -> RootSummary {
        RootSummary {
            id: id.to_string(),
            title: format!("secret name {id}"),
            busy: false,
            created_at: 0,
            status: String::new(),
            status_since: 0,
            last_activity_at: 0,
            permission_pending: permission,
            question_pending: question,
            unread,
        }
    }

    fn saved(id: &str, unread: Option<bool>) -> SessionItem {
        SessionItem {
            id: id.to_string(),
            title: format!("secret name {id}"),
            mtime: 0,
            workspace_id: String::new(),
            status: String::new(),
            last_activity_at: 0,
            unread,
        }
    }

    #[test]
    fn plain_when_nothing_waiting_or_unread() {
        assert_eq!(derive_title(0, 0), "Strike");
    }

    #[test]
    fn waiting_takes_precedence_over_unread() {
        assert_eq!(derive_title(1, 0), "(1) Strike \u{2014} waiting on you");
        assert_eq!(derive_title(2, 5), "(2) Strike \u{2014} waiting on you");
    }

    #[test]
    fn unread_only_shows_count() {
        assert_eq!(derive_title(0, 1), "(1) Strike");
        assert_eq!(derive_title(0, 3), "(3) Strike");
    }

    #[test]
    fn title_is_generic_counts_and_fixed_words_only() {
        // Inputs are counts only; no session-derived string can appear.
        for title in [derive_title(0, 0), derive_title(4, 0), derive_title(0, 4)] {
            assert!(!title.contains("secret"), "title leaked a name: {title}");
        }
    }

    #[test]
    fn blocking_counts_permission_and_question_combined_once_per_session() {
        let roots = vec![
            root("a", true, false, None),
            root("b", false, true, None),
            root("c", true, true, None), // both flags: still one session
            root("d", false, false, None),
        ];
        assert_eq!(blocking_count(&roots), 3);
        assert_eq!(blocking_count(&[]), 0);
    }

    #[test]
    fn unread_sessions_exclude_active_and_dedupe_live() {
        let roots = vec![
            root("live-unread", false, false, Some(true)),
            root("live-active", false, false, Some(true)),
            root("live-read", false, false, Some(false)),
            root("live-unknown", false, false, None),
        ];
        let saved = vec![
            saved("live-unread", Some(true)), // already counted as live
            saved("saved-unread", Some(true)),
            saved("saved-read", Some(false)),
        ];
        assert_eq!(unread_session_count(&roots, &saved, "live-active"), 2);
        assert_eq!(unread_session_count(&[], &[], ""), 0);
    }

    #[test]
    fn clearing_blocking_falls_back_to_unread_then_plain() {
        // The live-reset path the effect relies on: same unread count,
        // blocking dropping to zero must drop the waiting wording.
        assert_eq!(derive_title(1, 2), "(1) Strike \u{2014} waiting on you");
        assert_eq!(derive_title(0, 2), "(2) Strike");
        assert_eq!(derive_title(0, 0), "Strike");
    }
}
