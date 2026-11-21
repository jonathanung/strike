//! Prompt library (#1357): named, reusable composer prompts with
//! `{{variable}}` placeholders. Pure logic only — the browser stores the
//! list in localStorage and renders it; this crate validates, searches,
//! extracts placeholders and fills them, so it is testable off-wasm.
//!
//! Inserting a prompt only ever produces draft text. Nothing here sends,
//! expands daemon-side, or executes anything.

/// Longest prompt name, in chars.
pub const MAX_NAME: usize = 80;
/// Longest prompt body, in chars.
pub const MAX_BODY: usize = 20_000;
/// Most distinct placeholders one prompt may ask for.
pub const MAX_VARS: usize = 20;
/// Longest placeholder name, in chars.
pub const MAX_VAR_NAME: usize = 40;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Prompt {
    pub id: String,
    pub name: String,
    pub body: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PlaceholderError {
    /// `{{` with no matching `}}`. `at` is the char offset of the `{{`.
    Unclosed { at: usize },
    /// `{{}}` or `{{   }}`.
    Empty { at: usize },
    /// A name with characters outside letters, digits, `_`, `-`, space,
    /// or one that is too long.
    BadName { name: String },
    TooMany,
}

impl PlaceholderError {
    pub fn message(&self) -> String {
        match self {
            Self::Unclosed { at } => format!(
                "Placeholder at character {} is unclosed: every {{{{ needs a matching }}}}.",
                at + 1
            ),
            Self::Empty { at } => format!("Placeholder at character {} has no name.", at + 1),
            Self::BadName { name } => format!(
                "Placeholder {{{{{name}}}}} is not a valid name: use letters, digits, spaces, _ or - (up to {MAX_VAR_NAME})."
            ),
            Self::TooMany => format!("A prompt can ask for at most {MAX_VARS} placeholders."),
        }
    }
}

/// One `{{name}}` occurrence: byte range of the whole token and its name.
struct Token {
    start: usize,
    end: usize,
    name: String,
}

fn valid_var_name(name: &str) -> bool {
    !name.is_empty()
        && name.chars().count() <= MAX_VAR_NAME
        && name
            .chars()
            .all(|c| c.is_alphanumeric() || c == '_' || c == '-' || c == ' ')
}

fn tokens(body: &str) -> Result<Vec<Token>, PlaceholderError> {
    let mut out = Vec::new();
    let mut i = 0;
    while let Some(rel) = body[i..].find("{{") {
        let start = i + rel;
        let inner_start = start + 2;
        let at = body[..start].chars().count();
        let Some(close_rel) = body[inner_start..].find("}}") else {
            return Err(PlaceholderError::Unclosed { at });
        };
        let inner = &body[inner_start..inner_start + close_rel];
        // A second `{{` before the close means the first one never closed.
        if inner.contains("{{") {
            return Err(PlaceholderError::Unclosed { at });
        }
        let name = inner.trim();
        if name.is_empty() {
            return Err(PlaceholderError::Empty { at });
        }
        if !valid_var_name(name) {
            return Err(PlaceholderError::BadName { name: name.to_string() });
        }
        let end = inner_start + close_rel + 2;
        out.push(Token { start, end, name: name.to_string() });
        i = end;
    }
    Ok(out)
}

/// Distinct placeholder names in first-appearance order. A placeholder
/// repeated in the body is asked for once.
pub fn placeholders(body: &str) -> Result<Vec<String>, PlaceholderError> {
    let mut names: Vec<String> = Vec::new();
    for t in tokens(body)? {
        if !names.contains(&t.name) {
            names.push(t.name);
        }
    }
    if names.len() > MAX_VARS {
        return Err(PlaceholderError::TooMany);
    }
    Ok(names)
}

/// Replace every placeholder with its value (missing values become
/// empty). Values are inserted literally: a value containing `{{x}}` is
/// not expanded again.
pub fn fill(body: &str, values: &[(String, String)]) -> Result<String, PlaceholderError> {
    let toks = tokens(body)?;
    let mut out = String::with_capacity(body.len());
    let mut last = 0;
    for t in toks {
        out.push_str(&body[last..t.start]);
        if let Some((_, v)) = values.iter().find(|(k, _)| *k == t.name) {
            out.push_str(v);
        }
        last = t.end;
    }
    out.push_str(&body[last..]);
    Ok(out)
}

/// Save-time validation: a name, a body, sane lengths, and well-formed
/// placeholders. A malformed placeholder is rejected here, never at
/// insert time. Returns the placeholder names on success.
pub fn validate(name: &str, body: &str) -> Result<Vec<String>, String> {
    let name = name.trim();
    if name.is_empty() {
        return Err("Give the prompt a name.".into());
    }
    if name.chars().count() > MAX_NAME {
        return Err(format!("Names are at most {MAX_NAME} characters."));
    }
    if body.trim().is_empty() {
        return Err("The prompt body is empty.".into());
    }
    if body.chars().count() > MAX_BODY {
        return Err(format!("Prompt bodies are at most {MAX_BODY} characters."));
    }
    placeholders(body).map_err(|e| e.message())
}

/// Text after inserting `text` into the composer: the draft is never
/// overwritten — non-empty drafts get the prompt appended after a blank
/// line.
pub fn insert_into_draft(draft: &str, text: &str) -> String {
    let head = draft.trim_end();
    if head.is_empty() {
        text.to_string()
    } else {
        format!("{head}\n\n{text}")
    }
}

/// Case-insensitive search. Name matches rank before body-only matches;
/// an empty query lists everything in name order.
pub fn search<'a>(prompts: &'a [Prompt], query: &str) -> Vec<&'a Prompt> {
    let q = query.trim().to_lowercase();
    let mut by_name: Vec<&Prompt> = Vec::new();
    let mut by_body: Vec<&Prompt> = Vec::new();
    for p in prompts {
        if q.is_empty() || p.name.to_lowercase().contains(&q) {
            by_name.push(p);
        } else if p.body.to_lowercase().contains(&q) {
            by_body.push(p);
        }
    }
    let key = |p: &&Prompt| p.name.to_lowercase();
    by_name.sort_by_key(key);
    by_body.sort_by_key(key);
    by_name.extend(by_body);
    by_name
}

