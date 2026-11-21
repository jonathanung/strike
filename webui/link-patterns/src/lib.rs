//! Text Link Patterns (#1383): user rules that turn transcript mentions
//! (tracker keys, build ids, doc slugs) into links at display time.
//!
//! A rule pairs a regular expression with an absolute http/https URL
//! template containing `{match}`; the matched text is percent-encoded
//! into the template. Linking is display-time only: callers feed
//! already-parsed plain-text chunks and render the returned pieces; the
//! stored message never changes.
//!
//! Safety by construction:
//! - Patterns compile with the `regex` crate, whose matching is linear in
//!   the input (no backtracking), under a compiled-size cap. A pattern
//!   that matches the empty string is refused (it would link nothing and
//!   everything).
//! - Templates must be absolute `http://` or `https://`, contain
//!   `{match}`, and keep the host fixed: `{match}` may not appear in the
//!   scheme or authority, and userinfo (`@`) is refused. Whitespace,
//!   control characters and `"` `<` `>` `` ` `` are refused.
//! - The match is percent-encoded (RFC 3986 unreserved characters kept),
//!   so matched text cannot change the URL's scheme, host or structure.
//!
//! Precedence: literal `http(s)://` URLs in the text are never touched
//! (built-in linking owns them). Rules apply in the user's order; text a
//! rule linked is not offered to later rules, so the first matching rule
//! wins.

use regex::{Regex, RegexBuilder};

/// Most rules a user can keep (#1383 Q(b)).
pub const MAX_RULES: usize = 50;
/// Longest accepted pattern, in bytes.
pub const MAX_PATTERN_LEN: usize = 512;
/// Longest accepted URL template, in bytes.
pub const MAX_TEMPLATE_LEN: usize = 2048;
/// Longest accepted rule name, in characters.
pub const MAX_NAME_LEN: usize = 80;
/// Placeholder replaced by the percent-encoded match.
pub const PLACEHOLDER: &str = "{match}";
/// Compiled program size cap per pattern.
const REGEX_SIZE_LIMIT: usize = 1 << 20;

/// One user rule as stored and edited.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Rule {
    pub name: String,
    pub pattern: String,
    pub template: String,
}

/// Why a rule was refused. [`RuleError::message`] is the user-facing text.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RuleError {
    EmptyName,
    NameTooLong,
    EmptyPattern,
    PatternTooLong,
    BadPattern(String),
    MatchesEmpty,
    EmptyTemplate,
    TemplateTooLong,
    TemplateNotHttp,
    TemplateBadChar,
    TemplateNoPlaceholder,
    TemplateNoHost,
    TemplateHostFromMatch,
    TooManyRules,
}

impl RuleError {
    pub fn message(&self) -> String {
        match self {
            RuleError::EmptyName => "Give the rule a name.".into(),
            RuleError::NameTooLong => format!("Name is longer than {MAX_NAME_LEN} characters."),
            RuleError::EmptyPattern => "Enter a regular expression.".into(),
            RuleError::PatternTooLong => format!("Pattern is longer than {MAX_PATTERN_LEN} characters."),
            RuleError::BadPattern(why) => format!("Invalid regular expression: {why}"),
            RuleError::MatchesEmpty => "Pattern matches empty text; make it match at least one character.".into(),
            RuleError::EmptyTemplate => "Enter a URL template.".into(),
            RuleError::TemplateTooLong => format!("URL template is longer than {MAX_TEMPLATE_LEN} characters."),
            RuleError::TemplateNotHttp => "URL template must start with https:// or http://.".into(),
            RuleError::TemplateBadChar => "URL template has spaces, control characters or one of \" < > `.".into(),
            RuleError::TemplateNoPlaceholder => "URL template must contain {match}.".into(),
            RuleError::TemplateNoHost => "URL template needs a host after the scheme.".into(),
            RuleError::TemplateHostFromMatch => {
                "Put {match} in the path or query; the host must be fixed and may not contain @.".into()
            }
            RuleError::TooManyRules => format!("At most {MAX_RULES} rules."),
        }
    }
}

/// Compile a pattern under the size cap; refuse empty, overlong and
/// empty-matching patterns.
pub fn compile_pattern(pattern: &str) -> Result<Regex, RuleError> {
    if pattern.is_empty() {
        return Err(RuleError::EmptyPattern);
    }
    if pattern.len() > MAX_PATTERN_LEN {
        return Err(RuleError::PatternTooLong);
    }
    let re = RegexBuilder::new(pattern)
        .size_limit(REGEX_SIZE_LIMIT)
        .dfa_size_limit(REGEX_SIZE_LIMIT)
        .build()
        .map_err(|e| RuleError::BadPattern(first_line(&e.to_string())))?;
    if re.is_match("") {
        return Err(RuleError::MatchesEmpty);
    }
    Ok(re)
}

