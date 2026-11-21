//! PR watch (#1356): the session's linked pull request and its CI checks,
//! with a user-confirmed "Fix in this session" handoff.
//!
//! Data comes from `GET /v1/sessions/{id}/pr`; the daemon runs the host's
//! own `gh` (no token ever reaches the browser). "Fix in this session"
//! fetches a draft from `POST /v1/sessions/{id}/pr/fix-draft` (no side
//! effects), shows it, and only after the user confirms posts it as a
//! normal `user.input` op into that session. No automatic fix turns, no
//! push, no merge.
//!
//! Polling: while the cockpit has a session open and its PR is open, ask
//! again after the daemon's `pollAfterSec` (60s). Stop on merged/closed and
//! on errors retrying cannot fix (gh signed out, no GitHub remote). Back off
//! on transport failures. The daemon caches polls for 30s, so several tabs
//! do not multiply `gh` calls.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, js_encode, post_json_value, post_op};

pub const POLL_MS_DEFAULT: u32 = 60_000;
pub const POLL_MS_MIN: u32 = 30_000;
pub const BACKOFF_MAX_MS: u32 = 600_000;

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct PrCheck {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub url: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct PrStatus {
    #[serde(default, rename = "sessionId")]
    pub session_id: String,
    #[serde(default)]
    pub linked: bool,
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub number: i64,
    #[serde(default)]
    pub state: String,
    #[serde(default, rename = "reviewDecision")]
    pub review_decision: String,
    #[serde(default)]
    pub checks: Vec<PrCheck>,
    #[serde(default)]
    pub rollup: String,
    #[serde(default)]
    pub attention: bool,
    #[serde(default)]
    pub poll: bool,
    #[serde(default, rename = "pollAfterSec")]
    pub poll_after_sec: u32,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct FixDraft {
    #[serde(default)]
    pub text: String,
}

/// Delay before the next poll, or None to stop. Transport failures back off
/// (120s, 240s, 480s, then 600s); otherwise the daemon decides via `poll`.
pub fn next_poll_ms(status: Option<&PrStatus>, transport_failures: u32) -> Option<u32> {
    if transport_failures > 0 {
        let shift = transport_failures.min(4);
        return Some(POLL_MS_DEFAULT.saturating_mul(1 << shift).min(BACKOFF_MAX_MS));
    }
    let s = status?;
    if !s.linked || !s.poll {
        return None;
    }
    let ms = if s.poll_after_sec > 0 {
        s.poll_after_sec.saturating_mul(1000)
    } else {
        POLL_MS_DEFAULT
    };
    Some(ms.clamp(POLL_MS_MIN, BACKOFF_MAX_MS))
}

fn pr_name(s: &PrStatus) -> String {
    if s.number > 0 {
        format!("PR #{}", s.number)
    } else {
        "PR".into()
    }
}

/// One-line chip text.
pub fn chip_label(s: &PrStatus) -> String {
    let name = pr_name(s);
    if !s.available {
        return format!("{name} · tracking unavailable");
    }
    match s.state.as_str() {
        "merged" => format!("{name} · merged"),
        "closed" => format!("{name} · closed"),
        _ => {
            let checks = match s.rollup.as_str() {
                "pass" => "checks passing",
                "fail" => "checks failing",
                "running" => "checks running",
                "queued" => "checks queued",
                _ => "no checks",
            };
            if s.review_decision == "changes_requested" {
                format!("{name} · {checks} · changes requested")
            } else {
                format!("{name} · {checks}")
            }
        }
    }
}

/// Chip tone, used as `data-pr-tone` and a CSS modifier.
pub fn chip_tone(s: &PrStatus) -> &'static str {
    if !s.available {
        return "unavailable";
    }
    if s.attention {
        return "fail";
    }
    match s.state.as_str() {
        "merged" => "merged",
        "closed" => "closed",
        _ => match s.rollup.as_str() {
            "pass" => "pass",
            "running" | "queued" => "running",
            _ => "neutral",
        },
    }
}

/// Short state word for a check row (text, not colour alone).
pub fn check_word(state: &str) -> &'static str {
    match state {
        "pass" => "passed",
        "fail" => "failed",
        "running" => "running",
        "queued" => "queued",
        "skipped" => "skipped",
        _ => "unknown",
    }
}

/// "Fix in this session" flow. Text is posted only on `Confirm` while a
/// draft is being shown; every other path posts nothing.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum FixStep {
    Idle,
    Drafting,
    Confirming(String),
    Sending,
    Sent,
    Failed(String),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum FixEvent {
    Start,
    Drafted(String),
    DraftFailed(String),
    Confirm,
    Cancel,
    SendOk,
    SendFailed(String),
}