/// First non-empty line of a body, clipped — the picker's detail line.
pub fn preview(body: &str, max: usize) -> String {
    let line = body.lines().map(str::trim).find(|l| !l.is_empty()).unwrap_or("");
    if line.chars().count() <= max {
        line.to_string()
    } else {
        let mut s: String = line.chars().take(max.saturating_sub(1)).collect();
        s.push('…');
        s
    }
}

/// Add or replace a prompt (same id replaces). Names are unique,
/// case-insensitively, so the picker never shows two rows that read the
/// same.
pub fn upsert(prompts: &mut Vec<Prompt>, prompt: Prompt) -> Result<(), String> {
    validate(&prompt.name, &prompt.body)?;
    let name = prompt.name.trim().to_string();
    if prompts
        .iter()
        .any(|p| p.id != prompt.id && p.name.trim().eq_ignore_ascii_case(&name))
    {
        return Err(format!("A prompt named “{name}” already exists."));
    }
    let prompt = Prompt { name, ..prompt };
    match prompts.iter_mut().find(|p| p.id == prompt.id) {
        Some(slot) => *slot = prompt,
        None => prompts.push(prompt),
    }
    Ok(())
}

/// Remove by id; returns whether anything was removed.
pub fn remove(prompts: &mut Vec<Prompt>, id: &str) -> bool {
    let before = prompts.len();
    prompts.retain(|p| p.id != id);
    prompts.len() != before
}

#[cfg(test)]
mod tests {
    use super::*;

    fn p(id: &str, name: &str, body: &str) -> Prompt {
        Prompt { id: id.into(), name: name.into(), body: body.into() }
    }

    fn vals(pairs: &[(&str, &str)]) -> Vec<(String, String)> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn placeholders_are_distinct_in_first_appearance_order() {
        let cases: &[(&str, &[&str])] = &[
            ("no vars here", &[]),
            ("Review {{file}} for {{concern}}", &["file", "concern"]),
            ("{{file}} then {{ file }} again", &["file"]),
            ("{{b}} {{a}} {{b}}", &["b", "a"]),
            ("a lone }} is literal", &[]),
            ("{{CI job-name_2}}", &["CI job-name_2"]),
        ];
        for (body, want) in cases {
            assert_eq!(placeholders(body).unwrap(), want.to_vec(), "body {body:?}");
        }
    }

