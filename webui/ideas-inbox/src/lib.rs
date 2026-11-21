//! Ideas inbox view rules (#1323).
//!
//! The daemon store (`internal/persist/suggestion`) owns lifecycle, dedupe,
//! expiry and mutes. This crate owns what the cockpit derives from a list:
//! which rows are in the Open and Snoozed sections, the navbar badge text,
//! and the composer draft Accept writes into the new session (the proposed
//! prompt plus its evidence as context). Pure, no Leptos or serde; CI runs
//! `cargo test --manifest-path webui/ideas-inbox/Cargo.toml`.

/// Snooze choices offered on a row: label and hours.
pub const SNOOZE_CHOICES: [(&str, i64); 3] = [("1 hour", 1), ("1 day", 24), ("1 week", 168)];

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Evidence {
    pub label: String,
    pub session_id: String,
    pub path: String,
    pub url: String,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Idea {
    pub id: String,
    pub title: String,
    pub why: String,
    pub source: String,
    pub kind: String,
    pub effort: String,
    pub prompt: String,
    /// `open`, `snoozed`, `accepted` or `dismissed`.
    pub state: String,
    /// Unix ms the snooze ends; `0` when not snoozed.
    pub snooze_until_ms: i64,
    pub created_ms: i64,
    pub repeats: i64,
    pub evidence: Vec<Evidence>,
}

/// Open rows newest first, and snoozed rows soonest-to-wake first.
/// Accepted and dismissed rows are history and not listed.
pub fn sections(ideas: &[Idea]) -> (Vec<Idea>, Vec<Idea>) {
    let mut open: Vec<Idea> = ideas.iter().filter(|i| i.state == "open").cloned().collect();
    let mut snoozed: Vec<Idea> = ideas.iter().filter(|i| i.state == "snoozed").cloned().collect();
    open.sort_by(|a, b| b.created_ms.cmp(&a.created_ms).then_with(|| a.id.cmp(&b.id)));
    snoozed.sort_by(|a, b| a.snooze_until_ms.cmp(&b.snooze_until_ms).then_with(|| a.id.cmp(&b.id)));
    (open, snoozed)
}

/// Navbar badge text for the open count: hidden at zero, capped at `9+`.
pub fn badge(open: usize) -> Option<String> {
    match open {
        0 => None,
        1..=9 => Some(open.to_string()),
        _ => Some("9+".to_string()),
    }
}

fn evidence_line(ev: &Evidence) -> String {
    let mut parts = vec![ev.label.trim().to_string()];
    if !ev.session_id.trim().is_empty() {
        parts.push(format!("session {}", ev.session_id.trim()));
    }
    if !ev.path.trim().is_empty() {
        parts.push(ev.path.trim().to_string());
    }
    if !ev.url.trim().is_empty() {
        parts.push(ev.url.trim().to_string());
    }
    format!("- {}", parts.join(" \u{2014} "))
}

/// The composer draft for an accepted idea: the proposed prompt, then the
/// reasoning and evidence as labelled context. It is a draft: the user
/// reads and sends it, under the session's permission mode.
pub fn accept_draft(idea: &Idea) -> String {
    let mut out = idea.prompt.trim().to_string();
    let why = idea.why.trim();
    if why.is_empty() && idea.evidence.is_empty() {
        return out;
    }
    let source = if idea.source.trim().is_empty() { "unknown" } else { idea.source.trim() };
    out.push_str(&format!("\n\nContext from the Ideas inbox (suggested by {source}):"));
    if !why.is_empty() {
        out.push_str(&format!("\nWhy: {why}"));
    }
    if !idea.evidence.is_empty() {
        out.push_str("\nEvidence:");
        for ev in &idea.evidence {
            out.push('\n');
            out.push_str(&evidence_line(ev));
        }
    }
    out
}

/// One-line meta for a row: source, kind, effort, repeat count.
pub fn meta(idea: &Idea) -> String {
    let mut parts = vec![idea.source.clone(), idea.kind.clone()];
    if !idea.effort.trim().is_empty() {
        parts.push(idea.effort.trim().to_string());
    }
    if idea.repeats > 0 {
        parts.push(format!("seen {}\u{d7}", idea.repeats + 1));
    }
    parts.retain(|p| !p.trim().is_empty());
    parts.join(" \u{b7} ")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn idea(id: &str, state: &str, created: i64, wake: i64) -> Idea {
        Idea {
            id: id.into(),
            title: format!("Idea {id}"),
            state: state.into(),
            created_ms: created,
            snooze_until_ms: wake,
            prompt: "Do it".into(),
            source: "scheduler.heartbeat".into(),
            kind: "flaky-test".into(),
            ..Idea::default()
        }
    }

    #[test]
    fn sections_split_and_order() {
        let ideas = vec![
            idea("a", "open", 1, 0),
            idea("b", "open", 3, 0),
            idea("c", "snoozed", 2, 50),
            idea("d", "snoozed", 4, 10),
            idea("e", "accepted", 5, 0),
            idea("f", "dismissed", 6, 0),
        ];
        let (open, snoozed) = sections(&ideas);
        let ids = |v: &[Idea]| v.iter().map(|i| i.id.clone()).collect::<Vec<_>>();
        assert_eq!(ids(&open), vec!["b", "a"]);
        assert_eq!(ids(&snoozed), vec!["d", "c"]);
    }

    #[test]
    fn badge_hides_zero_and_caps() {
        assert_eq!(badge(0), None);
        assert_eq!(badge(1).as_deref(), Some("1"));
        assert_eq!(badge(9).as_deref(), Some("9"));
        assert_eq!(badge(10).as_deref(), Some("9+"));
    }

    #[test]
    fn accept_draft_carries_prompt_why_and_evidence() {
        let mut i = idea("a", "open", 1, 0);
        i.prompt = "  Investigate TestFoo  ".into();
        i.why = "failed 3 of 10 runs".into();
        i.evidence = vec![
            Evidence { label: "CI run".into(), url: "https://example.com/r/1".into(), ..Evidence::default() },
            Evidence { label: "Session".into(), session_id: "sess-1".into(), path: "pkg/foo_test.go".into(), ..Evidence::default() },
        ];
        let d = accept_draft(&i);
        assert_eq!(
            d,
            "Investigate TestFoo\n\nContext from the Ideas inbox (suggested by scheduler.heartbeat):\nWhy: failed 3 of 10 runs\nEvidence:\n- CI run \u{2014} https://example.com/r/1\n- Session \u{2014} session sess-1 \u{2014} pkg/foo_test.go"
        );
    }

    #[test]
    fn accept_draft_without_context_is_just_the_prompt() {
        let i = idea("a", "open", 1, 0);
        assert_eq!(accept_draft(&i), "Do it");
    }

    #[test]
    fn meta_line() {
        let mut i = idea("a", "open", 1, 0);
        assert_eq!(meta(&i), "scheduler.heartbeat \u{b7} flaky-test");
        i.effort = "~10 min".into();
        i.repeats = 2;
        assert_eq!(meta(&i), "scheduler.heartbeat \u{b7} flaky-test \u{b7} ~10 min \u{b7} seen 3\u{d7}");
    }

    #[test]
    fn snooze_choices_are_positive_and_within_store_limit() {
        for (label, hours) in SNOOZE_CHOICES {
            assert!(!label.is_empty());
            assert!(hours >= 1 && hours <= 30 * 24, "{label}");
        }
    }
}
