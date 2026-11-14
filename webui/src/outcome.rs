//! Shared reading of a daemon result.
//!
//! A failed call must stay a failure: a later success does not erase it,
//! an empty payload is not a stand-in for an error, and a success string
//! is only produced when the call actually succeeded. Panels keep their
//! own note/status channels; this module only decides the words and
//! whether the optimistic update is allowed to stand.

/// Keep the first failure. A later `Ok` does not clear `slot`, and a later
/// error does not replace it. An empty `label` stores the raw error; a
/// label prefixes it so a shared banner can say which load failed.
pub fn take_ok<T>(slot: &mut String, label: &str, result: Result<T, String>) -> Option<T> {
    match result {
        Ok(value) => Some(value),
        Err(err) => {
            if slot.is_empty() {
                *slot = if label.is_empty() {
                    err
                } else {
                    format!("{label}: {err}")
                };
            }
            None
        }
    }
}

pub const DEFAULTS_NOTE_PREFIX: &str = "Model defaults could not be loaded";

pub fn defaults_failure_note(err: &str) -> String {
    format!("{DEFAULTS_NOTE_PREFIX}: {err}")
}

/// Drop a previous defaults-load sentence, then append `failure` when the
/// latest `/v1/settings` load failed. `None` is a successful load.
pub fn merge_defaults_note(current: &str, failure: Option<&str>) -> String {
    let stripped = match current.find(DEFAULTS_NOTE_PREFIX) {
        Some(idx) => current[..idx].trim().to_string(),
        None => current.trim().to_string(),
    };
    match failure {
        None => stripped,
        Some(err) => {
            let msg = defaults_failure_note(err);
            if stripped.is_empty() {
                msg
            } else {
                format!("{stripped} {msg}")
            }
        }
    }
}

pub fn children_failure_note(err: &str) -> String {
    format!("Children could not be loaded — delegation history may be incomplete. {err}")
}

/// `-1` means the auto-approve settings load has not confirmed a value.
/// The countdown must not run from that sentinel.
pub const AUTO_SECONDS_UNKNOWN: i32 = -1;

pub fn confirmed_auto_seconds(loaded: Option<i32>) -> i32 {
    loaded.unwrap_or(AUTO_SECONDS_UNKNOWN)
}