    #[test]
    fn malformed_placeholders_are_rejected() {
        assert_eq!(placeholders("fix {{file"), Err(PlaceholderError::Unclosed { at: 4 }));
        assert_eq!(placeholders("{{a {{b}}"), Err(PlaceholderError::Unclosed { at: 0 }));
        assert_eq!(placeholders("x {{  }} y"), Err(PlaceholderError::Empty { at: 2 }));
        assert!(matches!(placeholders("{{a.b}}"), Err(PlaceholderError::BadName { .. })));
        let long = format!("{{{{{}}}}}", "x".repeat(MAX_VAR_NAME + 1));
        assert!(matches!(placeholders(&long), Err(PlaceholderError::BadName { .. })));
        let many: String = (0..=MAX_VARS).map(|i| format!("{{{{v{i}}}}} ")).collect();
        assert_eq!(placeholders(&many), Err(PlaceholderError::TooMany));
    }

    #[test]
    fn unclosed_offset_counts_chars_not_bytes() {
        assert_eq!(placeholders("é {{x"), Err(PlaceholderError::Unclosed { at: 2 }));
        assert!(PlaceholderError::Unclosed { at: 2 }.message().contains("character 3"));
    }

    #[test]
    fn fill_replaces_every_occurrence_and_is_literal() {
        let body = "Review {{file}} for {{concern}}; re-read {{ file }}.";
        let out = fill(body, &vals(&[("file", "a.go"), ("concern", "races")])).unwrap();
        assert_eq!(out, "Review a.go for races; re-read a.go.");
        // Values are not re-expanded.
        assert_eq!(fill("{{a}}", &vals(&[("a", "{{b}}")])).unwrap(), "{{b}}");
        // A missing value becomes empty.
        assert_eq!(fill("x{{a}}y", &[]).unwrap(), "xy");
        assert!(fill("{{oops", &[]).is_err());
    }

    #[test]
    fn validate_checks_name_body_and_placeholders() {
        assert_eq!(validate("Review", "Review {{file}}").unwrap(), vec!["file".to_string()]);
        assert!(validate("  ", "body").is_err());
        assert!(validate("x", "   ").is_err());
        assert!(validate(&"n".repeat(MAX_NAME + 1), "body").is_err());
        assert!(validate("x", &"b".repeat(MAX_BODY + 1)).is_err());
        let err = validate("x", "bad {{oops").unwrap_err();
        assert!(err.contains("unclosed"), "{err}");
    }

    #[test]
    fn insert_never_overwrites_the_draft() {
        assert_eq!(insert_into_draft("", "P"), "P");
        assert_eq!(insert_into_draft("  \n", "P"), "P");
        assert_eq!(insert_into_draft("my notes\n", "P"), "my notes\n\nP");
    }

    #[test]
    fn search_ranks_name_hits_before_body_hits() {
        let list = vec![
            p("1", "Write UAT report", "summarise the review"),
            p("2", "Review diff", "look at the diff"),
            p("3", "Triage CI", "find the failing job"),
        ];
        let names = |q: &str| search(&list, q).into_iter().map(|p| p.name.as_str()).collect::<Vec<_>>();
        assert_eq!(names(""), vec!["Review diff", "Triage CI", "Write UAT report"]);
        assert_eq!(names("review"), vec!["Review diff", "Write UAT report"]);
        assert_eq!(names("CI"), vec!["Triage CI"]);
        assert_eq!(names("nothing matches"), Vec::<&str>::new());
    }

    #[test]
    fn upsert_adds_replaces_and_keeps_names_unique() {
        let mut list = Vec::new();
        upsert(&mut list, p("1", " Review ", "r {{x}}")).unwrap();
        assert_eq!(list[0].name, "Review");
        upsert(&mut list, p("2", "Triage", "t")).unwrap();
        assert!(upsert(&mut list, p("3", "review", "dup")).is_err());
        // Renaming in place keeps its own name allowed.
        upsert(&mut list, p("1", "REVIEW", "edited")).unwrap();
        assert_eq!(list.len(), 2);
        assert_eq!(list[0].body, "edited");
        // Malformed bodies never reach the list.
        assert!(upsert(&mut list, p("4", "Bad", "{{")).is_err());
        assert_eq!(list.len(), 2);
    }

    #[test]
    fn remove_by_id() {
        let mut list = vec![p("1", "a", "x"), p("2", "b", "y")];
        assert!(remove(&mut list, "1"));
        assert!(!remove(&mut list, "1"));
        assert_eq!(list.len(), 1);
    }

    #[test]
    fn preview_takes_the_first_line_and_clips() {
        assert_eq!(preview("\n  first line \nsecond", 40), "first line");
        assert_eq!(preview("abcdef", 4), "abc…");
        assert_eq!(preview("", 4), "");
    }
}
