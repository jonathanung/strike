//! Pure logic for `@session:<id>` references in the composer (#1550
//! slice 2, #1906). The daemon expands the token on send and sizes it at
//! `GET /v1/sessions/{id}/reference`; this crate only parses tokens,
//! filters the `@` menu's Sessions section, and words the chip.

/// Token prefix the daemon expands.
pub const PREFIX: &str = "@session:";

/// Chip sentence for a deleted, pruned or unknown id (the route's 404).
pub const MISSING_TEXT: &str = "Session not found (deleted or pruned). Sending attaches nothing.";
/// Chip sentence for a reference to the session being typed in.
pub const SELF_TEXT: &str = "This session. Sending attaches nothing.";

/// Most sessions the `@` menu lists.
pub const MENU_LIMIT: usize = 6;

fn is_id_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | '.')
}

fn after_boundary(text: &str, at: usize) -> bool {
    at == 0 || text[..at].chars().next_back().is_some_and(char::is_whitespace)
}

/// Distinct `@session:<id>` ids in a draft, in order, using the daemon's
/// rule: the token starts the draft or follows whitespace, and trailing
/// dots are sentence punctuation, not id.
pub fn tokens(draft: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    let mut from = 0;
    while let Some(rel) = draft[from..].find(PREFIX) {
        let at = from + rel;
        from = at + PREFIX.len();
        if !after_boundary(draft, at) {
            continue;
        }
        let rest = &draft[from..];
        let len = rest.find(|c: char| !is_id_char(c)).unwrap_or(rest.len());
        let id = rest[..len].trim_end_matches('.');
        if !id.is_empty() && !out.iter().any(|seen| seen == id) {
            out.push(id.to_string());
        }
    }
    out
}

/// When `head` (draft up to the caret) ends inside an `@session:` token,
/// the byte offset of its `@` and the id typed so far.
pub fn trigger(head: &str) -> Option<(usize, &str)> {
    let id_start = head
        .char_indices()
        .rev()
        .take_while(|(_, c)| is_id_char(*c))
        .last()
        .map_or(head.len(), |(i, _)| i);
    let at = head[..id_start].strip_suffix(PREFIX).map(str::len)?;
    after_boundary(head, at).then(|| (at, &head[id_start..]))
}

fn is_subsequence(needle: &str, hay: &str) -> bool {
    let mut it = hay.chars();
    needle.chars().all(|c| it.any(|h| h == c))
}

/// One row of the `@` menu's Sessions section.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct MenuRow {
    pub id: String,
    pub label: String,
    /// The session the composer sends to: listed, never insertable.
    pub own: bool,
}

/// Sessions matching `query`: titles that contain it (or ids that start
/// with it) first, then fuzzy (subsequence) title matches, keeping the
/// caller's order (newest first) within each group.
pub fn menu(rows: &[(String, String)], self_id: &str, query: &str) -> Vec<MenuRow> {
    let q = query.trim().to_lowercase();
    let mut direct = Vec::new();
    let mut fuzzy = Vec::new();
    for (id, title) in rows {
        if id.is_empty() {
            continue;
        }
        let own = id == self_id;
        let title = title.trim();
        let label = match (title.is_empty(), own) {
            (true, true) => "This session".to_string(),
            (true, false) => id.clone(),
            _ => title.to_string(),
        };
        let row = MenuRow { id: id.clone(), label, own };
        let hay = row.label.to_lowercase();
        if q.is_empty() || hay.contains(&q) || id.to_lowercase().starts_with(&q) {
            direct.push(row);
        } else if is_subsequence(&q, &hay) {
            fuzzy.push(row);
        }
    }
    direct.into_iter().chain(fuzzy).take(MENU_LIMIT).collect()
}

/// What the chip knows about one reference.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Size {
    Loading,
    Ready { title: String, chars: usize, tokens: usize, own: bool },
    Missing,
    Failed(String),
}

impl Size {
    /// The current session needs no fetch: referencing it attaches nothing.
    pub fn own() -> Self {
        Size::Ready { title: String::new(), chars: 0, tokens: 0, own: true }
    }

    /// Map a GET error to a state. The webui's `get_json` reports a
    /// non-2xx answer as `HTTP <code>`.
    pub fn from_error(err: &str) -> Self {
        if err.starts_with("HTTP 404") {
            Size::Missing
        } else {
            Size::Failed(err.to_string())
        }
    }

    /// Chip label: the session title once known, else the id.
    pub fn name(&self, id: &str) -> String {
        match self {
            Size::Ready { title, .. } if !title.trim().is_empty() => title.trim().to_string(),
            _ => id.to_string(),
        }
    }

    /// Chip detail: the attached size, or an honest reason nothing attaches.
    pub fn detail(&self) -> String {
        match self {
            Size::Loading => "Sizing…".into(),
            Size::Missing => MISSING_TEXT.into(),
            Size::Failed(e) => format!("Size unavailable: {e}"),
            Size::Ready { own: true, .. } => SELF_TEXT.into(),
            Size::Ready { chars, tokens, .. } => {
                format!("{} chars · ~{} tokens", count(*chars), count(*tokens))
            }
        }
    }

