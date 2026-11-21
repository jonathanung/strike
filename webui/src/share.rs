//! Session share links (#1565).
//!
//! Two halves:
//! - `ShareView`: what a browser opened at `/#share=<token>` sees. It holds no
//!   gateway credential; it posts the token (in a JSON body, never a URL) to
//!   `POST /share/v1/transcript` and renders the one session that token was
//!   minted for in the transcript pane, read-only. Expired, revoked, and
//!   unknown links all land on the same dead end.
//! - `ShareDialog`: the cockpit's Share dialog — create a link with an
//!   expiry, copy it, list and revoke live links for the session.

use std::cell::Cell;

use gloo_net::http::Request;
use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{delete_json, get_json, js_encode, post_json_value};
use crate::transcript::Transcript;
use crate::transcript_view::{FindView, TranscriptHandlers, TranscriptPane};

const TOKEN_PREFIX: &str = "sh1.";
const TOKEN_MAX: usize = 80;
const POLL_MS: i32 = 5000;

/// Present in context when the transcript pane renders a share: the pane
/// hides actions that would change a session (rename, retry, fork, ...).
#[derive(Clone, Copy, Debug)]
pub struct ReadOnlyTranscript;

/// The share token in a location hash (`#share=<token>`), if it is
/// well-formed. Other hash keys may sit beside it.
pub fn share_token_from_hash(hash: &str) -> Option<String> {
    let raw = hash.trim_start_matches('#').trim_start_matches('/');
    for part in raw.split('&') {
        let Some(value) = part.strip_prefix("share=") else {
            continue;
        };
        let ok = value.starts_with(TOKEN_PREFIX)
            && value.len() > TOKEN_PREFIX.len()
            && value.len() <= TOKEN_MAX
            && value
                .chars()
                .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.');
        return ok.then(|| value.to_string());
    }
    None
}

pub fn share_token_from_location() -> Option<String> {
    let hash = web_sys::window()?.location().hash().ok()?;
    share_token_from_hash(&hash)
}

/// The full link for a minted share path (`/#share=...`) on this origin.
pub fn share_link(origin: &str, path: &str) -> String {
    format!("{}{}", origin.trim_end_matches('/'), path)
}

/// Plain-language expiry for the dialog ("in 7 days", "in 3 hours").
pub fn expiry_phrase(seconds_left: i64) -> String {
    if seconds_left <= 0 {
        return "expired".into();
    }
    let hours = seconds_left / 3600;
    let days = hours / 24;
    if days >= 2 {
        format!("expires in {days} days")
    } else if hours >= 2 {
        format!("expires in {hours} hours")
    } else {
        let mins = (seconds_left / 60).max(1);
        format!("expires in {mins} minutes")
    }
}

#[derive(Clone, Debug, Deserialize)]
struct ShareChunk {
    #[serde(default)]
    title: String,
    #[serde(default)]
    events: Vec<serde_json::Value>,
    #[serde(default)]
    next: i64,
    #[serde(default)]
    more: bool,
}

#[derive(Clone, Debug, PartialEq)]
enum ShareState {
    Loading,
    Ready,
    Dead,
    Offline(String),
}

enum FetchError {
    Dead,
    Net(String),
}

async fn fetch_chunk(token: &str, after: i64) -> Result<ShareChunk, FetchError> {
    let resp = Request::post("/share/v1/transcript")
        .credentials(web_sys::RequestCredentials::Omit)
        .json(&serde_json::json!({"token": token, "after": after}))
        .map_err(|e| FetchError::Net(e.to_string()))?
        .send()
        .await
        .map_err(|e| FetchError::Net(e.to_string()))?;
    match resp.status() {
        200 => resp
            .json::<ShareChunk>()
            .await
            .map_err(|e| FetchError::Net(e.to_string())),
        404 => Err(FetchError::Dead),
        429 => Err(FetchError::Net("Too many requests; retrying shortly.".into())),
        s => Err(FetchError::Net(format!("HTTP {s}"))),
    }
}

