//! Workspace picker. A workspace is a directory on the daemon. Session
//! folders, if the client grows them, only organise chats.

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::api::{js_encode, patch_json, post_json, post_json_value};
use crate::model::{mode_label, permission_modes, BrowseResponse, Workspace};

fn event_tag(ev: &web_sys::KeyboardEvent) -> String {
    ev.target()
        .and_then(|t| {
            js_sys::Reflect::get(&t, &wasm_bindgen::JsValue::from_str("tagName"))
                .ok()
                .and_then(|v| v.as_string())
        })
        .unwrap_or_default()
        .to_ascii_lowercase()
}

/// Extra line when the open chat is not already sitting in the workspace.
pub fn chip_note(ws_path: &str, cwd: &str) -> String {
    if ws_path.is_empty() || cwd.is_empty() || cwd == ws_path {
        return String::new();
    }
    if cwd.starts_with(&format!("{ws_path}/")) {
        return format!("Open chat is in {cwd}.");
    }
    format!("Open chat is in {cwd}. New sessions start in the workspace.")
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PolicyFields {
    pub rules: String,
    pub mode: String,
    pub roots: String,
}

/// What the policy effect should do for the active workspace.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PolicyLoad {
    /// The form already holds this workspace. Leave the fields alone.
    Latched,
    /// The list has the workspace. Copy its stored policy and latch that id.
    Hit(PolicyFields),
    /// The list does not have it yet. Do not blank the fields and do not latch,
    /// so a later list can still fill the form.
    Miss,
}

pub fn load_policy(latched_for: &str, id: &str, found: Option<&Workspace>) -> PolicyLoad {
    if latched_for == id {
        return PolicyLoad::Latched;
    }
    let Some(w) = found else {
        return PolicyLoad::Miss;
    };
    if w.id != id {
        return PolicyLoad::Miss;
    }
    PolicyLoad::Hit(PolicyFields {
        rules: w.rules.clone(),
        mode: w.permission_mode.clone(),
        roots: w.extra_roots.join("\n"),
    })
}

/// Save is refused until a hit has latched the active id. An empty id has
/// no workspace to write. A miss must not PATCH the blank form.
pub fn policy_save_block(latched_for: &str, id: &str) -> Option<&'static str> {
    if id.is_empty() {
        return Some("Open a workspace before saving its rules.");
    }
    if latched_for != id {
        return Some("Workspace rules are still loading.");
    }
    None
}

/// Confirm when the save sets a dangerous mode or leaves a stored one.
/// Clearing yolo by saving a blank form is the same weakening as turning it on.
pub fn policy_change_message(submitted: &str, stored: &str) -> Option<String> {
    if submitted == stored {
        return None;
    }
    let setting = crate::approvals::is_dangerous(submitted);
    let leaving = crate::approvals::is_dangerous(stored);
    if !setting && !leaving {
        return None;
    }
    let from = if stored.is_empty() {
        "Unset"
    } else {
        mode_label(stored)
    };
    let to = if submitted.is_empty() {
        "Unset"
    } else {
        mode_label(submitted)
    };
    Some(format!(
        "Change this workspace from {from} to {to}? This does not change the live session now. Deny rules still apply. Opening the workspace applies its stored mode to the live session."
    ))
}

fn picker_rows(recents: &[Workspace], all: &[Workspace]) -> Vec<Workspace> {
    let mut out = Vec::new();
    let mut seen = std::collections::HashSet::new();
    for w in recents.iter().chain(all.iter()) {
        if seen.insert(w.id.clone()) {
            out.push(w.clone());
        }
    }
    out
}

/// Case-insensitive substring match on the fields a picker row renders:
/// name and path. An empty (or whitespace-only) query matches everything.
/// Path matching is what tells identically-named checkouts apart (#1437 Q1).
pub fn workspace_matches(w: &Workspace, query: &str) -> bool {
    let q = query.trim().to_lowercase();
    q.is_empty() || w.name.to_lowercase().contains(&q) || w.path.to_lowercase().contains(&q)
}

