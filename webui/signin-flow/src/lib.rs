//! Provider sign-in flows for the cockpit's Providers page.
//!
//! The daemon runs device-code (#1533) and paste-assisted OAuth (#1538)
//! logins server-side and hands the browser only codes, URLs, status, and
//! redacted messages (`internal/frontend/server/auth_flows.go`). This crate
//! is the browser half's state: which methods a row may offer, the request
//! paths, how a status answer moves a flow, and the text each state shows.
//! It holds no credential and the page never persists a `Flow`.

/// Poll interval for a pending flow's status. The daemon keeps a flow for
/// 15 minutes and marks it expired itself, so polling stops on that answer.
pub const POLL_MS: i32 = 3000;

/// A non-key sign-in method the daemon advertises in a provider's
/// `methods` list. API-key entry is the card's existing form, not a flow.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Method {
    Device,
    OAuth,
}

impl Method {
    pub fn from_wire(name: &str) -> Option<Method> {
        match name {
            "device" => Some(Method::Device),
            "oauth" => Some(Method::OAuth),
            _ => None,
        }
    }

    pub fn wire(self) -> &'static str {
        match self {
            Method::Device => "device",
            Method::OAuth => "oauth",
        }
    }

    /// `POST` here with `{"provider": name}` to start a flow.
    pub fn start_path(self) -> &'static str {
        match self {
            Method::Device => "/v1/auth/device",
            Method::OAuth => "/v1/auth/oauth",
        }
    }

    pub fn button_label(self) -> &'static str {
        match self {
            Method::Device => "Sign in with device code",
            Method::OAuth => "Sign in with OAuth",
        }
    }
}

/// The sign-in flows a row offers, in the daemon's order. Only advertised
/// methods appear; `apiKey`, unknown names, and duplicates are dropped, so a
/// method the host adds later shows up once the daemon lists it here.
pub fn signin_methods<S: AsRef<str>>(methods: &[S]) -> Vec<Method> {
    let mut out = Vec::new();
    for name in methods {
        if let Some(method) = Method::from_wire(name.as_ref().trim()) {
            if !out.contains(&method) {
                out.push(method);
            }
        }
    }
    out
}

/// Flow ids are the daemon's 32-char hex. Anything else is refused rather
/// than spliced into a request path.
pub fn valid_flow_id(id: &str) -> bool {
    !id.is_empty() && id.len() <= 64 && id.bytes().all(|b| b.is_ascii_hexdigit())
}

/// `GET` for status, `DELETE` to cancel.
pub fn flow_path(method: Method, id: &str) -> String {
    format!("{}/{}", method.start_path(), id)
}

/// `POST {"paste": ...}` to finish an OAuth flow with the pasted redirect
/// URL or authorization code.
pub fn complete_path(id: &str) -> String {
    format!("/v1/auth/oauth/{id}/complete")
}

/// Only http(s) links are rendered as clickable; anything else is shown as
/// text so a hostile value can never become a `javascript:` href.
pub fn safe_link(url: &str) -> Option<&str> {
    let url = url.trim();
    let lower = url.to_ascii_lowercase();
    if lower.starts_with("https://") || lower.starts_with("http://") {
        Some(url)
    } else {
        None
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Phase {
    Pending,
    Completed,
    Failed,
    Expired,
    Canceled,
}

impl Phase {
    pub fn from_wire(status: &str) -> Option<Phase> {
        match status.trim() {
            "pending" => Some(Phase::Pending),
            "completed" => Some(Phase::Completed),
            "failed" => Some(Phase::Failed),
            "expired" => Some(Phase::Expired),
            "canceled" | "cancelled" => Some(Phase::Canceled),
            _ => None,
        }
    }

    pub fn wire(self) -> &'static str {
        match self {
            Phase::Pending => "pending",
            Phase::Completed => "completed",
            Phase::Failed => "failed",
            Phase::Expired => "expired",
            Phase::Canceled => "canceled",
        }
    }

    pub fn is_terminal(self) -> bool {
        self != Phase::Pending
    }
}

/// The fields of a start or status answer the page reads. Missing fields
/// are empty strings.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Answer {
    pub id: String,
    pub status: String,
    pub message: String,
    pub user_code: String,
    pub verification_uri: String,
    pub authorize_url: String,
    pub expires_at: String,
}

/// One provider's in-flight sign-in, held in page memory only.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Flow {
    pub method: Method,
    pub provider: String,
    pub id: String,
    pub user_code: String,
    pub verification_uri: String,
    pub authorize_url: String,
    pub expires_at: String,
    pub phase: Phase,
    /// The daemon's redacted message, shown verbatim when the flow fails.
    pub message: String,
}

