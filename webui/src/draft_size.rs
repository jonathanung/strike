//! Draft-size guard for the composer (#1542). Pure data: no DOM.
//!
//! `context_view` guards the session's current fill, but never counted
//! the message about to be added to it: a session at ~70% accepted a
//! huge paste with no warning. This module estimates the draft with the
//! codebase's existing chars÷4 convention (the engine's
//! `estimateTokens`: `(chars + 3) / 4`; the TUI doctor labels the same
//! convention as an estimate, #1537) and projects it onto the daemon's
//! used/limit numbers. The estimate is labelled `≈` everywhere it is
//! shown and is never presented as measured tokens.
//!
//! Thresholds mirror `context_view`: warn when the projected fill
//! reaches the 80% warn ratio, critical when the projected fill reaches
//! the known window. Below critical the warning never blocks. At
//! critical the first Send press arms the warning (draft not
//! dispatched, not cleared) and a second press on the *same* draft
//! sends anyway — there is no state in which a message is unsendable.
//! When the session-level hold is already active, the draft warning
//! defers to it instead of stacking a second message (#1542 point 4).
//!
//! Attachments whose expanded size the composer cannot know
//! client-side are excluded from the estimate; the warning copy only
//! ever claims the draft *text* estimate.

use crate::model_menu::format_tokens;

/// Estimated tokens for `text` under the chars÷4 convention, rounded
/// up like the engine's estimator. Counts characters, not bytes.
pub fn estimate_tokens(text: &str) -> i64 {
    let chars = text.chars().count() as i64;
    if chars <= 0 {
        return 0;
    }
    (chars + 3) / 4
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DraftSizeLevel {
    /// No warning: unknown window, empty draft, below the warn ratio,
    /// or deferred to an active session-level hold.
    None,
    Warn,
    Critical,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DraftSizeView {
    /// Estimated draft tokens (chars÷4). An estimate, never measured.
    pub estimate: i64,
    /// `used + estimate`, the projected session fill in tokens.
    pub projected: i64,
    /// Projected fill as a whole percent of the known window (0 when
    /// the window is unknown). Not clamped: overshoot is the point.
    pub projected_percent: i64,
    pub level: DraftSizeLevel,
    /// One quiet inline line for the composer. Empty at `None`.
    pub message: String,
}

/// Project `draft_text` onto the session's daemon numbers.
///
/// `used`/`limit` are the resolved `context_view` inputs (daemon limit
/// wins over the catalog window — the caller passes `view.used` /
/// `view.limit`, so this never re-derives them). `session_held` is the
/// session-level hold from `context_view`: when it is active the draft
/// warning defers (level `None`, no message).
pub fn draft_size_view(
    used: i64,
    limit: i64,
    session_held: bool,
    draft_text: &str,
) -> DraftSizeView {
    let estimate = estimate_tokens(draft_text);
    let projected = used.max(0).saturating_add(estimate);
    let projected_percent = if limit > 0 {
        projected.saturating_mul(100) / limit
    } else {
        0
    };
    let level = if session_held || limit <= 0 || estimate <= 0 {
        DraftSizeLevel::None
    } else if projected >= limit {
        DraftSizeLevel::Critical
    } else if projected * 5 >= limit * 4 {
        DraftSizeLevel::Warn
    } else {
        DraftSizeLevel::None
    };
    let message = match level {
        DraftSizeLevel::None => String::new(),
        DraftSizeLevel::Warn | DraftSizeLevel::Critical => format!(
            "Draft ≈{} tokens — this would take the session to ~{}% of its window.",
            format_tokens(estimate),
            projected_percent
        ),
    };
    DraftSizeView {
        estimate,
        projected,
        projected_percent,
        level,
        message,
    }
}

/// The armed (second-press) line shown once the first Send press at
/// critical has been held. Names the estimate and the override.
pub fn armed_message(view: &DraftSizeView) -> String {
    format!(
        "Draft ≈{} tokens — this would take the session to ~{}% of its window. Send was held; press Send again to send anyway, or trim the draft.",
        format_tokens(view.estimate),
        view.projected_percent
    )
}

/// Second-press state machine, mirroring the session hold: at
/// critical, a send is held unless it is a repeat press on the exact
/// draft that was already armed. Editing the draft after arming
/// re-arms (a different oversized draft gets its own first press).
pub fn send_held(view: &DraftSizeView, armed_draft: &str, draft_text: &str) -> bool {
    view.level == DraftSizeLevel::Critical && armed_draft != draft_text
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn estimator_uses_chars_over_four_rounded_up() {
        assert_eq!(estimate_tokens(""), 0);
        assert_eq!(estimate_tokens("a"), 1);
        assert_eq!(estimate_tokens("abcd"), 1);
        assert_eq!(estimate_tokens("abcde"), 2);
        assert_eq!(estimate_tokens(&"x".repeat(400)), 100);
        // Characters, not bytes: 4 two-byte chars are still 1 token.
        assert_eq!(estimate_tokens("éééé"), 1);
    }

    #[test]
    fn below_warn_ratio_is_quiet() {
        // 100 used of 200; a 40-char draft (≈10) projects to 55%.
        let view = draft_size_view(100, 200, false, &"x".repeat(40));
        assert_eq!(view.level, DraftSizeLevel::None);
        assert!(view.message.is_empty());
        assert_eq!(view.estimate, 10);
    }

    #[test]
    fn warn_at_the_context_view_threshold() {
        // Session at 70%: a ≈12-token draft projects to ~82% → warn,
        // warn-only (never held).
        let draft = "x".repeat(48); // ≈12 tokens
        let view = draft_size_view(70, 100, false, &draft);
        assert_eq!(view.level, DraftSizeLevel::Warn);
        assert!(view.message.contains("≈12 tokens"), "{}", view.message);
        assert!(view.message.contains("~82%"), "{}", view.message);
        assert!(!send_held(&view, "", &draft));
    }

    #[test]
    fn critical_at_projected_full_and_second_press_sends() {
        // Session at 70% + a ≈40-token paste projects past the window.
        let draft = "x".repeat(160);
        let view = draft_size_view(70, 100, false, &draft);
        assert_eq!(view.level, DraftSizeLevel::Critical);
        assert!(view.projected_percent >= 100);
        // First press: held. Second press on the same draft: sends.
        assert!(send_held(&view, "", &draft));
        assert!(!send_held(&view, &draft, &draft));
        // Editing after arming re-arms the new draft.
        let edited = format!("{draft}y");
        assert!(send_held(&view, &draft, &edited));
        assert!(armed_message(&view).contains("press Send again"));
    }

    #[test]
    fn defers_to_an_active_session_hold() {
        let draft = "x".repeat(400);
        let view = draft_size_view(99, 100, true, &draft);
        assert_eq!(view.level, DraftSizeLevel::None);
        assert!(view.message.is_empty(), "no double-warning");
        assert!(!send_held(&view, "", &draft));
    }

    #[test]
    fn unknown_window_never_warns() {
        let view = draft_size_view(0, 0, false, &"x".repeat(100_000));
        assert_eq!(view.level, DraftSizeLevel::None);
        assert_eq!(view.projected_percent, 0);
    }
}
