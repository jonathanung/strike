//! Skill and agent load warnings (#1386).
//!
//! The daemon rescans skill and agent roots with the real loaders and
//! returns per-file diagnostics as bootstrap `loadWarnings`. The cockpit
//! shows them in two places:
//! - Customize lists them in the Skills and Agents sections, with the
//!   reason and a fix.
//! - The chat column shows one notice when a session starts with
//!   diagnostics the browser has not seen yet.
//!
//! The notice warns once per unchanged file. Each diagnostic's
//! `fingerprint` (path + size + mtime) is remembered in this browser, so
//! the same broken file does not nag every session. Editing the file
//! changes the fingerprint, and it warns again.

use crate::model::LoadWarning;

/// localStorage key for fingerprints already announced in a notice.
pub const SEEN_KEY: &str = "strike.loadWarningsSeen";
/// Remembered fingerprints are capped; the oldest drop first.
pub const SEEN_CAP: usize = 256;

/// The key a diagnostic is deduped on: the daemon's fingerprint, or, for
/// a diagnostic without one, its path, code, and message. That fallback
/// still dedupes, but it cannot notice an edit that leaves the reason
/// unchanged.
pub fn dedupe_key(w: &LoadWarning) -> String {
    if w.fingerprint.is_empty() {
        format!("{}\u{0}{}\u{0}{}", w.path, w.code, w.message)
    } else {
        w.fingerprint.clone()
    }
}

/// Diagnostics not announced yet.
pub fn unseen<'a>(warnings: &'a [LoadWarning], seen: &[String]) -> Vec<&'a LoadWarning> {
    warnings
        .iter()
        .filter(|w| {
            let key = dedupe_key(w);
            !seen.iter().any(|s| s == &key)
        })
        .collect()
}

/// Add newly announced keys, keeping the newest `SEEN_CAP`.
pub fn remember(seen: &[String], fresh: &[&LoadWarning]) -> Vec<String> {
    let mut out: Vec<String> = seen.to_vec();
    for w in fresh {
        let key = dedupe_key(w);
        if !out.iter().any(|s| s == &key) {
            out.push(key);
        }
    }
    if out.len() > SEEN_CAP {
        out.drain(..out.len() - SEEN_CAP);
    }
    out
}

pub fn parse_seen(raw: &str) -> Vec<String> {
    serde_json::from_str(raw).unwrap_or_default()
}

fn display_name(w: &LoadWarning) -> &str {
    if !w.file.is_empty() {
        &w.file
    } else {
        &w.path
    }
}

fn plural(n: usize, one: &str, many: &str) -> String {
    if n == 1 {
        format!("1 {one}")
    } else {
        format!("{n} {many}")
    }
}

/// One-line session-start notice for fresh diagnostics. Empty input gives
/// an empty string (no notice). A strict-root failure is said plainly: it
/// stopped every skill (or agent) in that load, not just one file.
pub fn notice_text(fresh: &[&LoadWarning]) -> String {
    if fresh.is_empty() {
        return String::new();
    }
    let fatal: Vec<&&LoadWarning> = fresh.iter().filter(|w| w.fatal).collect();
    let mut parts = Vec::new();
    for kind in ["skill", "agent"] {
        if let Some(w) = fatal.iter().find(|w| w.kind == kind) {
            parts.push(format!(
                "No {kind}s loaded: {} failed the whole load.",
                display_name(w)
            ));
        }
    }
    let rest: Vec<&&LoadWarning> = fresh.iter().filter(|w| !w.fatal).collect();
    if !rest.is_empty() {
        let skills = rest.iter().filter(|w| w.kind == "skill").count();
        let agents = rest.iter().filter(|w| w.kind == "agent").count();
        let other = rest.len() - skills - agents;
        let mut counts = Vec::new();
        if skills > 0 {
            counts.push(plural(skills, "skill file", "skill files"));
        }
        if agents > 0 {
            counts.push(plural(agents, "agent file", "agent files"));
        }
        if other > 0 {
            counts.push(plural(other, "file", "files"));
        }
        let mut names: Vec<&str> = rest.iter().map(|w| display_name(w)).collect();
        let more = names.len().saturating_sub(3);
        names.truncate(3);
        let mut list = names.join(", ");
        if more > 0 {
            list.push_str(&format!(" and {more} more"));
        }
        let verb = if rest.len() == 1 { "has" } else { "have" };
        parts.push(format!("{} {verb} load problems: {list}.", counts.join(" and ")));
    }
    parts.push("See Customize.".into());
    parts.join(" ")
}