impl Flow {
    /// Build a flow from the start answer. A start answer without a usable
    /// id, or without the code/URL the user needs to act on, is an error:
    /// the page shows it instead of a flow the user cannot finish.
    pub fn start(method: Method, provider: &str, answer: &Answer) -> Result<Flow, String> {
        if !valid_flow_id(&answer.id) {
            return Err("The daemon did not return a usable sign-in id.".into());
        }
        match method {
            Method::Device if answer.user_code.is_empty() || answer.verification_uri.is_empty() => {
                return Err("The daemon did not return a device code and verification link.".into());
            }
            Method::OAuth if answer.authorize_url.is_empty() => {
                return Err("The daemon did not return an authorization link.".into());
            }
            _ => {}
        }
        let mut flow = Flow {
            method,
            provider: provider.to_string(),
            id: answer.id.clone(),
            user_code: answer.user_code.clone(),
            verification_uri: answer.verification_uri.clone(),
            authorize_url: answer.authorize_url.clone(),
            expires_at: answer.expires_at.clone(),
            phase: Phase::Pending,
            message: String::new(),
        };
        if let Some(phase) = Phase::from_wire(&answer.status) {
            flow.phase = phase;
            flow.message = answer.message.clone();
        }
        Ok(flow)
    }

    /// Apply a status (or complete) answer. A terminal flow never moves
    /// again, and an unknown status changes nothing. Returns true when the
    /// phase changed.
    pub fn apply(&mut self, status: &str, message: &str) -> bool {
        if self.phase.is_terminal() {
            return false;
        }
        let Some(next) = Phase::from_wire(status) else {
            return false;
        };
        if next == self.phase {
            return false;
        }
        self.phase = next;
        self.message = message.trim().to_string();
        true
    }

    /// The status request itself failed (daemon restarted, flow gone). The
    /// flow cannot finish, so it fails with the request's error text.
    pub fn lost(&mut self, error: &str) -> bool {
        let error = error.trim();
        let message = if error.is_empty() {
            "The daemon no longer knows this sign-in. Start again."
        } else {
            error
        };
        self.apply("failed", message)
    }

    pub fn polls(&self) -> bool {
        self.phase == Phase::Pending
    }

    /// Paste completion is OAuth-only and only while pending.
    pub fn accepts_paste(&self) -> bool {
        self.method == Method::OAuth && self.phase == Phase::Pending
    }

    pub fn can_cancel(&self) -> bool {
        self.phase == Phase::Pending
    }

    /// Short heading for the flow's current state.
    pub fn phase_label(&self) -> &'static str {
        match (self.phase, self.method) {
            (Phase::Pending, Method::Device) => "Waiting for you to approve the code",
            (Phase::Pending, Method::OAuth) => "Waiting for you to sign in",
            (Phase::Completed, _) => "Signed in",
            (Phase::Failed, _) => "Sign-in failed",
            (Phase::Expired, _) => "Sign-in expired",
            (Phase::Canceled, _) => "Sign-in canceled",
        }
    }

    /// Body text for the current state. A failure shows the daemon's
    /// redacted message verbatim and nothing else.
    pub fn note(&self) -> String {
        match self.phase {
            Phase::Pending => match self.method {
                Method::Device => "Open the link, enter the code, and approve. This page updates when the daemon sees it.".into(),
                Method::OAuth => "Open the link and sign in. If the page you land on cannot reach the daemon, paste its full address (or the code) below.".into(),
            },
            Phase::Completed => "The credential is stored on the daemon. It is not shown here.".into(),
            Phase::Failed if self.message.is_empty() => "The daemon did not say why.".into(),
            Phase::Failed => self.message.clone(),
            Phase::Expired => "The sign-in timed out before it finished. Start again for a new one.".into(),
            Phase::Canceled => "Start again any time.".into(),
        }
    }
}

/// Whether a failed status request ends the flow. A 4xx means the daemon
/// answered and no longer has (or will not show) this flow; anything else —
/// a dropped connection, a 5xx — is retried on the next poll, so a phone
/// blip does not fail a sign-in the daemon is still running.
pub fn poll_error_is_final(error: &str) -> bool {
    let rest = error.trim_start();
    let Some(code) = rest.strip_prefix("HTTP ") else {
        return false;
    };
    code.starts_with('4')
}

