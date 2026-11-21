//! Quote a message (#1362).
//!
//! A "Quote" action on a settled transcript line attaches a chip to the
//! composer. The next send carries the quoted line as a clearly
//! delimited block ahead of the user's own text, so the agent receives
//! the exact referenced text and the stored session log keeps the
//! reference. On replay the transcript parses the block back out and
//! renders a quote card above the message instead of the raw block:
//!
//! ```text
//! [Quoting Strike, message #128]
//! > the quoted line
//! >
//! > keeps its paragraphs
//! [End quote]
//!
//! what the user typed
//! ```
//!
//! `#128` is the anchor: the ordinal of the transcript event that
//! created the quoted line. Jump-back resolves it against the loaded
//! transcript and checks the text still matches before jumping; when
//! nothing matches, the card says the original is unavailable rather
//! than jumping somewhere wrong.
//!
//! No web dependencies, so the rules run as host unit tests in CI.

/// Card / chip excerpt length, in characters.
pub const EXCERPT_MAX: usize = 140;
/// Longest quoted text a send carries, in characters. Longer lines are
/// cut with an ellipsis; the jump-back still reaches the full original.
pub const QUOTED_MAX: usize = 8000;
/// Characters of quoted text compared when resolving jump-back.
const MATCH_KEY: usize = 60;

const HEADER_PREFIX: &str = "[Quoting ";
const HEADER_MID: &str = ", message #";
const END: &str = "[End quote]";

/// A quote parsed out of a stored user message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Quote {
    /// Role class of the quoted line (`you`, `strike`, `tool`, …).
    pub role: String,
    /// Event ordinal of the quoted line in the transcript.
    pub anchor: usize,
    /// The quoted text as sent (possibly capped at [`QUOTED_MAX`]).
    pub text: String,
}

/// Human label for a transcript role class.
pub fn role_label(role: &str) -> &'static str {
    match role {
        "you" => "You",
        "strike" => "Strike",
        "tool" => "Tool",
        "child" => "Agent",
        "permission" => "Permission",
        _ => "Notice",
    }
}

fn role_class(label: &str) -> Option<&'static str> {
    Some(match label {
        "You" => "you",
        "Strike" => "strike",
        "Tool" => "tool",
        "Agent" => "child",
        "Permission" => "permission",
        "Notice" => "notice",
        _ => return None,
    })
}

/// Cap quoted text at [`QUOTED_MAX`] characters.
pub fn cap(text: &str) -> String {
    let text = text.trim();
    if text.chars().count() <= QUOTED_MAX {
        return text.to_string();
    }
    let mut out: String = text.chars().take(QUOTED_MAX).collect();
    out.push('…');
    out
}

/// The text a send carries: the quote block, a blank line, then the
/// user's own message.
pub fn compose(role: &str, anchor: usize, quoted: &str, text: &str) -> String {
    let mut out = format!("{HEADER_PREFIX}{}{HEADER_MID}{anchor}]\n", role_label(role));
    for line in cap(quoted).split('\n') {
        let line = line.trim_end_matches('\r');
        if line.is_empty() {
            out.push_str(">\n");
        } else {
            out.push_str("> ");
            out.push_str(line);
            out.push('\n');
        }
    }
    out.push_str(END);
    out.push_str("\n\n");
    out.push_str(text.trim());
    out
}

fn parse_header(line: &str) -> Option<(&'static str, usize)> {
    let inner = line
        .trim_end_matches('\r')
        .strip_prefix(HEADER_PREFIX)?
        .strip_suffix(']')?;
    let (label, num) = inner.split_once(HEADER_MID)?;
    let anchor = num.parse::<usize>().ok()?;
    Some((role_class(label)?, anchor))
}