/// Read-only shell for a share link. Mounted instead of the cockpit.
#[component]
pub fn ShareView(token: String) -> impl IntoView {
    provide_context(ReadOnlyTranscript);
    let transcript = RwSignal::new(Transcript::new());
    let session = RwSignal::new("shared".to_string());
    let loading = RwSignal::new(true);
    let load_error = RwSignal::new(String::new());
    let tick = RwSignal::new(0u32);
    let note = RwSignal::new(String::new());
    let find = RwSignal::new(FindView::default());
    let state = RwSignal::new(ShareState::Loading);
    let cursor = RwSignal::new(0i64);
    let busy = RwSignal::new(false);
    let handlers = TranscriptHandlers {
        send: (|_: String| {}).into(),
        rename: (|_: String, _: String| {}).into(),
        regenerate: (|| {}).into(),
        export_timeline: (|| {}).into(),
        export_markdown: (|| {}).into(),
        retry: (|_: String| {}).into(),
        fork: (|_: i32| {}).into(),
        open_file: (|_: String, _: u32| {}).into(),
        // Read-only share view: no composer, so no edit & resend (#1361).
        edit_last: (|_: u32| {}).into(),
    };

    // Pull everything after `cursor`, a chunk at a time, then stop. Live:
    // the same link shows the session as it grows (polled).
    let pull = move |token: String| {
        if busy.get_untracked() {
            return;
        }
        busy.set(true);
        spawn_local(async move {
            loop {
                match fetch_chunk(&token, cursor.get_untracked()).await {
                    Ok(chunk) => {
                        transcript.update(|t| {
                            for ev in &chunk.events {
                                t.apply_envelope(&ev.to_string());
                            }
                            if t.title.is_empty() && !chunk.title.is_empty() {
                                t.title = chunk.title.clone();
                            }
                        });
                        cursor.set(chunk.next);
                        state.set(ShareState::Ready);
                        loading.set(false);
                        if !chunk.more {
                            break;
                        }
                    }
                    Err(FetchError::Dead) => {
                        // A link that worked and then stops (revoked or
                        // expired mid-read) also ends here.
                        state.set(ShareState::Dead);
                        loading.set(false);
                        break;
                    }
                    Err(FetchError::Net(msg)) => {
                        if state.get_untracked() != ShareState::Ready {
                            state.set(ShareState::Offline(msg));
                        }
                        loading.set(false);
                        break;
                    }
                }
            }
            busy.set(false);
        });
    };
    pull(token.clone());
    if let Some(window) = web_sys::window() {
        let poll_token = token.clone();
        let tick_cb = Closure::<dyn FnMut()>::new(move || {
            if state.get_untracked() != ShareState::Dead {
                pull(poll_token.clone());
            }
        });
        let _ = window.set_interval_with_callback_and_timeout_and_arguments_0(
            tick_cb.as_ref().unchecked_ref(),
            POLL_MS,
        );
        // The share view is the page root and is never unmounted.
        tick_cb.forget();
    }

    view! {
        <div class="share-shell">
            <Show
                when=move || state.get() != ShareState::Dead
                fallback=|| view! {
                    <main class="share-dead" role="main">
                        <h1>"This share link is not available"</h1>
                        <p>"It may have expired or been revoked, or the link is incomplete. Ask the person who shared it for a new link."</p>
                    </main>
                }
            >
                <header class="share-banner" role="status">
                    <strong>"Shared session · read-only"</strong>
                    <span>"You are viewing one session someone shared with you. You cannot send messages, answer approvals, or open other sessions."</span>
                </header>
                {move || match state.get() {
                    ShareState::Offline(msg) => view! {
                        <p class="share-offline" role="alert">{format!("Could not reach the gateway: {msg}")}</p>
                    }.into_any(),
                    _ => view! { <span></span> }.into_any(),
                }}
                <main class="share-transcript">
                    <TranscriptPane
                        transcript=transcript
                        session=session
                        loading=loading
                        load_error=load_error
                        tick=tick
                        note=note
                        handlers=handlers
                        find=find
                    />
                </main>
            </Show>
        </div>
    }
}

thread_local! {
    static OPEN_DIALOG: Cell<Option<RwSignal<bool>>> = const { Cell::new(None) };
}

