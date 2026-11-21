//! User-facing copy for cockpit errors (#1535).
//!
//! Panels keep their own error signals, and those signals often hold a
//! human label followed by whatever the fetch layer produced:
//! `"Could not load notifications: TypeError: Failed to fetch"`, or
//! `"Changed files: invalid type: null, expected a sequence at line 1 column 13"`.
//! The tail is an engine string — a browser `TypeError`, a serde message, an
//! HTTP status with a JSON body — and must never be the rendered text.
//!
//! [`describe`] is the one place that maps a stored message to copy:
//! it keeps the panel's label, replaces the internal tail with a plain
//! sentence for its [`ErrorClass`], and says what the user can do. The raw
//! string stays available to the caller for the console and a deliberate
//! "Copy details" action. Messages with no recognised internal tail are
//! returned unchanged — they are already copy a panel wrote on purpose.

/// Known error classes. One sentence and one action per class.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ErrorClass {
    /// The request never reached the daemon (browser network failure).
    Unreachable,
    /// HTTP 401 / the token was rejected.
    Unauthorized,
    /// HTTP 403.
    Forbidden,
    /// HTTP 404.
    NotFound,
    /// HTTP 409 / 412.
    Conflict,
    /// HTTP 429.
    Busy,
    /// HTTP 5xx.
    Server,
    /// Any other non-2xx status.
    Http(u16),
    /// The daemon answered, but the body could not be decoded.
    Parse,
    /// No internal tail recognised: the message is already user copy.
    Plain,
}

impl ErrorClass {
    /// Stable slug for `data-error-class` (tests and styling hooks).
    pub fn slug(self) -> &'static str {
        match self {
            ErrorClass::Unreachable => "unreachable",
            ErrorClass::Unauthorized => "unauthorized",
            ErrorClass::Forbidden => "forbidden",
            ErrorClass::NotFound => "not-found",
            ErrorClass::Conflict => "conflict",
            ErrorClass::Busy => "busy",
            ErrorClass::Server => "server",
            ErrorClass::Http(_) => "http",
            ErrorClass::Parse => "parse",
            ErrorClass::Plain => "plain",
        }
    }

    fn sentence(self) -> &'static str {
        match self {
            ErrorClass::Unreachable => "the daemon can't be reached.",
            ErrorClass::Unauthorized => "the daemon didn't accept this browser's sign-in.",
            ErrorClass::Forbidden => "this browser isn't allowed to do that.",
            ErrorClass::NotFound => "the daemon doesn't have that.",
            ErrorClass::Conflict => "it was changed somewhere else first.",
            ErrorClass::Busy => "the daemon is busy.",
            ErrorClass::Server => "the daemon hit an internal error.",
            ErrorClass::Http(_) => "the daemon refused the request.",
            ErrorClass::Parse => "the daemon's reply couldn't be read.",
            ErrorClass::Plain => "",
        }
    }

    fn action(self) -> &'static str {
        match self {
            ErrorClass::Unreachable => {
                "Check that strike serve is running and this device can reach it, then retry."
            }
            ErrorClass::Unauthorized => "Check the daemon token, or pair this browser again.",
            ErrorClass::Forbidden => "Ask the daemon owner for access, or pair this browser again.",
            ErrorClass::NotFound => "It may have been removed. Refresh to see what's there now.",
            ErrorClass::Conflict => "Reload to get the latest version, then try again.",
            ErrorClass::Busy => "Wait a moment, then retry.",
            ErrorClass::Server => {
                "Retry. If it keeps happening, copy the details into a bug report."
            }
            ErrorClass::Http(_) => "Retry, or copy the details into a bug report.",
            ErrorClass::Parse => {
                "Retry. If it persists, reload the page — the daemon and this page may be different versions."
            }
            ErrorClass::Plain => "",
        }
    }
}

/// What a panel renders for one stored error message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct UserError {
    pub class: ErrorClass,
    /// The headline: the panel's label plus a plain sentence.
    pub message: String,
    /// What the user can do next. Empty for [`ErrorClass::Plain`].
    pub action: &'static str,
    /// True when the stored message carried an internal string that was
    /// replaced — the caller should log it and offer "Copy details".
    pub internal: bool,
}

/// Browser network failures (Chromium, Firefox, Safari wordings).
const UNREACHABLE: &[&str] = &[
    "TypeError: Failed to fetch",
    "TypeError: NetworkError when attempting to fetch resource.",
    "TypeError: Load failed",
    "Failed to fetch",
    "NetworkError when attempting to fetch resource",
    "JsError(",
];

/// serde_json / JSON.parse / gloo decode messages.
const PARSE: &[&str] = &[
    "SyntaxError:",
    "invalid type: ",
    "invalid value: ",
    "invalid length ",
    "missing field `",
    "unknown field `",
    "unknown variant `",
    "duplicate field `",
    "expected value at line ",
    "expected `:` at line ",
    "expected `,` or `",
    "EOF while parsing",
    "trailing characters at line ",
    "key must be a string at line ",
    "control character (\\u0000-\\u001F) found",
    "SerdeError(",
];