/// The one filtered list render and keyboard navigation share (#1437):
/// `picker_rows` deduped (recents first), then filtered by the query.
pub fn picker_rows_matching(
    recents: &[Workspace],
    all: &[Workspace],
    query: &str,
) -> Vec<Workspace> {
    picker_rows(recents, all)
        .into_iter()
        .filter(|w| workspace_matches(w, query))
        .collect()
}

/// Filter one rendered section (Recents or All) by the same rule.
pub fn filtered_workspaces(list: &[Workspace], query: &str) -> Vec<Workspace> {
    list.iter()
        .filter(|w| workspace_matches(w, query))
        .cloned()
        .collect()
}

/// Browse rows render only the entry name; match that plus the path the
/// click would navigate to (optional slice 2 of #1437).
pub fn browse_matches(e: &crate::model::BrowseEntry, query: &str) -> bool {
    let q = query.trim().to_lowercase();
    q.is_empty() || e.name.to_lowercase().contains(&q) || e.path.to_lowercase().contains(&q)
}

pub fn filtered_browse_entries(
    list: &[crate::model::BrowseEntry],
    query: &str,
) -> Vec<crate::model::BrowseEntry> {
    list.iter()
        .filter(|e| browse_matches(e, query))
        .cloned()
        .collect()
}

fn fetch_browse(
    base: String,
    token: String,
    path: String,
    browse_path: RwSignal<String>,
    browse_parent: RwSignal<String>,
    browse_entries: RwSignal<Vec<crate::model::BrowseEntry>>,
    browse_error: RwSignal<String>,
) {
    spawn_local(async move {
        let url = if path.is_empty() {
            "/v1/workspaces/browse".to_string()
        } else {
            format!("/v1/workspaces/browse?path={}", js_encode(&path))
        };
        let resp = gloo_net::http::Request::get(&crate::api::api(&base, &url))
            .header("Authorization", &crate::api::auth_header(&token))
            .send()
            .await;
        match resp {
            Ok(r) if r.ok() => match r.json::<BrowseResponse>().await {
                Ok(body) => {
                    browse_path.set(body.path);
                    browse_parent.set(body.parent);
                    browse_entries.set(body.entries);
                    browse_error.set(String::new());
                }
                Err(e) => browse_error.set(e.to_string()),
            },
            Ok(r) => {
                let status = r.status();
                let text = r.text().await.unwrap_or_default();
                let msg = serde_json::from_str::<serde_json::Value>(&text)
                    .ok()
                    .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
                    .filter(|s| !s.is_empty())
                    .unwrap_or_else(|| format!("HTTP {status}"));
                browse_error.set(msg);
            }
            Err(e) => browse_error.set(e.to_string()),
        }
    });
}

