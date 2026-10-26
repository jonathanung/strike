//! Queue apps. The launcher, list, reading pane, and handoff button are
//! generic. Rows come from `GET /v1/apps` (see docs/webui-apps.md). This
//! file does not invent apps.

use std::cell::Cell;

use gloo_net::http::Request;
use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::de::DeserializeOwned;
use serde::Deserialize;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{self, auth_header, post_json_value};

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
pub struct AppInfo {
    pub id: String,
    pub title: String,
    pub summary: String,
    #[serde(default)]
    pub handoff: bool,
}

#[derive(Clone, Debug, Deserialize)]
struct AppCatalog {
    #[serde(default)]
    apps: Vec<AppInfo>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
pub struct QueueItem {
    pub id: String,
    pub title: String,
    #[serde(default)]
    pub subtitle: String,
    #[serde(default)]
    pub badge: String,
    #[serde(default, rename = "updatedAt")]
    pub updated_at: String,
    #[serde(default)]
    pub labels: Vec<String>,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub number: i64,
}

#[derive(Clone, Debug, Deserialize)]
struct QueueResponse {
    #[serde(default)]
    repo: String,
    #[serde(default)]
    items: Vec<QueueItem>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct Note {
    #[serde(default)]
    author: String,
    #[serde(default)]
    body: String,
    #[serde(default)]
    at: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct ActionSpec {
    id: String,
    label: String,
    #[serde(default, rename = "needsBody")]
    needs_body: bool,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct ItemDetail {
    #[serde(flatten)]
    item: QueueItem,
    #[serde(default)]
    body: String,
    #[serde(default)]
    comments: Vec<Note>,
    #[serde(default)]
    actions: Vec<ActionSpec>,
}

pub fn queue_query(filter: &str) -> &'static str {
    match filter {
        "open-prs" => "kind=pr&state=open",
        "closed" => "kind=all&state=closed",
        _ => "kind=issue&state=open",
    }
}

pub fn filter_queue(items: &[QueueItem], query: &str) -> Vec<QueueItem> {
    let q = query.trim().to_ascii_lowercase();
    if q.is_empty() {
        return items.to_vec();
    }
    items
        .iter()
        .filter(|item| queue_haystack(item).contains(&q))
        .cloned()
        .collect()
}

fn queue_haystack(item: &QueueItem) -> String {
    let mut parts = vec![
        item.title.clone(),
        item.subtitle.clone(),
        item.badge.clone(),
        item.kind.clone(),
        item.id.clone(),
        item.number.to_string(),
    ];
    parts.extend(item.labels.iter().cloned());
    parts.join("\n").to_ascii_lowercase()
}

pub fn step_index(len: usize, current: Option<usize>, delta: i32) -> usize {
    if len == 0 {
        return 0;
    }
    let Some(i) = current else {
        if delta < 0 {
            return len - 1;
        }
        return 0;
    };
    let n = i as i32 + delta;
    if n < 0 {
        return 0;
    }
    if n as usize >= len {
        return len - 1;
    }
    n as usize
}

fn move_id(items: &[QueueItem], current: &str, delta: i32) -> String {
    if items.is_empty() {
        return String::new();
    }
    let idx = items.iter().position(|item| item.id == current);
    items[step_index(items.len(), idx, delta)].id.clone()
}

fn short_when(iso: &str) -> String {
    if iso.len() < 16 {
        return iso.to_string();
    }
    let mut s: String = iso.chars().take(16).collect();
    s = s.replace('T', " ");
    s
}

fn kind_label(kind: &str) -> &str {
    match kind {
        "pr" => "PR",
        "issue" => "Issue",
        _ => "",
    }
}

fn badge_class(badge: &str) -> String {
    match badge {
        "open" => "badge badge-running".into(),
        "closed" => "badge badge-stopped".into(),
        "merged" => "badge badge-done".into(),
        _ => "badge".into(),
    }
}

fn typing_target(ev: &web_sys::KeyboardEvent) -> bool {
    let Some(target) = ev.target() else {
        return false;
    };
    let Ok(tag) = js_sys::Reflect::get(&target, &"tagName".into()) else {
        return false;
    };
    let Some(tag) = tag.as_string() else {
        return false;
    };
    matches!(
        tag.to_ascii_lowercase().as_str(),
        "input" | "textarea" | "select"
    )
}

async fn fetch_json<T: DeserializeOwned>(base: &str, token: &str, path: &str) -> Result<T, String> {
    let resp = Request::get(&api::api(base, path))
        .header("Authorization", &auth_header(token))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    if status == 401 {
        return Err("unauthorized: check the daemon token".into());
    }
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status) {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&text) {
            if let Some(err) = v.get("error").and_then(|e| e.as_str()) {
                if !err.is_empty() {
                    return Err(err.to_string());
                }
            }
        }
        return Err(format!("HTTP {status}"));
    }
    serde_json::from_str(&text).map_err(|e| e.to_string())
}

thread_local! {
    static APPS_KEY_EPOCH: Cell<u32> = const { Cell::new(0) };
}

#[component]
pub fn AppsPane(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    palette_open: RwSignal<bool>,
    open_session: impl Fn(String) + Copy + Send + Sync + 'static,
) -> impl IntoView {
    let catalog = RwSignal::new(Vec::<AppInfo>::new());
    let catalog_error = RwSignal::new(String::new());
    let catalog_loading = RwSignal::new(true);
    let open_id = RwSignal::new(String::new());
    let open_title = RwSignal::new(String::new());
    let open_handoff = RwSignal::new(false);
    let repo = RwSignal::new(String::new());
    let items = RwSignal::new(Vec::<QueueItem>::new());
    let list_error = RwSignal::new(String::new());
    let list_loading = RwSignal::new(false);
    let filter = RwSignal::new("open-issues".to_string());
    let query = RwSignal::new(String::new());
    let selected = RwSignal::new(String::new());
    let detail = RwSignal::new(None::<ItemDetail>);
    let detail_error = RwSignal::new(String::new());
    let detail_loading = RwSignal::new(false);
    let comment = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let action_error = RwSignal::new(String::new());
    let launch_i = RwSignal::new(0usize);
    let reload = RwSignal::new(0u32);
    let detail_reload = RwSignal::new(0u32);
    let list_gen = RwSignal::new(0u32);
    let detail_gen = RwSignal::new(0u32);

    let load_catalog = move || {
        catalog_loading.set(true);
        catalog_error.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match fetch_json::<AppCatalog>(&b, &t, "/v1/apps").await {
                Ok(cat) => {
                    catalog.set(cat.apps);
                    catalog_error.set(String::new());
                }
                Err(e) => {
                    catalog.set(Vec::new());
                    catalog_error.set(e);
                }
            }
            catalog_loading.set(false);
        });
    };
    load_catalog();

    Effect::new(move |_| {
        let id = open_id.get();
        let filt = filter.get();
        let _ = reload.get();
        let keep = selected.get_untracked();
        if id.is_empty() {
            items.set(Vec::new());
            repo.set(String::new());
            list_error.set(String::new());
            list_loading.set(false);
            return;
        }
        list_loading.set(true);
        list_error.set(String::new());
        let gen = list_gen.get_untracked().wrapping_add(1);
        list_gen.set(gen);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = format!(
            "/v1/apps/{}/items?{}",
            api::js_encode(&id),
            queue_query(&filt)
        );
        spawn_local(async move {
            let result = fetch_json::<QueueResponse>(&b, &t, &path).await;
            if list_gen.get_untracked() != gen {
                return;
            }
            match result {
                Ok(resp) => {
                    repo.set(resp.repo);
                    let still = !keep.is_empty() && resp.items.iter().any(|item| item.id == keep);
                    items.set(resp.items);
                    list_error.set(String::new());
                    if !still {
                        selected.set(String::new());
                    } else {
                        detail_reload.update(|n| *n = n.wrapping_add(1));
                    }
                }
                Err(e) => {
                    items.set(Vec::new());
                    repo.set(String::new());
                    list_error.set(e);
                    selected.set(String::new());
                }
            }
            list_loading.set(false);
        });
    });

    Effect::new(move |_| {
        let app = open_id.get();
        let id = selected.get();
        let _ = detail_reload.get();
        if app.is_empty() || id.is_empty() {
            detail.set(None);
            detail_error.set(String::new());
            detail_loading.set(false);
            return;
        }
        detail_loading.set(true);
        detail_error.set(String::new());
        let gen = detail_gen.get_untracked().wrapping_add(1);
        detail_gen.set(gen);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = format!(
            "/v1/apps/{}/items/{}",
            api::js_encode(&app),
            api::js_encode(&id)
        );
        spawn_local(async move {
            let result = fetch_json::<ItemDetail>(&b, &t, &path).await;
            if detail_gen.get_untracked() != gen || selected.get_untracked() != id {
                return;
            }
            match result {
                Ok(found) => {
                    detail.set(Some(found));
                    detail_error.set(String::new());
                }
                Err(e) => {
                    detail.set(None);
                    detail_error.set(e);
                }
            }
            detail_loading.set(false);
        });
    });

    let run_action = move |action: String| {
        if busy.get_untracked() {
            return;
        }
        let app = open_id.get_untracked();
        let id = selected.get_untracked();
        if app.is_empty() || id.is_empty() {
            return;
        }
        let needs_body = detail
            .get_untracked()
            .map(|d| d.actions.iter().any(|a| a.id == action && a.needs_body))
            .unwrap_or(false);
        let text = comment.get_untracked().trim().to_string();
        if needs_body && text.is_empty() {
            action_error.set("Write the comment before sending.".into());
            return;
        }
        action_error.set(String::new());
        busy.set(true);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = format!(
            "/v1/apps/{}/items/{}/act",
            api::js_encode(&app),
            api::js_encode(&id)
        );
        spawn_local(async move {
            let result = post_json_value(
                &b,
                &t,
                &path,
                serde_json::json!({"action": action, "body": text}),
            )
            .await;
            busy.set(false);
            match result {
                Ok(_) => {
                    comment.set(String::new());
                    action_error.set(String::new());
                    status.set("Triage action sent.".into());
                    reload.update(|n| *n = n.wrapping_add(1));
                }
                Err(e) => action_error.set(e),
            }
        });
    };

    let run_handoff = move || {
        if busy.get_untracked() || !open_handoff.get_untracked() {
            return;
        }
        let app = open_id.get_untracked();
        let id = selected.get_untracked();
        if app.is_empty() || id.is_empty() {
            return;
        }
        action_error.set(String::new());
        busy.set(true);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = format!(
            "/v1/apps/{}/items/{}/handoff",
            api::js_encode(&app),
            api::js_encode(&id)
        );
        spawn_local(async move {
            let result = post_json_value(&b, &t, &path, serde_json::json!({})).await;
            busy.set(false);
            match result {
                Ok(v) => {
                    let root = v
                        .get("rootId")
                        .and_then(|value| value.as_str())
                        .unwrap_or("")
                        .to_string();
                    if root.is_empty() {
                        action_error.set("The daemon did not start a session.".into());
                        return;
                    }
                    status.set("Handed off. The new session has this item.".into());
                    open_session(root);
                }
                Err(e) => action_error.set(e),
            }
        });
    };

    let open_app = move |app: AppInfo| {
        launch_i.set(0);
        query.set(String::new());
        filter.set("open-issues".into());
        selected.set(String::new());
        detail.set(None);
        action_error.set(String::new());
        open_title.set(app.title);
        open_handoff.set(app.handoff);
        open_id.set(app.id);
    };

    let epoch = APPS_KEY_EPOCH.with(|cell| {
        let next = cell.get().wrapping_add(1);
        cell.set(next);
        next
    });
    let key_handler =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            if APPS_KEY_EPOCH.with(|cell| cell.get()) != epoch {
                return;
            }
            if palette_open.get_untracked() || ev.default_prevented() {
                return;
            }
            if ev.meta_key() || ev.ctrl_key() || ev.alt_key() {
                return;
            }
            let key = ev.key();
            if key == "Escape" {
                if typing_target(&ev) {
                    if let Some(target) = ev.target() {
                        if let Some(el) = target.dyn_ref::<web_sys::HtmlElement>() {
                            let _ = el.blur();
                        }
                    }
                    return;
                }
                if !open_id.get_untracked().is_empty() {
                    ev.prevent_default();
                    open_id.set(String::new());
                    selected.set(String::new());
                }
                return;
            }
            if typing_target(&ev) {
                return;
            }
            let delta = match key.as_str() {
                "ArrowDown" | "j" => 1,
                "ArrowUp" | "k" => -1,
                "Home" => i32::MIN / 2,
                "End" => i32::MAX / 2,
                _ => 0,
            };
            if delta == 0 {
                if key == "Enter" && open_id.get_untracked().is_empty() {
                    let apps = catalog.get_untracked();
                    if apps.is_empty() {
                        return;
                    }
                    let i = launch_i.get_untracked().min(apps.len() - 1);
                    ev.prevent_default();
                    open_app(apps[i].clone());
                }
                return;
            }
            ev.prevent_default();
            if open_id.get_untracked().is_empty() {
                let n = catalog.get_untracked().len();
                if n == 0 {
                    return;
                }
                let cur = Some(launch_i.get_untracked().min(n - 1));
                launch_i.set(step_index(n, cur, delta));
                return;
            }
            let shown = filter_queue(&items.get_untracked(), &query.get_untracked());
            let next = move_id(&shown, &selected.get_untracked(), delta);
            if !next.is_empty() {
                selected.set(next);
            }
        });
    if let Some(window) = web_sys::window() {
        let _ = window
            .add_event_listener_with_callback("keydown", key_handler.as_ref().unchecked_ref());
    }
    // Closure is not Send, so it cannot live in on_cleanup. A newer mount
    // bumps the epoch and the previous listener returns immediately.
    key_handler.forget();

    let filters = [
        ("open-issues", "Issues"),
        ("open-prs", "Pull requests"),
        ("closed", "Closed"),
    ];

    view! {
        <div class="apps">
            {move || {
                if catalog_loading.get() && open_id.get().is_empty() {
                    return view! {
                        <div class="apps-launch">
                            <div class="skeleton" aria-hidden="true">
                                <div class="skeleton-line"></div>
                                <div class="skeleton-line short"></div>
                            </div>
                        </div>
                    }.into_any();
                }
                if open_id.get().is_empty() {
                    let err = catalog_error.get();
                    let apps = catalog.get();
                    let cursor = launch_i.get();
                    if !err.is_empty() {
                        let retry = load_catalog;
                        return view! {
                            <div class="apps-launch">
                                <h2 class="panel-title">"Apps"</h2>
                                <div class="notice" role="alert">
                                    <span>{err}</span>
                                    <button class="secondary" on:click=move |_| retry()>"Retry"</button>
                                </div>
                            </div>
                        }.into_any();
                    }
                    if apps.is_empty() {
                        return view! {
                            <div class="apps-launch">
                                <div class="empty-state">
                                    <strong>"No apps"</strong>
                                    <p>"This daemon has not registered a queue."</p>
                                </div>
                            </div>
                        }.into_any();
                    }
                    let cards = apps.into_iter().enumerate().map(|(i, app)| {
                        let on = i == cursor;
                        let klass = if on { "apps-card on" } else { "apps-card" };
                        let open = open_app;
                        let chosen = app.clone();
                        let title = app.title.clone();
                        let summary = app.summary.clone();
                        view! {
                            <button class=klass on:click=move |_| open(chosen.clone())>
                                <span class="apps-kicker">"Queue"</span>
                                <span class="name">{title}</span>
                                <span class="path">{summary}</span>
                            </button>
                        }
                    }).collect_view();
                    return view! {
                        <div class="apps-launch">
                            <h2 class="panel-title">"Apps"</h2>
                            <p class="hint">"A queue and a reading pane for work that does not belong in the chat. Cards are the apps this daemon registered."</p>
                            <div class="apps-grid">{cards}</div>
                            <p class="hint keys">"Arrow keys move. Enter opens. Ctrl+K is commands."</p>
                        </div>
                    }.into_any();
                }

                let title = open_title.get();
                let repo_name = repo.get();
                let err = list_error.get();
                let err_empty = err.is_empty();
                let loading = list_loading.get();
                let shown = filter_queue(&items.get(), &query.get());
                let empty = shown.is_empty();
                let current = selected.get();
                let rows = shown.into_iter().map(|item| {
                    let id = item.id.clone();
                    let on = id == current;
                    let klass = if on { "apps-row on" } else { "apps-row" };
                    let label = item.title.clone();
                    let who = item.subtitle.clone();
                    let when = short_when(&item.updated_at);
                    let kind = kind_label(&item.kind).to_string();
                    let number = item.number;
                    let badge = item.badge.clone();
                    let badge_klass = badge_class(&badge);
                    let labels = item.labels.clone();
                    view! {
                        <button
                            class=klass
                            role="option"
                            aria-selected=on
                            on:click=move |_| selected.set(id.clone())
                        >
                            <span class="apps-row-top">
                                <span class="name">{label}</span>
                                <span class=badge_klass>{badge.clone()}</span>
                            </span>
                            <span class="path">
                                {kind}" #"{number}
                                {(!who.is_empty()).then(|| format!(" · {who}"))}
                                {(!when.is_empty()).then(|| format!(" · {when}"))}
                            </span>
                            <span class="apps-labels">
                                {labels.into_iter().take(3).map(|label| view! { <span class="apps-label">{label}</span> }).collect_view()}
                            </span>
                        </button>
                    }
                }).collect_view();
                let err_view = if err.is_empty() {
                    ().into_any()
                } else {
                    view! {
                        <div class="notice" role="alert">
                            <span>{err}</span>
                            <button class="secondary" on:click=move |_| reload.update(|n| *n = n.wrapping_add(1))>"Retry"</button>
                        </div>
                    }.into_any()
                };
                view! {
                    <div class=move || if selected.get().is_empty() { "apps-shell" } else { "apps-shell has-detail" }>
                        <section class="apps-queue" aria-label="Queue">
                            <div class="section-head">
                                <h2 class="panel-title">{title.clone()}</h2>
                                <button class="secondary apps-back" on:click=move |_| {
                                    selected.set(String::new());
                                }>"Queue"</button>
                                <button class="secondary" on:click=move |_| open_id.set(String::new())>"All apps"</button>
                            </div>
                            <p class="hint">{if repo_name.is_empty() { "Repository comes from the daemon.".into() } else { repo_name.clone() }}</p>
                            <div class="chips" role="tablist" aria-label="Queue filter">
                                {filters.into_iter().map(|(id, label)| {
                                    let id = id.to_string();
                                    let label = label.to_string();
                                    let mark = id.clone();
                                    view! {
                                        <button class=move || if filter.get() == mark { "chip on" } else { "chip" } on:click=move |_| filter.set(id.clone())>{label}</button>
                                    }
                                }).collect_view()}
                            </div>
                            <input class="search" placeholder="Filter this queue" prop:value=move || query.get() on:input=move |ev| query.set(event_target_value(&ev)) />
                            {err_view}
                            <Show when=move || loading>
                                <div class="skeleton" aria-hidden="true">
                                    <div class="skeleton-line"></div>
                                    <div class="skeleton-line short"></div>
                                </div>
                            </Show>
                            <Show when=move || !loading && err_empty && empty>
                                <div class="empty-state">
                                    <strong>"Nothing in this queue"</strong>
                                    <p>"gh returned no rows for this filter."</p>
                                </div>
                            </Show>
                            <div class="apps-rows" role="listbox" aria-label="Items">{rows}</div>
                            <p class="hint keys">"Arrow keys move. Esc returns to Apps. Esc in a field leaves the field."</p>
                        </section>
                        <section class="apps-read" aria-label="Reading pane">
                            {move || {
                                if detail_loading.get() && detail.get().is_none() {
                                    return view! {
                                        <div class="skeleton" aria-hidden="true">
                                            <div class="skeleton-line"></div>
                                            <div class="skeleton-line short"></div>
                                        </div>
                                    }.into_any();
                                }
                                let derr = detail_error.get();
                                if !derr.is_empty() && detail.get().is_none() {
                                    return view! {
                                        <div class="notice" role="alert"><span>{derr}</span></div>
                                    }.into_any();
                                }
                                let Some(found) = detail.get() else {
                                    return view! {
                                        <div class="empty-state">
                                            <strong>"Select an item"</strong>
                                            <p>"The reading pane shows the issue or pull request gh returned."</p>
                                        </div>
                                    }.into_any();
                                };
                                let item_title = found.item.title.clone();
                                let item_url = found.item.url.clone();
                                let item_body = found.body.clone();
                                let item_badge = found.item.badge.clone();
                                let badge_klass = badge_class(&item_badge);
                                let kind = kind_label(&found.item.kind).to_string();
                                let number = found.item.number;
                                let who = found.item.subtitle.clone();
                                let labels = found.item.labels.clone();
                                let notes = found.comments.clone();
                                let actions = found.actions.clone();
                                let needs_body = actions.iter().any(|a| a.needs_body);
                                let handoff = open_handoff.get();
                                let notes_block = if notes.is_empty() {
                                    ().into_any()
                                } else {
                                    let note_views = notes.into_iter().map(|note| {
                                        let author = if note.author.is_empty() { "comment".to_string() } else { note.author };
                                        let when = short_when(&note.at);
                                        let body = note.body;
                                        view! {
                                            <li class="apps-note">
                                                <span class="who">{author}{if when.is_empty() { String::new() } else { format!(" · {when}") }}</span>
                                                <p class="apps-body">{body}</p>
                                            </li>
                                        }
                                    }).collect_view();
                                    view! {
                                        <div class="apps-notes">
                                            <h3 class="apps-kicker">"Comments"</h3>
                                            <ul class="plain">{note_views}</ul>
                                        </div>
                                    }.into_any()
                                };
                                let action_views = actions.into_iter().map(|action| {
                                    let id = action.id.clone();
                                    let label = action.label.clone();
                                    let klass = if action.id == "close" { "secondary apps-close" } else { "secondary" };
                                    let run = run_action;
                                    view! {
                                        <button class=klass disabled=move || busy.get() on:click=move |_| run(id.clone())>{label}</button>
                                    }
                                }).collect_view();
                                view! {
                                    <article class="apps-article">
                                        <header class="apps-read-head">
                                            <button class="secondary apps-back" on:click=move |_| selected.set(String::new())>"Queue"</button>
                                            <h2 class="apps-title">{item_title}</h2>
                                            <p class="path">
                                                <span class=badge_klass>{item_badge}</span>
                                                {" "}{kind}" #"{number}
                                                {(!who.is_empty()).then(|| format!(" · {who}"))}
                                            </p>
                                            <p class="apps-labels">
                                                {labels.into_iter().map(|label| view! { <span class="apps-label">{label}</span> }).collect_view()}
                                            </p>
                                            {(!item_url.is_empty()).then(|| view! {
                                                <a class="apps-link" href=item_url.clone() target="_blank" rel="noreferrer noopener">{item_url.clone()}</a>
                                            })}
                                        </header>
                                        <div class="apps-read-body">
                                            <p class="apps-body">{item_body}</p>
                                            {notes_block}
                                        </div>
                                        <div class="apps-actions">
                                            <Show when=move || !action_error.get().is_empty()>
                                                <div class="notice" role="alert"><span>{move || action_error.get()}</span></div>
                                            </Show>
                                            <Show when=move || needs_body>
                                                <textarea
                                                    class="apps-comment"
                                                    placeholder="Comment on this item"
                                                    prop:value=move || comment.get()
                                                    on:input=move |ev| comment.set(event_target_value(&ev))
                                                ></textarea>
                                            </Show>
                                            <div class="apps-action-row">
                                                {action_views}
                                                <Show when=move || handoff>
                                                    <button class="primary" disabled=move || busy.get() || selected.get().is_empty() on:click=move |_| run_handoff()>"Hand off to an agent"</button>
                                                </Show>
                                            </div>
                                            <p class="hint">"Hand off starts a session with this item. The browser does not call GitHub."</p>
                                        </div>
                                    </article>
                                }.into_any()
                            }}
                        </section>
                    </div>
                }.into_any()
            }}
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn item(id: &str, title: &str, subtitle: &str) -> QueueItem {
        QueueItem {
            id: id.into(),
            title: title.into(),
            subtitle: subtitle.into(),
            badge: "open".into(),
            updated_at: String::new(),
            labels: vec!["webui".into()],
            url: String::new(),
            kind: "issue".into(),
            number: 10,
        }
    }

    #[test]
    fn filter_matches_title_author_and_label() {
        let items = vec![
            item("issue-10", "Palette", "ada"),
            item("issue-2", "Docs", "grace"),
        ];
        assert_eq!(filter_queue(&items, "  ").len(), 2);
        assert_eq!(filter_queue(&items, "PAL")[0].id, "issue-10");
        assert_eq!(filter_queue(&items, "grace")[0].id, "issue-2");
        assert_eq!(filter_queue(&items, "webui").len(), 2);
        assert!(filter_queue(&items, "missing").is_empty());
    }

    #[test]
    fn step_clamps_and_starts_from_either_end() {
        assert_eq!(step_index(0, None, 1), 0);
        assert_eq!(step_index(4, None, 1), 0);
        assert_eq!(step_index(4, None, -1), 3);
        assert_eq!(step_index(4, Some(0), -1), 0);
        assert_eq!(step_index(4, Some(3), 1), 3);
        assert_eq!(step_index(4, Some(1), 1), 2);
        let items = vec![item("a", "A", ""), item("b", "B", "")];
        assert_eq!(move_id(&items, "", 1), "a");
        assert_eq!(move_id(&items, "a", 1), "b");
        assert_eq!(move_id(&items, "b", 1), "b");
    }

    #[test]
    fn queue_query_maps_the_three_chips() {
        assert_eq!(queue_query("open-issues"), "kind=issue&state=open");
        assert_eq!(queue_query("open-prs"), "kind=pr&state=open");
        assert_eq!(queue_query("closed"), "kind=all&state=closed");
        assert_eq!(queue_query("other"), "kind=issue&state=open");
    }

    #[test]
    fn detail_json_flattens_onto_the_item() {
        let raw = r#"{"id":"issue-10","title":"Palette","subtitle":"ada","badge":"open","number":10,"kind":"issue","body":"hello","comments":[{"author":"grace","body":"ok"}],"actions":[{"id":"comment","label":"Comment","needsBody":true}]}"#;
        let detail: ItemDetail = serde_json::from_str(raw).unwrap();
        assert_eq!(detail.item.id, "issue-10");
        assert_eq!(detail.item.number, 10);
        assert_eq!(detail.body, "hello");
        assert_eq!(detail.comments.len(), 1);
        assert!(detail.actions[0].needs_body);
    }
}