pub fn require_ticket(result: Result<String, String>) -> Result<String, String> {
    match result {
        Ok(ticket) if !ticket.trim().is_empty() => Ok(ticket),
        Ok(_) => Err("Terminal attach failed: the daemon returned an empty ticket.".into()),
        Err(err) => Err(format!(
            "Terminal attach failed: could not mint a ticket: {err}"
        )),
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SocketFailure {
    pub connected: bool,
    pub note: String,
}

pub fn socket_failed() -> SocketFailure {
    SocketFailure {
        connected: false,
        note: "Terminal connection failed.".into(),
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ScheduleOpen {
    pub opened: bool,
    pub status: String,
    pub error: String,
}

/// Stay on the Schedule page unless activation succeeded. The status line
/// says "Opened" only then.
pub fn schedule_open(id: &str, result: Result<(), String>) -> ScheduleOpen {
    match result {
        Ok(()) => ScheduleOpen {
            opened: true,
            status: format!("Opened {id}"),
            error: String::new(),
        },
        Err(err) => ScheduleOpen {
            opened: false,
            status: String::new(),
            error: format!("Schedule open failed: {err}"),
        },
    }
}

/// `None` when the new root activated. `Some` is the status/note to show:
/// the root exists, but it is not the live session.
pub fn new_session_activation(result: Result<(), String>) -> Option<String> {
    match result {
        Ok(()) => None,
        Err(err) => Some(format!("new session created but activation failed: {err}")),
    }
}

pub const LOGOUT_SUCCESS: &str = "Logged out this browser. The host token file was not changed.";

/// Clear the local token only on `Ok`. A non-2xx status or a transport
/// error leaves the grant live, so the copy must not say the browser logged out.
pub fn logout_outcome(result: Result<(u16, String), String>) -> Result<&'static str, String> {
    match result {
        Ok((code, _)) if (200..300).contains(&code) => Ok(LOGOUT_SUCCESS),
        Ok((code, _)) => Err(format!(
            "Logout failed: the gateway did not revoke this browser's grant (HTTP {code}). You are still logged in. The auth cookie may still authenticate a reload."
        )),
        Err(err) => Err(format!(
            "Logout failed: the gateway did not revoke this browser's grant ({err}). You are still logged in. The auth cookie may still authenticate a reload."
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn later_success_does_not_erase_an_earlier_failure() {
        let mut err = String::new();
        let settings: Option<&str> = take_ok(&mut err, "Settings", Err("HTTP 500".into()));
        let rules: Option<&str> = take_ok(&mut err, "Rules", Ok("rules"));
        let audit: Option<&str> = take_ok(&mut err, "Audit", Ok("audit"));
        assert!(settings.is_none());
        assert_eq!(rules, Some("rules"));
        assert_eq!(audit, Some("audit"));
        assert_eq!(err, "Settings: HTTP 500");

        let mut second = err.clone();
        let _: Option<&str> = take_ok(&mut second, "Audit", Err("HTTP 503".into()));
        assert_eq!(second, "Settings: HTTP 500");
    }

    #[test]
    fn empty_success_is_not_a_load_failure() {
        let mut err = String::new();
        let files: Option<Vec<String>> =
            take_ok(&mut err, "Changed files", Ok(Vec::<String>::new()));
        assert_eq!(files, Some(vec![]));
        assert!(err.is_empty());

        let mut failed = String::new();
        let missing: Option<Vec<String>> =
            take_ok(&mut failed, "Changed files", Err("HTTP 500".into()));
        assert!(missing.is_none());
        assert_eq!(failed, "Changed files: HTTP 500");
        assert_ne!(failed, "");
    }

    #[test]
    fn defaults_note_distinguishes_failure_from_a_cleared_load() {
        let failed = merge_defaults_note("", Some("HTTP 500"));
        assert_eq!(failed, "Model defaults could not be loaded: HTTP 500");
        assert!(!failed.contains("No global default"));

        let kept = merge_defaults_note("Live status unavailable: HTTP 503", Some("HTTP 500"));
        assert!(kept.contains("Live status unavailable"));
        assert!(kept.contains("Model defaults could not be loaded: HTTP 500"));

        let cleared = merge_defaults_note(&kept, None);
        assert_eq!(cleared, "Live status unavailable: HTTP 503");
        assert!(!cleared.contains(DEFAULTS_NOTE_PREFIX));
    }

    #[test]
    fn children_failure_names_the_error_and_does_not_claim_childless() {
        let note = children_failure_note("HTTP 500");
        assert!(note.contains("HTTP 500"));
        assert!(note.contains("could not be loaded"));
        assert!(!note.contains("No live tasks"));
        assert_ne!(note, "");
    }

    #[test]
    fn unknown_auto_approve_is_a_sentinel() {
        assert_eq!(confirmed_auto_seconds(None), AUTO_SECONDS_UNKNOWN);
        assert_eq!(confirmed_auto_seconds(Some(0)), 0);
        assert_eq!(confirmed_auto_seconds(Some(12)), 12);
        assert!(confirmed_auto_seconds(None) < 0);
    }

    #[test]
    fn ticket_failure_does_not_yield_a_ticket() {
        let err = require_ticket(Err("HTTP 500".into())).unwrap_err();
        assert!(err.contains("could not mint a ticket"));
        assert!(err.contains("HTTP 500"));
        assert!(require_ticket(Ok(String::new())).is_err());
        assert!(require_ticket(Ok("   ".into())).is_err());
        assert_eq!(require_ticket(Ok("abc".into())).unwrap(), "abc");
    }

    #[test]
    fn socket_failure_is_not_connected() {
        let failed = socket_failed();
        assert!(!failed.connected);
        assert!(!failed.note.is_empty());
    }

    #[test]
    fn schedule_open_does_not_claim_opened_on_failure() {
        let bad = schedule_open("root-1", Err("HTTP 500".into()));
        assert!(!bad.opened);
        assert!(bad.status.is_empty());
        assert!(bad.error.contains("Schedule open failed"));
        assert!(bad.error.contains("HTTP 500"));
        assert!(!bad.error.contains("Opened"));

        let good = schedule_open("root-1", Ok(()));
        assert!(good.opened);
        assert_eq!(good.status, "Opened root-1");
        assert!(good.error.is_empty());
    }

    #[test]
    fn new_session_activation_failure_is_not_silent() {
        assert_eq!(new_session_activation(Ok(())), None);
        let msg = new_session_activation(Err("HTTP 500".into())).unwrap();
        assert!(msg.contains("created but activation failed"));
        assert!(msg.contains("HTTP 500"));
        assert!(!msg.contains("Opened"));
    }

    #[test]
    fn logout_only_succeeds_on_2xx() {
        assert_eq!(
            logout_outcome(Ok((200, "{}".into()))).unwrap(),
            LOGOUT_SUCCESS
        );
        assert_eq!(
            logout_outcome(Ok((204, String::new()))).unwrap(),
            LOGOUT_SUCCESS
        );

        let http = logout_outcome(Ok((500, "nope".into()))).unwrap_err();
        assert!(http.contains("HTTP 500"));
        assert!(http.contains("still logged in"));
        assert!(http.contains("auth cookie"));
        assert!(!http.contains("Logged out"));

        let down = logout_outcome(Err("unreachable".into())).unwrap_err();
        assert!(down.contains("unreachable"));
        assert!(down.contains("still logged in"));
        assert!(!down.contains("Logged out"));
    }
}