#[component]
pub fn WorkspacePicker(
    open: RwSignal<bool>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    workspaces: RwSignal<Vec<Workspace>>,
    active_workspace: RwSignal<String>,
    recents: RwSignal<Vec<Workspace>>,
    refresh: impl Fn() + Copy + Send + Sync + 'static,
) -> impl IntoView {
    let browse_path = RwSignal::new(String::new());
    let browse_parent = RwSignal::new(String::new());
    let browse_entries = RwSignal::new(Vec::<crate::model::BrowseEntry>::new());
    let browse_error = RwSignal::new(String::new());
    let new_folder = RwSignal::new(String::new());
    let jump = RwSignal::new(String::new());
    let filter = RwSignal::new(String::new());
    let browse_filter = RwSignal::new(String::new());
    let cursor = RwSignal::new(0usize);
    let renaming = RwSignal::new(String::new());
    let rename_value = RwSignal::new(String::new());
    let confirm_remove = RwSignal::new(String::new());
    let policy_rules = RwSignal::new(String::new());
    let policy_mode = RwSignal::new(String::new());
    let policy_roots = RwSignal::new(String::new());
    let policy_for = RwSignal::new(String::new());
    let note = RwSignal::new(String::new());

    Effect::new(move |_| {
        if !open.get() {
            return;
        }
        // No persistence (#1437 slice 1): every open starts unfiltered,
        // with the cursor back on the first row.
        filter.set(String::new());
        browse_filter.set(String::new());
        cursor.set(0);
        fetch_browse(
            base.get(),
            token.get(),
            String::new(),
            browse_path,
            browse_parent,
            browse_entries,
            browse_error,
        );
    });

    Effect::new(move |_| {
        let id = active_workspace.get();
        let list = workspaces.get();
        let found = list.iter().find(|w| w.id == id);
        match load_policy(&policy_for.get_untracked(), &id, found) {
            PolicyLoad::Latched | PolicyLoad::Miss => {}
            PolicyLoad::Hit(fields) => {
                policy_rules.set(fields.rules);
                policy_mode.set(fields.mode);
                policy_roots.set(fields.roots);
                policy_for.set(id);
            }
        }
    });

    let go = move |path: String| {
        // A filter typed for one directory must not hide the next one's
        // entries; navigation starts each folder unfiltered.
        browse_filter.set(String::new());
        fetch_browse(
            base.get(),
            token.get(),
            path,
            browse_path,
            browse_parent,
            browse_entries,
            browse_error,
        );
    };

    let activate = move |id: String| {
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match post_json(
                &b,
                &t,
                &format!("/v1/workspaces/{id}/activate"),
                serde_json::json!({}),
            )
            .await
            {
                Ok(()) => {
                    active_workspace.set(id);
                    policy_for.set(String::new());
                    status.set("Workspace opened. New sessions start there.".into());
                    refresh();
                }
                Err(e) => status.set(format!("Could not open workspace: {e}")),
            }
        });
    };

    let use_folder = move |path: String| {
        if path.trim().is_empty() {
            status.set("Open a folder first.".into());
            return;
        }
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match post_json_value(
                &b,
                &t,
                "/v1/workspaces",
                serde_json::json!({ "path": path }),
            )
            .await
            {
                Ok(v) => {
                    let id = v
                        .get("id")
                        .and_then(|x| x.as_str())
                        .unwrap_or("")
                        .to_string();
                    if id.is_empty() {
                        note.set("Workspace added.".into());
                        status.set("Workspace added.".into());
                        refresh();
                        return;
                    }
                    match post_json(
                        &b,
                        &t,
                        &format!("/v1/workspaces/{id}/activate"),
                        serde_json::json!({}),
                    )
                    .await
                    {
                        Ok(()) => {
                            active_workspace.set(id);
                            policy_for.set(String::new());
                            note.set("Workspace opened. New sessions start there.".into());
                            status.set("Workspace opened. New sessions start there.".into());
                            refresh();
                        }
                        Err(e) => {
                            note.set(format!("Added, but could not open it: {e}"));
                            status.set(format!("Added, but could not open it: {e}"));
                            refresh();
                        }
                    }
                }
                Err(e) => {
                    browse_error.set(e.clone());
                    note.set(e.clone());
                    status.set(format!("Could not add workspace: {e}"));
                }
            }
        });
    };

    let make_folder = move |_| {
        let parent = browse_path.get();
        let name = new_folder.get();
        if parent.is_empty() {
            browse_error.set("Open a folder before creating one.".into());
            return;
        }
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match post_json(
                &b,
                &t,
                "/v1/workspaces/mkdir",
                serde_json::json!({ "parent": parent, "name": name }),
            )
            .await
            {
                Ok(()) => {
                    new_folder.set(String::new());
                    browse_error.set(String::new());
                    fetch_browse(
                        b,
                        t,
                        parent,
                        browse_path,
                        browse_parent,
                        browse_entries,
                        browse_error,
                    );
                }
                Err(e) => browse_error.set(e),
            }
        });
    };

    let save_policy = move |_| {
        let id = active_workspace.get();
        if let Some(msg) = policy_save_block(&policy_for.get(), &id) {
            status.set(msg.into());
            return;
        }
        let rules = policy_rules.get();
        let mode = policy_mode.get();
        let roots: Vec<String> = policy_roots
            .get()
            .lines()
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .map(str::to_string)
            .collect();
        let stored = workspaces
            .get()
            .into_iter()
            .find(|w| w.id == id)
            .map(|w| w.permission_mode)
            .unwrap_or_default();
        if let Some(msg) = policy_change_message(&mode, &stored) {
            let ok = web_sys::window()
                .and_then(|w| w.confirm_with_message(&msg).ok())
                .unwrap_or(false);
            if !ok {
                status.set("workspace mode left unchanged".into());
                return;
            }
        }
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match patch_json(
                &b,
                &t,
                &format!("/v1/workspaces/{id}"),
                serde_json::json!({
                    "rules": rules,
                    "permissionMode": mode,
                    "extraRoots": roots,
                }),
            )
            .await
            {
                Ok(()) => {
                    policy_for.set(String::new());
                    status.set("Workspace rules saved.".into());
                    refresh();
                }
                Err(e) => status.set(format!("Could not save workspace: {e}")),
            }
        });
    };

    let do_rename = move |id: String| {
        let name = rename_value.get();
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match patch_json(
                &b,
                &t,
                &format!("/v1/workspaces/{id}"),
                serde_json::json!({ "name": name }),
            )
            .await
            {
                Ok(()) => {
                    renaming.set(String::new());
                    refresh();
                }
                Err(e) => status.set(format!("Could not rename workspace: {e}")),
            }
        });
    };

    let do_remove = move |id: String| {
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            let resp = gloo_net::http::Request::delete(&crate::api::api(
                &b,
                &format!("/v1/workspaces/{id}"),
            ))
            .header("Authorization", &crate::api::auth_header(&t))
            .send()
            .await;
            match resp {
                Ok(r) if r.ok() => {
                    confirm_remove.set(String::new());
                    if active_workspace.get_untracked() == id {
                        active_workspace.set(String::new());
                    }
                    status.set(
                        "Workspace removed from the registry. The directory is still on disk."
                            .into(),
                    );
                    refresh();
                }
                Ok(r) => status.set(format!("Could not remove workspace: HTTP {}", r.status())),
                Err(e) => status.set(format!("Could not remove workspace: {e}")),
            }
        });
    };

    view! {
        <Show when=move || open.get()>
            <div class="ws-back" on:click=move |_| open.set(false)>
                <div
                    class="ws-dialog"
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="ws-title"
                    tabindex="0"
                    autofocus=true
                    on:click=move |ev| ev.stop_propagation()
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        let tag = event_tag(&ev);
                        if tag == "input" || tag == "textarea" || tag == "select" {
                            if ev.key() == "Escape" {
                                ev.prevent_default();
                                // The dialog consumed this Escape; keep the
                                // window handlers from also dismissing
                                // whatever sits behind it (#1475, #1476).
                                ev.stop_propagation();
                                open.set(false);
                            }
                            return;
                        }
                        let rows = picker_rows_matching(&recents.get(), &workspaces.get(), &filter.get());
                        match ev.key().as_str() {
                            "Escape" => {
                                ev.prevent_default();
                                ev.stop_propagation();
                                open.set(false);
                            }
                            "ArrowDown" => {
                                ev.prevent_default();
                                if !rows.is_empty() {
                                    cursor.update(|i| *i = (*i + 1) % rows.len());
                                }
                            }
                            "ArrowUp" => {
                                ev.prevent_default();
                                if !rows.is_empty() {
                                    cursor.update(|i| {
                                        if *i == 0 { *i = rows.len() - 1; } else { *i -= 1; }
                                    });
                                }
                            }
                            "Enter" => {
                                ev.prevent_default();
                                let i = cursor.get();
                                if let Some(w) = rows.get(i) {
                                    activate(w.id.clone());
                                }
                            }
                            _ => {}
                        }
                    }
                >
                    <header class="ws-head">
                        <h2 id="ws-title" class="panel-title">"Workspaces"</h2>
                        <button class="secondary" on:click=move |_| open.set(false)>"Close"</button>
                    </header>
                    <p class="hint">"A workspace is a directory on this daemon. New sessions start there, load its rules file, and begin in its stored approval mode. Sessions in the list are chats. They are not folders, and picking a workspace does not file them into one."</p>
                    <Show when=move || !note.get().is_empty()>
                        <p class="notice" role="status">{move || note.get()}</p>
                    </Show>
                    <input
                        data-workspace-filter=""
                        placeholder="Filter workspaces by name or path"
                        aria-label="Filter workspaces"
                        prop:value=move || filter.get()
                        on:input=move |ev| {
                            filter.set(event_target_value(&ev));
                            // Q2 (#1437): the cursor resets to the first
                            // filtered row whenever the query changes.
                            cursor.set(0);
                        }
                        on:keydown=move |ev: web_sys::KeyboardEvent| {
                            // The dialog handler returns early for input
                            // targets, so the filter handles its own
                            // Arrow/Enter over the same filtered list.
                            // Handled keys stop here so the dialog does not
                            // also act; Escape is left to bubble to the
                            // dialog's input-target Escape close.
                            let rows = picker_rows_matching(&recents.get(), &workspaces.get(), &filter.get());
                            match ev.key().as_str() {
                                "ArrowDown" => {
                                    ev.prevent_default();
                                    ev.stop_propagation();
                                    if !rows.is_empty() {
                                        cursor.update(|i| *i = (*i + 1) % rows.len());
                                    }
                                }
                                "ArrowUp" => {
                                    ev.prevent_default();
                                    ev.stop_propagation();
                                    if !rows.is_empty() {
                                        cursor.update(|i| {
                                            if *i == 0 { *i = rows.len() - 1; } else { *i -= 1; }
                                        });
                                    }
                                }
                                "Enter" => {
                                    ev.prevent_default();
                                    ev.stop_propagation();
                                    if let Some(w) = rows.get(cursor.get()) {
                                        activate(w.id.clone());
                                    }
                                }
                                _ => {}
                            }
                        }
                    />
                    <div class="ws-cols">
                        <section>
                            <h3 class="ws-kicker">"Recent"</h3>
                            <Show when=move || recents.get().is_empty() && filter.get().trim().is_empty()>
                                <p class="hint">"Opened workspaces show up here."</p>
                            </Show>
                            <ul class="plain">
                                {move || filtered_workspaces(&recents.get(), &filter.get()).into_iter().map(|w| {
                                    let id = w.id.clone();
                                    let name = w.name.clone();
                                    let path = w.path.clone();
                                    view! {
                                        <li>
                                            <button class="link ws-row" on:click=move |_| activate(id.clone())>
                                                <span class="name">{name}</span>
                                                <span class="path">{path}</span>
                                            </button>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                            <h3 class="ws-kicker">"All workspaces"</h3>
                            <Show when=move || workspaces.get().is_empty() && filter.get().trim().is_empty()>
                                <p class="hint">"None yet. Browse on the right and use the folder."</p>
                            </Show>
                            <Show when=move || !filter.get().trim().is_empty() && picker_rows_matching(&recents.get(), &workspaces.get(), &filter.get()).is_empty()>
                                <p class="hint" data-workspace-filter-empty="">{move || format!("No workspaces match \"{}\".", filter.get().trim())}</p>
                            </Show>
                            <ul class="plain">
                                {move || {
                                    let rows = picker_rows_matching(&recents.get(), &workspaces.get(), &filter.get());
                                    let cur = cursor.get();
                                    filtered_workspaces(&workspaces.get(), &filter.get()).into_iter().enumerate().map(|(i, w)| {
                                        let id = w.id.clone();
                                        let id_open = id.clone();
                                        let id_save = id.clone();
                                        let id_rename_start = id.clone();
                                        let id_remove = id.clone();
                                        let id_remove_ask = id.clone();
                                        let name = w.name.clone();
                                        let name_edit = name.clone();
                                        let path = w.path.clone();
                                        let on = active_workspace.get() == id;
                                        let selected = rows.iter().position(|r| r.id == id).unwrap_or(i) == cur;
                                        let klass = match (on, selected) {
                                            (true, true) => "ws-row card on key",
                                            (true, false) => "ws-row card on",
                                            (false, true) => "ws-row card key",
                                            (false, false) => "ws-row card",
                                        };
                                        let editing = renaming.get() == id;
                                        let confirming = confirm_remove.get() == id;
                                        let rename_ctl = if editing {
                                            view! {
                                                <span class="ws-inline">
                                                    <input
                                                        prop:value=move || rename_value.get()
                                                        on:input=move |ev| rename_value.set(event_target_value(&ev))
                                                    />
                                                    <button class="secondary" on:click=move |_| do_rename(id_save.clone())>"Save name"</button>
                                                </span>
                                            }.into_any()
                                        } else {
                                            view! {
                                                <button class="secondary" on:click=move |_| {
                                                    rename_value.set(name_edit.clone());
                                                    renaming.set(id_rename_start.clone());
                                                }>"Rename"</button>
                                            }.into_any()
                                        };
                                        let remove_ctl = if confirming {
                                            view! {
                                                <button class="secondary" on:click=move |_| do_remove(id_remove.clone())>"Confirm remove"</button>
                                            }.into_any()
                                        } else {
                                            view! {
                                                <button class="secondary" on:click=move |_| confirm_remove.set(id_remove_ask.clone())>"Remove"</button>
                                            }.into_any()
                                        };
                                        view! {
                                            <li class=klass>
                                                <button class="link" on:click=move |_| activate(id_open.clone())>
                                                    <span class="name">{name}</span>
                                                    <span class="path">{path}</span>
                                                </button>
                                                {rename_ctl}
                                                {remove_ctl}
                                            </li>
                                        }
                                    }).collect_view()
                                }}
                            </ul>
                        </section>
                        <section>
                            <h3 class="ws-kicker">"Browse"</h3>
                            <p class="hint">"Folders stay inside the daemon's allowed roots. A symlink that leaves them is not listed."</p>
                            <p class="path">{move || {
                                let p = browse_path.get();
                                if p.is_empty() { "Allowed roots".into() } else { p }
                            }}</p>
                            <div class="ws-browse-actions">
                                <button class="secondary" on:click=move |_| go(String::new())>"Allowed roots"</button>
                                <button class="secondary" disabled=move || browse_parent.get().is_empty() && browse_path.get().is_empty() on:click=move |_| go(browse_parent.get())>"Up"</button>
                                <button class="primary" disabled=move || browse_path.get().is_empty() on:click=move |_| use_folder(browse_path.get())>"Use this folder"</button>
                            </div>
                            <input
                                data-workspace-browse-filter=""
                                placeholder="Filter folders in this directory"
                                aria-label="Filter folders"
                                prop:value=move || browse_filter.get()
                                on:input=move |ev| browse_filter.set(event_target_value(&ev))
                            />
                            <Show when=move || !browse_filter.get().trim().is_empty() && filtered_browse_entries(&browse_entries.get(), &browse_filter.get()).is_empty()>
                                <p class="hint" data-workspace-browse-filter-empty="">{move || format!("No folders match \"{}\".", browse_filter.get().trim())}</p>
                            </Show>
                            <ul class="plain">
                                {move || filtered_browse_entries(&browse_entries.get(), &browse_filter.get()).into_iter().map(|e| {
                                    let path = e.path.clone();
                                    let name = e.name.clone();
                                    view! {
                                        <li>
                                            <button class="link ws-row" on:click=move |_| go(path.clone())>
                                                <span class="name">{name}</span>
                                            </button>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                            <div class="row">
                                <input
                                    placeholder="New folder name"
                                    prop:value=move || new_folder.get()
                                    on:input=move |ev| new_folder.set(event_target_value(&ev))
                                />
                                <button class="secondary" on:click=make_folder>"Create folder"</button>
                            </div>
                            <form class="row" on:submit=move |ev: web_sys::SubmitEvent| {
                                ev.prevent_default();
                                go(jump.get());
                            }>
                                <input
                                    placeholder="Go to a path"
                                    prop:value=move || jump.get()
                                    on:input=move |ev| jump.set(event_target_value(&ev))
                                />
                                <button class="secondary" type="submit">"Go"</button>
                            </form>
                            <Show when=move || !browse_error.get().is_empty()>
                                <p class="notice" role="alert">{move || browse_error.get()}</p>
                            </Show>
                        </section>
                    </div>
                    <section class="ws-policy">
                        <h3 class="ws-kicker">"Rules for the open workspace"</h3>
                        <p class="hint">"The rules file must sit inside the workspace. The starting approval is applied when you open this workspace and when a new session starts here. The composer chip still changes the live session."</p>
                        <label>"Rules file"</label>
                        <input
                            placeholder="AGENTS.md"
                            prop:value=move || policy_rules.get()
                            on:input=move |ev| policy_rules.set(event_target_value(&ev))
                        />
                        <label>"Starting approval"</label>
                        <select class="mode" prop:value=move || policy_mode.get() on:change=move |ev| policy_mode.set(event_target_value(&ev))>
                            <option value="">"Unset"</option>
                            {permission_modes().iter().map(|m| {
                                let id = m.id.to_string();
                                let label = m.label.to_string();
                                view! { <option value=id>{label}</option> }
                            }).collect_view()}
                        </select>
                        <label>"Extra roots, one path per line"</label>
                        <textarea
                            placeholder="/home/you/other-checkout"
                            prop:value=move || policy_roots.get()
                            on:input=move |ev| policy_roots.set(event_target_value(&ev))
                        ></textarea>
                        <Show when=move || {
                            let id = active_workspace.get();
                            !id.is_empty() && policy_for.get() != id
                        }>
                            <p class="hint">"Workspace rules are still loading."</p>
                        </Show>
                        <button
                            class="primary"
                            disabled=move || {
                                let id = active_workspace.get();
                                id.is_empty() || policy_for.get() != id
                            }
                            on:click=save_policy
                        >"Save rules"</button>
                    </section>
                    <p class="hint keys">"Up and Down move through workspaces. Enter opens the selected one. Escape closes."</p>
                </div>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ws(id: &str, mode: &str, rules: &str, roots: &[&str]) -> Workspace {
        Workspace {
            id: id.into(),
            name: id.into(),
            path: format!("/{id}"),
            rules: rules.into(),
            permission_mode: mode.into(),
            extra_roots: roots.iter().map(|s| (*s).to_string()).collect(),
            last_opened: String::new(),
        }
    }

    #[test]
    fn policy_miss_does_not_latch_and_save_waits_for_a_hit() {
        let stored = ws("ws-1", "yolo", "AGENTS.md", &["/other"]);
        assert!(matches!(load_policy("", "ws-1", None), PolicyLoad::Miss));
        // A second look at the same missing id still does not latch.
        assert!(matches!(load_policy("", "ws-1", None), PolicyLoad::Miss));
        assert_eq!(
            policy_save_block("", "ws-1"),
            Some("Workspace rules are still loading.")
        );
        match load_policy("", "ws-1", Some(&stored)) {
            PolicyLoad::Hit(fields) => {
                assert_eq!(fields.rules, "AGENTS.md");
                assert_eq!(fields.mode, "yolo");
                assert_eq!(fields.roots, "/other");
            }
            other => panic!("{other:?}"),
        }
        assert_eq!(policy_save_block("ws-1", "ws-1"), None);
        // Already loaded: editing the form must not be overwritten.
        assert!(matches!(
            load_policy("ws-1", "ws-1", Some(&stored)),
            PolicyLoad::Latched
        ));
        // Leaving a stored dangerous mode confirms, including a blank save.
        assert!(policy_change_message("", "yolo").unwrap().contains("Yolo"));
        assert!(policy_change_message("default", "yolo").is_some());
        assert!(policy_change_message("yolo", "default")
            .unwrap()
            .contains("Yolo"));
        assert!(policy_change_message("plan", "default").is_none());
        assert!(policy_change_message("yolo", "yolo").is_none());
    }

    fn named(id: &str, name: &str, path: &str) -> Workspace {
        Workspace {
            name: name.into(),
            path: path.into(),
            ..ws(id, "", "", &[])
        }
    }

    #[test]
    fn filter_empty_query_restores_deduped_picker_rows() {
        let recents = vec![named("a", "Alpha", "/p/alpha")];
        let all = vec![
            named("a", "Alpha", "/p/alpha"),
            named("b", "Beta", "/p/beta"),
        ];
        for q in ["", "  "] {
            let rows = picker_rows_matching(&recents, &all, q);
            assert_eq!(rows.len(), 2);
            assert_eq!(rows[0].id, "a");
            assert_eq!(rows[1].id, "b");
        }
    }

    #[test]
    fn filter_matches_name_and_path_case_insensitively() {
        let all = vec![
            named("a", "Strike", "/home/u/projects/strike"),
            named("b", "strike", "/home/u/checkouts/strike-2"),
            named("c", "Peasant", "/home/u/projects/peasant"),
        ];
        // Name match, upper-case query.
        let rows = picker_rows_matching(&[], &all, "PEASANT");
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].id, "c");
        // Path match tells identically-named checkouts apart.
        let rows = picker_rows_matching(&[], &all, "checkouts/STRIKE-2");
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].id, "b");
        // No match yields the empty list behind the named empty state.
        assert!(picker_rows_matching(&[], &all, "zzz").is_empty());
    }

    #[test]
    fn filtered_cursor_row_is_the_intended_workspace() {
        // The load-bearing property (#1437): keyboard activation reads
        // index `cursor` of this same filtered list the render shows.
        // Unfiltered, index 0 is Alpha; filtered to "beta", index 0 is Beta.
        let recents = vec![named("a", "Alpha", "/p/alpha")];
        let all = vec![
            named("a", "Alpha", "/p/alpha"),
            named("b", "Beta", "/p/beta"),
            named("g", "Gamma", "/p/gamma"),
        ];
        assert_eq!(picker_rows_matching(&recents, &all, "")[0].id, "a");
        let rows = picker_rows_matching(&recents, &all, "beta");
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].id, "b");
        // Sections filter by the identical rule.
        assert_eq!(filtered_workspaces(&all, "beta")[0].id, "b");
        assert_eq!(filtered_workspaces(&recents, "beta").len(), 0);
        assert_eq!(filtered_workspaces(&recents, "alp").len(), 1);
    }

    #[test]
    fn browse_filter_matches_name_and_path() {
        let entries = vec![
            crate::model::BrowseEntry {
                name: "src".into(),
                path: "/p/src".into(),
            },
            crate::model::BrowseEntry {
                name: "Docs".into(),
                path: "/p/docs".into(),
            },
        ];
        assert_eq!(filtered_browse_entries(&entries, "").len(), 2);
        assert_eq!(filtered_browse_entries(&entries, "DOC")[0].name, "Docs");
        assert!(filtered_browse_entries(&entries, "zzz").is_empty());
    }
}
