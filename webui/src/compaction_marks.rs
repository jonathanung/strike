//! Compaction and budget markers in the transcript (#1359).
//!
//! The daemon already emits `compaction.started`, `compaction.completed`,
//! `context.fit_warning` and `session.budget_warning`, and they reach the
//! browser, but the transcript dropped all four. This module turns each
//! payload into one inline marker line at the point it occurred, so a
//! session that "forgot" something shows where and why.
//!
//! Rules:
//! - Only fields the envelope carries are shown. An absent count, summary
//!   or residue list is omitted, never estimated or re-summarised.
//! - The completed marker shows removed/kept counts in its body; the
//!   compaction summary and structured residue sit in the collapsed detail
//!   fold, one click away.
//! - A payload the generated schema rejects (wrong types, non-object data)
//!   degrades to [`minimal`]: a title-only marker, never a broken
//!   transcript.
//!
//! Markers are pure functions of the envelope, so JSONL replay rebuilds
//! them in order exactly like every other transcript line.

use serde_json::Value;

/// `Message::key` prefix for every marker from this module; the view uses
/// it for the detail fold label and `data-compaction`.
pub const KEY_PREFIX: &str = "compaction:";

/// Event types this module renders.
pub const EVENTS: [&str; 4] = [
    "compaction.started",
    "compaction.completed",
    "context.fit_warning",
    "session.budget_warning",
];

/// One marker line, ready for the transcript.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Mark {
    pub title: String,
    pub body: String,
    pub badge: String,
    pub detail: String,
    /// `KEY_PREFIX` + a short kind (`started`, `completed`, `fit`, `budget`).
    pub key: String,
}

impl Mark {
    fn new(kind: &str, title: impl Into<String>) -> Self {
        Mark {
            title: title.into(),
            body: String::new(),
            badge: String::new(),
            detail: String::new(),
            key: format!("{KEY_PREFIX}{kind}"),
        }
    }
}

/// The marker for one decoded event, or `None` for a type this module
/// does not own.
pub fn mark(typ: &str, data: &Value) -> Option<Mark> {
    match typ {
        "compaction.started" => Some(started(data)),
        "compaction.completed" => Some(completed(data)),
        "context.fit_warning" => Some(fit_warning(data)),
        "session.budget_warning" => Some(budget_warning(data)),
        _ => None,
    }
}

/// Title-only fallback for a payload the schema rejected.
pub fn minimal(typ: &str) -> Option<Mark> {
    let (kind, title) = match typ {
        "compaction.started" => ("started", "Compacting context"),
        "compaction.completed" => ("completed", "Context compacted"),
        "context.fit_warning" => ("fit", "Context warning"),
        "session.budget_warning" => ("budget", "Budget warning"),
        _ => return None,
    };
    Some(Mark::new(kind, title))
}

fn started(data: &Value) -> Mark {
    let mut m = Mark::new("started", "Compacting context");
    m.body = cause(data);
    m
}

fn completed(data: &Value) -> Mark {
    let mut m = Mark::new("completed", "Context compacted");
    let mut parts = Vec::new();
    let removed = int(data, "removed");
    let kept = int(data, "kept");
    if let Some(n) = removed {
        parts.push(format!("removed {}", messages(n)));
    }
    if let Some(n) = kept {
        parts.push(format!("kept {}", messages(n)));
    }
    let counts = capitalise(&parts.join(", "));
    let cause = cause(data);
    m.body = match (counts.is_empty(), cause.is_empty()) {
        (false, false) => format!("{counts} · {cause}"),
        (false, true) => counts,
        (true, false) => cause,
        (true, true) => String::new(),
    };
    if let (Some(r), Some(k)) = (removed, kept) {
        m.badge = format!("-{r} / {k}");
    }
    m.detail = completed_detail(data);
    m
}

/// Summary then residue sections, each only when present.
fn completed_detail(data: &Value) -> String {
    let mut sections = Vec::new();
    let summary = text(data, "summary");
    if !summary.is_empty() {
        sections.push(format!("Summary\n{summary}"));
    }
    if let Some(residue) = data.get("residue").filter(|r| r.is_object()) {
        let residue_summary = text(residue, "summary");
        if !residue_summary.is_empty() && residue_summary != summary {
            sections.push(format!("Residue summary\n{residue_summary}"));
        }
        for (field, label) in [
            ("facts", "Facts kept"),
            ("decisions", "Decisions kept"),
            ("openQuestions", "Open questions kept"),
        ] {
            let items = residue_items(residue.get(field));
            if !items.is_empty() {
                sections.push(format!("{label}\n{}", items.join("\n")));
            }
        }
        let pinned = strings(residue.get("pinnedKinds"));
        if !pinned.is_empty() {
            sections.push(format!("Pinned kinds\n{}", pinned.join(", ")));
        }
    }
    sections.join("\n\n")
}

