//! Per-turn verification badge (#1373): claimed vs verified.
//!
//! `turn.completed` carries `verification` (`VerificationReport`) when the
//! harness ran completion gates for the turn. `claimed` is the model's
//! own "done"; `verified` / `passed` are harness-owned and independent of
//! model prose. The badge reads only that payload — never transcript
//! text — and an absent payload renders nothing (no gates ran, so there
//! is nothing to label; it is never shown as verified).
//!
//! Labels and the state mapping mirror the TUI's `verificationBadgeLabel`
//! (`internal/frontend/tui/app/_src/app/harness_trust.go`) exactly, so
//! the two frontends never disagree about what a state is called. A
//! failed gate reads "unverified", as in the TUI.

use serde_json::Value;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum VerifyState {
    /// Harness gates ran and passed.
    Verified,
    /// The agent claimed done; the harness did not confirm it.
    Claimed,
    /// Gates ran and did not pass.
    Unverified,
}

impl VerifyState {
    /// TUI vocabulary: `verified` / `claimed` / `unverified`.
    pub fn label(self) -> &'static str {
        match self {
            VerifyState::Verified => "verified",
            VerifyState::Claimed => "claimed",
            VerifyState::Unverified => "unverified",
        }
    }

    /// Hover text used when the report has no summary of its own. Same
    /// fallbacks as the TUI's `formatVerificationNotice`.
    fn fallback_summary(self) -> &'static str {
        match self {
            VerifyState::Verified => "gates passed",
            VerifyState::Claimed => "claimed done, not harness-verified",
            VerifyState::Unverified => "gates failed",
        }
    }
}

/// One turn's badge: state plus the hover detail.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TurnVerify {
    pub state: VerifyState,
    pub detail: String,
}

fn flag(rep: &Value, key: &str) -> bool {
    rep.get(key).and_then(Value::as_bool).unwrap_or(false)
}

/// The state for a `VerificationReport` object, or `None` when there is
/// no report (absent, null, or not an object). Mirrors
/// `verificationBadgeLabel` case for case.
pub fn verify_state(rep: Option<&Value>) -> Option<VerifyState> {
    let rep = rep.filter(|v| v.is_object())?;
    let passed = flag(rep, "passed");
    let claimed = flag(rep, "claimed");
    let verified = flag(rep, "verified");
    Some(if verified && passed {
        VerifyState::Verified
    } else if claimed && !verified {
        // Claimed with gates the harness accepted still reads verified.
        if passed {
            VerifyState::Verified
        } else {
            VerifyState::Claimed
        }
    } else if !passed {
        VerifyState::Unverified
    } else {
        VerifyState::Verified
    })
}

/// Badge for a `turn.completed` data object, or `None` when it carries
/// no verification report.
pub fn turn_verify(data: &Value) -> Option<TurnVerify> {
    let rep = data.get("verification");
    let state = verify_state(rep)?;
    let rep = rep?;
    let summary = rep
        .get("summary")
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .unwrap_or(state.fallback_summary());
    let checks = rep.get("checks").and_then(Value::as_array);
    let detail = match checks {
        Some(c) if !c.is_empty() => {
            let ok = c.iter().filter(|g| flag(g, "passed")).count();
            format!("{}: {summary} ({ok}/{} checks passed)", state.label(), c.len())
        }
        _ => format!("{}: {summary}", state.label()),
    };
    Some(TurnVerify { state, detail })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn state_matches_tui_mapping() {
        // (passed, claimed, verified) -> TUI verificationBadgeLabel.
        let cases = [
            ((true, false, true), VerifyState::Verified),
            ((true, true, true), VerifyState::Verified),
            ((false, true, false), VerifyState::Claimed),
            ((true, true, false), VerifyState::Verified),
            ((false, false, false), VerifyState::Unverified),
            ((false, false, true), VerifyState::Unverified),
            ((false, true, true), VerifyState::Unverified),
            ((true, false, false), VerifyState::Verified),
        ];
        for ((passed, claimed, verified), want) in cases {
            let rep = json!({"passed": passed, "claimed": claimed, "verified": verified});
            assert_eq!(
                verify_state(Some(&rep)),
                Some(want),
                "passed={passed} claimed={claimed} verified={verified}"
            );
        }
    }

    #[test]
    fn absent_report_renders_nothing() {
        for data in [json!({}), json!({"verification": null}), json!({"verification": "yes"})] {
            assert_eq!(turn_verify(&data), None, "{data}");
        }
    }

    #[test]
    fn never_inferred_from_text() {
        // Only the payload counts; a stopReason or prose claim does not.
        let data = json!({"stopReason": "end_turn", "text": "verified, all tests pass"});
        assert_eq!(turn_verify(&data), None);
    }

    #[test]
    fn labels_are_tui_vocabulary() {
        assert_eq!(VerifyState::Verified.label(), "verified");
        assert_eq!(VerifyState::Claimed.label(), "claimed");
        assert_eq!(VerifyState::Unverified.label(), "unverified");
    }

    #[test]
    fn detail_uses_summary_and_check_counts() {
        let data = json!({"verification": {
            "passed": false, "claimed": true, "verified": false,
            "summary": "go test failed",
            "checks": [{"kind": "cmd", "passed": true}, {"kind": "cmd", "passed": false}]
        }});
        let v = turn_verify(&data).unwrap();
        assert_eq!(v.state, VerifyState::Claimed);
        assert_eq!(v.detail, "claimed: go test failed (1/2 checks passed)");
    }

    #[test]
    fn detail_falls_back_without_summary() {
        let data = json!({"verification": {"passed": true, "verified": true, "checks": []}});
        let v = turn_verify(&data).unwrap();
        assert_eq!(v.detail, "verified: gates passed");
    }
}