/// Opens the Share dialog from the transcript header.
pub fn request_open() {
    OPEN_DIALOG.with(|slot| {
        if let Some(open) = slot.get() {
            open.set(true);
        }
    });
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct ShareRow {
    id: String,
    label: String,
    #[serde(rename = "expiresAt")]
    expires_at: String,
}

#[derive(Deserialize)]
struct ShareList {
    shares: Vec<ShareRow>,
}

fn seconds_until(rfc3339: &str) -> i64 {
    let target = js_sys::Date::parse(rfc3339);
    if target.is_nan() {
        return 0;
    }
    ((target - js_sys::Date::now()) / 1000.0) as i64
}

fn page_origin() -> String {
    web_sys::window()
        .and_then(|w| w.location().origin().ok())
        .unwrap_or_default()
}

/// Expiry choices: (label, seconds). Seven days is the default.
pub const EXPIRY_CHOICES: [(&str, i64); 3] = [
    ("24 hours", 24 * 3600),
    ("7 days", 7 * 24 * 3600),
    ("30 days", 30 * 24 * 3600),
];

/// The cockpit's Share dialog for the active root session.
#[component]
pub fn ShareDialog(
    base: RwSignal<String>,
    token: RwSignal<String>,
    session: RwSignal<String>,
) -> impl IntoView {
    let open = RwSignal::new(false);
    OPEN_DIALOG.with(|slot| slot.set(Some(open)));
    on_cleanup(move || {
        OPEN_DIALOG.with(|slot| {
            if slot.get() == Some(open) {
                slot.set(None);
            }
        });
    });
    let rows = RwSignal::new(Vec::<ShareRow>::new());
    let expiry = RwSignal::new(EXPIRY_CHOICES[1].1);
    let label = RwSignal::new(String::new());
    let link = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let copied = RwSignal::new(String::new());

    let reload = move || {
        let id = session.get_untracked();
        if id.is_empty() {
            rows.set(Vec::new());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/sessions/{}/shares", js_encode(&id));
            match get_json::<ShareList>(&b, &t, &path).await {
                Ok(list) => rows.set(list.shares),
                Err(e) => error.set(format!("Could not load share links: {e}")),
            }
        });
    };
    Effect::new(move |_| {
        if open.get() {
            link.set(String::new());
            error.set(String::new());
            copied.set(String::new());
            reload();
        }
    });

    let create = move |_| {
        let id = session.get_untracked();
        if id.is_empty() {
            error.set("Open a session first.".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let body = serde_json::json!({"label": label.get_untracked(), "ttlSeconds": expiry.get_untracked()});
        spawn_local(async move {
            let path = format!("/v1/sessions/{}/shares", js_encode(&id));
            match post_json_value(&b, &t, &path, body).await {
                Ok(v) => {
                    let p = v.get("path").and_then(|p| p.as_str()).unwrap_or_default();
                    if p.starts_with("/#share=") {
                        link.set(share_link(&page_origin(), p));
                        error.set(String::new());
                        label.set(String::new());
                    } else {
                        error.set("The gateway returned no link.".into());
                    }
                    reload();
                }
                Err(e) => error.set(format!("Could not create a link: {e}")),
            }
        });
    };

    let copy = move |_| {
        let text = link.get_untracked();
        spawn_local(async move {
            let ok = crate::clipboard::copy_text(&text).await.is_ok();
            copied.set(if ok { "Copied.".into() } else { "Copy didn't work; select the link and copy it.".into() });
        });
    };

    view! {
        <Show when=move || open.get()>
            <div class="share-scrim" on:click=move |_| open.set(false)>
                <div class="share-dialog" role="dialog" aria-modal="true" aria-labelledby="share-dialog-title"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                    on:keydown=move |ev: web_sys::KeyboardEvent| if ev.key() == "Escape" { open.set(false); }>
                    <h2 id="share-dialog-title">"Share this session"</h2>
                    <p class="share-scope">"Anyone with this link sees this whole session, including code and tool output, as it grows. They cannot send messages, answer approvals, open other sessions, or download attachments."</p>
                    <p class="share-scope">"The link only works for someone who can reach this gateway — the same network as pairing links. Nothing is published to the internet."</p>
                    <div class="share-form">
                        <label>
                            "Expires after"
                            <select on:change=move |ev| {
                                if let Ok(v) = event_target_value(&ev).parse::<i64>() {
                                    expiry.set(v);
                                }
                            }>
                                {EXPIRY_CHOICES.iter().map(|(name, secs)| {
                                    let secs = *secs;
                                    view! { <option value=secs.to_string() selected=move || expiry.get() == secs>{*name}</option> }
                                }).collect_view()}
                            </select>
                        </label>
                        <label>
                            "Label (optional)"
                            <input type="text" maxlength="64" prop:value=move || label.get()
                                on:input=move |ev| label.set(event_target_value(&ev)) />
                        </label>
                        <button type="button" on:click=create>"Create link"</button>
                    </div>
                    <Show when=move || !link.get().is_empty()>
                        <div class="share-link">
                            <input type="text" readonly aria-label="Share link" prop:value=move || link.get() />
                            <button type="button" class="secondary" on:click=copy>"Copy"</button>
                            <span role="status">{move || copied.get()}</span>
                            <p class="share-hint">"This is the only time the link is shown. Revoke it below to turn it off."</p>
                        </div>
                    </Show>
                    <Show when=move || !error.get().is_empty()>
                        <p class="share-error" role="alert">{move || error.get()}</p>
                    </Show>
                    <h3>"Live links"</h3>
                    <Show when=move || !rows.get().is_empty() fallback=|| view! { <p class="share-empty">"No live links for this session."</p> }>
                        <ul class="share-rows">
                            {move || rows.get().into_iter().map(|row| {
                                let id = row.id.clone();
                                let when = expiry_phrase(seconds_until(&row.expires_at));
                                view! {
                                    <li>
                                        <span class="share-row-label">{row.label.clone()}</span>
                                        <span class="share-row-expiry">{when}</span>
                                        <button type="button" class="secondary" on:click=move |_| {
                                            let sid = session.get_untracked();
                                            let rid = id.clone();
                                            let (b, t) = (base.get_untracked(), token.get_untracked());
                                            spawn_local(async move {
                                                let path = format!("/v1/sessions/{}/shares/{}", js_encode(&sid), js_encode(&rid));
                                                match delete_json(&b, &t, &path, None).await {
                                                    Ok(()) => {
                                                        rows.update(|list| list.retain(|r| r.id != rid));
                                                        link.set(String::new());
                                                    }
                                                    Err(e) => error.set(format!("Could not revoke: {e}")),
                                                }
                                            });
                                        }>"Revoke"</button>
                                    </li>
                                }
                            }).collect_view()}
                        </ul>
                    </Show>
                    <div class="share-actions">
                        <button type="button" class="secondary" on:click=move |_| open.set(false)>"Close"</button>
                    </div>
                </div>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn share_token_parsing() {
        let tok = "sh1.abcDEF-_123";
        let cases = [
            ("#share=sh1.abcDEF-_123", Some(tok)),
            ("share=sh1.abcDEF-_123", Some(tok)),
            ("#/share=sh1.abcDEF-_123", Some(tok)),
            ("#session=x&share=sh1.abcDEF-_123", Some(tok)),
            ("#share=", None),
            ("#share=sh1.", None),
            ("#share=p1.pairingcode", None),
            ("#share=sh1.bad<script>", None),
            ("#session=abc", None),
            ("", None),
        ];
        for (hash, want) in cases {
            assert_eq!(share_token_from_hash(hash).as_deref(), want, "{hash}");
        }
        let long = format!("#share=sh1.{}", "a".repeat(100));
        assert_eq!(share_token_from_hash(&long), None);
    }

    #[test]
    fn link_and_expiry_text() {
        assert_eq!(
            share_link("http://100.64.0.5:8787/", "/#share=sh1.x"),
            "http://100.64.0.5:8787/#share=sh1.x"
        );
        assert_eq!(expiry_phrase(0), "expired");
        assert_eq!(expiry_phrase(30), "expires in 1 minutes");
        assert_eq!(expiry_phrase(3 * 3600), "expires in 3 hours");
        assert_eq!(expiry_phrase(7 * 24 * 3600), "expires in 7 days");
        assert_eq!(EXPIRY_CHOICES[1], ("7 days", 7 * 24 * 3600));
    }
}
