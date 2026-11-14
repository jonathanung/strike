//! Per-turn usage in the transcript (#1558).
//!
//! The daemon emits one `usage.reported` per completed provider stream,
//! including tool-loop intermediate streams. A turn's footer sums the
//! streams that arrived between its start and its `turn.completed`, and
//! is stamped once on the turn's final line, beside the elapsed label
//! (#1364).
//!
//! Honesty rules, reused from the protocol's `TokenCount`:
//! - a count is summed only from `known: true` values; a field no stream
//!   reported stays unknown and is never shown as `0`;
//! - a turn with no known input or output count renders no footer;
//! - any stream with `source: "estimated"` labels the whole turn `est.`.
//!
//! Child rule: each footer counts only its own session's streams. A
//! `usage.reported` whose `sessionId` differs from the turn's own session
//! (taken from `turn.started`) belongs to another session — a delegated
//! child — and is ignored, so a parent turn never silently double-counts
//! a child; a child session's transcript footers its own turns the same
//! way. Session totals and dollars stay on the Usage page (tokens-only
//! in this slice).

use serde_json::Value;

use crate::model_menu::{format_tokens, known_tokens};

/// Running token sums for one turn. `None` = no stream reported it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TurnUsage {
    pub input: Option<i64>,
    pub output: Option<i64>,
    pub cache_read: Option<i64>,
    pub cache_creation: Option<i64>,
    pub estimated: bool,
    pub streams: u32,
}

fn add(slot: &mut Option<i64>, v: Option<i64>) {
    if let Some(n) = v {
        *slot = Some(slot.unwrap_or(0) + n);
    }
}

impl TurnUsage {
    /// Fold one `usage.reported` data object in. `own_session` is the
    /// turn's session id (empty when unknown). Streams from another
    /// session and streams with no known token count are ignored.
    pub fn absorb(&mut self, data: &Value, own_session: &str) {
        let session = data.get("sessionId").and_then(|v| v.as_str()).unwrap_or("");
        if !own_session.is_empty() && !session.is_empty() && session != own_session {
            return;
        }
        let input = known_tokens(data.get("input"));
        let output = known_tokens(data.get("output"));
        let cache_read = known_tokens(data.get("cacheRead"));
        let cache_creation = known_tokens(data.get("cacheCreation"));
        if input.is_none() && output.is_none() && cache_read.is_none() && cache_creation.is_none() {
            return;
        }
        add(&mut self.input, input);
        add(&mut self.output, output);
        add(&mut self.cache_read, cache_read);
        add(&mut self.cache_creation, cache_creation);
        if data.get("source").and_then(|v| v.as_str()) == Some("estimated") {
            self.estimated = true;
        }
        self.streams += 1;
    }

    /// The quiet footer text, or `None` when the turn has no known input
    /// or output count (no fabricated zeros).
    pub fn label(&self) -> Option<String> {
        let mut parts = Vec::new();
        if let Some(n) = self.input {
            parts.push(format!("{} in", format_tokens(n)));
        }
        if let Some(n) = self.output {
            parts.push(format!("{} out", format_tokens(n)));
        }
        if parts.is_empty() {
            return None;
        }
        let mut s = parts.join(" · ");
        if self.estimated {
            s.push_str(" · est.");
        }
        Some(s)
    }

    /// Hover detail: exact counts, the cache split when the vendor broke
    /// it out, stream count, and the source.
    pub fn detail(&self) -> String {
        let mut parts = Vec::new();
        let mut push = |name: &str, v: Option<i64>| {
            if let Some(n) = v {
                parts.push(format!("{name} {n}"));
            }
        };
        push("Input", self.input);
        push("Output", self.output);
        push("Cache read", self.cache_read);
        push("Cache write", self.cache_creation);
        let streams = if self.streams == 1 {
            "1 model call".to_string()
        } else {
            format!("{} model calls", self.streams)
        };
        parts.push(streams);
        parts.push(if self.estimated { "estimated".into() } else { "reported by provider".into() });
        format!("Turn tokens: {}", parts.join(" · "))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn known(n: i64) -> Value {
        json!({"n": n, "known": true})
    }

    #[test]
    fn sums_known_counts_across_streams() {
        let mut u = TurnUsage::default();
        u.absorb(&json!({"input": known(1200), "output": known(300), "source": "actual"}), "");
        u.absorb(&json!({"input": known(800), "output": known(200), "cacheRead": known(5000)}), "");
        assert_eq!(u.input, Some(2000));
        assert_eq!(u.output, Some(500));
        assert_eq!(u.cache_read, Some(5000));
        assert_eq!(u.cache_creation, None, "never reported stays unknown");
        assert_eq!(u.streams, 2);
        assert_eq!(u.label().as_deref(), Some("2k in · 500 out"));
        let d = u.detail();
        assert!(d.contains("Input 2000") && d.contains("Cache read 5000"), "{d}");
        assert!(!d.contains("Cache write"), "{d}");
        assert!(d.contains("2 model calls") && d.contains("reported by provider"), "{d}");
    }

    #[test]
    fn unknown_counts_are_not_zero() {
        let mut u = TurnUsage::default();
        u.absorb(&json!({"input": {"known": false}, "output": {"known": false}, "used": known(9)}), "");
        assert_eq!(u, TurnUsage::default(), "a stream with nothing known is ignored");
        assert_eq!(u.label(), None);
        // Output only: input is not printed as 0.
        u.absorb(&json!({"output": known(40)}), "");
        assert_eq!(u.label().as_deref(), Some("40 out"));
        // A reported zero is a real zero.
        let mut z = TurnUsage::default();
        z.absorb(&json!({"input": known(0), "output": known(0)}), "");
        assert_eq!(z.label().as_deref(), Some("0 in · 0 out"));
    }

    #[test]
    fn cache_only_turn_has_no_footer() {
        let mut u = TurnUsage::default();
        u.absorb(&json!({"cacheRead": known(100)}), "");
        assert_eq!(u.label(), None);
    }

    #[test]
    fn estimated_source_labels_the_turn() {
        let mut u = TurnUsage::default();
        u.absorb(&json!({"input": known(10), "output": known(5), "source": "actual"}), "");
        u.absorb(&json!({"input": known(10), "output": known(5), "source": "estimated"}), "");
        assert_eq!(u.label().as_deref(), Some("20 in · 10 out · est."));
        assert!(u.detail().ends_with("estimated"));
    }

    #[test]
    fn other_session_streams_are_not_counted() {
        let mut u = TurnUsage::default();
        u.absorb(&json!({"sessionId": "child", "input": known(10), "output": known(1)}), "root");
        assert_eq!(u, TurnUsage::default(), "a child's stream is not the parent's");
        u.absorb(&json!({"sessionId": "root", "input": known(7), "output": known(2)}), "root");
        // Unknown on either side is not evidence of another session.
        u.absorb(&json!({"input": known(3), "output": known(1)}), "root");
        u.absorb(&json!({"sessionId": "x", "input": known(1), "output": known(1)}), "");
        assert_eq!(u.input, Some(11));
        assert_eq!(u.streams, 3);
    }
}