/// Split a stored user message into its quote (if it starts with a
/// well-formed quote block) and the user's own text. Anything that is
/// not exactly the block [`compose`] writes is left as plain text.
pub fn split(text: &str) -> (Option<Quote>, String) {
    let mut lines = text.split('\n');
    let Some((role, anchor)) = lines.next().and_then(parse_header) else {
        return (None, text.to_string());
    };
    let mut body = Vec::new();
    let mut closed = false;
    for line in lines.by_ref() {
        let line = line.trim_end_matches('\r');
        if line == END {
            closed = true;
            break;
        }
        if let Some(rest) = line.strip_prefix("> ") {
            body.push(rest);
        } else if line == ">" {
            body.push("");
        } else {
            return (None, text.to_string());
        }
    }
    if !closed {
        return (None, text.to_string());
    }
    let rest: Vec<&str> = lines.collect();
    let rest = rest.join("\n");
    (
        Some(Quote {
            role: role.to_string(),
            anchor,
            text: body.join("\n"),
        }),
        rest.trim_start_matches(['\n', '\r']).to_string(),
    )
}

fn collapse(text: &str) -> String {
    text.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// One-line excerpt for the chip and the card.
pub fn excerpt(text: &str) -> String {
    let flat = collapse(text);
    if flat.chars().count() <= EXCERPT_MAX {
        return flat;
    }
    let mut out: String = flat.chars().take(EXCERPT_MAX).collect();
    out.push('…');
    out
}

/// Chip / card label: role and excerpt.
pub fn label(role: &str, text: &str) -> String {
    format!("{}: {}", role_label(role), excerpt(text))
}

/// A loaded transcript line jump-back may land on.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Candidate<'a> {
    pub id: u32,
    pub ordinal: usize,
    pub role: &'a str,
    pub text: &'a str,
}

fn match_key(quoted: &str) -> String {
    let flat = collapse(quoted.trim_end_matches('…'));
    flat.chars().take(MATCH_KEY).collect()
}

