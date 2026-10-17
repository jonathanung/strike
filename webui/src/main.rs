//! Strike web UI skeleton (Leptos CSR + PWA).
//! One page: connect to a daemon (base URL + bearer token), check health,
//! and pick a workspace. Chat/session views land on top of /v1/ws + /v1/ops.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Serialize, Deserialize)]
struct Workspace {
    id: String,
    name: String,
    path: String,
}

#[derive(Clone, Debug, Deserialize)]
struct WorkspacesResponse {
    workspaces: Vec<Workspace>,
    #[serde(default)]
    #[serde(rename = "activeId")]
    active_id: String,
}

#[derive(Clone, Debug, Deserialize)]
struct Health {
    ok: bool,
    version: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
struct Remote {
    name: String,
    url: String,
}

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

fn load(key: &str) -> String {
    storage()
        .and_then(|s| s.get_item(key).ok().flatten())
        .unwrap_or_default()
}

fn save(key: &str, value: &str) {
    if let Some(s) = storage() {
        let _ = s.set_item(key, value);
    }
}

/// Join a daemon base ("" = same origin) with an API path.
fn api(base: &str, path: &str) -> String {
    format!("{}{}", base.trim_end_matches('/'), path)
}

async fn fetch_health(base: String) -> Result<Health, String> {
    gloo_net::http::Request::get(&api(&base, "/health"))
        .send()
        .await
        .map_err(|e| e.to_string())?
        .json::<Health>()
        .await
        .map_err(|e| e.to_string())
}

async fn fetch_workspaces(base: String, token: String) -> Result<WorkspacesResponse, String> {
    let resp = gloo_net::http::Request::get(&api(&base, "/v1/workspaces"))
        .header("Authorization", &format!("Bearer {token}"))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if resp.status() == 401 {
        return Err("unauthorized: check the daemon token".into());
    }
    resp.json::<WorkspacesResponse>()
        .await
        .map_err(|e| e.to_string())
}

async fn post_json(base: String, token: String, path: &str, body: serde_json::Value) -> Result<(), String> {
    let resp = gloo_net::http::Request::post(&api(&base, path))
        .header("Authorization", &format!("Bearer {token}"))
        .json(&body)
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if resp.ok() {
        Ok(())
    } else {
        Err(format!("HTTP {}", resp.status()))
    }
}

fn load_remotes() -> Vec<Remote> {
    serde_json::from_str(&load("strike.remotes")).unwrap_or_default()
}

#[component]
fn App() -> impl IntoView {
    let base = RwSignal::new(load("strike.base"));
    let token = RwSignal::new(load("strike.token"));
    let status = RwSignal::new("not connected yet".to_string());
    let workspaces = RwSignal::new(Vec::<Workspace>::new());
    let active_id = RwSignal::new(String::new());
    let new_path = RwSignal::new(String::new());
    let remotes = RwSignal::new(load_remotes());
    let remote_name = RwSignal::new(String::new());
    let remote_url = RwSignal::new(String::new());

    let refresh = move || {
        let (b, t) = (base.get(), token.get());
        save("strike.base", &b);
        save("strike.token", &t);
        spawn_local(async move {
            match fetch_health(b.clone()).await {
                Ok(h) if h.ok => status.set(format!("daemon ok · strike {}", h.version)),
                Ok(_) => status.set("daemon answered but not ok".into()),
                Err(e) => status.set(format!("health failed: {e}")),
            }
            match fetch_workspaces(b, t).await {
                Ok(r) => {
                    active_id.set(r.active_id);
                    workspaces.set(r.workspaces);
                }
                Err(e) => status.set(format!("workspaces failed: {e}")),
            }
        });
    };

    // Connect on first load with any saved settings.
    refresh();

    let add_workspace = move |_| {
        let path = new_path.get();
        if path.trim().is_empty() {
            return;
        }
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match post_json(b.clone(), t.clone(), "/v1/workspaces", serde_json::json!({ "path": path })).await {
                Ok(()) => {
                    new_path.set(String::new());
                    if let Ok(r) = fetch_workspaces(b, t).await {
                        active_id.set(r.active_id);
                        workspaces.set(r.workspaces);
                    }
                }
                Err(e) => status.set(format!("add workspace failed: {e}")),
            }
        });
    };

    let add_remote = move |_| {
        let (name, url) = (remote_name.get(), remote_url.get());
        if name.trim().is_empty() || url.trim().is_empty() {
            return;
        }
        remotes.update(|list| list.push(Remote { name, url }));
        save("strike.remotes", &serde_json::to_string(&remotes.get()).unwrap_or_default());
        remote_name.set(String::new());
        remote_url.set(String::new());
    };

    view! {
        <main>
            <h1>"Strike"</h1>
            <p class="sub">"Pick a daemon and a workspace. Chat lands here next; the engine API is already live."</p>

            <h2>"Daemon"</h2>
            <label>"Base URL (blank = this daemon)"</label>
            <input placeholder="http://100.64.0.5:8787"
                prop:value=move || base.get()
                on:input=move |ev| base.set(event_target_value(&ev)) />
            <label>"Token (strike daemon token)"</label>
            <input type="password" autocomplete="off"
                prop:value=move || token.get()
                on:input=move |ev| token.set(event_target_value(&ev)) />
            <button on:click=move |_| refresh()>"Connect"</button>
            <div class="status">{move || status.get()}</div>

            <h2>"My daemons"</h2>
            <p class="sub">"Saved in this browser only. Each entry is one of your machines running strike serve."</p>
            <ul>
                {move || remotes.get().into_iter().map(|r| {
                    let url = r.url.clone();
                    view! {
                        <li>
                            <span>{r.name}<div class="path">{r.url}</div></span>
                            <button class="secondary" on:click=move |_| { base.set(url.clone()); refresh(); }>"Use"</button>
                        </li>
                    }
                }).collect_view()}
            </ul>
            <label>"Name"</label>
            <input prop:value=move || remote_name.get()
                on:input=move |ev| remote_name.set(event_target_value(&ev)) />
            <label>"URL"</label>
            <div class="row">
                <input placeholder="http://desktop.tailnet:8787"
                    prop:value=move || remote_url.get()
                    on:input=move |ev| remote_url.set(event_target_value(&ev)) />
                <button class="secondary" on:click=add_remote>"Save"</button>
            </div>

            <h2>"Workspaces"</h2>
            <Show when=move || workspaces.get().is_empty()>
                <p class="empty">"No workspaces yet. Add a directory on the daemon host below."</p>
            </Show>
            <ul>
                {move || workspaces.get().into_iter().map(|w| {
                    let id = w.id.clone();
                    let is_active = move || active_id.get() == id;
                    let activate_id = w.id.clone();
                    view! {
                        <li>
                            <span>
                                {w.name}
                                <Show when=is_active>" "<span class="active-tag">"active"</span></Show>
                                <div class="path">{w.path}</div>
                            </span>
                            <button class="secondary" on:click=move |_| {
                                let (b, t, id) = (base.get(), token.get(), activate_id.clone());
                                spawn_local(async move {
                                    let path = format!("/v1/workspaces/{id}/activate");
                                    match post_json(b.clone(), t.clone(), &path, serde_json::json!({})).await {
                                        Ok(()) => active_id.set(id),
                                        Err(e) => status.set(format!("activate failed: {e}")),
                                    }
                                });
                            }>"Open"</button>
                        </li>
                    }
                }).collect_view()}
            </ul>
            <label>"Add workspace (path on the daemon host)"</label>
            <div class="row">
                <input placeholder="/home/you/Projects/my-repo"
                    prop:value=move || new_path.get()
                    on:input=move |ev| new_path.set(event_target_value(&ev)) />
                <button on:click=add_workspace>"Add"</button>
            </div>
        </main>
    }
}

fn main() {
    leptos::mount::mount_to_body(App);
}