    /// Stable state name for `data-session-ref-state`.
    pub fn state(&self) -> &'static str {
        match self {
            Size::Loading => "loading",
            Size::Missing => "missing",
            Size::Failed(_) => "failed",
            Size::Ready { own: true, .. } => "self",
            Size::Ready { .. } => "ready",
        }
    }

    /// A chip opens its session only when that session exists and is
    /// not the one already open.
    pub fn openable(&self) -> bool {
        matches!(self, Size::Ready { own: false, .. })
    }
}

/// Compact count: 812, 3.0k, 12k.
pub fn count(n: usize) -> String {
    if n >= 10_000 {
        format!("{}k", n / 1000)
    } else if n >= 1000 {
        format!("{:.1}k", n as f64 / 1000.0)
    } else {
        n.to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tokens_follow_the_daemon_rule() {
        let cases: [(&str, &[&str]); 7] = [
            ("plain text", &[]),
            ("@session:a1", &["a1"]),
            ("@session:a1 and @session:b-2.", &["a1", "b-2"]),
            ("dup @session:a1 @session:a1", &["a1"]),
            ("mail@session:a1 @session: ", &[]),
            ("(@session:a1)", &[]),
            ("line\n@session:x_y.z, ok", &["x_y.z"]),
        ];
        for (draft, want) in cases {
            assert_eq!(tokens(draft), want, "{draft:?}");
        }
    }

    #[test]
    fn trigger_finds_the_token_under_the_caret() {
        let cases: [(&str, Option<(usize, &str)>); 6] = [
            ("see @session:ab", Some((4, "ab"))),
            ("@session:", Some((0, ""))),
            ("x@session:ab", None),
            ("@sess", None),
            ("@session:ab cd", None),
            ("é @session:a", Some((3, "a"))),
        ];
        for (head, want) in cases {
            assert_eq!(trigger(head), want, "{head:?}");
        }
    }

    fn rows() -> Vec<(String, String)> {
        [
            ("live", ""),
            ("auth-1", "Auth investigation"),
            ("p2", "Palette polish"),
            ("bare", ""),
        ]
        .iter()
        .map(|(a, b)| (a.to_string(), b.to_string()))
        .collect()
    }

    #[test]
    fn menu_filters_and_marks_self() {
        let all = menu(&rows(), "live", "");
        let labels: Vec<_> = all.iter().map(|r| r.label.as_str()).collect();
        assert_eq!(labels, ["This session", "Auth investigation", "Palette polish", "bare"]);
        assert!(all[0].own && !all[1].own);

        let cases: [(&str, &[&str]); 5] = [
            ("AUTH", &["auth-1"]),
            ("p2", &["p2"]),
            ("plsh", &["p2"]),
            ("polish", &["p2"]),
            ("zzz", &[]),
        ];
        for (q, want) in cases {
            let got: Vec<_> = menu(&rows(), "live", q).into_iter().map(|r| r.id).collect();
            assert_eq!(got, want, "{q}");
        }
    }

    #[test]
    fn menu_puts_direct_hits_before_fuzzy_and_caps() {
        let rows: Vec<(String, String)> = vec![
            ("f".into(), "a-b-c".into()),
            ("d".into(), "abc".into()),
        ];
        let got: Vec<_> = menu(&rows, "", "abc").into_iter().map(|r| r.id).collect();
        assert_eq!(got, ["d", "f"]);
        let many: Vec<(String, String)> =
            (0..20).map(|i| (format!("s{i}"), format!("t{i}"))).collect();
        assert_eq!(menu(&many, "", "").len(), MENU_LIMIT);
    }

    #[test]
    fn chip_wording_per_state() {
        let ready = |title: &str, chars: usize| Size::Ready {
            title: title.into(),
            chars,
            tokens: chars.div_ceil(4),
            own: false,
        };
        let cases = [
            (Size::Loading, "a", "Sizing…", "loading", false),
            (Size::Missing, "a", MISSING_TEXT, "missing", false),
            (Size::own(), "a", SELF_TEXT, "self", false),
            (ready("Auth dig", 812), "Auth dig", "812 chars · ~203 tokens", "ready", true),
            (ready("Big", 12_000), "Big", "12k chars · ~3.0k tokens", "ready", true),
            (Size::from_error("HTTP 500"), "a", "Size unavailable: HTTP 500", "failed", false),
        ];
        for (size, name, detail, state, openable) in cases {
            assert_eq!(size.name("a"), name);
            assert_eq!(size.detail(), detail);
            assert_eq!(size.state(), state);
            assert_eq!(size.openable(), openable);
        }
        assert_eq!(Size::from_error("HTTP 404"), Size::Missing);
    }
}
