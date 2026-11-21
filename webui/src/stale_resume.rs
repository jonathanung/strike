//! Warn before resuming a stale, large session (#1502).
//!
//! The first turn after a long idle re-sends the session's whole history
//! with a cold prompt cache. When the user opens a session that is both
//! stale (no activity for [`STALE_MS`]) and large (its last recorded
//! context occupancy, `historyTokens` from the daemon's session list, is at
//! least half the selected model's context window, or [`FLOOR_TOKENS`] when
//! the window is unknown), the opened session shows one inline card first:
//! Resume anyway (remembered for that session at that size), Start fresh
//! (a new session whose composer is pre-filled with a reference to the old
//! one, never sent automatically and never a copy of its transcript), or
//! Cancel (back to where the user was).
//!
//! Opening a session is unchanged below either threshold, for the session
//! already being viewed, and whenever the daemon could not tell the size
//! (`historyTokens` absent): no guessed numbers.

use std::collections::HashMap;

use leptos::prelude::*;

use crate::model::SessionItem;

/// No activity for this long makes a session stale (24h).
pub const STALE_MS: i64 = 24 * 60 * 60 * 1000;
/// "Large" when the selected model's context window is unknown.
pub const FLOOR_TOKENS: i64 = 100_000;

const DISMISS_KEY: &str = "strike.staleResume.dismissed";

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StaleWarning {
    pub id: String,
    pub title: String,
    pub age_ms: i64,
    pub tokens: i64,
    /// The session open before this one ("" when none): Cancel returns there.
    pub previous: String,
}

/// Tokens at or above which a history counts as large.
pub fn threshold(context_limit: i64) -> i64 {
    if context_limit > 0 {
        context_limit / 2
    } else {
        FLOOR_TOKENS
    }
}

/// Pure warning decision. `dismissed` is the size at which the user last
/// chose Resume anyway for this session; the warning stays quiet until the
/// history grows past it.
pub fn should_warn(
    last_activity_ms: i64,
    tokens: i64,
    now_ms: i64,
    context_limit: i64,
    dismissed: Option<i64>,
) -> bool {
    if tokens <= 0 || last_activity_ms <= 0 {
        return false;
    }
    if now_ms - last_activity_ms < STALE_MS {
        return false;
    }
    if tokens < threshold(context_limit) {
        return false;
    }
    !dismissed.is_some_and(|d| tokens <= d)
}

/// Decide for one list row being opened. No warning for the session the
/// user is already viewing.
pub fn check(
    item: &SessionItem,
    now_ms: i64,
    context_limit: i64,
    viewing: &str,
    dismissed: Option<i64>,
) -> Option<StaleWarning> {
    if item.id == viewing {
        return None;
    }
    let last = if item.last_activity_at > 0 {
        item.last_activity_at
    } else {
        item.mtime * 1000
    };
    should_warn(last, item.history_tokens, now_ms, context_limit, dismissed).then(|| {
        StaleWarning {
            id: item.id.clone(),
            title: item.title.clone(),
            age_ms: now_ms - last,
            tokens: item.history_tokens,
            previous: viewing.to_string(),
        }
    })
}

pub fn age_label(ms: i64) -> String {
    let hours = ms / (60 * 60 * 1000);
    if hours < 48 {
        format!("{}h ago", hours.max(1))
    } else {
        format!("{}d ago", hours / 24)
    }
}

pub fn tokens_label(n: i64) -> String {
    if n >= 1_000_000 {
        format!("~{:.1}M", n as f64 / 1_000_000.0)
    } else if n >= 1_000 {
        format!("~{}k", n / 1_000)
    } else {
        format!("~{n}")
    }
}

pub fn message(w: &StaleWarning) -> String {
    format!(
        "Last active {} \u{b7} {} tokens of history \u{2014} the next turn re-reads all of it.",
        age_label(w.age_ms),
        tokens_label(w.tokens)
    )
}

/// Composer text for Start fresh: a reference by id and title only. The
/// agent can look the old session up with its session tools if needed.
pub fn fresh_prompt(id: &str, title: &str) -> String {
    let title = title.trim();
    let name = if title.is_empty() {
        String::new()
    } else {
        format!(" \u{201c}{title}\u{201d}")
    };
    format!(
        "Continuing from earlier session{name} (id {id}). Look it up only if you need its context. "
    )
}

pub fn parse_dismissals(raw: &str) -> HashMap<String, i64> {
    serde_json::from_str(raw).unwrap_or_default()
}

pub fn dismissed_tokens(id: &str) -> Option<i64> {
    parse_dismissals(&crate::load(DISMISS_KEY)).get(id).copied()
}

pub fn remember_dismissal(id: &str, tokens: i64) {
    let mut map = parse_dismissals(&crate::load(DISMISS_KEY));
    map.insert(id.to_string(), tokens);
    if let Ok(raw) = serde_json::to_string(&map) {
        crate::save(DISMISS_KEY, &raw);
    }
}