/// Which loaded line the quote points at. The anchor wins when the line
/// there has the quoted role and still starts with the quoted text.
/// Otherwise the same-role line containing the quoted text nearest the
/// anchor (a replay that counted events differently). `None` means the
/// original is not in the loaded transcript: say so, do not guess.
pub fn resolve(quote: &Quote, candidates: &[Candidate<'_>]) -> Option<u32> {
    let key = match_key(&quote.text);
    if key.is_empty() {
        return None;
    }
    let same_role = candidates.iter().filter(|c| c.role == quote.role);
    if let Some(c) = same_role
        .clone()
        .find(|c| c.ordinal == quote.anchor && collapse(c.text).starts_with(&key))
    {
        return Some(c.id);
    }
    same_role
        .filter(|c| collapse(c.text).contains(&key))
        .min_by_key(|c| c.ordinal.abs_diff(quote.anchor))
        .map(|c| c.id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn compose_and_split_round_trip() {
        let quoted = "The bug is in parse().\n\n  It drops the last line.";
        let sent = compose("strike", 128, quoted, "  fix that, but keep the API  ");
        assert_eq!(
            sent,
            "[Quoting Strike, message #128]\n> The bug is in parse().\n>\n>   It drops the last line.\n[End quote]\n\nfix that, but keep the API"
        );
        let (quote, rest) = split(&sent);
        assert_eq!(
            quote,
            Some(Quote {
                role: "strike".into(),
                anchor: 128,
                text: quoted.into(),
            })
        );
        assert_eq!(rest, "fix that, but keep the API");
    }

    #[test]
    fn every_role_round_trips() {
        for role in ["you", "strike", "tool", "child", "permission", "notice"] {
            let (quote, rest) = split(&compose(role, 3, "x", "y"));
            assert_eq!(quote.map(|q| q.role), Some(role.to_string()), "{role}");
            assert_eq!(rest, "y");
        }
    }

    #[test]
    fn plain_text_is_never_a_quote() {
        let cases = [
            "hello",
            "",
            "[Quoting Strike, message #12]",
            "[Quoting Strike, message #12]\n> open block never closed",
            "[Quoting Strike, message #12]\nnot a quote line\n[End quote]\n\nhi",
            "[Quoting Somebody, message #12]\n> x\n[End quote]\n\nhi",
            "[Quoting Strike, message #abc]\n> x\n[End quote]\n\nhi",
            "> a markdown quote\n\nhi",
            "intro\n[Quoting Strike, message #1]\n> x\n[End quote]",
        ];
        for text in cases {
            let (quote, rest) = split(text);
            assert_eq!(quote, None, "{text:?}");
            assert_eq!(rest, text);
        }
    }

    #[test]
    fn quoted_end_marker_inside_the_body_stays_quoted() {
        let sent = compose("strike", 1, "line\n[End quote]\nmore", "mine");
        let (quote, rest) = split(&sent);
        assert_eq!(quote.unwrap().text, "line\n[End quote]\nmore");
        assert_eq!(rest, "mine");
    }

    #[test]
    fn crlf_lines_parse() {
        let sent = "[Quoting You, message #4]\r\n> a\r\n[End quote]\r\n\r\nb";
        let (quote, rest) = split(sent);
        assert_eq!(quote.unwrap().text, "a");
        assert_eq!(rest, "b");
    }

    #[test]
    fn long_quotes_are_capped() {
        let long = "a".repeat(QUOTED_MAX + 10);
        let capped = cap(&long);
        assert_eq!(capped.chars().count(), QUOTED_MAX + 1);
        assert!(capped.ends_with('…'));
        assert_eq!(cap("  short  "), "short");
    }

    #[test]
    fn excerpt_collapses_and_cuts_at_140() {
        assert_eq!(excerpt("a\n\n  b\tc"), "a b c");
        let long = "x".repeat(200);
        let e = excerpt(&long);
        assert_eq!(e.chars().count(), EXCERPT_MAX + 1);
        assert!(e.ends_with('…'));
        assert_eq!(label("strike", "hi  there"), "Strike: hi there");
        assert_eq!(label("you", "q"), "You: q");
    }

    fn quote(role: &str, anchor: usize, text: &str) -> Quote {
        Quote {
            role: role.into(),
            anchor,
            text: text.into(),
        }
    }

    #[test]
    fn resolve_prefers_the_anchor_when_the_text_matches() {
        let lines = [
            Candidate { id: 1, ordinal: 1, role: "you", text: "first" },
            Candidate { id: 2, ordinal: 3, role: "strike", text: "The bug is in parse()." },
            Candidate { id: 3, ordinal: 9, role: "strike", text: "The bug is in parse(). Again." },
        ];
        assert_eq!(resolve(&quote("strike", 3, "The bug is in parse()."), &lines), Some(2));
        assert_eq!(resolve(&quote("strike", 9, "The bug is in parse()."), &lines), Some(3));
    }

    #[test]
    fn resolve_falls_back_to_the_nearest_matching_line() {
        let lines = [
            Candidate { id: 7, ordinal: 20, role: "strike", text: "Use a mutex here." },
            Candidate { id: 8, ordinal: 50, role: "strike", text: "Use a mutex here." },
        ];
        // The anchor drifted (replayed with a different event count).
        assert_eq!(resolve(&quote("strike", 45, "Use a mutex here."), &lines), Some(8));
        assert_eq!(resolve(&quote("strike", 22, "Use a mutex here."), &lines), Some(7));
    }

    #[test]
    fn resolve_never_lands_on_the_wrong_line() {
        let lines = [
            Candidate { id: 1, ordinal: 3, role: "strike", text: "something else" },
            Candidate { id: 2, ordinal: 4, role: "you", text: "The bug is in parse()." },
        ];
        // Same anchor, different text: not a match.
        assert_eq!(resolve(&quote("strike", 3, "The bug is in parse()."), &lines), None);
        // Text matches only on another role: not a match.
        assert_eq!(resolve(&quote("strike", 4, "The bug is in parse()."), &lines), None);
        assert_eq!(resolve(&quote("strike", 3, ""), &lines), None);
        assert_eq!(resolve(&quote("strike", 3, "x"), &[]), None);
    }

    #[test]
    fn resolve_matches_capped_and_reflowed_text() {
        let original = format!("{}\n\nend", "word ".repeat(40));
        let lines = [Candidate { id: 5, ordinal: 2, role: "strike", text: &original }];
        let sent = cap(&original);
        assert_eq!(resolve(&quote("strike", 2, &sent), &lines), Some(5));
        let truncated = format!("{}…", &original[..30]);
        assert_eq!(resolve(&quote("strike", 2, &truncated), &lines), Some(5));
    }
}