/// The regex crate's errors are multi-line ASCII art; keep the last
/// non-empty line, which names the problem ("error: unclosed group").
fn first_line(msg: &str) -> String {
    msg.lines()
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .last()
        .unwrap_or("unknown error")
        .trim_start_matches("error: ")
        .to_string()
}

/// Check a URL template (see the module docs for the rules).
pub fn validate_template(template: &str) -> Result<(), RuleError> {
    if template.is_empty() {
        return Err(RuleError::EmptyTemplate);
    }
    if template.len() > MAX_TEMPLATE_LEN {
        return Err(RuleError::TemplateTooLong);
    }
    if template
        .chars()
        .any(|c| c.is_whitespace() || c.is_control() || matches!(c, '"' | '<' | '>' | '`'))
    {
        return Err(RuleError::TemplateBadChar);
    }
    let lower = template.to_ascii_lowercase();
    let rest = if lower.starts_with("https://") {
        &template["https://".len()..]
    } else if lower.starts_with("http://") {
        &template["http://".len()..]
    } else {
        return Err(RuleError::TemplateNotHttp);
    };
    if !template.contains(PLACEHOLDER) {
        return Err(RuleError::TemplateNoPlaceholder);
    }
    let authority = rest.split(['/', '?', '#']).next().unwrap_or("");
    if authority.contains(PLACEHOLDER) || authority.contains('@') {
        return Err(RuleError::TemplateHostFromMatch);
    }
    if authority.is_empty() || authority.contains(['{', '}']) {
        return Err(RuleError::TemplateNoHost);
    }
    Ok(())
}

/// Validate one rule; on success return its compiled pattern.
pub fn validate(rule: &Rule) -> Result<Regex, RuleError> {
    let name = rule.name.trim();
    if name.is_empty() {
        return Err(RuleError::EmptyName);
    }
    if name.chars().count() > MAX_NAME_LEN {
        return Err(RuleError::NameTooLong);
    }
    let re = compile_pattern(&rule.pattern)?;
    validate_template(&rule.template)?;
    Ok(re)
}

/// Validate a whole list for saving: every rule must pass and there may
/// be at most [`MAX_RULES`]. The error carries the first bad rule's index
/// (`None` for the count cap).
pub fn validate_all(rules: &[Rule]) -> Result<(), (Option<usize>, RuleError)> {
    if rules.len() > MAX_RULES {
        return Err((None, RuleError::TooManyRules));
    }
    for (i, rule) in rules.iter().enumerate() {
        validate(rule).map_err(|e| (Some(i), e))?;
    }
    Ok(())
}

/// Percent-encode everything but RFC 3986 unreserved characters.
pub fn percent_encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'.' | b'_' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

/// The template with every `{match}` replaced by the encoded match.
pub fn expand(template: &str, matched: &str) -> String {
    template.replace(PLACEHOLDER, &percent_encode(matched))
}

/// One rendered piece of a plain-text chunk.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Piece {
    Text(String),
    Link { text: String, href: String },
}

struct Compiled {
    find: Regex,
    whole: Regex,
    template: String,
}

/// The valid rules, compiled, in the user's order.
#[derive(Default)]
pub struct LinkRules {
    rules: Vec<Compiled>,
}

impl LinkRules {
    /// Compile `rules`, silently dropping invalid ones (they are refused
    /// in Settings; a hand-edited store must still never apply them) and
    /// anything past [`MAX_RULES`].
    pub fn new(rules: &[Rule]) -> Self {
        let rules = rules
            .iter()
            .take(MAX_RULES)
            .filter_map(|rule| {
                let find = validate(rule).ok()?;
                let whole = RegexBuilder::new(&format!("^(?:{})$", rule.pattern))
                    .size_limit(REGEX_SIZE_LIMIT)
                    .dfa_size_limit(REGEX_SIZE_LIMIT)
                    .build()
                    .ok()?;
                Some(Compiled {
                    find,
                    whole,
                    template: rule.template.clone(),
                })
            })
            .collect();
        LinkRules { rules }
    }

    pub fn is_empty(&self) -> bool {
        self.rules.is_empty()
    }