/// The inline card at the top of the opened session.
#[component]
pub fn StaleResumeCard(
    warning: RwSignal<Option<StaleWarning>>,
    on_fresh: Callback<StaleWarning>,
    on_cancel: Callback<StaleWarning>,
) -> impl IntoView {
    view! {
        {move || warning.get().map(|w| {
            let text = message(&w);
            let id_attr = w.id.clone();
            let (resume_w, fresh_w, cancel_w) = (w.clone(), w.clone(), w);
            view! {
                <div class="stale-resume" role="alert" data-stale-resume=id_attr>
                    <p class="stale-resume-text">{text}</p>
                    <div class="stale-resume-actions">
                        <button type="button" class="primary" data-stale-resume-action="resume" on:click=move |_| {
                            remember_dismissal(&resume_w.id, resume_w.tokens);
                            warning.set(None);
                        }>"Resume anyway"</button>
                        <button type="button" class="secondary" data-stale-resume-action="fresh" on:click=move |_| {
                            warning.set(None);
                            on_fresh.run(fresh_w.clone());
                        }>"Start fresh"</button>
                        <button type="button" class="secondary" data-stale-resume-action="cancel" on:click=move |_| {
                            warning.set(None);
                            on_cancel.run(cancel_w.clone());
                        }>"Cancel"</button>
                    </div>
                </div>
            }
        })}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const H: i64 = 60 * 60 * 1000;
    const NOW: i64 = 1_800_000_000_000;

    fn item(id: &str, last: i64, tokens: i64) -> SessionItem {
        SessionItem {
            id: id.into(),
            title: format!("title {id}"),
            mtime: 0,
            workspace_id: String::new(),
            status: String::new(),
            last_activity_at: last,
            unread: None,
            history_tokens: tokens,
        }
    }

    #[test]
    fn warns_only_when_stale_and_large() {
        let cases = [
            ("stale and large", NOW - 72 * H, 150_000, 200_000, None, true),
            ("fresh and large", NOW - 2 * H, 150_000, 200_000, None, false),
            ("stale and small", NOW - 72 * H, 20_000, 200_000, None, false),
            ("exactly 24h and exactly half", NOW - 24 * H, 100_000, 200_000, None, true),
            ("size unknown never warns", NOW - 72 * H, 0, 200_000, None, false),
            ("activity unknown never warns", 0, 150_000, 200_000, None, false),
            ("unknown window uses the floor", NOW - 72 * H, FLOOR_TOKENS, 0, None, true),
            ("below the floor", NOW - 72 * H, FLOOR_TOKENS - 1, 0, None, false),
            ("dismissed at this size", NOW - 72 * H, 150_000, 200_000, Some(150_000), false),
            ("grew past the dismissal", NOW - 72 * H, 160_000, 200_000, Some(150_000), true),
        ];
        for (name, last, tokens, limit, dismissed, want) in cases {
            assert_eq!(should_warn(last, tokens, NOW, limit, dismissed), want, "{name}");
        }
    }

    #[test]
    fn check_skips_the_session_already_viewed_and_records_where_to_return() {
        let row = item("old", NOW - 72 * H, 150_000);
        assert_eq!(check(&row, NOW, 200_000, "old", None), None);
        let w = check(&row, NOW, 200_000, "current", None).expect("warning");
        assert_eq!(w.previous, "current");
        assert_eq!(w.tokens, 150_000);
        assert_eq!(w.age_ms, 72 * H);
    }

    #[test]
    fn check_falls_back_to_mtime_seconds() {
        let mut row = item("old", 0, 150_000);
        row.mtime = (NOW - 72 * H) / 1000;
        assert!(check(&row, NOW, 0, "", None).is_some());
    }

    #[test]
    fn labels_read_naturally() {
        assert_eq!(age_label(30 * 60 * 1000), "1h ago");
        assert_eq!(age_label(26 * H), "26h ago");
        assert_eq!(age_label(72 * H), "3d ago");
        assert_eq!(tokens_label(950), "~950");
        assert_eq!(tokens_label(150_400), "~150k");
        assert_eq!(tokens_label(1_240_000), "~1.2M");
        let w = StaleWarning {
            id: "s".into(),
            title: "t".into(),
            age_ms: 72 * H,
            tokens: 150_000,
            previous: String::new(),
        };
        assert_eq!(
            message(&w),
            "Last active 3d ago \u{b7} ~150k tokens of history \u{2014} the next turn re-reads all of it."
        );
    }

    #[test]
    fn fresh_prompt_references_by_id_only() {
        let p = fresh_prompt("abc123", "Login refactor");
        assert!(p.contains("abc123"));
        assert!(p.contains("\u{201c}Login refactor\u{201d}"));
        assert!(!fresh_prompt("abc123", "  ").contains('\u{201c}'));
    }

    #[test]
    fn dismissals_parse_defensively() {
        assert!(parse_dismissals("").is_empty());
        assert!(parse_dismissals("not json").is_empty());
        assert_eq!(parse_dismissals(r#"{"a":5}"#).get("a"), Some(&5));
    }
}
