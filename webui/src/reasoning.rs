//! Opt-in reasoning visibility (#1372).
//!
//! The engine streams `reasoning.delta`; the cockpit used to drop it. The
//! TUI shows reasoning in a muted cell, hidden by default and toggled with
//! `/think` (#188). This is the cockpit equivalent.
//!
//! Quarantine (the #1310 "secret chain" concern): reasoning never enters a
//! transcript [`Message`](crate::transcript::Message). It lives in this
//! side log, keyed by the id of the message it preceded, so message bodies,
//! copy text, markdown export, find-in-transcript and the minimap never see
//! it. The pane renders it only while the per-session toggle is on, as a
//! labelled block that is visibly not the answer.
//!
//! Attachment rule: deltas accumulate as `live` until the next message of
//! the turn is pushed (tool row, notice or the answer), and are then
//! attached to that message (rendered above it). An answer that keeps
//! streaming after more reasoning gets that reasoning appended to its own
//! entry. Reasoning still `live` at turn end (or before the next user
//! message) attaches to the turn's last message.

use std::collections::HashMap;

/// Keyboard shortcut in the transcript pane that flips the toggle.
pub const TOGGLE_KEY: &str = "h";

#[derive(Clone, Debug, Default, PartialEq)]
pub struct ReasoningLog {
    attached: HashMap<u32, String>,
    live: String,
}

impl ReasoningLog {
    /// Append one `reasoning.delta` chunk to the reasoning in progress.
    pub fn push_delta(&mut self, text: &str) {
        self.live.push_str(text);
    }

    /// Move the reasoning in progress onto message `id`. No-op when there
    /// is none, so callers can invoke it on every push.
    pub fn attach_live(&mut self, id: u32) {
        if self.live.is_empty() {
            return;
        }
        let text = std::mem::take(&mut self.live);
        self.attached.entry(id).or_default().push_str(&text);
    }

    /// Reasoning attached to message `id`, if any.
    pub fn for_message(&self, id: u32) -> Option<&str> {
        self.attached
            .get(&id)
            .map(String::as_str)
            .filter(|s| !s.trim().is_empty())
    }

    /// Reasoning streaming now, not yet attached to a message.
    pub fn live(&self) -> &str {
        &self.live
    }

    /// Whether this session has produced any visible reasoning text. The
    /// toggle only renders once this is true.
    pub fn is_empty(&self) -> bool {
        self.live.trim().is_empty() && self.attached.values().all(|s| s.trim().is_empty())
    }
}

/// Summary line of a reasoning block.
pub fn block_label(live: bool) -> &'static str {
    if live {
        "Reasoning — thinking…"
    } else {
        "Reasoning"
    }
}

/// Text of the toggle button.
pub fn toggle_label(on: bool) -> &'static str {
    if on {
        "Hide reasoning"
    } else {
        "Show reasoning"
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn live_reasoning_attaches_to_next_message_once() {
        let mut log = ReasoningLog::default();
        assert!(log.is_empty());
        log.push_delta("look at ");
        log.push_delta("the parser");
        assert_eq!(log.live(), "look at the parser");
        assert!(!log.is_empty());
        log.attach_live(3);
        assert_eq!(log.live(), "");
        assert_eq!(log.for_message(3), Some("look at the parser"));
        // Nothing live: attaching again changes nothing.
        log.attach_live(4);
        assert_eq!(log.for_message(4), None);
    }

    #[test]
    fn later_reasoning_appends_to_the_same_message() {
        let mut log = ReasoningLog::default();
        log.push_delta("first. ");
        log.attach_live(7);
        log.push_delta("second.");
        log.attach_live(7);
        assert_eq!(log.for_message(7), Some("first. second."));
    }

    #[test]
    fn whitespace_only_reasoning_counts_as_empty() {
        let mut log = ReasoningLog::default();
        log.push_delta("  \n");
        assert!(log.is_empty());
        log.attach_live(1);
        assert_eq!(log.for_message(1), None);
        assert!(log.is_empty());
    }

    #[test]
    fn labels() {
        assert_eq!(block_label(true), "Reasoning — thinking…");
        assert_eq!(block_label(false), "Reasoning");
        assert_eq!(toggle_label(false), "Show reasoning");
        assert_eq!(toggle_label(true), "Hide reasoning");
    }
}
