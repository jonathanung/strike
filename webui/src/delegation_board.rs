//! Delegation board (#1381): the delegations the daemon already aggregates
//! into every `/v1/team` reply (`TeamSnapshot.delegations`), rendered
//! read-only in the Activity tab.
//!
//! Members are sessions that exist; delegations are the work. A queued
//! delegation has no session yet, so it shows here and nowhere else.
//! Ordering is by state urgency — review and blocked first (they wait on
//! the lead or the human), then working, queued, then terminal — and by
//! name within a state, since the snapshot carries no timestamp. No
//! transitions or controls in this slice.

use leptos::prelude::*;

use crate::model::{Delegation, TeamSnapshot};
use crate::status::{StatusKind, StatusMark, StatusSlot};

/// One display-ready board row.
#[derive(Clone, Debug, PartialEq)]
pub struct BoardRow {
    pub id: String,
    pub name: String,
    pub state: String,
    pub session: Option<String>,
    pub reason: String,
}

/// Urgency rank of a delegation state; lower sorts first.
pub fn state_rank(state: &str) -> u8 {
    match state {
        "review" => 0,
        "blocked" => 1,
        "working" => 2,
        "queued" => 3,
        "done" => 4,
        "failed" => 5,
        "canceled" => 6,
        _ => 7,
    }
}

/// Status tone and label for a delegation state, reusing the shared
/// status vocabulary where it maps.
pub fn state_mark(state: &str) -> (StatusKind, String) {
    match state {
        "queued" => (StatusKind::Idle, "Queued".into()),
        "working" => (StatusKind::Running, "Working".into()),
        "blocked" => (StatusKind::Blocked, "Blocked".into()),
        "review" => (StatusKind::Waiting, "Review".into()),
        "done" => (StatusKind::Done, "Done".into()),
        "failed" => (StatusKind::Failed, "Failed".into()),
        "canceled" => (StatusKind::Stopped, "Canceled".into()),
        "" => (StatusKind::Idle, "Unknown".into()),
        other => (StatusKind::Idle, other.to_string()),
    }
}

fn session_name(team: &TeamSnapshot, sid: &str) -> String {
    let named = team.members.iter().find_map(|(key, m)| {
        let id = if m.session_id.is_empty() { key } else { &m.session_id };
        (id == sid && !m.name.trim().is_empty()).then(|| m.name.clone())
    });
    named.unwrap_or_else(|| sid.chars().take(8).collect())
}

fn row(team: &TeamSnapshot, key: &str, d: &Delegation) -> BoardRow {
    let id = if d.id.is_empty() { key.to_string() } else { d.id.clone() };
    let name = if d.name.trim().is_empty() {
        id.clone()
    } else {
        d.name.trim().to_string()
    };
    let sid = d.session_id.trim();
    BoardRow {
        id,
        name,
        state: d.state.trim().to_ascii_lowercase(),
        session: (!sid.is_empty()).then(|| session_name(team, sid)),
        reason: d.reason.trim().to_string(),
    }
}

/// Board rows from one team snapshot, in urgency order.
pub fn board_rows(team: &TeamSnapshot) -> Vec<BoardRow> {
    let mut rows: Vec<BoardRow> = team
        .delegations
        .iter()
        .map(|(key, d)| row(team, key, d))
        .collect();
    rows.sort_by(|a, b| {
        state_rank(&a.state)
            .cmp(&state_rank(&b.state))
            .then_with(|| a.name.cmp(&b.name))
            .then_with(|| a.id.cmp(&b.id))
    });
    rows
}

