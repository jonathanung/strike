//! Deploy watch verdict card (#1785, cockpit half of #1602).
//!
//! A deploy watch runs as an ordinary session. When the active session is a
//! watch session, this card shows the watch state and, once settled, its
//! single verdict with the per-check evidence. On a regression it offers the
//! two confirmed handoffs from #1784. The daemon (`/v1/deploy-watches`) is the
//! source of truth; the card only renders it.

use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::api::{get_json, post_json_value};

/// Refetch cadence in shell ticks (one tick per second).
const REFRESH_TICKS: u32 = 15;

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct CheckResult {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub detail: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Handoff {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default)]
    pub pr: i64,
    #[serde(default)]
    pub url: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct Watch {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub repo: String,
    #[serde(default)]
    pub pr: i64,
    #[serde(default)]
    pub sha: String,
    #[serde(default)]
    pub environment: String,
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub verdict: String,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub latest: Vec<CheckResult>,
    #[serde(default, rename = "failureThreshold")]
    pub failure_threshold: i64,
    #[serde(default, rename = "notifyError")]
    pub notify_error: String,
    #[serde(default, rename = "fixSession")]
    pub fix_session: Option<Handoff>,
    #[serde(default, rename = "revertPr")]
    pub revert_pr: Option<Handoff>,
}

#[derive(Clone, Deserialize)]
struct WatchList {
    #[serde(default)]
    watches: Vec<Watch>,
}

/// The watch whose session is `session`. The daemon lists newest first, so
/// the first match is the current watch for that session.
pub fn pick(watches: &[Watch], session: &str) -> Option<Watch> {
    let session = session.trim();
    if session.is_empty() {
        return None;
    }
    watches.iter().find(|w| w.session_id == session).cloned()
}

/// `(data-verdict key, badge text)`. A watching watch has no verdict yet;
/// a cancelled one is inconclusive, never healthy.
pub fn headline(w: &Watch) -> (&'static str, String) {
    match (w.status.as_str(), w.verdict.as_str()) {
        ("watching", _) | (_, "") => ("watching", "Watching".into()),
        (_, "healthy") => ("healthy", "Healthy".into()),
        (_, "regression") => ("regression", "Regression".into()),
        ("cancelled", _) => ("inconclusive", "Inconclusive · cancelled".into()),
        _ => ("inconclusive", "Inconclusive".into()),
    }
}

/// `owner/name @ sha12 · PR #n · env`.
pub fn target_line(w: &Watch) -> String {
    let sha: String = w.sha.chars().take(12).collect();
    let mut out = format!("{} @ {}", w.repo, sha);
    if w.pr > 0 {
        out.push_str(&format!(" · PR #{}", w.pr));
    }
    if !w.environment.is_empty() {
        out.push_str(" · ");
        out.push_str(&w.environment);
    }
    out
}

/// Handoffs are offered only on a regression verdict.
pub fn handoffs_allowed(w: &Watch) -> bool {
    w.verdict == "regression"
}

/// Short state word for one check row.
pub fn state_label(state: &str) -> &'static str {
    match state {
        "healthy" => "healthy",
        "failing" => "failing",
        "pending" => "pending",
        "unreachable" => "unreachable",
        _ => "unknown",
    }
}

/// Body every handoff POST sends: the daemon refuses anything unconfirmed.
pub fn handoff_body() -> Value {
    json!({ "confirm": true })
}

/// Read a handoff response; an error keeps the daemon's words.
pub fn parse_watch(value: Value) -> Result<Watch, String> {
    serde_json::from_value::<Watch>(value).map_err(|e| e.to_string())
}

fn refresh(
    base: RwSignal<String>,
    token: RwSignal<String>,
    session: String,
    watch: RwSignal<Option<Watch>>,
    note: RwSignal<String>,
) {
    let b = base.get_untracked();
    let t = token.get_untracked();
    spawn_local(async move {
        match get_json::<WatchList>(&b, &t, "/v1/deploy-watches").await {
            Ok(list) => watch.set(pick(&list.watches, &session)),
            // 503 means this server has no deploy watches; stay hidden.
            Err(e) if e.contains("503") => watch.set(None),
            Err(e) => note.set(format!("Deploy watch could not be loaded: {e}")),
        }
    });
}