fn fit_warning(data: &Value) -> Mark {
    let level = text(data, "level");
    let title = if level.eq_ignore_ascii_case("critical") {
        "Context over limit"
    } else {
        "Context near limit"
    };
    let mut m = Mark::new("fit", title);
    m.badge = level;
    let message = text(data, "message");
    let est = int(data, "estimatedTokens").filter(|n| *n > 0);
    let limit = int(data, "contextLimit").filter(|n| *n > 0);
    let usage = match (est, limit) {
        (Some(e), Some(l)) => {
            let approx = if text(data, "source") == "actual" { "" } else { "~" };
            format!(
                "{approx}{} of {} tokens ({}%)",
                thousands(e),
                thousands(l),
                e.saturating_mul(100) / l
            )
        }
        _ => String::new(),
    };
    m.body = join_dot(&message, &usage);
    m
}

fn budget_warning(data: &Value) -> Mark {
    let exhausted = data.get("exhausted").and_then(Value::as_bool).unwrap_or(false);
    let mut m = Mark::new(
        "budget",
        if exhausted { "Budget exhausted" } else { "Budget warning" },
    );
    let level = text(data, "level");
    if !level.is_empty() {
        m.badge = if level.chars().all(|c| c.is_ascii_digit()) {
            format!("{level}%")
        } else {
            level
        };
    }
    let message = text(data, "message");
    let usage = match text(data, "kind").as_str() {
        "cost_usd" => match (num(data, "costUsd"), num(data, "maxCostUsd")) {
            (Some(c), Some(max)) if max > 0.0 => format!("${c:.2} of ${max:.2} session cost"),
            _ => String::new(),
        },
        "turn_tokens" => match (int(data, "tokensUsed"), int(data, "maxTokens")) {
            (Some(u), Some(max)) if max > 0 => {
                format!("{} of {} turn tokens", thousands(u), thousands(max))
            }
            _ => String::new(),
        },
        _ => String::new(),
    };
    m.body = join_dot(&message, &usage);
    m
}

/// Plain-language reason and strategy, each only when present.
fn cause(data: &Value) -> String {
    let reason = match text(data, "reason").as_str() {
        "" => String::new(),
        "manual" => "requested manually".into(),
        "threshold" => "context reached the compaction threshold".into(),
        "overflow" => "context overflowed the model window".into(),
        other => other.to_string(),
    };
    let strategy = match text(data, "strategy").as_str() {
        "" => String::new(),
        "trim" => "dropping the oldest messages".into(),
        "summarize" => "summarizing older messages".into(),
        other => format!("strategy {other}"),
    };
    capitalise(&match (reason.is_empty(), strategy.is_empty()) {
        (false, false) => format!("{reason}; {strategy}"),
        (false, true) => reason,
        (true, false) => strategy,
        (true, true) => String::new(),
    })
}

fn residue_items(v: Option<&Value>) -> Vec<String> {
    v.and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(|item| {
                    let t = item.get("text").and_then(Value::as_str)?.trim();
                    (!t.is_empty()).then(|| format!("- {t}"))
                })
                .collect()
        })
        .unwrap_or_default()
}

fn strings(v: Option<&Value>) -> Vec<String> {
    v.and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::trim)
                .filter(|s| !s.is_empty())
                .map(str::to_string)
                .collect()
        })
        .unwrap_or_default()
}

fn text(data: &Value, key: &str) -> String {
    data.get(key)
        .and_then(Value::as_str)
        .map(|s| s.trim().to_string())
        .unwrap_or_default()
}

/// An integer field only when the envelope carries it.
fn int(data: &Value, key: &str) -> Option<i64> {
    data.get(key).and_then(Value::as_i64)
}

fn num(data: &Value, key: &str) -> Option<f64> {
    data.get(key).and_then(Value::as_f64)
}

fn messages(n: i64) -> String {
    if n == 1 {
        "1 message".into()
    } else {
        format!("{n} messages")
    }
}

fn thousands(n: i64) -> String {
    let digits = n.unsigned_abs().to_string();
    let mut out = String::new();
    for (i, c) in digits.chars().enumerate() {
        if i > 0 && (digits.len() - i) % 3 == 0 {
            out.push(',');
        }
        out.push(c);
    }
    if n < 0 {
        out.insert(0, '-');
    }
    out
}

fn capitalise(s: &str) -> String {
    let mut chars = s.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().chain(chars).collect(),
        None => String::new(),
    }
}