/// Locate the first recognised internal tail: `(byte index, class)`.
fn find_internal(msg: &str) -> Option<(usize, ErrorClass)> {
    let mut best: Option<(usize, ErrorClass)> = None;
    let mut consider = |idx: usize, class: ErrorClass| {
        if best.map_or(true, |(b, _)| idx < b) {
            best = Some((idx, class));
        }
    };
    for marker in UNREACHABLE {
        if let Some(idx) = msg.find(marker) {
            consider(idx, ErrorClass::Unreachable);
        }
    }
    for marker in PARSE {
        if let Some(idx) = msg.find(marker) {
            consider(idx, ErrorClass::Parse);
        }
    }
    if let Some(idx) = msg.find("unauthorized: check the daemon token") {
        consider(idx, ErrorClass::Unauthorized);
    }
    if let Some((idx, status)) = find_http_status(msg) {
        consider(idx, http_class(status));
    }
    best
}

/// `HTTP <3 digits>` at a word boundary.
fn find_http_status(msg: &str) -> Option<(usize, u16)> {
    let bytes = msg.as_bytes();
    let mut from = 0;
    while let Some(rel) = msg[from..].find("HTTP ") {
        let idx = from + rel;
        let start = idx + 5;
        let boundary_before = idx == 0 || !bytes[idx - 1].is_ascii_alphanumeric();
        let digits = bytes.get(start..start + 3);
        let boundary_after = bytes
            .get(start + 3)
            .map_or(true, |b| !b.is_ascii_digit());
        if boundary_before && boundary_after {
            if let Some(d) = digits {
                if d.iter().all(u8::is_ascii_digit) {
                    let status = (d[0] - b'0') as u16 * 100
                        + (d[1] - b'0') as u16 * 10
                        + (d[2] - b'0') as u16;
                    return Some((idx, status));
                }
            }
        }
        from = start;
    }
    None
}

fn http_class(status: u16) -> ErrorClass {
    match status {
        401 => ErrorClass::Unauthorized,
        403 => ErrorClass::Forbidden,
        404 => ErrorClass::NotFound,
        409 | 412 => ErrorClass::Conflict,
        429 => ErrorClass::Busy,
        500..=599 => ErrorClass::Server,
        s => ErrorClass::Http(s),
    }
}

/// Daemon reason after `HTTP 4xx ` worth keeping: short plain text, not a
/// JSON/HTML body and not another internal string.
fn plain_reason(tail: &str) -> Option<&str> {
    let reason = tail.trim().trim_end_matches('.').trim();
    if reason.is_empty()
        || reason.len() > 160
        || reason.starts_with('{')
        || reason.starts_with('[')
        || reason.starts_with('<')
        || reason.contains('\n')
        || find_internal(reason).is_some()
    {
        return None;
    }
    Some(reason)
}

/// Strip the separator a panel put between its label and the raw tail.
fn label_of(prefix: &str) -> &str {
    prefix
        .trim_end()
        .trim_end_matches([':', '—', '-', '(', '·'])
        .trim_end()
}

/// Map one stored error message to user-facing copy.
pub fn describe(msg: &str) -> UserError {
    let trimmed = msg.trim();
    let Some((idx, class)) = find_internal(trimmed) else {
        return UserError {
            class: ErrorClass::Plain,
            message: trimmed.to_string(),
            action: "",
            internal: false,
        };
    };
    let label = label_of(&trimmed[..idx]);
    let mut sentence = class.sentence().to_string();
    // Keep a short plain daemon reason on client errors: it is copy the
    // daemon wrote for people (e.g. "path must be absolute").
    let client_error = matches!(
        class,
        ErrorClass::Http(_) | ErrorClass::NotFound | ErrorClass::Conflict | ErrorClass::Forbidden
    );
    if client_error && trimmed[idx..].starts_with("HTTP ") {
        // find_http_status guarantees "HTTP " + three ASCII digits here.
        if let Some(reason) = plain_reason(&trimmed[idx + 8..]) {
            sentence = format!("the daemon refused the request: {reason}.");
        }
    }
    let message = if label.is_empty() {
        capitalise(&sentence)
    } else if class == ErrorClass::Unreachable && label.to_ascii_lowercase().contains("reach") {
        // "Could not reach the daemon" already says it; don't say it twice.
        format!("{label}.")
    } else {
        format!("{label}: {sentence}")
    };
    UserError {
        class,
        message,
        action: class.action(),
        internal: true,
    }
}

/// Just the headline, for inline notes that mix status and error text.
pub fn humanize(msg: &str) -> String {
    describe(msg).message
}