/// Pure transition. The second value is the message to post, and is Some
/// only for `Confirm` from `Confirming` with a non-empty draft.
pub fn fix_step(step: &FixStep, ev: FixEvent) -> (FixStep, Option<String>) {
    match (step, ev) {
        (FixStep::Idle | FixStep::Sent | FixStep::Failed(_), FixEvent::Start) => {
            (FixStep::Drafting, None)
        }
        (FixStep::Drafting, FixEvent::Drafted(text)) => {
            if text.trim().is_empty() {
                (FixStep::Failed("The daemon returned an empty draft.".into()), None)
            } else {
                (FixStep::Confirming(text), None)
            }
        }
        (FixStep::Drafting, FixEvent::DraftFailed(err)) => (FixStep::Failed(err), None),
        (FixStep::Confirming(text), FixEvent::Confirm) => (FixStep::Sending, Some(text.clone())),
        (FixStep::Confirming(_) | FixStep::Failed(_) | FixStep::Sent, FixEvent::Cancel) => {
            (FixStep::Idle, None)
        }
        (FixStep::Sending, FixEvent::SendOk) => (FixStep::Sent, None),
        (FixStep::Sending, FixEvent::SendFailed(err)) => (FixStep::Failed(err), None),
        (other, _) => (other.clone(), None),
    }
}

/// The daemon's `{"error":...}` text from a transport error string.
fn daemon_error(err: &str) -> String {
    if let Some(idx) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&err[idx..]) {
            if let Some(msg) = v.get("error").and_then(|e| e.as_str()) {
                if !msg.is_empty() {
                    return msg.to_string();
                }
            }
        }
    }
    err.to_string()
}

fn later(ms: u32, f: impl FnOnce() + 'static) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(f);
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        ms.min(i32::MAX as u32) as i32,
    );
    cb.forget();
}

#[derive(Clone, Copy)]
struct Poller {
    base: RwSignal<String>,
    token: RwSignal<String>,
    session: RwSignal<String>,
    status: RwSignal<Option<PrStatus>>,
    failures: RwSignal<u32>,
    gen: RwSignal<u64>,
}

/// One poll for generation `my_gen`; reschedules itself while the daemon
/// says to keep polling. A newer generation (session switch) or a disposed
/// component ends the chain.
fn poll_once(p: Poller, my_gen: u64, refresh: bool) {
    if p.gen.try_get_untracked() != Some(my_gen) {
        return;
    }
    let sid = p.session.get_untracked();
    if sid.is_empty() {
        return;
    }
    let (b, t) = (p.base.get_untracked(), p.token.get_untracked());
    spawn_local(async move {
        let mut path = format!("/v1/sessions/{}/pr", js_encode(&sid));
        if refresh {
            path.push_str("?refresh=1");
        }
        let res = get_json::<PrStatus>(&b, &t, &path).await;
        if p.gen.try_get_untracked() != Some(my_gen) {
            return;
        }
        match res {
            Ok(st) => {
                p.failures.set(0);
                p.status.set(Some(st));
            }
            Err(_) => p.failures.update(|n| *n = n.saturating_add(1)),
        }
        let fails = p.failures.get_untracked();
        let delay = p.status.with_untracked(|s| next_poll_ms(s.as_ref(), fails));
        if let Some(ms) = delay {
            later(ms, move || poll_once(p, my_gen, false));
        }
    });
}