/// Diagnostics for one Customize section (`skills` / `agents`).
pub fn for_section<'a>(warnings: &'a [LoadWarning], section: &str) -> Vec<&'a LoadWarning> {
    let kind = match section {
        "skills" => "skill",
        "agents" => "agent",
        _ => return Vec::new(),
    };
    warnings.iter().filter(|w| w.kind == kind).collect()
}

/// Section heading with the count, e.g. "2 skill files have load problems".
pub fn section_heading(section: &str, count: usize) -> String {
    let (one, many) = match section {
        "agents" => ("agent file", "agent files"),
        _ => ("skill file", "skill files"),
    };
    let verb = if count == 1 { "has" } else { "have" };
    format!("{} {verb} load problems", plural(count, one, many))
}

/// Short label for a diagnostic code.
pub fn code_label(code: &str) -> &'static str {
    match code {
        "unreadable" => "Unreadable",
        "empty" => "Empty",
        "malformed_frontmatter" => "Malformed frontmatter",
        "missing_description" => "No description",
        "invalid" => "Invalid",
        "skipped" => "Skipped",
        _ => "Problem",
    }
}

/// How to fix it, per code. Copy only; it never reads the file.
pub fn fix_hint(w: &LoadWarning) -> &'static str {
    match w.code.as_str() {
        "unreadable" => "Check the file's permissions so Strike can read it.",
        "empty" => "Add instructions below the frontmatter, or delete the file.",
        "malformed_frontmatter" => {
            "Start the file with a --- line and close the frontmatter with another --- line."
        }
        "missing_description" => {
            "Add a description: line to the frontmatter so pickers can say what it does."
        }
        "invalid" if w.fatal => {
            "Fix the field named in the message. Until then nothing else in this load is used."
        }
        "invalid" => "Fix the field named in the message.",
        "skipped" => "Fix the field named in the message. Other files in this folder still loaded.",
        _ => "Open the file and fix the problem named in the message.",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn warn(kind: &str, code: &str, file: &str, fp: &str, fatal: bool) -> LoadWarning {
        LoadWarning {
            kind: kind.into(),
            severity: if fatal { "error" } else { "warning" }.into(),
            code: code.into(),
            path: format!("/w/{file}"),
            file: file.into(),
            message: "reason".into(),
            fatal,
            fingerprint: fp.into(),
        }
    }

    #[test]
    fn unseen_skips_announced_keys() {
        let list = vec![
            warn("skill", "empty", "a.md", "f1", false),
            warn("skill", "empty", "b.md", "f2", false),
            warn("agent", "empty", "c.md", "", false),
        ];
        let seen = vec!["f1".to_string()];
        let fresh = unseen(&list, &seen);
        let files: Vec<&str> = fresh.iter().map(|w| w.file.as_str()).collect();
        assert_eq!(files, vec!["b.md", "c.md"]);
        assert_eq!(unseen(&[], &seen).len(), 0);
        // Once remembered, an unfingerprinted diagnostic stops re-announcing.
        let kept = remember(&seen, &fresh);
        assert!(unseen(&list, &kept).is_empty());
    }

    #[test]
    fn changed_file_warns_again() {
        let seen = vec!["old-fp".to_string()];
        let edited = vec![warn("skill", "empty", "a.md", "new-fp", false)];
        assert_eq!(unseen(&edited, &seen).len(), 1);
    }

    #[test]
    fn remember_dedupes_and_caps() {
        let a = warn("skill", "empty", "a.md", "f1", false);
        let b = warn("skill", "empty", "b.md", "f2", false);
        let none = warn("skill", "empty", "c.md", "", false);
        let out = remember(&["f1".to_string()], &[&a, &b, &none]);
        assert_eq!(out, vec!["f1".to_string(), "f2".to_string(), dedupe_key(&none)]);
        assert_eq!(dedupe_key(&none), "/w/c.md\u{0}empty\u{0}reason");

        let many: Vec<String> = (0..SEEN_CAP).map(|i| format!("x{i}")).collect();
        let out = remember(&many, &[&b]);
        assert_eq!(out.len(), SEEN_CAP);
        assert_eq!(out.first().map(String::as_str), Some("x1"));
        assert_eq!(out.last().map(String::as_str), Some("f2"));
    }

    #[test]
    fn parse_seen_tolerates_garbage() {
        assert_eq!(parse_seen(r#"["a","b"]"#), vec!["a".to_string(), "b".to_string()]);
        assert!(parse_seen("").is_empty());
        assert!(parse_seen("{nope").is_empty());
    }

    #[test]
    fn notice_text_cases() {
        let one = warn("skill", "missing_description", "a.md", "f1", false);
        let two = warn("skill", "empty", "b/SKILL.md", "f2", false);
        let agent = warn("agent", "malformed_frontmatter", "rev.md", "f3", false);
        let fatal = warn("skill", "invalid", "bad.md", "f4", true);
        let cases: Vec<(Vec<&LoadWarning>, &str)> = vec![
            (vec![], ""),
            (vec![&one], "1 skill file has load problems: a.md. See Customize."),
            (
                vec![&one, &two, &agent],
                "2 skill files and 1 agent file have load problems: a.md, b/SKILL.md, rev.md. See Customize.",
            ),
            (
                vec![&fatal, &agent],
                "No skills loaded: bad.md failed the whole load. 1 agent file has load problems: rev.md. See Customize.",
            ),
        ];
        for (input, want) in cases {
            assert_eq!(notice_text(&input), want);
        }
    }

    #[test]
    fn notice_text_truncates_long_lists() {
        let list: Vec<LoadWarning> = (0..5)
            .map(|i| warn("skill", "empty", &format!("s{i}.md"), &format!("f{i}"), false))
            .collect();
        let refs: Vec<&LoadWarning> = list.iter().collect();
        assert_eq!(
            notice_text(&refs),
            "5 skill files have load problems: s0.md, s1.md, s2.md and 2 more. See Customize."
        );
    }

    #[test]
    fn notice_falls_back_to_path_when_file_is_empty() {
        let mut w = warn("agent", "empty", "", "f1", false);
        w.path = "/w/.strike/agents/x.md".into();
        assert_eq!(
            notice_text(&[&w]),
            "1 agent file has load problems: /w/.strike/agents/x.md. See Customize."
        );
    }

    #[test]
    fn sections_filter_by_kind() {
        let list = vec![
            warn("skill", "empty", "a.md", "f1", false),
            warn("agent", "empty", "b.md", "f2", false),
        ];
        assert_eq!(for_section(&list, "skills").len(), 1);
        assert_eq!(for_section(&list, "agents")[0].file, "b.md");
        assert!(for_section(&list, "mcp").is_empty());
        assert_eq!(section_heading("skills", 1), "1 skill file has load problems");
        assert_eq!(section_heading("agents", 2), "2 agent files have load problems");
    }

    #[test]
    fn labels_and_hints() {
        assert_eq!(code_label("malformed_frontmatter"), "Malformed frontmatter");
        assert_eq!(code_label("weird"), "Problem");
        let fatal = warn("skill", "invalid", "bad.md", "f", true);
        let soft = warn("skill", "invalid", "bad.md", "f", false);
        assert!(fix_hint(&fatal).contains("nothing else in this load"));
        assert_eq!(fix_hint(&soft), "Fix the field named in the message.");
        assert!(fix_hint(&warn("skill", "skipped", "x.md", "f", false)).contains("still loaded"));
    }
}
