//! Ideas inbox page (#1323).
//!
//! Lists `GET /v1/suggestions` (proactive suggestions producers deposited)
//! with Accept, Snooze and Dismiss, plus the inbox controls: muted sources
//! and "never suggest this kind". Accept records the decision
//! (`POST /v1/suggestions/{id}/accept`), opens a new session through the
//! normal session-start path, and drafts the proposed prompt with its
//! evidence in that session's composer. Nothing is sent or approved for
//! the user: they read the draft and send it under the session's
//! permission mode. View rules live in the `webui/ideas-inbox` crate.

use std::cell::Cell;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::json;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, now_ms, post_json, put_json_value};
use ideas_inbox::{Evidence, Idea};

/// How long an accepted draft waits for the new session to become active.
const DRAFT_WAIT_MS: f64 = 15_000.0;

#[derive(Clone, Debug, Default, Deserialize)]
struct EvidenceDto {
    #[serde(default)]
    label: String,
    #[serde(default, rename = "sessionId")]
    session_id: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    url: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct IdeaDto {
    #[serde(default)]
    id: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    why: String,
    #[serde(default)]
    source: String,
    #[serde(default)]
    kind: String,
    #[serde(default)]
    effort: String,
    #[serde(default)]
    prompt: String,
    #[serde(default)]
    state: String,
    #[serde(default, rename = "snoozeUntil")]
    snooze_until: Option<String>,
    #[serde(default, rename = "createdAt")]
    created_at: String,
    #[serde(default)]
    repeats: i64,
    #[serde(default)]
    evidence: Vec<EvidenceDto>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
pub struct IdeaPrefs {
    #[serde(default, rename = "mutedSources")]
    pub muted_sources: Vec<String>,
    #[serde(default, rename = "mutedKinds")]
    pub muted_kinds: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct ListDto {
    #[serde(default)]
    suggestions: Vec<IdeaDto>,
    #[serde(default)]
    prefs: IdeaPrefs,
    #[serde(default)]
    open: usize,
}

fn parse_ms(raw: &str) -> i64 {
    let raw = raw.trim();
    if raw.is_empty() {
        return 0;
    }
    let ms = js_sys::Date::parse(raw);
    if ms.is_finite() && ms > 0.0 {
        ms as i64
    } else {
        0
    }
}

fn to_idea(d: IdeaDto) -> Idea {
    Idea {
        snooze_until_ms: d.snooze_until.as_deref().map(parse_ms).unwrap_or(0),
        created_ms: parse_ms(&d.created_at),
        id: d.id,
        title: d.title,
        why: d.why,
        source: d.source,
        kind: d.kind,
        effort: d.effort,
        prompt: d.prompt,
        state: d.state,
        repeats: d.repeats,
        evidence: d
            .evidence
            .into_iter()
            .map(|e| Evidence { label: e.label, session_id: e.session_id, path: e.path, url: e.url })
            .collect(),
    }
}

fn wake_label(ms: i64) -> String {
    if ms <= 0 {
        return String::new();
    }
    let date = js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(ms as f64));
    let text: String = date.to_locale_string("default", &wasm_bindgen::JsValue::UNDEFINED).into();
    format!("Back {text}")
}

fn later(ms: i32, f: impl FnOnce() + 'static) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(f);
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(cb.as_ref().unchecked_ref(), ms);
    cb.forget();
}

// Open count for the rail badge. The page and the badge share it.
thread_local! {
    static OPEN_COUNT: Cell<Option<RwSignal<usize>>> = const { Cell::new(None) };
}

fn set_open_count(n: usize) {
    OPEN_COUNT.with(|slot| {
        if let Some(sig) = slot.get() {
            // The badge remounts with the chat view; a stale handle is a no-op.
            let _ = sig.try_set(n);
        }
    });
}

/// Rail badge: the open count, hidden at zero. It loads when the chat view
/// mounts (so returning from any page refreshes it) and when the Ideas page
/// reloads its list. No timer: a remounting component must not stack polls.
#[component]
pub fn IdeasBadge(base: RwSignal<String>, token: RwSignal<String>, mode: RwSignal<String>) -> impl IntoView {
    let open = RwSignal::new(0usize);
    OPEN_COUNT.with(|slot| slot.set(Some(open)));
    let refresh = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Ok(list) = get_json::<ListDto>(&b, &t, "/v1/suggestions").await {
                open.set(list.open);
            }
        });
    };
    Effect::new(move |_| {
        let _ = (base.get(), token.get());
        refresh();
    });
    move || match ideas_inbox::badge(open.get()) {
        None => view! { <span></span> }.into_any(),
        Some(text) => {
            let label = format!("{} open ideas", open.get());
            view! {
                <button type="button" class="ideas-badge" data-ideas-badge="true" title=label.clone() aria-label=label on:click=move |_| mode.set("ideas".into())>
                    <span class="ideas-badge-label">"Ideas"</span>
                    <span class="badge">{text}</span>
                </button>
            }
            .into_any()
        }
    }
}

