//! Turn failure visibility in the transcript (#1371).
//!
//! Before this, a turn the engine ended abnormally looked exactly like a
//! clean one: `turn.completed` closed the open line and nothing else. The
//! engine's own `stopReason` (protocol `TurnCompleted.StopReason`) and the
//! loop guard's `tool.loop_detected` were never read.
//!
//! This module is the pure wording for the two failure lines the
//! transcript now pushes:
//!
//! - an end-of-turn marker for the abnormal stop reasons the engine emits
//!   (`error`, `loop_detected`, `timeout`, `budget_exhausted`), badged with
//!   the same `failed` vocabulary a tool row uses for `isError`;
//! - a loop-guard line for `tool.loop_detected`, badged `blocked`.
//!
//! A clean `end_turn`, a user `interrupted` stop, an unknown reason and a
//! missing reason all produce nothing, so those turns stay visually
//! unchanged. Every field shown comes from the envelope; a missing field
//! is omitted, never estimated.

/// `Message::key` prefix that marks an end-of-turn failure marker. The
/// view reads the reason after the prefix into `data-turn-stop`.
pub const STOP_KEY_PREFIX: &str = "turn-stop:";

/// Badge on the end-of-turn marker: the tool row `isError` vocabulary.
pub const STOP_BADGE: &str = "failed";

/// Badge on the loop-guard line: the guard blocked the repeating call.
pub const LOOP_BADGE: &str = "blocked";

/// Title on the loop-guard line.
pub const LOOP_TITLE: &str = "Loop guard";

/// Wording for one end-of-turn failure marker.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StopMarker {
    /// Canonical reason, used in the message key and `data-turn-stop`.
    pub reason: &'static str,
    pub title: &'static str,
    pub body: &'static str,
}

/// The marker for a `turn.completed` stop reason, or `None` when the turn
/// ended normally (or for a reason this slice does not flag).
pub fn stop_marker(stop_reason: &str) -> Option<StopMarker> {
    let marker = match stop_reason.trim().to_ascii_lowercase().as_str() {
        // `failed` / `provider_error` are the aliases the protocol's
        // NormalizeStop already folds into a failed root.
        "error" | "failed" | "provider_error" => StopMarker {
            reason: "error",
            title: "Turn failed",
            body: "The engine stopped this turn with an error. The reply above may be incomplete.",
        },
        "loop_detected" => StopMarker {
            reason: "loop_detected",
            title: "Turn stopped by loop guard",
            body: "The model kept repeating failing tool calls, so the harness stopped the turn.",
        },
        "timeout" => StopMarker {
            reason: "timeout",
            title: "Turn timed out",
            body: "The turn hit its deadline before it finished. The reply above may be incomplete.",
        },
        "budget_exhausted" => StopMarker {
            reason: "budget_exhausted",
            title: "Budget exhausted",
            body: "The session budget ran out, so the turn stopped.",
        },
        _ => return None,
    };
    Some(marker)
}

/// The stop reason carried by a marker's `Message::key`, if it is one.
pub fn stop_reason_of(key: &str) -> Option<&str> {
    key.strip_prefix(STOP_KEY_PREFIX).filter(|r| !r.is_empty())
}

/// Body of the loop-guard line from a `tool.loop_detected` payload. Uses
/// the engine's own message; tool name and repeat count are appended only
/// when the envelope carries them.
pub fn loop_body(message: &str, tool_name: &str, count: i64) -> String {
    let message = message.trim();
    let mut out = if message.is_empty() {
        "Repeated failing tool calls".to_string()
    } else {
        let mut chars = message.chars();
        match chars.next() {
            Some(first) => first.to_uppercase().chain(chars).collect(),
            None => String::new(),
        }
    };
    let tool_name = tool_name.trim();
    match (tool_name.is_empty(), count > 0) {
        (false, true) => out.push_str(&format!(" · {tool_name}, {count} times")),
        (false, false) => out.push_str(&format!(" · {tool_name}")),
        (true, true) => out.push_str(&format!(" · {count} times")),
        (true, false) => {}
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn abnormal_stop_reasons_get_a_marker() {
        let cases = [
            ("error", "error", "Turn failed"),
            ("failed", "error", "Turn failed"),
            ("provider_error", "error", "Turn failed"),
            ("loop_detected", "loop_detected", "Turn stopped by loop guard"),
            ("timeout", "timeout", "Turn timed out"),
            ("budget_exhausted", "budget_exhausted", "Budget exhausted"),
            (" Timeout ", "timeout", "Turn timed out"),
        ];
        for (raw, reason, title) in cases {
            let m = stop_marker(raw).unwrap_or_else(|| panic!("{raw}: no marker"));
            assert_eq!(m.reason, reason, "{raw}");
            assert_eq!(m.title, title, "{raw}");
            assert!(!m.body.is_empty(), "{raw}");
        }
    }

    #[test]
    fn clean_and_unknown_stops_get_nothing() {
        for raw in ["end_turn", "", "interrupted", "canceled", "tool_use", "max_tokens", "weird"] {
            assert_eq!(stop_marker(raw), None, "{raw}");
        }
    }

    #[test]
    fn stop_reason_round_trips_through_key() {
        assert_eq!(stop_reason_of("turn-stop:timeout"), Some("timeout"));
        assert_eq!(stop_reason_of("turn-stop:"), None);
        assert_eq!(stop_reason_of("bash"), None);
        assert_eq!(stop_reason_of(""), None);
    }

    #[test]
    fn loop_body_uses_only_fields_present() {
        let cases = [
            ("repeated identical failing tool calls", "bash", 3, "Repeated identical failing tool calls · bash, 3 times"),
            ("oscillating failing tool calls", "edit", 0, "Oscillating failing tool calls · edit"),
            ("", "", 4, "Repeated failing tool calls · 4 times"),
            ("  ", "", 0, "Repeated failing tool calls"),
            ("échec", "", -1, "Échec"),
        ];
        for (msg, tool, count, want) in cases {
            assert_eq!(loop_body(msg, tool, count), want, "{msg:?}/{tool:?}/{count}");
        }
    }
}