#[component]
pub fn DeployWatchCard(
    base: RwSignal<String>,
    token: RwSignal<String>,
    active: RwSignal<String>,
    tick: RwSignal<u32>,
) -> impl IntoView {
    let watch = RwSignal::new(None::<Watch>);
    let note = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let last_session = RwSignal::new(String::new());

    Effect::new(move |_| {
        let session = active.get();
        let n = tick.get();
        let switched = session != last_session.get_untracked();
        if switched {
            last_session.set(session.clone());
            watch.set(None);
            note.set(String::new());
        }
        if switched || n % REFRESH_TICKS == 0 {
            refresh(base, token, session, watch, note);
        }
    });

    let handoff = move |path: &'static str, title: &'static str, body: &'static str, label: &'static str| {
        let Some(w) = watch.get_untracked() else {
            return;
        };
        if busy.get_untracked() {
            return;
        }
        let url = format!("/v1/deploy-watches/{}/{}", w.id, path);
        let opened = crate::confirm::ask(
            crate::confirm::ConfirmRequest {
                title: title.into(),
                body: body.into(),
                confirm_label: label.into(),
            },
            Rc::new(move || {
                let b = base.get_untracked();
                let t = token.get_untracked();
                let url = url.clone();
                busy.set(true);
                note.set(String::new());
                spawn_local(async move {
                    let res = post_json_value(&b, &t, &url, handoff_body()).await.and_then(parse_watch);
                    busy.set(false);
                    match res {
                        Ok(updated) => watch.set(Some(updated)),
                        Err(e) => note.set(e),
                    }
                });
            }),
        );
        if !opened {
            note.set(crate::confirm::BUSY.into());
        }
    };

    view! {
        {move || watch.get().map(|w| {
            let (key, badge) = headline(&w);
            let target = target_line(&w);
            let reason = w.reason.clone();
            let notify = w.notify_error.clone();
            let threshold = if w.failure_threshold > 0 { w.failure_threshold } else { 3 };
            let rows = w.latest.clone();
            let fix = w.fix_session.clone();
            let revert = w.revert_pr.clone();
            let show_actions = handoffs_allowed(&w);
            view! {
                <section class="deploy-watch-card" data-verdict=key aria-label="Deploy watch" aria-live="polite">
                    <header class="dw-head">
                        <span class="dw-badge">{badge}</span>
                        <span class="dw-target">{target}</span>
                    </header>
                    {(!reason.is_empty()).then(|| view! { <p class="dw-reason">{reason}</p> })}
                    {(key == "watching").then(|| view! {
                        <p class="dw-hint">{format!("A check counts as a regression after {threshold} consecutive failures. Inconclusive is never a pass.")}</p>
                    })}
                    {(!rows.is_empty()).then(|| view! {
                        <ul class="dw-checks">
                            {rows.into_iter().map(|r| view! {
                                <li data-state=state_label(&r.state)>
                                    <span class="dw-check-name">{r.name}</span>
                                    <span class="dw-check-state">{state_label(&r.state)}</span>
                                    <span class="dw-check-detail">{r.detail}</span>
                                </li>
                            }).collect_view()}
                        </ul>
                    })}
                    {(!notify.is_empty()).then(|| view! {
                        <p class="dw-notify" role="status">{format!("Posting to the PR or session failed: {notify}")}</p>
                    })}
                    {show_actions.then(|| view! {
                        <div class="dw-actions">
                            {match fix {
                                Some(h) => view! { <span class="dw-done">{format!("Fix session started: {}", h.session_id)}</span> }.into_any(),
                                None => view! {
                                    <button type="button" class="primary" data-dw-action="fix"
                                        prop:disabled=move || busy.get()
                                        on:click=move |_| handoff(
                                            "fix-session",
                                            "Open fix session?",
                                            "Starts a new session seeded with this regression's evidence. It works under the normal approval gates and does not merge anything.",
                                            "Open fix session",
                                        )>"Open fix session"</button>
                                }.into_any(),
                            }}
                            {match revert {
                                Some(h) if !h.url.is_empty() => view! {
                                    <a class="dw-done" href=h.url.clone() target="_blank" rel="noopener noreferrer">{format!("Draft revert PR #{}", h.pr)}</a>
                                }.into_any(),
                                Some(h) => view! { <span class="dw-done">{format!("Draft revert PR #{}", h.pr)}</span> }.into_any(),
                                None => view! {
                                    <button type="button" class="secondary" data-dw-action="revert"
                                        prop:disabled=move || busy.get()
                                        on:click=move |_| handoff(
                                            "revert-pr",
                                            "Draft a revert PR?",
                                            "Reverts the watched commit on a strike/revert branch and opens a draft PR. It goes through the repository's normal review and CI gates; Strike never merges it.",
                                            "Draft revert PR",
                                        )>"Draft revert PR"</button>
                                }.into_any(),
                            }}
                        </div>
                    })}
                    {move || {
                        let msg = note.get();
                        (!msg.is_empty()).then(|| view! { <p class="dw-error" role="alert">{msg}</p> })
                    }}
                </section>
            }
        })}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn watch(status: &str, verdict: &str) -> Watch {
        Watch {
            id: "dw_1".into(),
            repo: "acme/app".into(),
            sha: "0123456789abcdef0123".into(),
            session_id: "s1".into(),
            status: status.into(),
            verdict: verdict.into(),
            ..Watch::default()
        }
    }

    #[test]
    fn pick_matches_session_newest_first() {
        let mut older = watch("done", "healthy");
        older.id = "dw_old".into();
        let newer = watch("watching", "");
        let other = Watch { session_id: "s2".into(), ..watch("done", "regression") };
        let list = vec![newer.clone(), older, other];
        assert_eq!(pick(&list, "s1").map(|w| w.id), Some(newer.id));
        assert!(pick(&list, "nope").is_none());
        assert!(pick(&list, "  ").is_none());
    }

    #[test]
    fn headline_never_calls_unsettled_healthy() {
        assert_eq!(headline(&watch("watching", "")).0, "watching");
        assert_eq!(headline(&watch("done", "healthy")).0, "healthy");
        assert_eq!(headline(&watch("done", "regression")).0, "regression");
        assert_eq!(headline(&watch("done", "inconclusive")).0, "inconclusive");
        let (key, label) = headline(&watch("cancelled", "inconclusive"));
        assert_eq!(key, "inconclusive");
        assert!(label.contains("cancelled"));
    }

    #[test]
    fn target_line_and_handoffs() {
        let mut w = watch("done", "regression");
        assert_eq!(target_line(&w), "acme/app @ 0123456789ab");
        w.pr = 42;
        w.environment = "prod".into();
        assert_eq!(target_line(&w), "acme/app @ 0123456789ab · PR #42 · prod");
        assert!(handoffs_allowed(&w));
        assert!(!handoffs_allowed(&watch("done", "inconclusive")));
        assert!(!handoffs_allowed(&watch("watching", "")));
        assert_eq!(handoff_body(), json!({"confirm": true}));
    }

    #[test]
    fn parses_daemon_watch_json() {
        let v = json!({
            "id": "dw_9", "repo": "acme/app", "sha": "abc1234", "sessionId": "s9",
            "status": "done", "verdict": "regression", "reason": "failing: api (3 consecutive)",
            "failureThreshold": 3, "notifyError": "",
            "latest": [{"name": "api", "kind": "http", "state": "failing", "detail": "status 502, want 200", "at": "2026-11-20T00:00:00Z"}],
            "fixSession": {"at": "2026-11-20T00:00:00Z", "sessionId": "fix-1"},
            "rounds": []
        });
        let w = parse_watch(v).expect("parse");
        assert_eq!(w.latest[0].state, "failing");
        assert_eq!(w.fix_session.map(|h| h.session_id), Some("fix-1".to_string()));
        assert!(w.revert_pr.is_none());
        assert_eq!(state_label("weird"), "unknown");
    }
}