/// The Ideas inbox page.
#[component]
pub fn IdeasPage(
    base: RwSignal<String>,
    token: RwSignal<String>,
    draft: RwSignal<String>,
    active_root: RwSignal<String>,
    mode: RwSignal<String>,
) -> impl IntoView {
    let ideas = RwSignal::new(Vec::<Idea>::new());
    let prefs = RwSignal::new(IdeaPrefs::default());
    let loaded = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let note = RwSignal::new(String::new());
    let busy = RwSignal::new(String::new());
    // (root before Accept, draft text, deadline ms)
    let pending = RwSignal::new(None::<(String, String, f64)>);

    let load = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<ListDto>(&b, &t, "/v1/suggestions").await {
                Ok(list) => {
                    set_open_count(list.open);
                    prefs.set(list.prefs);
                    ideas.set(list.suggestions.into_iter().map(to_idea).collect());
                    error.set(String::new());
                }
                Err(e) => error.set(format!("Could not load ideas: {e}")),
            }
            loaded.set(true);
        });
    };
    Effect::new(move |_| {
        let _ = (base.get(), token.get());
        load();
    });

    // Write the accepted draft once the new session is the active one. The
    // per-session draft book swaps drafts on the same root change, so the
    // write waits one tick to land after that restore.
    Effect::new(move |_| {
        let root = active_root.get();
        let Some((before, text, deadline)) = pending.get_untracked() else {
            return;
        };
        if now_ms() > deadline {
            pending.set(None);
            return;
        }
        if root.is_empty() || root == before {
            return;
        }
        pending.set(None);
        later(0, move || {
            draft.set(text);
            mode.set("chat".into());
        });
    });

    let act = move |id: String, path: String, body: serde_json::Value, done: &'static str| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        busy.set(id.clone());
        spawn_local(async move {
            let res = post_json(&b, &t, &path, body).await;
            busy.set(String::new());
            match res {
                Ok(()) => note.set(done.to_string()),
                Err(e) => error.set(format!("{done} failed: {e}")),
            }
            load();
        });
    };

    let accept = move |idea: Idea| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        busy.set(idea.id.clone());
        let path = format!("/v1/suggestions/{}/accept", crate::api::js_encode(&idea.id));
        spawn_local(async move {
            let res = post_json(&b, &t, &path, json!({})).await;
            busy.set(String::new());
            match res {
                Ok(()) => {
                    pending.set(Some((
                        active_root.get_untracked(),
                        ideas_inbox::accept_draft(&idea),
                        now_ms() + DRAFT_WAIT_MS,
                    )));
                    if let Err(e) = crate::shell::new_session() {
                        pending.set(None);
                        error.set(format!("Accepted, but a session could not start: {e}"));
                    } else {
                        note.set("Accepted. The new session has the prompt drafted; review it and send.".into());
                    }
                }
                Err(e) => error.set(format!("Accept failed: {e}")),
            }
            load();
        });
    };

    let save_prefs = move |next: IdeaPrefs| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let body = json!({ "mutedSources": next.muted_sources, "mutedKinds": next.muted_kinds });
            match put_json_value(&b, &t, "/v1/suggestions/prefs", body).await {
                Ok(_) => note.set("Inbox controls saved.".into()),
                Err(e) => error.set(format!("Saving inbox controls failed: {e}")),
            }
            load();
        });
    };

    let row_view = move |idea: Idea, snoozed: bool| {
        let id = idea.id.clone();
        let enc = crate::api::js_encode(&idea.id);
        let title = idea.title.clone();
        let why = idea.why.clone();
        let meta = ideas_inbox::meta(&idea);
        let source = idea.source.clone();
        let wake = if snoozed { wake_label(idea.snooze_until_ms) } else { String::new() };
        let evidence = idea.evidence.clone();
        let is_busy = {
            let id = id.clone();
            move || busy.get() == id
        };
        let accept_idea = idea.clone();
        let snooze_enc = enc.clone();
        let snooze_id = id.clone();
        let dismiss_enc = enc.clone();
        let dismiss_id = id.clone();
        let never_enc = enc;
        let never_id = id.clone();
        view! {
            <li class="card ideas-row" data-idea=id.clone()>
                <div class="ideas-row-head">
                    <strong class="ideas-title">{title}</strong>
                    <span class="hint ideas-meta">{meta}</span>
                </div>
                <Show when={let w = wake.clone(); move || !w.is_empty()}>
                    <p class="hint">{wake.clone()}</p>
                </Show>
                <Show when={let w = why.clone(); move || !w.is_empty()}>
                    <p class="ideas-why">{why.clone()}</p>
                </Show>
                <Show when={let e = evidence.clone(); move || !e.is_empty()}>
                    <ul class="plain ideas-evidence" aria-label="Evidence">
                        {evidence.clone().into_iter().map(|ev| {
                            let mut parts = vec![ev.label.clone()];
                            if !ev.session_id.is_empty() { parts.push(format!("session {}", ev.session_id)); }
                            if !ev.path.is_empty() { parts.push(ev.path.clone()); }
                            let text = parts.join(" \u{2014} ");
                            if ev.url.is_empty() {
                                view! { <li>{text}</li> }.into_any()
                            } else {
                                let href = ev.url.clone();
                                view! { <li><a href=href target="_blank" rel="noopener noreferrer">{text}</a></li> }.into_any()
                            }
                        }).collect_view()}
                    </ul>
                </Show>
                <div class="row-actions ideas-actions">
                    <button type="button" class="primary ideas-accept" disabled=is_busy.clone() on:click={
                        let accept_idea = accept_idea.clone();
                        move |_| accept(accept_idea.clone())
                    }>"Accept"</button>
                    {ideas_inbox::SNOOZE_CHOICES.into_iter().map(|(label, hours)| {
                        let enc = snooze_enc.clone();
                        let id = snooze_id.clone();
                        let busy_now = is_busy.clone();
                        view! {
                            <button type="button" class="secondary ideas-snooze" disabled=busy_now on:click=move |_| {
                                act(id.clone(), format!("/v1/suggestions/{enc}/snooze"), json!({ "hours": hours }), "Snoozed")
                            }>{format!("Snooze {label}")}</button>
                        }
                    }).collect_view()}
                    <button type="button" class="secondary ideas-dismiss" disabled=is_busy.clone() on:click={
                        let enc = dismiss_enc.clone();
                        let id = dismiss_id.clone();
                        move |_| act(id.clone(), format!("/v1/suggestions/{enc}/dismiss"), json!({}), "Dismissed")
                    }>"Dismiss"</button>
                    <button type="button" class="secondary ideas-never" disabled=is_busy.clone() title="Dismiss and never suggest this kind again" on:click={
                        let enc = never_enc.clone();
                        let id = never_id.clone();
                        move |_| act(id.clone(), format!("/v1/suggestions/{enc}/dismiss"), json!({ "never": true }), "Dismissed; this kind is muted")
                    }>"Never this kind"</button>
                    <button type="button" class="link ideas-mute-source" on:click={
                        let source = source.clone();
                        move |_| {
                            let mut next = prefs.get_untracked();
                            if !next.muted_sources.contains(&source) {
                                next.muted_sources.push(source.clone());
                            }
                            save_prefs(next);
                        }
                    }>{format!("Mute {source}")}</button>
                </div>
            </li>
        }
    };

    view! {
        <div class="ideas-shell" id="ideas-root">
            <header class="section-head">
                <h2 class="panel-title">"Ideas"</h2>
                <button type="button" class="secondary" on:click=move |_| load()>"Refresh"</button>
            </header>
            <p class="hint">"Suggestions for new work from background producers. Accept opens a new session with the prompt drafted; nothing runs until you send it."</p>
            <Show when=move || !error.get().is_empty()>
                <div class="notice" role="alert">{move || error.get()}</div>
            </Show>
            <Show when=move || !note.get().is_empty()>
                <p class="hint" role="status">{move || note.get()}</p>
            </Show>
            {move || {
                if !loaded.get() {
                    return view! { <p class="hint">"Loading…"</p> }.into_any();
                }
                let (open, snoozed) = ideas_inbox::sections(&ideas.get());
                if open.is_empty() && snoozed.is_empty() {
                    if !error.get().is_empty() {
                        return view! { <span></span> }.into_any();
                    }
                    return view! {
                        <div class="empty-state" data-ideas-empty="true">
                            <strong>"No ideas right now"</strong>
                            <p>"Producers such as scheduled heartbeats deposit suggestions here."</p>
                        </div>
                    }.into_any();
                }
                let snoozed_count = snoozed.len();
                view! {
                    <section aria-label="Open ideas">
                        <ul class="plain ideas-list">
                            {open.into_iter().map(|i| row_view(i, false)).collect_view()}
                        </ul>
                    </section>
                    <Show when=move || { snoozed_count > 0 }>
                        <details class="fold ideas-snoozed">
                            <summary>{format!("Snoozed ({snoozed_count})")}</summary>
                            <ul class="plain ideas-list">
                                {snoozed.clone().into_iter().map(|i| row_view(i, true)).collect_view()}
                            </ul>
                        </details>
                    </Show>
                }.into_any()
            }}
            <details class="fold ideas-controls">
                <summary>"Inbox controls"</summary>
                <p class="hint">"Muted sources and kinds cannot deposit ideas."</p>
                {move || {
                    let p = prefs.get();
                    if p.muted_sources.is_empty() && p.muted_kinds.is_empty() {
                        return view! { <p class="hint">"Nothing muted."</p> }.into_any();
                    }
                    let sources = p.muted_sources.clone();
                    let kinds = p.muted_kinds.clone();
                    view! {
                        <ul class="plain ideas-muted">
                            {sources.into_iter().map(|s| {
                                let label = format!("Source: {s}");
                                view! {
                                    <li>
                                        <span>{label}</span>
                                        <button type="button" class="link" on:click=move |_| {
                                            let mut next = prefs.get_untracked();
                                            next.muted_sources.retain(|x| x != &s);
                                            save_prefs(next);
                                        }>"Unmute"</button>
                                    </li>
                                }
                            }).collect_view()}
                            {kinds.into_iter().map(|k| {
                                let label = format!("Kind: {k}");
                                view! {
                                    <li>
                                        <span>{label}</span>
                                        <button type="button" class="link" on:click=move |_| {
                                            let mut next = prefs.get_untracked();
                                            next.muted_kinds.retain(|x| x != &k);
                                            save_prefs(next);
                                        }>"Unmute"</button>
                                    </li>
                                }
                            }).collect_view()}
                        </ul>
                    }.into_any()
                }}
            </details>
        </div>
    }
}