/// Text to show after a paste was refused. The paste never appears in it,
/// even when the daemon's error echoed part of it.
pub fn paste_error(error: &str, paste: &str) -> String {
    let error = error.trim();
    let paste = paste.trim();
    let text = if paste.len() >= 4 {
        error.replace(paste, "••••")
    } else {
        error.to_string()
    };
    if text.is_empty() {
        "The daemon refused the pasted value.".into()
    } else {
        text
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn device_start() -> Answer {
        Answer {
            id: "0123456789abcdef0123456789abcdef".into(),
            status: "pending".into(),
            user_code: "WXYZ-1234".into(),
            verification_uri: "https://accounts.x.ai/device".into(),
            expires_at: "2026-11-21T06:00:00Z".into(),
            ..Answer::default()
        }
    }

    fn oauth_start() -> Answer {
        Answer {
            id: "fedcba9876543210fedcba9876543210".into(),
            status: "pending".into(),
            authorize_url: "https://auth.openai.com/oauth/authorize?client_id=x&state=y".into(),
            expires_at: "2026-11-21T06:00:00Z".into(),
            ..Answer::default()
        }
    }

    #[test]
    fn signin_methods_follow_the_daemon_list() {
        let cases: &[(&[&str], &[Method])] = &[
            (&["apiKey"], &[]),
            (&[], &[]),
            (&["apiKey", "device", "oauth"], &[Method::Device, Method::OAuth]),
            (&["apiKey", "oauth"], &[Method::OAuth]),
            (&["oauth", "device"], &[Method::OAuth, Method::Device]),
            (&["device", "device", " oauth "], &[Method::Device, Method::OAuth]),
            (&["passkey", "magic"], &[]),
        ];
        for &(input, want) in cases {
            assert_eq!(signin_methods(input), want.to_vec(), "methods {input:?}");
        }
    }

    #[test]
    fn method_paths_and_labels() {
        assert_eq!(Method::Device.start_path(), "/v1/auth/device");
        assert_eq!(Method::OAuth.start_path(), "/v1/auth/oauth");
        assert_eq!(flow_path(Method::Device, "ab12"), "/v1/auth/device/ab12");
        assert_eq!(flow_path(Method::OAuth, "ab12"), "/v1/auth/oauth/ab12");
        assert_eq!(complete_path("ab12"), "/v1/auth/oauth/ab12/complete");
        assert_eq!(Method::Device.button_label(), "Sign in with device code");
        assert_eq!(Method::OAuth.button_label(), "Sign in with OAuth");
        for m in [Method::Device, Method::OAuth] {
            assert_eq!(Method::from_wire(m.wire()), Some(m));
        }
        assert_eq!(Method::from_wire("apiKey"), None);
    }

    #[test]
    fn flow_ids_must_be_hex() {
        let long = "a".repeat(65);
        let cases = [
            ("0123456789abcdef0123456789abcdef", true),
            ("ABCDEF01", true),
            ("", false),
            ("../key", false),
            ("abc/def", false),
            ("abc?x=1", false),
            (long.as_str(), false),
        ];
        for (id, want) in cases {
            assert_eq!(valid_flow_id(id), want, "id {id:?}");
        }
    }

    #[test]
    fn safe_link_allows_only_http_schemes() {
        assert_eq!(safe_link(" https://x.ai/device "), Some("https://x.ai/device"));
        assert_eq!(safe_link("HTTP://127.0.0.1:1455/cb"), Some("HTTP://127.0.0.1:1455/cb"));
        assert_eq!(safe_link("javascript:alert(1)"), None);
        assert_eq!(safe_link("data:text/html,hi"), None);
        assert_eq!(safe_link(""), None);
    }

    #[test]
    fn start_rejects_answers_the_user_cannot_act_on() {
        let mut bad_id = device_start();
        bad_id.id = "not/hex".into();
        assert!(Flow::start(Method::Device, "xai", &bad_id).is_err());
        let mut no_code = device_start();
        no_code.user_code.clear();
        assert!(Flow::start(Method::Device, "xai", &no_code).is_err());
        let mut no_url = oauth_start();
        no_url.authorize_url.clear();
        assert!(Flow::start(Method::OAuth, "openai", &no_url).is_err());
        // A device answer is not required to carry an authorize URL.
        assert!(Flow::start(Method::Device, "xai", &device_start()).is_ok());
    }

    #[test]
    fn device_pending_to_completed() {
        let mut flow = Flow::start(Method::Device, "xai", &device_start()).unwrap();
        assert_eq!(flow.phase, Phase::Pending);
        assert!(flow.polls() && flow.can_cancel() && !flow.accepts_paste());
        assert_eq!(flow.user_code, "WXYZ-1234");
        assert_eq!(flow.phase_label(), "Waiting for you to approve the code");
        assert!(!flow.apply("pending", ""), "same phase is not a change");
        assert!(flow.apply("completed", "logged in to xai"));
        assert_eq!(flow.phase_label(), "Signed in");
        assert!(!flow.polls() && !flow.can_cancel());
        assert!(!flow.note().contains("logged in to xai"), "completed shows our copy");
    }

    #[test]
    fn device_pending_to_expired() {
        let mut flow = Flow::start(Method::Device, "xai", &device_start()).unwrap();
        assert!(flow.apply("expired", "device code expired"));
        assert_eq!(flow.phase, Phase::Expired);
        assert_eq!(flow.phase_label(), "Sign-in expired");
        assert!(flow.note().contains("Start again"));
        assert!(!flow.polls());
    }

    #[test]
    fn failure_shows_the_redacted_message_verbatim_and_nothing_else() {
        let redacted = "xai device token: HTTP 400 access_denied [REDACTED]";
        for (method, start) in [(Method::Device, device_start()), (Method::OAuth, oauth_start())] {
            let mut flow = Flow::start(method, "xai", &start).unwrap();
            assert!(flow.apply("failed", redacted));
            assert_eq!(flow.phase_label(), "Sign-in failed");
            assert_eq!(flow.note(), redacted);
        }
        let mut quiet = Flow::start(Method::Device, "xai", &device_start()).unwrap();
        quiet.apply("failed", "  ");
        assert_eq!(quiet.note(), "The daemon did not say why.");
    }

    #[test]
    fn terminal_flows_never_move_again() {
        let mut flow = Flow::start(Method::OAuth, "openai", &oauth_start()).unwrap();
        assert!(flow.apply("canceled", "canceled by user"));
        assert_eq!(flow.phase_label(), "Sign-in canceled");
        // A late status answer racing the cancel must not resurrect it.
        assert!(!flow.apply("completed", "oauth completed"));
        assert!(!flow.apply("pending", ""));
        assert_eq!(flow.phase, Phase::Canceled);
        assert!(!flow.lost("HTTP 404"));
    }

    #[test]
    fn unknown_status_changes_nothing() {
        let mut flow = Flow::start(Method::Device, "xai", &device_start()).unwrap();
        assert!(!flow.apply("authorizing", "x"));
        assert_eq!(flow.phase, Phase::Pending);
        assert!(flow.message.is_empty());
    }

    #[test]
    fn lost_flow_fails_with_the_request_error() {
        let mut flow = Flow::start(Method::Device, "xai", &device_start()).unwrap();
        assert!(flow.lost(r#"device login not found"#));
        assert_eq!(flow.note(), "device login not found");
        let mut blank = Flow::start(Method::Device, "xai", &device_start()).unwrap();
        blank.lost("");
        assert!(blank.note().contains("no longer knows"));
    }

    #[test]
    fn oauth_pending_to_completed_via_paste() {
        let mut flow = Flow::start(Method::OAuth, "openai", &oauth_start()).unwrap();
        assert!(flow.accepts_paste() && flow.can_cancel() && flow.polls());
        assert_eq!(flow.phase_label(), "Waiting for you to sign in");
        assert!(flow.note().contains("paste"));
        // The complete endpoint answers {"status":"completed"}.
        assert!(flow.apply("completed", ""));
        assert!(!flow.accepts_paste());
        assert_eq!(flow.phase_label(), "Signed in");
    }

    #[test]
    fn start_answer_already_terminal_is_honoured() {
        let mut answer = device_start();
        answer.status = "failed".into();
        answer.message = "vendor said no".into();
        let flow = Flow::start(Method::Device, "xai", &answer).unwrap();
        assert_eq!(flow.phase, Phase::Failed);
        assert_eq!(flow.note(), "vendor said no");
    }

    #[test]
    fn phases_round_trip() {
        for p in [Phase::Pending, Phase::Completed, Phase::Failed, Phase::Expired, Phase::Canceled] {
            assert_eq!(Phase::from_wire(p.wire()), Some(p));
            assert_eq!(p.is_terminal(), p != Phase::Pending);
        }
        assert_eq!(Phase::from_wire("cancelled"), Some(Phase::Canceled));
        assert_eq!(Phase::from_wire("nope"), None);
    }

    #[test]
    fn only_4xx_poll_errors_end_the_flow() {
        let cases = [
            (r#"HTTP 404 {"error":"device login not found"}"#, true),
            ("HTTP 401", true),
            ("HTTP 502 bad gateway", false),
            ("HTTP 500", false),
            ("TypeError: Failed to fetch", false),
            ("", false),
        ];
        for (err, want) in cases {
            assert_eq!(poll_error_is_final(err), want, "error {err:?}");
        }
    }

    #[test]
    fn paste_error_never_echoes_the_paste() {
        let paste = "http://localhost:1455/auth/callback?code=abc123secret&state=s";
        let err = format!("invalid paste {paste}");
        let shown = paste_error(&err, paste);
        assert!(!shown.contains("abc123secret"));
        assert!(shown.contains("••••"));
        assert_eq!(paste_error("", paste), "The daemon refused the pasted value.");
        assert_eq!(paste_error("state mismatch", "abc"), "state mismatch");
    }
}
