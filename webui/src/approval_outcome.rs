//! Approval lifecycle closure (#1387): names for how an ask ended.
//!
//! The daemon closes every ask with `permission.resolved`. A reply carries
//! the user's decision; an ask that dies unanswered (turn stopped, session
//! closed) carries `expired`. A repeat of an action the user already denied
//! is refused without a card and audited as `permission.decided` with layer
//! `user-denied`. These helpers turn those into transcript and row labels.

use serde_json::Value;

/// Layer the permission service stamps on a deny-latch refusal.
pub const LAYER_USER_DENIED: &str = "user-denied";

/// Transcript title for an ask line once it is resolved.
pub fn resolved_title(decision: &str) -> &'static str {
    match decision {
        "expired" => "Expired",
        "reject" => "Denied",
        "once" | "always" | "project" | "folder" => "Allowed",
        _ => "Approval",
    }
}

/// Session-row label for the daemon's `askOutcome` (allowed | denied |
/// expired). None for an empty or unknown value.
pub fn row_label(outcome: &str) -> Option<&'static str> {
    match outcome {
        "allowed" => Some("Allowed"),
        "denied" => Some("Denied"),
        "expired" => Some("Expired"),
        _ => None,
    }
}

/// True when a `permission.decided` payload is a deny-latch refusal.
pub fn is_latched_denial(data: &Value) -> bool {
    data.get("layer").and_then(Value::as_str) == Some(LAYER_USER_DENIED)
        && data.get("action").and_then(Value::as_str) == Some("deny")
}

/// Detail line under a latched denial.
pub const LATCHED_DETAIL: &str = "You denied this action. It is not asked again until your next message.";

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn resolved_titles_name_the_outcome() {
        assert_eq!(resolved_title("expired"), "Expired");
        assert_eq!(resolved_title("reject"), "Denied");
        for d in ["once", "always", "project", "folder"] {
            assert_eq!(resolved_title(d), "Allowed", "{d}");
        }
        assert_eq!(resolved_title(""), "Approval");
        assert_eq!(resolved_title("future"), "Approval");
    }

    #[test]
    fn row_labels_only_for_known_outcomes() {
        assert_eq!(row_label("allowed"), Some("Allowed"));
        assert_eq!(row_label("denied"), Some("Denied"));
        assert_eq!(row_label("expired"), Some("Expired"));
        assert_eq!(row_label(""), None);
        assert_eq!(row_label("pending"), None);
    }

    #[test]
    fn latched_denial_needs_layer_and_deny() {
        assert!(is_latched_denial(&json!({"layer": "user-denied", "action": "deny"})));
        assert!(!is_latched_denial(&json!({"layer": "config", "action": "deny"})));
        assert!(!is_latched_denial(&json!({"layer": "user-denied", "action": "ask"})));
        assert!(!is_latched_denial(&json!({})));
    }
}