    pub fn len(&self) -> usize {
        self.rules.len()
    }

    /// Split a plain-text chunk into text and links. Literal http(s) URLs
    /// are left as text; with no rules or no matches the result is the
    /// input as one `Text` piece.
    pub fn apply(&self, text: &str) -> Vec<Piece> {
        if self.rules.is_empty() || text.is_empty() {
            return vec![Piece::Text(text.to_string())];
        }
        // Protected (URL) and linked spans are frozen; only `Open` text
        // is offered to the next rule.
        enum Span {
            Open(String),
            Frozen(Piece),
        }
        let mut spans: Vec<Span> = split_urls(text)
            .into_iter()
            .map(|(s, is_url)| if is_url { Span::Frozen(Piece::Text(s)) } else { Span::Open(s) })
            .collect();
        for rule in &self.rules {
            let mut next = Vec::with_capacity(spans.len());
            for span in spans {
                let Span::Open(t) = span else {
                    next.push(span);
                    continue;
                };
                let mut last = 0;
                for m in rule.find.find_iter(&t) {
                    if m.is_empty() {
                        continue;
                    }
                    if m.start() > last {
                        next.push(Span::Open(t[last..m.start()].to_string()));
                    }
                    next.push(Span::Frozen(Piece::Link {
                        text: m.as_str().to_string(),
                        href: expand(&rule.template, m.as_str()),
                    }));
                    last = m.end();
                }
                if last == 0 {
                    next.push(Span::Open(t));
                } else if last < t.len() {
                    next.push(Span::Open(t[last..].to_string()));
                }
            }
            spans = next;
        }
        // Merge adjacent text so an unmatched chunk stays one piece.
        let mut out: Vec<Piece> = Vec::new();
        for span in spans {
            let piece = match span {
                Span::Open(t) | Span::Frozen(Piece::Text(t)) => Piece::Text(t),
                Span::Frozen(link) => link,
            };
            match (out.last_mut(), piece) {
                (Some(Piece::Text(prev)), Piece::Text(t)) => prev.push_str(&t),
                (_, piece) => out.push(piece),
            }
        }
        out
    }

    /// Link for an inline-code span: only when a rule matches the span's
    /// whole content (first such rule in order).
    pub fn whole(&self, text: &str) -> Option<String> {
        if text.is_empty() || is_url_start(text) {
            return None;
        }
        self.rules
            .iter()
            .find(|r| r.whole.is_match(text))
            .map(|r| expand(&r.template, text))
    }
}

fn is_url_start(s: &str) -> bool {
    let lower: String = s.chars().take(8).collect::<String>().to_ascii_lowercase();
    lower.starts_with("https://") || lower.starts_with("http://")
}