fn capitalise(s: &str) -> String {
    let mut chars = s.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().chain(chars).collect(),
        None => String::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classifies_known_tails() {
        let cases: &[(&str, ErrorClass, &str)] = &[
            (
                "Could not load notifications: TypeError: Failed to fetch",
                ErrorClass::Unreachable,
                "Could not load notifications: the daemon can't be reached.",
            ),
            (
                "Live status unavailable: TypeError: Failed to fetch",
                ErrorClass::Unreachable,
                "Live status unavailable: the daemon can't be reached.",
            ),
            (
                "Could not reach the daemon: TypeError: Failed to fetch",
                ErrorClass::Unreachable,
                "Could not reach the daemon.",
            ),
            (
                "TypeError: Failed to fetch",
                ErrorClass::Unreachable,
                "The daemon can't be reached.",
            ),
            (
                "Load failed: TypeError: NetworkError when attempting to fetch resource.",
                ErrorClass::Unreachable,
                "Load failed: the daemon can't be reached.",
            ),
            (
                "Changed files: invalid type: null, expected a sequence at line 1 column 13",
                ErrorClass::Parse,
                "Changed files: the daemon's reply couldn't be read.",
            ),
            (
                "invalid type: null, expected a sequence at line 1 column 13",
                ErrorClass::Parse,
                "The daemon's reply couldn't be read.",
            ),
            (
                "Sessions failed to load: missing field `id` at line 1 column 2",
                ErrorClass::Parse,
                "Sessions failed to load: the daemon's reply couldn't be read.",
            ),
            (
                "unauthorized: check the daemon token",
                ErrorClass::Unauthorized,
                "The daemon didn't accept this browser's sign-in.",
            ),
            (
                "Could not load apps: HTTP 500 {\"error\":\"boom\"}",
                ErrorClass::Server,
                "Could not load apps: the daemon hit an internal error.",
            ),
            (
                "HTTP 503",
                ErrorClass::Server,
                "The daemon hit an internal error.",
            ),
            (
                "Could not save workspace: HTTP 400 path must be absolute",
                ErrorClass::Http(400),
                "Could not save workspace: the daemon refused the request: path must be absolute.",
            ),
            (
                "Could not save: HTTP 400 {\"error\":\"bad\"}",
                ErrorClass::Http(400),
                "Could not save: the daemon refused the request.",
            ),
            (
                "Open failed: HTTP 404",
                ErrorClass::NotFound,
                "Open failed: the daemon doesn't have that.",
            ),
            (
                "Save failed — HTTP 409 revision changed",
                ErrorClass::Conflict,
                "Save failed: the daemon refused the request: revision changed.",
            ),
            (
                "HTTP 429",
                ErrorClass::Busy,
                "The daemon is busy.",
            ),
        ];
        for (raw, class, want) in cases {
            let got = describe(raw);
            assert_eq!(got.class, *class, "class for {raw:?}");
            assert_eq!(got.message, *want, "message for {raw:?}");
            assert!(got.internal, "internal for {raw:?}");
            assert!(!got.action.is_empty(), "action for {raw:?}");
            for leak in ["TypeError", "invalid type", "line 1 column", "{\"error\""] {
                assert!(!got.message.contains(leak), "{leak} leaked in {:?}", got.message);
            }
        }
    }

    #[test]
    fn plain_copy_is_unchanged() {
        for msg in [
            "Name is required.",
            "Test passed.",
            "Add a workspace in Daemon. The shell starts in that directory.",
            "The daemon answered, but it is not ok.",
            "HTTPS is required for pairing.",
            "Saved HTTP 2000 rows",
        ] {
            let got = describe(msg);
            assert_eq!(got.class, ErrorClass::Plain, "{msg}");
            assert_eq!(got.message, msg);
            assert!(!got.internal);
            assert_eq!(got.action, "");
        }
    }

    #[test]
    fn earliest_tail_wins() {
        // A parse error nested after an HTTP status is still an HTTP error.
        let got = describe("Load: HTTP 502 invalid type: null");
        assert_eq!(got.class, ErrorClass::Server);
        assert_eq!(got.message, "Load: the daemon hit an internal error.");
    }

    #[test]
    fn reason_rejects_internal_or_bulky_bodies() {
        let long = format!("Save: HTTP 400 {}", "x".repeat(200));
        assert_eq!(describe(&long).message, "Save: the daemon refused the request.");
        let html = describe("Save: HTTP 400 <html>bad</html>");
        assert_eq!(html.message, "Save: the daemon refused the request.");
        let multi = describe("Save: HTTP 400 line one\nline two");
        assert_eq!(multi.message, "Save: the daemon refused the request.");
    }

    #[test]
    fn slugs_are_stable() {
        assert_eq!(ErrorClass::Unreachable.slug(), "unreachable");
        assert_eq!(ErrorClass::Parse.slug(), "parse");
        assert_eq!(ErrorClass::Http(418).slug(), "http");
        assert_eq!(ErrorClass::Plain.slug(), "plain");
    }

    #[test]
    fn humanize_matches_describe() {
        let raw = "Could not load goals: TypeError: Failed to fetch";
        assert_eq!(humanize(raw), describe(raw).message);
        assert_eq!(humanize(""), "");
    }
}
