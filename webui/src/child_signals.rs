//! Child signals (#1375): a child tripping a budget (`child.escalated`) and
//! what agents say to each other (`agent.message`).
//!
//! Escalations reach the cockpit two ways, both daemon-sourced: the
//! transcript line from the parent stream (live and replay), and the
//! member row's `escalation` in the polled `/v1/team` snapshot. Messages
//! come only from the snapshot's bounded `messages` (the daemon already
//! aggregates and redacts them). The feed is summary-first and bounded:
//! an awareness feed, not a second transcript. Bodies are never stored
//! client-side beyond the snapshot the poll already holds.

use std::collections::HashMap;

use leptos::prelude::*;

use crate::model::{TeamEscalation, TeamMessage};

/// Messages shown before "Show all".
pub const FEED_LIMIT: usize = 5;
/// Summary length when a message carries no summary and the body is used.
const SUMMARY_MAX: usize = 120;

/// Plain label for an escalation kind (protocol: wall_clock|tokens|
/// cost_usd|tool_calls|dangerous_tools|stall|loop).
pub fn kind_label(kind: &str) -> String {
    match kind {
        "wall_clock" => "Time budget".into(),
        "tokens" => "Token budget".into(),
        "cost_usd" => "Cost budget".into(),
        "tool_calls" => "Tool-call budget".into(),
        "dangerous_tools" => "Risky-tool budget".into(),
        "stall" => "Stalled".into(),
        "loop" => "Loop detected".into(),
        "" => "Escalated".into(),
        other => other.replace('_', " "),
    }
}

/// What the engine did about the trip (protocol: interrupted|finalizing|
/// signaled).
pub fn action_label(action: &str) -> String {
    match action {
        "interrupted" => "stopped".into(),
        "finalizing" => "finishing with a handoff".into(),
        "signaled" => "flagged, still running".into(),
        "" => "no action reported".into(),
        other => other.replace('_', " "),
    }
}

/// One-sentence escalation: "Stalled: no progress for 300s. Action:
/// finishing with a handoff."
pub fn escalation_sentence(kind: &str, reason: &str, action: &str) -> String {
    let reason = reason.trim().trim_end_matches('.');
    let head = kind_label(kind);
    if reason.is_empty() {
        format!("{head}. Action: {}.", action_label(action))
    } else {
        format!("{head}: {reason}. Action: {}.", action_label(action))
    }
}

pub fn escalation_line(esc: &TeamEscalation) -> String {
    escalation_sentence(&esc.kind, &esc.reason, &esc.action)
}

/// One feed row, display-ready.
#[derive(Clone, Debug, PartialEq)]
pub struct FeedRow {
    pub key: String,
    pub from: String,
    pub to: String,
    pub summary: String,
    pub body: String,
    pub urgency: String,
    pub kind: String,
}

impl FeedRow {
    /// True when expanding would show more than the summary.
    pub fn has_more(&self) -> bool {
        !self.body.trim().is_empty() && self.body.trim() != self.summary
    }
}

fn clip(text: &str, max: usize) -> String {
    let flat = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() <= max {
        return flat;
    }
    let mut short: String = flat.chars().take(max.saturating_sub(1)).collect();
    short.push('…');
    short
}

/// Who a session id is, for the feed: the lead, a member's name, or a
/// short id.
pub fn who(id: &str, names: &HashMap<String, String>, lead_id: &str) -> String {
    let id = id.trim();
    if id.is_empty() {
        return "unknown".into();
    }
    if !lead_id.is_empty() && id == lead_id {
        return "Lead".into();
    }
    match names.get(id) {
        Some(n) if !n.trim().is_empty() => n.clone(),
        _ => id.chars().take(8).collect(),
    }
}