/// Split `text` into `(segment, is_url)` runs; a URL runs from
/// `http://`/`https://` to the next whitespace.
fn split_urls(text: &str) -> Vec<(String, bool)> {
    let mut out = Vec::new();
    let mut i = 0;
    let mut start = 0;
    while i < text.len() {
        if is_url_start(&text[i..]) {
            if i > start {
                out.push((text[start..i].to_string(), false));
            }
            let end = text[i..]
                .find(char::is_whitespace)
                .map(|n| i + n)
                .unwrap_or(text.len());
            out.push((text[i..end].to_string(), true));
            i = end;
            start = end;
            continue;
        }
        i += text[i..].chars().next().map(char::len_utf8).unwrap_or(1);
    }
    if start < text.len() {
        out.push((text[start..].to_string(), false));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{Duration, Instant};

    fn rule(name: &str, pattern: &str, template: &str) -> Rule {
        Rule {
            name: name.into(),
            pattern: pattern.into(),
            template: template.into(),
        }
    }

    fn jira() -> Rule {
        rule("Jira", r"\b[A-Z]{2,}-\d+\b", "https://tracker.example.com/browse/{match}")
    }

    fn link(text: &str, href: &str) -> Piece {
        Piece::Link {
            text: text.into(),
            href: href.into(),
        }
    }

    fn text(t: &str) -> Piece {
        Piece::Text(t.into())
    }

    #[test]
    fn links_matches_in_plain_text() {
        let rules = LinkRules::new(&[jira()]);
        assert_eq!(
            rules.apply("fix ABC-12 and XY-3 today"),
            vec![
                text("fix "),
                link("ABC-12", "https://tracker.example.com/browse/ABC-12"),
                text(" and "),
                link("XY-3", "https://tracker.example.com/browse/XY-3"),
                text(" today"),
            ]
        );
    }

    #[test]
    fn no_rules_or_no_match_returns_input_unchanged() {
        let none = LinkRules::new(&[]);
        assert!(none.is_empty());
        assert_eq!(none.apply("ABC-12"), vec![text("ABC-12")]);
        let rules = LinkRules::new(&[jira()]);
        assert_eq!(rules.apply("nothing here"), vec![text("nothing here")]);
        assert_eq!(rules.apply(""), vec![text("")]);
    }

    #[test]
    fn first_matching_rule_wins_in_user_order() {
        let build = rule("Build", r"ABC-\d+", "https://ci.example.com/b/{match}");
        let a = LinkRules::new(&[build.clone(), jira()]);
        assert_eq!(a.apply("ABC-7"), vec![link("ABC-7", "https://ci.example.com/b/ABC-7")]);
        let b = LinkRules::new(&[jira(), build]);
        assert_eq!(b.apply("ABC-7"), vec![link("ABC-7", "https://tracker.example.com/browse/ABC-7")]);
    }

    #[test]
    fn later_rules_never_rewrap_linked_text() {
        let digits = rule("Num", r"\d+", "https://n.example.com/{match}");
        let rules = LinkRules::new(&[jira(), digits]);
        assert_eq!(
            rules.apply("ABC-12 then 99"),
            vec![
                link("ABC-12", "https://tracker.example.com/browse/ABC-12"),
                text(" then "),
                link("99", "https://n.example.com/99"),
            ]
        );
    }

    #[test]
    fn literal_urls_are_skipped() {
        let rules = LinkRules::new(&[jira()]);
        assert_eq!(
            rules.apply("see https://tracker.example.com/browse/ABC-12 or ABC-13"),
            vec![
                text("see https://tracker.example.com/browse/ABC-12 or "),
                link("ABC-13", "https://tracker.example.com/browse/ABC-13"),
            ]
        );
    }

    #[test]
    fn code_span_links_only_on_whole_match() {
        let rules = LinkRules::new(&[jira()]);
        assert_eq!(
            rules.whole("ABC-12").as_deref(),
            Some("https://tracker.example.com/browse/ABC-12")
        );
        assert_eq!(rules.whole("run ABC-12 now"), None);
        assert_eq!(rules.whole(""), None);
        let alt = LinkRules::new(&[rule("Alt", "a|ab", "https://x.example.com/{match}")]);
        assert_eq!(alt.whole("ab").as_deref(), Some("https://x.example.com/ab"));
    }

    #[test]
    fn match_is_percent_encoded_into_template() {
        let rules = LinkRules::new(&[rule("Doc", r"doc:\S+", "https://wiki.example.com/s?q={match}")]);
        assert_eq!(
            rules.apply("doc:a/b?c&d=é\"x"),
            vec![link("doc:a/b?c&d=é\"x", "https://wiki.example.com/s?q=doc%3Aa%2Fb%3Fc%26d%3D%C3%A9%22x")]
        );
        assert_eq!(percent_encode("A-z_0.9~"), "A-z_0.9~");
        assert_eq!(percent_encode("javascript:alert(1)"), "javascript%3Aalert%281%29");
        assert_eq!(expand("https://h.example/{match}/{match}", "a b"), "https://h.example/a%20b/a%20b");
    }

    #[test]
    fn invalid_patterns_are_rejected() {
        let cases = [
            ("", RuleError::EmptyPattern),
            ("(unclosed", RuleError::BadPattern(String::new())),
            ("[z-a]", RuleError::BadPattern(String::new())),
            ("a*", RuleError::MatchesEmpty),
            ("x?", RuleError::MatchesEmpty),
        ];
        for (pattern, want) in cases {
            let got = compile_pattern(pattern).err().expect(pattern);
            match (&got, &want) {
                (RuleError::BadPattern(why), RuleError::BadPattern(_)) => {
                    assert!(!why.is_empty() && !why.contains('\n'), "{pattern}: {why:?}")
                }
                _ => assert_eq!(got, want, "{pattern}"),
            }
        }
        assert_eq!(compile_pattern(&"a".repeat(MAX_PATTERN_LEN + 1)).err(), Some(RuleError::PatternTooLong));
        assert!(compile_pattern(r"\b[A-Z]+-\d+\b").is_ok());
    }

    #[test]
    fn malformed_templates_are_rejected() {
        let cases = [
            ("", RuleError::EmptyTemplate),
            ("javascript:alert({match})", RuleError::TemplateNotHttp),
            ("JavaScript://x/{match}", RuleError::TemplateNotHttp),
            ("data:text/html,{match}", RuleError::TemplateNotHttp),
            ("/relative/{match}", RuleError::TemplateNotHttp),
            ("ftp://h.example/{match}", RuleError::TemplateNotHttp),
            ("https://h.example/none", RuleError::TemplateNoPlaceholder),
            ("https://{match}/x", RuleError::TemplateHostFromMatch),
            ("https://h{match}.example/x", RuleError::TemplateHostFromMatch),
            ("https://user@h.example/{match}", RuleError::TemplateHostFromMatch),
            ("https:///{match}", RuleError::TemplateNoHost),
            ("https://h.example/a b/{match}", RuleError::TemplateBadChar),
            ("https://h.example/\"{match}", RuleError::TemplateBadChar),
            ("https://h.example/<{match}>", RuleError::TemplateBadChar),
            ("https://h.example/\n{match}", RuleError::TemplateBadChar),
        ];
        for (template, want) in cases {
            assert_eq!(validate_template(template), Err(want), "{template:?}");
        }
        let long = format!("https://h.example/{}{PLACEHOLDER}", "a".repeat(MAX_TEMPLATE_LEN));
        assert_eq!(validate_template(&long), Err(RuleError::TemplateTooLong));
        for ok in [
            "https://h.example/{match}",
            "HTTP://h.example:8080/browse/{match}?x=1",
            "https://h.example/?q={match}#top",
        ] {
            assert_eq!(validate_template(ok), Ok(()), "{ok}");
        }
    }

    #[test]
    fn rule_names_are_required_and_bounded() {
        assert_eq!(validate(&rule(" ", "a", "https://h.example/{match}")).err(), Some(RuleError::EmptyName));
        let long = "n".repeat(MAX_NAME_LEN + 1);
        assert_eq!(validate(&rule(&long, "a", "https://h.example/{match}")).err(), Some(RuleError::NameTooLong));
    }

    #[test]
    fn invalid_rules_are_never_applied() {
        let rules = LinkRules::new(&[
            rule("Bad regex", "(", "https://h.example/{match}"),
            rule("Bad template", "ABC", "javascript:{match}"),
            rule("Good", "ABC", "https://h.example/{match}"),
        ]);
        assert_eq!(rules.len(), 1);
        assert_eq!(rules.apply("ABC"), vec![link("ABC", "https://h.example/ABC")]);
    }

    #[test]
    fn validate_all_reports_first_bad_rule_and_count_cap() {
        let good = rule("Ok", "a", "https://h.example/{match}");
        let bad = rule("Bad", "(", "https://h.example/{match}");
        assert_eq!(validate_all(&[good.clone(), good.clone()]), Ok(()));
        let err = validate_all(&[good.clone(), bad]).unwrap_err();
        assert_eq!(err.0, Some(1));
        assert!(matches!(err.1, RuleError::BadPattern(_)));
        let many = vec![good; MAX_RULES + 1];
        assert_eq!(validate_all(&many), Err((None, RuleError::TooManyRules)));
        assert_eq!(LinkRules::new(&many).len(), MAX_RULES);
    }

    #[test]
    fn catastrophic_backtracking_shape_does_not_hang() {
        let rules = LinkRules::new(&[rule("Evil", r"(a+)+b", "https://h.example/{match}")]);
        assert_eq!(rules.len(), 1);
        let input = format!("{}!", "a".repeat(20_000));
        let start = Instant::now();
        assert_eq!(rules.apply(&input), vec![text(&input)]);
        assert_eq!(rules.whole(&input), None);
        assert!(start.elapsed() < Duration::from_secs(5), "took {:?}", start.elapsed());
    }

    #[test]
    fn multibyte_text_splits_on_char_boundaries() {
        let rules = LinkRules::new(&[jira()]);
        assert_eq!(
            rules.apply("é ABC-1 ü https://x.example/ü"),
            vec![
                text("é "),
                link("ABC-1", "https://tracker.example.com/browse/ABC-1"),
                text(" ü https://x.example/ü"),
            ]
        );
    }

    #[test]
    fn error_messages_are_user_facing() {
        assert!(RuleError::TemplateNotHttp.message().contains("https://"));
        assert!(RuleError::TemplateNoPlaceholder.message().contains("{match}"));
        assert!(RuleError::TooManyRules.message().contains("50"));
    }
}