/// Delegations section of the Activity tab. Renders nothing when there are
/// no delegations, so single-session use looks as before.
#[component]
pub fn DelegationBoard(rows: RwSignal<Vec<BoardRow>>) -> impl IntoView {
    view! {
        <Show when=move || rows.with(|r| !r.is_empty())>
            <section class="delegation-board" data-delegation-board="true" aria-label="Delegations">
                <header class="delegation-board-head">
                    <span class="hint">"Delegations"</span>
                    <span class="hint">{move || rows.with(|r| r.len()).to_string()}</span>
                </header>
                <ul class="plain delegation-list">
                    {move || rows.get().into_iter().map(|r| {
                        let (kind, label) = state_mark(&r.state);
                        let has_reason = !r.reason.is_empty();
                        let reason = r.reason.clone();
                        let linked = r.session.is_some();
                        let session = r.session.clone().unwrap_or_default();
                        view! {
                            <li class="delegation-row" data-delegation=r.id.clone() data-state=r.state.clone()>
                                <span class="delegation-line">
                                    <span class="delegation-name">{r.name.clone()}</span>
                                    <StatusMark kind=kind place=StatusSlot::Badge label=label />
                                </span>
                                <span class="delegation-meta">
                                    {if linked {
                                        view! { <span class="delegation-session">{session}</span> }.into_any()
                                    } else {
                                        view! { <span class="delegation-session none" data-no-session="true">"No session yet"</span> }.into_any()
                                    }}
                                </span>
                                <Show when=move || has_reason>
                                    <span class="delegation-reason">{reason.clone()}</span>
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

    fn snap(raw: &str) -> TeamSnapshot {
        serde_json::from_str(raw).unwrap()
    }

    #[test]
    fn queued_delegation_without_session() {
        let team = snap(
            r#"{"available":true,"members":{},"delegations":{"d1":{"id":"d1","state":"queued","name":"write tests"}}}"#,
        );
        let rows = board_rows(&team);
        assert_eq!(
            rows,
            vec![BoardRow {
                id: "d1".into(),
                name: "write tests".into(),
                state: "queued".into(),
                session: None,
                reason: String::new(),
            }]
        );
    }

    #[test]
    fn linked_session_resolves_to_member_name() {
        let team = snap(
            r#"{"available":true,
                "members":{"c1":{"sessionId":"c1","name":"scout"}},
                "delegations":{
                    "d1":{"id":"d1","state":"working","sessionId":"c1","name":"survey"},
                    "d2":{"id":"d2","state":"working","sessionId":"abcdef123456","name":"other"}
                }}"#,
        );
        let rows = board_rows(&team);
        let d1 = rows.iter().find(|r| r.id == "d1").unwrap();
        assert_eq!(d1.session.as_deref(), Some("scout"));
        let d2 = rows.iter().find(|r| r.id == "d2").unwrap();
        assert_eq!(d2.session.as_deref(), Some("abcdef12"));
    }

    #[test]
    fn rows_sort_by_urgency_then_name() {
        let team = snap(
            r#"{"available":true,"members":{},"delegations":{
                "a":{"id":"a","state":"done","name":"a"},
                "b":{"id":"b","state":"queued","name":"b"},
                "c":{"id":"c","state":"review","name":"c"},
                "d":{"id":"d","state":"working","name":"d"},
                "e":{"id":"e","state":"blocked","name":"e"},
                "f":{"id":"f","state":"canceled","name":"f"},
                "g":{"id":"g","state":"failed","name":"g"},
                "h":{"id":"h","state":"working","name":"a-first"}
            }}"#,
        );
        let order: Vec<String> = board_rows(&team).into_iter().map(|r| r.id).collect();
        assert_eq!(order, vec!["c", "e", "h", "d", "b", "a", "g", "f"]);
    }

    #[test]
    fn name_and_id_fallbacks() {
        let team = snap(
            r#"{"available":true,"members":{},"delegations":{"key-1":{"state":"Review","reason":" waiting on lead "}}}"#,
        );
        let r = &board_rows(&team)[0];
        assert_eq!(r.id, "key-1");
        assert_eq!(r.name, "key-1");
        assert_eq!(r.state, "review");
        assert_eq!(r.reason, "waiting on lead");
    }

    #[test]
    fn empty_and_null_delegations() {
        for raw in [
            r#"{"available":true,"members":{}}"#,
            r#"{"available":true,"members":{},"delegations":null}"#,
        ] {
            assert!(board_rows(&snap(raw)).is_empty(), "{raw}");
        }
    }

    #[test]
    fn marks_reuse_status_vocabulary() {
        let cases = [
            ("queued", StatusKind::Idle, "Queued"),
            ("working", StatusKind::Running, "Working"),
            ("blocked", StatusKind::Blocked, "Blocked"),
            ("review", StatusKind::Waiting, "Review"),
            ("done", StatusKind::Done, "Done"),
            ("failed", StatusKind::Failed, "Failed"),
            ("canceled", StatusKind::Stopped, "Canceled"),
        ];
        for (state, kind, label) in cases {
            assert_eq!(state_mark(state), (kind, label.to_string()), "{state}");
        }
    }
}