#[component]
pub fn PrWatch(
    base: RwSignal<String>,
    token: RwSignal<String>,
    /// The open (live) session; "Fix in this session" posts here.
    session: RwSignal<String>,
    /// Git tab visibility. Opening the tab re-polls once (the daemon cache
    /// absorbs repeats), so a PR linked since the last poll shows up.
    active: Memo<bool>,
) -> impl IntoView {
    let status = RwSignal::new(None::<PrStatus>);
    let failures = RwSignal::new(0u32);
    let gen = RwSignal::new(0u64);
    let expanded = RwSignal::new(false);
    let fix = RwSignal::new(FixStep::Idle);
    let poller = Poller {
        base,
        token,
        session,
        status,
        failures,
        gen,
    };

    // Session switch: drop the old session's state and start a new chain.
    Effect::new(move |_| {
        let sid = session.get();
        let g = gen.get_untracked().wrapping_add(1);
        gen.set(g);
        status.set(None);
        failures.set(0);
        expanded.set(false);
        fix.set(FixStep::Idle);
        if !sid.is_empty() {
            poll_once(poller, g, false);
        }
    });
    // Tab opened: restart the chain from a fresh poll, keeping what is shown.
    Effect::new(move |prev: Option<bool>| {
        let on = active.get();
        if on && prev == Some(false) {
            let g = gen.get_untracked().wrapping_add(1);
            gen.set(g);
            failures.set(0);
            poll_once(poller, g, false);
        }
        on
    });

    let refresh = move || {
        let g = gen.get_untracked().wrapping_add(1);
        gen.set(g);
        failures.set(0);
        poll_once(poller, g, true);
    };

    let start_fix = move || {
        let (next, _) = fix_step(&fix.get_untracked(), FixEvent::Start);
        if next != FixStep::Drafting {
            return;
        }
        fix.set(next);
        let sid = session.get_untracked();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/sessions/{}/pr/fix-draft", js_encode(&sid));
            let ev = match post_json_value(&b, &t, &path, serde_json::json!({})).await {
                Ok(v) => match serde_json::from_value::<FixDraft>(v) {
                    Ok(d) => FixEvent::Drafted(d.text),
                    Err(e) => FixEvent::DraftFailed(format!("Could not read the draft: {e}")),
                },
                Err(e) => FixEvent::DraftFailed(daemon_error(&e)),
            };
            if session.try_get_untracked().as_deref() != Some(sid.as_str()) {
                return;
            }
            let (next, _) = fix_step(&fix.get_untracked(), ev);
            fix.set(next);
        });
    };

    let confirm_fix = move || {
        let (next, post) = fix_step(&fix.get_untracked(), FixEvent::Confirm);
        let Some(text) = post else {
            return;
        };
        fix.set(next);
        let sid = session.get_untracked();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let ev = match post_op(&b, &t, &sid, "user.input", serde_json::json!({ "text": text })).await {
                Ok(()) => FixEvent::SendOk,
                Err(e) => FixEvent::SendFailed(format!(
                    "Could not post to the session: {}",
                    daemon_error(&e)
                )),
            };
            if fix.try_get_untracked().is_none() {
                return;
            }
            let (next, _) = fix_step(&fix.get_untracked(), ev);
            fix.set(next);
        });
    };

    let cancel_fix = move || {
        let (next, _) = fix_step(&fix.get_untracked(), FixEvent::Cancel);
        fix.set(next);
    };

    move || {
        let Some(s) = status.get() else {
            return ().into_any();
        };
        if !s.linked {
            return ().into_any();
        }
        let tone = chip_tone(&s);
        let label = chip_label(&s);
        let url = s.url.clone();
        let has_url = url.starts_with("https://github.com/");
        let attention = s.attention;
        let available = s.available;
        let reason = s.reason.clone();
        let checks = s.checks.clone();
        let review = s.review_decision == "changes_requested";
        let polling = s.poll;
        view! {
            <section class="pr-watch" data-pr-watch="" data-pr-tone=tone aria-label="Pull request">
                <div class="pr-watch-head">
                    <button
                        type="button"
                        class=format!("pr-chip pr-chip-{tone}")
                        data-pr-chip=""
                        aria-expanded=move || expanded.get().to_string()
                        on:click=move |_| expanded.update(|v| *v = !*v)
                    >{label}</button>
                    {has_url.then(|| view! {
                        <a class="pr-link" href=url.clone() target="_blank" rel="noopener noreferrer">"Open on GitHub"</a>
                    })}
                    <button type="button" class="secondary pr-refresh" data-pr-refresh="" on:click=move |_| refresh()>"Refresh"</button>
                </div>
                {(!available).then(|| view! {
                    <p class="pr-reason" role="status" data-pr-unavailable="">{reason.clone()}</p>
                })}
                {(available && !polling).then(|| view! {
                    <p class="pr-meta">"Not polling: this pull request is no longer open."</p>
                })}
                <Show when=move || expanded.get()>
                    <ul class="pr-checks" data-pr-checks="">
                        {checks.is_empty().then(|| view! { <li class="pr-meta">"No checks reported yet."</li> })}
                        {checks.clone().into_iter().map(|c| {
                            let word = check_word(&c.state);
                            let state = c.state.clone();
                            let link = c.url.clone();
                            view! {
                                <li class=format!("pr-check pr-check-{state}") data-pr-check=state.clone()>
                                    <span class="pr-check-state">{word}</span>
                                    {if link.is_empty() {
                                        view! { <span class="pr-check-name">{c.name.clone()}</span> }.into_any()
                                    } else {
                                        view! { <a class="pr-check-name" href=link target="_blank" rel="noopener noreferrer">{c.name.clone()}</a> }.into_any()
                                    }}
                                </li>
                            }
                        }).collect_view()}
                        {review.then(|| view! { <li class="pr-check pr-check-fail">"A review requested changes."</li> })}
                    </ul>
                </Show>
                {attention.then(|| view! {
                    <div class="pr-fix">
                        {move || match fix.get() {
                            FixStep::Drafting => view! { <span class="pr-meta" role="status">"Preparing the fix message…"</span> }.into_any(),
                            FixStep::Sending => view! { <span class="pr-meta" role="status">"Posting…"</span> }.into_any(),
                            FixStep::Confirming(text) => view! {
                                <div
                                    class="pr-fix-confirm"
                                    role="dialog"
                                    aria-label="Post this fix request to the session?"
                                    data-pr-fix-confirm=""
                                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                                        if ev.key() == "Escape" {
                                            ev.prevent_default();
                                            ev.stop_propagation();
                                            cancel_fix();
                                        }
                                    }
                                >
                                    <p class="pr-meta">"This message will be posted into this session as your next prompt. Nothing is pushed or merged."</p>
                                    <pre class="pr-fix-text">{text}</pre>
                                    <div class="pr-fix-actions">
                                        <button type="button" class="primary" data-pr-fix-post="" autofocus=true on:click=move |_| confirm_fix()>"Post to session"</button>
                                        <button type="button" class="secondary" data-pr-fix-cancel="" on:click=move |_| cancel_fix()>"Cancel"</button>
                                    </div>
                                </div>
                            }.into_any(),
                            other => view! {
                                <button type="button" class="primary pr-fix-start" data-pr-fix="" on:click=move |_| start_fix()>"Fix in this session"</button>
                                {match other {
                                    FixStep::Sent => view! { <span class="pr-meta" role="status">"Posted to the session."</span> }.into_any(),
                                    FixStep::Failed(e) => view! { <span class="pr-error" role="alert">{e}</span> }.into_any(),
                                    _ => ().into_any(),
                                }}
                            }.into_any(),
                        }}
                    </div>
                })}
            </section>
        }
        .into_any()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn st(state: &str, rollup: &str) -> PrStatus {
        PrStatus {
            session_id: "s".into(),
            linked: true,
            available: true,
            number: 42,
            state: state.into(),
            rollup: rollup.into(),
            poll: state == "open",
            poll_after_sec: if state == "open" { 60 } else { 0 },
            ..Default::default()
        }
    }

    #[test]
    fn decodes_daemon_payload() {
        let raw = r#"{"sessionId":"s1","linked":true,"available":true,"url":"https://github.com/a/b/pull/42","number":42,"state":"open","reviewDecision":"changes_requested","checks":[{"name":"CI / vet","state":"fail","url":"https://github.com/a/b/actions/runs/1/job/2"}],"rollup":"fail","attention":true,"poll":true,"pollAfterSec":60,"checkedAt":"2026-11-20T00:00:00Z"}"#;
        let s: PrStatus = serde_json::from_str(raw).unwrap();
        assert_eq!(s.session_id, "s1");
        assert_eq!(s.number, 42);
        assert!(s.attention && s.poll);
        assert_eq!(s.review_decision, "changes_requested");
        assert_eq!(s.checks[0].state, "fail");
        assert_eq!(s.poll_after_sec, 60);
    }

    #[test]
    fn polls_while_open_and_stops_on_terminal_or_unavailable() {
        assert_eq!(next_poll_ms(Some(&st("open", "running")), 0), Some(60_000));
        assert_eq!(next_poll_ms(Some(&st("merged", "pass")), 0), None);
        assert_eq!(next_poll_ms(Some(&st("closed", "fail")), 0), None);
        let mut gone = st("open", "");
        gone.available = false;
        gone.poll = false;
        assert_eq!(next_poll_ms(Some(&gone), 0), None);
        let mut unlinked = st("open", "");
        unlinked.linked = false;
        assert_eq!(next_poll_ms(Some(&unlinked), 0), None);
        assert_eq!(next_poll_ms(None, 0), None);
    }

    #[test]
    fn poll_interval_is_clamped_and_backs_off() {
        let mut fast = st("open", "running");
        fast.poll_after_sec = 1;
        assert_eq!(next_poll_ms(Some(&fast), 0), Some(POLL_MS_MIN));
        let mut slow = st("open", "running");
        slow.poll_after_sec = 300;
        assert_eq!(next_poll_ms(Some(&slow), 0), Some(300_000));
        assert_eq!(next_poll_ms(None, 1), Some(120_000));
        assert_eq!(next_poll_ms(None, 2), Some(240_000));
        assert_eq!(next_poll_ms(None, 3), Some(480_000));
        assert_eq!(next_poll_ms(None, 9), Some(BACKOFF_MAX_MS));
    }

    #[test]
    fn chip_text_and_tone() {
        assert_eq!(chip_label(&st("open", "pass")), "PR #42 · checks passing");
        assert_eq!(chip_tone(&st("open", "pass")), "pass");
        let mut failing = st("open", "fail");
        failing.attention = true;
        assert_eq!(chip_label(&failing), "PR #42 · checks failing");
        assert_eq!(chip_tone(&failing), "fail");
        assert_eq!(chip_tone(&st("open", "queued")), "running");
        assert_eq!(chip_label(&st("merged", "pass")), "PR #42 · merged");
        assert_eq!(chip_tone(&st("merged", "pass")), "merged");
        let mut review = st("open", "pass");
        review.review_decision = "changes_requested".into();
        review.attention = true;
        assert_eq!(chip_label(&review), "PR #42 · checks passing · changes requested");
        let mut gone = st("open", "");
        gone.available = false;
        assert_eq!(chip_label(&gone), "PR #42 · tracking unavailable");
        assert_eq!(chip_tone(&gone), "unavailable");
    }

    #[test]
    fn check_words_cover_every_state() {
        for (state, word) in [
            ("queued", "queued"),
            ("running", "running"),
            ("pass", "passed"),
            ("fail", "failed"),
            ("skipped", "skipped"),
            ("weird", "unknown"),
        ] {
            assert_eq!(check_word(state), word);
        }
    }

    #[test]
    fn nothing_is_posted_without_explicit_confirmation() {
        // Start → draft shown: nothing posted yet.
        let (s, post) = fix_step(&FixStep::Idle, FixEvent::Start);
        assert_eq!(s, FixStep::Drafting);
        assert!(post.is_none());
        let (s, post) = fix_step(&s, FixEvent::Drafted("fix vet".into()));
        assert_eq!(s, FixStep::Confirming("fix vet".into()));
        assert!(post.is_none());
        // Cancel posts nothing and returns to idle.
        let (cancelled, post) = fix_step(&s, FixEvent::Cancel);
        assert_eq!(cancelled, FixStep::Idle);
        assert!(post.is_none());
        // Confirm is the only path that yields text.
        let (sending, post) = fix_step(&s, FixEvent::Confirm);
        assert_eq!(sending, FixStep::Sending);
        assert_eq!(post.as_deref(), Some("fix vet"));
        // Confirm from any other step posts nothing.
        for step in [
            FixStep::Idle,
            FixStep::Drafting,
            FixStep::Sending,
            FixStep::Sent,
            FixStep::Failed("x".into()),
        ] {
            let (_, post) = fix_step(&step, FixEvent::Confirm);
            assert!(post.is_none(), "confirm from {step:?} must not post");
        }
        // A second confirm while sending cannot double-post.
        let (_, again) = fix_step(&sending, FixEvent::Confirm);
        assert!(again.is_none());
    }

    #[test]
    fn fix_flow_errors_and_empty_drafts() {
        let (s, _) = fix_step(&FixStep::Drafting, FixEvent::Drafted("  ".into()));
        assert!(matches!(s, FixStep::Failed(_)));
        let (s, _) = fix_step(&FixStep::Drafting, FixEvent::DraftFailed("nothing to fix".into()));
        assert_eq!(s, FixStep::Failed("nothing to fix".into()));
        let (s, _) = fix_step(&FixStep::Sending, FixEvent::SendFailed("503".into()));
        assert_eq!(s, FixStep::Failed("503".into()));
        let (s, _) = fix_step(&FixStep::Sending, FixEvent::SendOk);
        assert_eq!(s, FixStep::Sent);
        let (s, _) = fix_step(&FixStep::Sent, FixEvent::Start);
        assert_eq!(s, FixStep::Drafting);
        // Starting while a draft is shown does not restart the flow.
        let (s, _) = fix_step(&FixStep::Confirming("t".into()), FixEvent::Start);
        assert_eq!(s, FixStep::Confirming("t".into()));
    }

    #[test]
    fn daemon_error_extracts_error_field() {
        assert_eq!(
            daemon_error(r#"HTTP 409 {"error":"nothing to fix: no failed checks or change requests"}"#),
            "nothing to fix: no failed checks or change requests"
        );
        assert_eq!(daemon_error("network down"), "network down");
    }
}