fn join_dot(a: &str, b: &str) -> String {
    match (a.is_empty(), b.is_empty()) {
        (false, false) => format!("{a} · {b}"),
        (false, true) => a.to_string(),
        (true, false) => b.to_string(),
        (true, true) => String::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn started_marker_states_reason_and_strategy() {
        let cases = [
            (json!({"reason": "threshold", "strategy": "summarize"}),
             "Context reached the compaction threshold; summarizing older messages"),
            (json!({"reason": "manual", "strategy": "trim"}),
             "Requested manually; dropping the oldest messages"),
            (json!({"reason": "overflow"}), "Context overflowed the model window"),
            (json!({"strategy": "custom"}), "Strategy custom"),
            (json!({}), ""),
        ];
        for (data, want) in cases {
            let m = mark("compaction.started", &data).unwrap();
            assert_eq!(m.title, "Compacting context");
            assert_eq!(m.body, want, "{data}");
            assert_eq!(m.key, "compaction:started");
        }
    }

    #[test]
    fn completed_marker_shows_counts_summary_and_residue() {
        let data = json!({
            "reason": "threshold",
            "strategy": "summarize",
            "removed": 12,
            "kept": 1,
            "summary": "User is fixing the parser.",
            "residue": {
                "schemaVersion": "1",
                "facts": [{"id": "f1", "kind": "fact", "text": "parser lives in src/parse.rs"}],
                "decisions": [{"id": "d1", "text": "keep the old API"}, {"id": "d2", "text": " "}],
                "openQuestions": null,
                "pinnedKinds": ["decision", "constraint"],
                "summary": "User is fixing the parser."
            }
        });
        let m = mark("compaction.completed", &data).unwrap();
        assert_eq!(m.title, "Context compacted");
        assert_eq!(
            m.body,
            "Removed 12 messages, kept 1 message · Context reached the compaction threshold; summarizing older messages"
        );
        assert_eq!(m.badge, "-12 / 1");
        assert_eq!(
            m.detail,
            "Summary\nUser is fixing the parser.\n\n\
             Facts kept\n- parser lives in src/parse.rs\n\n\
             Decisions kept\n- keep the old API\n\n\
             Pinned kinds\ndecision, constraint"
        );
    }

    #[test]
    fn completed_marker_omits_absent_fields() {
        let m = mark("compaction.completed", &json!({"reason": "manual", "removed": 3})).unwrap();
        assert_eq!(m.body, "Removed 3 messages · Requested manually");
        assert_eq!(m.badge, "");
        assert_eq!(m.detail, "");
        let bare = mark("compaction.completed", &json!({})).unwrap();
        assert_eq!((bare.body.as_str(), bare.detail.as_str()), ("", ""));
    }

    #[test]
    fn fit_warning_marker_uses_level_message_and_counts() {
        let cases = [
            (json!({"level": "warn", "estimatedTokens": 180000, "contextLimit": 200000, "message": "context is getting full"}),
             "Context near limit", "warn", "context is getting full · ~180,000 of 200,000 tokens (90%)"),
            (json!({"level": "critical", "estimatedTokens": 210000, "contextLimit": 200000, "source": "actual"}),
             "Context over limit", "critical", "210,000 of 200,000 tokens (105%)"),
            (json!({"level": "warn", "contextLimit": 0, "estimatedTokens": 5}),
             "Context near limit", "warn", ""),
        ];
        for (data, title, badge, body) in cases {
            let m = mark("context.fit_warning", &data).unwrap();
            assert_eq!((m.title.as_str(), m.badge.as_str(), m.body.as_str()), (title, badge, body), "{data}");
            assert_eq!(m.key, "compaction:fit");
        }
    }

    #[test]
    fn budget_warning_marker_covers_both_kinds() {
        let cases = [
            (json!({"level": "80", "kind": "cost_usd", "costUsd": 4.0, "maxCostUsd": 5.0, "message": "80% of session budget"}),
             "Budget warning", "80%", "80% of session budget · $4.00 of $5.00 session cost"),
            (json!({"level": "100", "kind": "turn_tokens", "exhausted": true, "tokensUsed": 51000, "maxTokens": 50000}),
             "Budget exhausted", "100%", "51,000 of 50,000 turn tokens"),
            (json!({"kind": "cost_usd", "costUsd": 1.0}), "Budget warning", "", ""),
        ];
        for (data, title, badge, body) in cases {
            let m = mark("session.budget_warning", &data).unwrap();
            assert_eq!((m.title.as_str(), m.badge.as_str(), m.body.as_str()), (title, badge, body), "{data}");
        }
    }

    #[test]
    fn minimal_markers_cover_every_owned_event() {
        for typ in EVENTS {
            let m = minimal(typ).unwrap();
            assert!(!m.title.is_empty());
            assert!(m.body.is_empty() && m.detail.is_empty() && m.badge.is_empty());
            assert!(m.key.starts_with(KEY_PREFIX));
        }
        assert_eq!(minimal("text.delta"), None);
        assert_eq!(mark("text.delta", &json!({})), None);
    }

    #[test]
    fn thousands_groups_digits() {
        for (n, want) in [(0, "0"), (999, "999"), (1000, "1,000"), (1234567, "1,234,567"), (-4200, "-4,200")] {
            assert_eq!(thousands(n), want);
        }
    }
}