/// Newest-first feed rows. `limit` 0 means all.
pub fn feed_rows(
    messages: &[TeamMessage],
    names: &HashMap<String, String>,
    lead_id: &str,
    limit: usize,
) -> Vec<FeedRow> {
    let take = if limit == 0 { messages.len() } else { limit };
    messages
        .iter()
        .enumerate()
        .rev()
        .take(take)
        .map(|(i, m)| {
            let summary = if m.summary.trim().is_empty() {
                clip(&m.body, SUMMARY_MAX)
            } else {
                m.summary.trim().to_string()
            };
            let key = if m.message_id.is_empty() {
                format!("idx-{i}")
            } else {
                m.message_id.clone()
            };
            FeedRow {
                key,
                from: who(&m.from, names, lead_id),
                to: who(&m.to, names, lead_id),
                summary,
                body: m.body.clone(),
                urgency: if m.urgency.is_empty() {
                    "normal".into()
                } else {
                    m.urgency.clone()
                },
                kind: if m.kind.is_empty() {
                    "message".into()
                } else {
                    m.kind.clone()
                },
            }
        })
        .collect()
}

/// Recent inter-agent messages for the Activity panel. Renders nothing
/// when the team has exchanged none.
#[component]
pub fn TeamMessagesFeed(
    messages: RwSignal<Vec<TeamMessage>>,
    names: RwSignal<HashMap<String, String>>,
    lead_id: RwSignal<String>,
) -> impl IntoView {
    let show_all = RwSignal::new(false);
    let open = RwSignal::new(Vec::<String>::new());
    let total = move || messages.with(|m| m.len());
    // Outside view!: a bare `>` in an attribute closes the tag.
    let many = move || total() > FEED_LIMIT;
    let rows = move || {
        let limit = if show_all.get() { 0 } else { FEED_LIMIT };
        messages.with(|m| names.with(|n| feed_rows(m, n, &lead_id.get(), limit)))
    };
    view! {
        <Show when=move || total() != 0>
            <section class="agent-feed" data-agent-feed="true" aria-label="Agent messages">
                <header class="agent-feed-head">
                    <span class="hint">"Agent messages"</span>
                    <Show when=many>
                        <button
                            type="button"
                            class="secondary"
                            data-agent-feed-more="true"
                            aria-expanded=move || show_all.get().to_string()
                            on:click=move |_| show_all.update(|v| *v = !*v)
                        >
                            {move || if show_all.get() {
                                "Show recent".to_string()
                            } else {
                                format!("Show all {}", total())
                            }}
                        </button>
                    </Show>
                </header>
                <ul class="plain agent-feed-list">
                    {move || rows().into_iter().map(|row| {
                        let key = row.key.clone();
                        let key_toggle = key.clone();
                        let key_open = key.clone();
                        let more = row.has_more();
                        let body = row.body.clone();
                        let urgent = row.urgency != "normal";
                        let urgency = row.urgency.clone();
                        let urgency_attr = row.urgency.clone();
                        let kind = row.kind.clone();
                        let show_kind = kind != "message";
                        let is_open = Memo::new(move |_| open.with(|o| o.contains(&key_open)));
                        view! {
                            <li class="agent-msg" data-agent-msg=key data-urgency=urgency_attr>
                                <div class="agent-msg-line">
                                    <span class="agent-msg-route">{format!("{} → {}", row.from, row.to)}</span>
                                    <Show when=move || urgent>
                                        <span class="agent-msg-tag urgent">{urgency.clone()}</span>
                                    </Show>
                                    <Show when=move || show_kind>
                                        <span class="agent-msg-tag">{kind.clone()}</span>
                                    </Show>
                                </div>
                                <p class="agent-msg-summary">{row.summary.clone()}</p>
                                <Show when=move || more>
                                    <button
                                        type="button"
                                        class="link agent-msg-toggle"
                                        aria-expanded=move || is_open.get().to_string()
                                        on:click={
                                            let key_toggle = key_toggle.clone();
                                            move |_| {
                                                let k = key_toggle.clone();
                                                open.update(|o| {
                                                    if let Some(i) = o.iter().position(|x| *x == k) {
                                                        o.remove(i);
                                                    } else {
                                                        o.push(k);
                                                    }
                                                });
                                            }
                                        }
                                    >
                                        {move || if is_open.get() { "Hide message" } else { "Show message" }}
                                    </button>
                                </Show>
                                <Show when=move || more && is_open.get()>
                                    <pre class="agent-msg-body">{body.clone()}</pre>
                                </Show>
                            </li>
                        }
                    }).collect_view()}
                </ul>
            </section>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn msg(id: &str, from: &str, to: &str, summary: &str, body: &str) -> TeamMessage {
        TeamMessage {
            message_id: id.into(),
            from: from.into(),
            to: to.into(),
            summary: summary.into(),
            body: body.into(),
            urgency: String::new(),
            kind: String::new(),
            task_id: String::new(),
        }
    }

    fn names() -> HashMap<String, String> {
        HashMap::from([
            ("c1".to_string(), "scout".to_string()),
            ("c2".to_string(), "fixer".to_string()),
        ])
    }

    #[test]
    fn escalation_sentences() {
        let cases = [
            ("stall", "no progress for 300s.", "finalizing", "Stalled: no progress for 300s. Action: finishing with a handoff."),
            ("tokens", "used 200000 of 200000", "interrupted", "Token budget: used 200000 of 200000. Action: stopped."),
            ("loop", "", "signaled", "Loop detected. Action: flagged, still running."),
            ("new_kind", "x", "", "new kind: x. Action: no action reported."),
        ];
        for (kind, reason, action, want) in cases {
            assert_eq!(escalation_sentence(kind, reason, action), want);
        }
    }

    #[test]
    fn feed_is_newest_first_and_bounded() {
        let msgs: Vec<TeamMessage> = (0..8)
            .map(|i| msg(&format!("m{i}"), "c1", "c2", &format!("s{i}"), ""))
            .collect();
        let rows = feed_rows(&msgs, &names(), "lead", FEED_LIMIT);
        assert_eq!(rows.len(), FEED_LIMIT);
        assert_eq!(rows[0].key, "m7");
        assert_eq!(rows[4].key, "m3");
        assert_eq!(feed_rows(&msgs, &names(), "lead", 0).len(), 8);
    }

    #[test]
    fn feed_resolves_names_and_lead() {
        let msgs = vec![
            msg("a", "lead", "c1", "go", ""),
            msg("b", "c2", "unknown-session-id", "hi", ""),
        ];
        let rows = feed_rows(&msgs, &names(), "lead", 0);
        assert_eq!((rows[1].from.as_str(), rows[1].to.as_str()), ("Lead", "scout"));
        assert_eq!((rows[0].from.as_str(), rows[0].to.as_str()), ("fixer", "unknown-"));
    }

    #[test]
    fn feed_is_summary_first() {
        let long = "word ".repeat(60);
        let msgs = vec![
            msg("a", "c1", "c2", "blocked on schema", "full body text"),
            msg("b", "c1", "c2", "", &long),
            msg("c", "c1", "c2", "", "short"),
        ];
        let rows = feed_rows(&msgs, &names(), "", 0);
        let a = rows.iter().find(|r| r.key == "a").unwrap();
        assert_eq!(a.summary, "blocked on schema");
        assert!(a.has_more());
        let b = rows.iter().find(|r| r.key == "b").unwrap();
        assert!(b.summary.ends_with('…'));
        assert!(b.summary.chars().count() <= SUMMARY_MAX);
        assert!(b.has_more());
        let c = rows.iter().find(|r| r.key == "c").unwrap();
        assert_eq!(c.summary, "short");
        assert!(!c.has_more());
    }

    #[test]
    fn feed_defaults_urgency_and_kind() {
        let rows = feed_rows(&[msg("", "c1", "c2", "s", "")], &names(), "", 0);
        assert_eq!(rows[0].urgency, "normal");
        assert_eq!(rows[0].kind, "message");
        assert_eq!(rows[0].key, "idx-0");
    }
}
