//! Providers and API keys. The list comes from `GET /v1/providers`.
//! Keys are posted to the daemon and never written to localStorage,
//! never copied into a status string, and never logged.

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::Value;

use crate::api::{self, delete_json, get_text, post_json_value};
use load_guard::{apply_provider_load, prepare_provider_load, LoadEpoch, ProviderView};
use wasm_bindgen::JsCast;

/// Wire dialects `config.ValidateWireAPI` accepts. Not a provider catalog.
const WIRE_APIS: [&str; 3] = ["openai", "responses", "anthropic"];

const KEY_PROVIDERS: &[&str] = &[
    "anthropic",
    "openai",
    "xai",
    "google",
    "kimi",
    "deepseek",
];

thread_local! {
    static KEY_NAMES: RefCell<Vec<String>> = RefCell::new(
        KEY_PROVIDERS.iter().map(|name| (*name).to_string()).collect(),
    );
}

pub fn key_provider_names() -> Vec<String> {
    KEY_NAMES.with(|names| names.borrow().clone())
}

fn publish_key_names(rows: &[ProviderStatus]) {
    let mut names: Vec<String> = rows
        .iter()
        .filter(|row| row.api_key && !row.custom)
        .map(|row| row.name.clone())
        .collect();
    if names.is_empty() {
        names = KEY_PROVIDERS.iter().map(|name| (*name).to_string()).collect();
    }
    KEY_NAMES.with(|slot| *slot.borrow_mut() = names);
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
pub struct ProviderStatus {
    pub name: String,
    #[serde(default)]
    pub detail: String,
    #[serde(default)]
    pub authed: bool,
    #[serde(default)]
    pub builtin: bool,
    #[serde(default)]
    pub custom: bool,
    #[serde(default, rename = "apiKey")]
    pub api_key: bool,
    #[serde(default, rename = "wireAPI")]
    pub wire_api: String,
    #[serde(default, rename = "baseURL")]
    pub base_url: String,
    /// API-key verification state from the daemon (#1531): "", "stored"
    /// (present, never verified), "verified", or "rejected".
    #[serde(default, rename = "keyState")]
    pub key_state: String,
    #[serde(default)]
    pub methods: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct ProviderList {
    #[serde(default)]
    providers: Vec<ProviderStatus>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct CustomProvider {
    name: String,
    #[serde(default, rename = "baseURL")]
    base_url: String,
    #[serde(default)]
    api: String,
    #[serde(default)]
    models: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct CustomList {
    #[serde(default)]
    providers: Vec<CustomProvider>,
}

#[derive(Clone, Debug, Deserialize)]
struct ModelRow {
    id: String,
}

#[derive(Clone, Debug, Deserialize)]
struct ModelList {
    #[serde(default)]
    models: Vec<ModelRow>,
}

/// The badge text for a provider card. A stored API key is not a
/// connection (#1531): it reads "Key stored — not tested" until the
/// daemon's probe has actually presented it to the provider, and
/// "Key rejected" when the provider refused it. "Connected" is reserved
/// for credentials that need no key (OAuth, device, builtin) or a key the
/// provider has accepted.
pub fn connection_label(authed: bool, key_state: &str) -> &'static str {
    if !authed {
        return "Not connected";
    }
    match key_state {
        "verified" => "Connected",
        "rejected" => "Key rejected",
        "stored" => "Key stored — not tested",
        _ => "Connected",
    }
}

/// The note shown after a key probe (`POST /v1/auth/probe`). The daemon's
/// detail strings are written for display and never contain the key.
pub fn probe_note(status: &str, detail: &str) -> String {
    match status {
        "valid" => "Key verified — the provider accepted the stored key.".to_string(),
        "invalid" => format!(
            "Key rejected — {detail}. Paste a corrected key and save it."
        ),
        "unreachable" => format!("Could not verify the key — {detail}."),
        "no-key" => "No key stored yet — paste a key and Save key first.".to_string(),
        "unsupported" => format!("Nothing to test — {detail}."),
        _ if detail.is_empty() => "Test finished without a verdict.".to_string(),
        _ => detail.to_string(),
    }
}

/// `auth.Describe` returns the env var name (`OPENAI_API_KEY`) when that
/// variable is set. Other details (`api key`, `oauth`, `none`) are not.
pub fn env_var_from_detail(detail: &str) -> Option<&str> {
    for token in detail.split(|c: char| c.is_whitespace() || c == '·' || c == ',') {
        let token = token.trim().trim_matches('.');
        if looks_like_env(token) {
            return Some(token);
        }
    }
    None
}

pub fn env_note(detail: &str) -> Option<String> {
    env_var_from_detail(detail).map(|name| format!("Found {name} in environment."))
}

fn looks_like_env(token: &str) -> bool {
    let bytes = token.as_bytes();
    bytes.len() >= 3
        && bytes
            .iter()
            .all(|b| b.is_ascii_uppercase() || b.is_ascii_digit() || *b == b'_')
        && token.contains('_')
}

pub fn scrub_secret(message: &str, key: &str) -> String {
    let key = key.trim();
    if key.len() < 4 {
        return message.to_string();
    }
    message.replace(key, "••••")
}

pub fn error_message(raw: &str) -> String {
    let Some(start) = raw.find('{') else {
        return raw.trim().to_string();
    };
    if let Ok(value) = serde_json::from_str::<Value>(&raw[start..]) {
        if let Some(text) = value.get("error").and_then(|v| v.as_str()) {
            return text.trim().to_string();
        }
    }
    raw.trim().to_string()
}

fn note_from(raw: &str, key: &str) -> String {
    scrub_secret(&error_message(raw), key)
}

/// What the note says after a custom provider's "Remove provider" (#1544).
/// `credential` is the `DELETE /v1/auth/{name}` result; `definition` is the
/// `DELETE /v1/custom-providers/{name}` result, or `None` when it was never
/// attempted — the flow deletes the credential first and stops on failure,
/// so a failed removal changes nothing and leaves no orphan credential
/// behind for a provider that is already gone. Errors arrive scrubbed by
/// `note_from`. The copy never claims more than actually happened.
pub fn custom_removal_note(
    credential: &Result<(), String>,
    definition: Option<&Result<(), String>>,
) -> String {
    match (credential, definition) {
        (Ok(()), Some(Ok(()))) => "Provider removed.".into(),
        (Ok(()), Some(Err(err))) => format!(
            "Stored credential removed, but the provider definition could not be deleted: {err}"
        ),
        (Ok(()), None) => "Stored credential removed.".into(),
        (Err(err), Some(Ok(()))) => format!(
            "Provider definition deleted, but its stored credential could not be removed: {err}"
        ),
        (Err(err), _) => {
            format!("Could not remove the stored credential: {err} The provider was not removed.")
        }
    }
}

fn clock_label() -> String {
    let now = js_sys::Date::new_0();
    format!(
        "{:02}:{:02}:{:02}",
        now.get_hours(),
        now.get_minutes(),
        now.get_seconds()
    )
}

fn focus_provider(name: &str) {
    let Some(doc) = web_sys::window().and_then(|window| window.document()) else {
        return;
    };
    let selector = if name == "custom" {
        "[data-add-host]".to_string()
    } else {
        format!("[data-provider=\"{name}\"]")
    };
    let Ok(Some(el)) = doc.query_selector(&selector) else {
        return;
    };
    if let Some(html) = el.dyn_ref::<web_sys::HtmlElement>() {
        html.scroll_into_view();
    }
    let field = if name == "custom" {
        "[data-new-name]"
    } else {
        "[data-provider-key]"
    };
    if let Ok(Some(input)) = el.query_selector(field) {
        if let Some(html) = input.dyn_ref::<web_sys::HtmlElement>() {
            let _ = html.focus();
        }
    }
}

fn request_reload(bump: RwSignal<u32>) {
    bump.update(|n| *n = n.wrapping_add(1));
}

/// Note shown when a base-URL edit misses the loaded custom list (#1578).
/// A miss after a failed custom-providers load must blame the load, not
/// the host.
fn edit_host_miss_note(custom_load_failed: bool) -> &'static str {
    if custom_load_failed {
        "Custom providers could not be loaded, so this host cannot be checked right now."
    } else {
        "This host is not an editable custom provider."
    }
}

fn map_insert(map: &RwSignal<HashMap<String, String>>, key: String, value: String) {
    map.update(|found| {
        found.insert(key, value);
    });
}

#[component]
fn ProviderCard(
    row: ProviderStatus,
    customs: RwSignal<Vec<CustomProvider>>,
    custom_error: RwSignal<String>,
    drafts: RwSignal<HashMap<String, String>>,
    notes: RwSignal<HashMap<String, String>>,
    base_drafts: RwSignal<HashMap<String, String>>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    bump: RwSignal<u32>,
) -> impl IntoView {
    let name = row.name.clone();
    let detail = row.detail.clone();
    let env = env_note(&detail);
    let authed = row.authed;
    let custom = row.custom;
    let api_key = row.api_key;
    let builtin = row.builtin;
    let base_url = row.base_url.clone();
    let wire = row.wire_api.clone();
    let key_state = row.key_state.clone();
    let state = connection_label(authed, &key_state);
    let badge = match state {
        "Connected" => "chip ok",
        "Key rejected" => "chip bad",
        _ => "chip",
    };

    let save_key = {
        let name = name.clone();
        move |_| {
            let key = drafts
                .get_untracked()
                .get(&name)
                .cloned()
                .unwrap_or_default();
            let key = key.trim().to_string();
            if key.is_empty() {
                map_insert(&notes, name.clone(), "Paste a key first.".into());
                return;
            }
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let name = name.clone();
            spawn_local(async move {
                let result = post_json_value(
                    &b,
                    &t,
                    "/v1/auth/key",
                    serde_json::json!({ "provider": name, "key": key }),
                )
                .await;
                match result {
                    Ok(_) => {
                        map_insert(&drafts, name.clone(), String::new());
                        map_insert(
                            &notes,
                            name,
                            "Key stored on the daemon. It is not shown again. It has not been tested yet — Test key checks it against the provider.".into(),
                        );
                        request_reload(bump);
                    }
                    Err(err) => map_insert(&notes, name, note_from(&err, &key)),
                }
            });
        }
    };

    let test_provider = move |name: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let key = drafts
            .get_untracked()
            .get(&name)
            .cloned()
            .unwrap_or_default();
        spawn_local(async move {
            if api_key {
                // Real credential probe (#1531): the daemon presents the
                // stored key to the provider itself. A catalog listing is
                // not a test — it never touches the key.
                let probed = post_json_value(
                    &b,
                    &t,
                    "/v1/auth/probe",
                    serde_json::json!({ "provider": name }),
                )
                .await;
                match probed {
                    Ok(value) => {
                        let status = value
                            .get("status")
                            .and_then(|v| v.as_str())
                            .unwrap_or_default();
                        let detail = value
                            .get("detail")
                            .and_then(|v| v.as_str())
                            .unwrap_or_default();
                        let note = probe_note(status, detail);
                        map_insert(&notes, name, note_from(&note, &key));
                        // Refresh so the badge picks up the new keyState.
                        request_reload(bump);
                    }
                    Err(err) => map_insert(&notes, name, note_from(&err, &key)),
                }
                return;
            }
            let path = format!("/v1/models?provider={}", api::js_encode(&name));
            match get_text(&b, &t, &path).await {
                Ok(body) => match serde_json::from_str::<ModelList>(&body) {
                    Ok(list) => {
                        let n = list.models.len();
                        map_insert(&notes, name, format!("Listed {n} models."));
                    }
                    Err(err) => map_insert(&notes, name, note_from(&err.to_string(), &key)),
                },
                Err(err) => map_insert(&notes, name, note_from(&err, &key)),
            }
        });
    };

    // "Remove provider" is rendered only for custom providers, and for them
    // it must remove the provider (#1544): after the shared confirm, delete
    // the stored credential, then the provider definition itself, so the
    // provider is still gone after a reload. A credential failure stops
    // before the definition delete; a definition failure is reported as the
    // partial removal it is. Built-in providers never render this button —
    // their credential-only path is the palette's "Remove stored key".
    let remove_provider = move |name: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let run_name = name.clone();
        let run = Rc::new(move || {
            let (b, t) = (b.clone(), t.clone());
            let name = run_name.clone();
            spawn_local(async move {
                let cred_path = format!("/v1/auth/{}", api::js_encode(&name));
                let credential = delete_json(&b, &t, &cred_path, None)
                    .await
                    .map_err(|err| note_from(&err, ""));
                let definition = if credential.is_ok() {
                    let def_path = format!("/v1/custom-providers/{}", api::js_encode(&name));
                    Some(
                        delete_json(&b, &t, &def_path, None)
                            .await
                            .map_err(|err| note_from(&err, "")),
                    )
                } else {
                    None
                };
                map_insert(
                    &notes,
                    name,
                    custom_removal_note(&credential, definition.as_ref()),
                );
                request_reload(bump);
            });
        });
        let opened = crate::confirm::ask(
            crate::confirm::ConfirmRequest {
                title: format!("Remove provider {name}?"),
                body: "This deletes the provider definition and its stored credential from the daemon. The provider stays gone after a reload.".into(),
                confirm_label: "Remove provider".into(),
            },
            run,
        );
        if !opened {
            map_insert(&notes, name, crate::confirm::BUSY.into());
        }
    };

    let save_base = {
        let name = name.clone();
        move |_| {
            let url = base_drafts
                .get_untracked()
                .get(&name)
                .cloned()
                .unwrap_or_default();
            let url = url.trim().to_string();
            if url.is_empty() {
                map_insert(&notes, name.clone(), "Base URL is required.".into());
                return;
            }
            let existing = customs
                .get_untracked()
                .into_iter()
                .find(|item| item.name == name);
            let Some(existing) = existing else {
                map_insert(
                    &notes,
                    name.clone(),
                    edit_host_miss_note(!custom_error.get_untracked().is_empty()).into(),
                );
                return;
            };
            let (b, t) = (base.get_untracked(), token.get_untracked());
            let name = name.clone();
            spawn_local(async move {
                let body = serde_json::json!({
                    "name": existing.name,
                    "baseURL": url,
                    "api": existing.api,
                    "models": existing.models,
                });
                match post_json_value(&b, &t, "/v1/custom-providers", body).await {
                    Ok(_) => {
                        map_insert(&notes, name, "Base URL saved.".into());
                        request_reload(bump);
                    }
                    Err(err) => map_insert(&notes, name, note_from(&err, "")),
                }
            });
        }
    };

    let name_test_key = name.clone();
    let name_test_plain = name.clone();
    let name_remove_key = name.clone();
    let name_remove_plain = name.clone();

    let name_attr = name.clone();
    let heading = name.clone();
    let key_label = name.clone();
    let key_value = name.clone();
    let key_input = name.clone();
    let base_label = name.clone();
    let base_value = name.clone();
    let base_input = name.clone();
    let seeded = base_url.clone();
    let note_name = name.clone();
    view! {
        <li class="providers-card" data-provider=name_attr>
            <div class="providers-card-head">
                <h2>{heading}</h2>
                <span class=badge data-provider-state=state>{state}</span>
            </div>
            {env.map(|text| view! { <p class="providers-env" data-env-note="true">{text}</p> })}
            {(!wire.is_empty()).then(|| view! { <p class="hint">"Wire: " {wire}</p> })}
            {(!base_url.is_empty() && !custom).then(|| {
                let shown = base_url.clone();
                let attr = shown.clone();
                view! { <p class="providers-base" data-base-url=attr>"Base URL " {shown}</p> }
            })}
            {custom.then(|| view! {
                <div class="providers-block">
                <label>
                    "Base URL"
                    <input
                        type="url"
                        autocomplete="off"
                        spellcheck="false"
                        aria-label=format!("Base URL for {base_label}")
                        data-base-input="true"
                        prop:value=move || {
                            base_drafts.with(|map| map.get(&base_value).cloned().unwrap_or_else(|| seeded.clone()))
                        }
                        on:input=move |ev| {
                            let value = event_target_value(&ev);
                            map_insert(&base_drafts, base_input.clone(), value);
                        }
                    />
                </label>
                <button type="button" class="secondary" on:click=save_base>"Save base URL"</button>
                </div>
            })}
            {api_key.then(|| view! {
                <div class="providers-block">
                <label>
                    "API key"
                    <input
                        type="password"
                        autocomplete="off"
                        spellcheck="false"
                        name="strike-provider-key"
                        data-provider-key="true"
                        aria-label=format!("API key for {key_label}")
                        prop:value=move || drafts.with(|map| map.get(&key_value).cloned().unwrap_or_default())
                        on:input=move |ev| {
                            let value = event_target_value(&ev);
                            map_insert(&drafts, key_input.clone(), value);
                        }
                    />
                </label>
                <div class="providers-actions">
                    <button type="button" class="primary" data-save-key="true" on:click=save_key>"Save key"</button>
                    <button type="button" class="secondary" data-test-provider="true" on:click=move |_| test_provider(name_test_key.clone())>"Test key"</button>
                    {custom.then(|| {
                        let name_remove_key = name_remove_key.clone();
                        view! {
                            <button type="button" class="secondary" data-remove-key="true" on:click=move |_| remove_provider(name_remove_key.clone())>"Remove provider"</button>
                        }
                    })}
                </div>
                </div>
            })}
            {(!api_key).then(|| view! {
                <div class="providers-actions">
                    <button type="button" class="secondary" data-test-provider="true" on:click=move |_| test_provider(name_test_plain.clone())>"Test"</button>
                    {(custom && !builtin).then(|| view! {
                        <button type="button" class="secondary" data-remove-key="true" on:click=move |_| remove_provider(name_remove_plain.clone())>"Remove provider"</button>
                    })}
                </div>
            })}
            {move || {
                let text = notes.with(|map| map.get(&note_name).cloned().unwrap_or_default());
                (!text.is_empty()).then(|| view! {
                    <p class="providers-note" role="status" data-provider-note="true">{text}</p>
                })
            }}
        </li>
    }
}

#[component]
pub fn ProvidersPage(base: RwSignal<String>, token: RwSignal<String>) -> impl IntoView {
    let rows = RwSignal::new(Vec::<ProviderStatus>::new());
    let customs = RwSignal::new(Vec::<CustomProvider>::new());
    let custom_error = RwSignal::new(String::new());
    let load_error = RwSignal::new(String::new());
    let loading = RwSignal::new(true);
    let drafts = RwSignal::new(HashMap::<String, String>::new());
    let notes = RwSignal::new(HashMap::<String, String>::new());
    let base_drafts = RwSignal::new(HashMap::<String, String>::new());

    let new_name = RwSignal::new(String::new());
    let new_url = RwSignal::new(String::new());
    let new_api = RwSignal::new("openai".to_string());
    let new_model = RwSignal::new(String::new());
    let new_key = RwSignal::new(String::new());
    let new_note = RwSignal::new(String::new());

    let bump = RwSignal::new(0u32);
    let load_epoch = RwSignal::new(LoadEpoch::new());
    let loaded_base = RwSignal::new(String::new());
    let loaded_at = RwSignal::new(String::new());
    let stale_note = RwSignal::new(String::new());
    Effect::new(move |_| {
        let _ = base.get();
        let _ = bump.get();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let mut view = ProviderView {
            rows: rows.get_untracked(),
            customs: customs.get_untracked(),
            load_error: load_error.get_untracked(),
            stale_note: stale_note.get_untracked(),
            loading: loading.get_untracked(),
            loaded_base: loaded_base.get_untracked(),
            loaded_at: loaded_at.get_untracked(),
        };
        let mut epoch = load_epoch.get_untracked();
        let ticket = prepare_provider_load(&mut epoch, &mut view, &b);
        load_epoch.set(epoch);
        rows.set(view.rows.clone());
        customs.set(view.customs.clone());
        load_error.set(view.load_error.clone());
        stale_note.set(view.stale_note.clone());
        loading.set(view.loading);
        loaded_base.set(view.loaded_base.clone());
        loaded_at.set(view.loaded_at.clone());
        spawn_local(async move {
            let listed = get_text(&b, &t, "/v1/providers").await;
            if !load_epoch.get_untracked().accept(ticket) {
                return;
            }
            let custom = get_text(&b, &t, "/v1/custom-providers").await;
            if !load_epoch.get_untracked().accept(ticket) {
                return;
            }
            let rows_result = match listed {
                Ok(body) => match serde_json::from_str::<ProviderList>(&body) {
                    Ok(list) => Ok(list.providers),
                    Err(err) => Err(format!("Could not read providers: {err}")),
                },
                Err(err) => Err(note_from(&err, "")),
            };
            let published = match &rows_result {
                Ok(rows) => Some(rows.clone()),
                Err(_) => None,
            };
            let customs_result = match custom {
                Ok(body) => match serde_json::from_str::<CustomList>(&body) {
                    Ok(list) => Ok(list.providers),
                    Err(_) => Err("Could not read custom providers.".into()),
                },
                Err(err) => Err(err),
            };
            // Main's #1670 surfacing, applied only inside the guarded commit
            // below: a superseded load must not paint this notice either.
            let custom_notice = match &customs_result {
                Ok(_) => String::new(),
                Err(err) => format!(
                    "Custom providers could not be loaded — the list below may be incomplete. ({err})"
                ),
            };
            if apply_provider_load(
                &load_epoch.get_untracked(),
                ticket,
                &mut view,
                rows_result,
                customs_result,
                &clock_label(),
            ) {
                if let Some(rows) = published {
                    publish_key_names(&rows);
                }
                custom_error.set(custom_notice);
                rows.set(view.rows);
                customs.set(view.customs);
                load_error.set(view.load_error);
                stale_note.set(view.stale_note);
                loading.set(view.loading);
                loaded_base.set(view.loaded_base);
                loaded_at.set(view.loaded_at);
            }
        });
    });

    let add_host = move || {
        let name = new_name.get_untracked().trim().to_string();
        let url = new_url.get_untracked().trim().to_string();
        let api_name = new_api.get_untracked();
        let model = new_model.get_untracked().trim().to_string();
        let key = new_key.get_untracked().trim().to_string();
        if name.is_empty() || url.is_empty() || model.is_empty() {
            new_note.set("Name, base URL, and a model id are required.".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let body = serde_json::json!({
                "name": name,
                "baseURL": url,
                "api": api_name,
                "models": [model],
            });
            if let Err(err) = post_json_value(&b, &t, "/v1/custom-providers", body).await {
                new_note.set(note_from(&err, &key));
                return;
            }
            if !key.is_empty() {
                if let Err(err) = post_json_value(
                    &b,
                    &t,
                    "/v1/auth/key",
                    serde_json::json!({ "provider": name, "key": key }),
                )
                .await
                {
                    new_key.set(String::new());
                    new_note.set(note_from(&err, &key));
                    request_reload(bump);
                    return;
                }
            }
            new_key.set(String::new());
            new_name.set(String::new());
            new_url.set(String::new());
            new_model.set(String::new());
            new_note.set("Host saved. The key, if you pasted one, stays on the daemon and has not been tested yet.".into());
            request_reload(bump);
        });
    };

    let remove_named = move |name: String| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/auth/{}", api::js_encode(&name));
            match delete_json(&b, &t, &path, None).await {
                Ok(()) => {
                    map_insert(&notes, name, "Stored credential removed.".into());
                    request_reload(bump);
                }
                Err(err) => map_insert(&notes, name, note_from(&err, "")),
            }
        });
    };
    crate::shell::bind(
        "providers.focus",
        Rc::new(move |name| {
            if loading.get_untracked() {
                crate::park::put("providers.focus", name);
            } else {
                focus_provider(name);
            }
            Ok(())
        }),
    );
    crate::shell::bind(
        "providers.refresh",
        Rc::new(move |_| {
            request_reload(bump);
            Ok(())
        }),
    );
    crate::shell::bind(
        "providers.remove-key",
        Rc::new(move |name| {
            if name.trim().is_empty() {
                return Err("Pick a provider.".into());
            }
            remove_named(name.to_string());
            Ok(())
        }),
    );
    Effect::new(move |_| {
        if loading.get() {
            return;
        }
        if let Some(name) = crate::park::take("providers.focus") {
            focus_provider(&name);
        }
        if crate::park::take("providers.refresh").is_some() {
            request_reload(bump);
        }
        if let Some(name) = crate::park::take("providers.remove-key") {
            remove_named(name);
        }
    });

    view! {
        <main class="providers-page" data-providers-root="true">
            <header class="providers-head">
                <h1 class="panel-title">"Providers"</h1>
                <p class="hint">"Keys stay on the daemon. This page does not put them in browser storage."</p>
            </header>
            <Show when=move || loading.get()>
                <p class="hint">"Loading providers from the daemon."</p>
            </Show>
            <Show when=move || !load_error.get().is_empty()>
                <p class="notice" role="alert">{move || load_error.get()}</p>
            </Show>
            <Show when=move || !stale_note.get().is_empty()>
                <p class="hint" data-providers-stale="true">{move || stale_note.get()}</p>
            </Show>
            <Show when=move || !custom_error.get().is_empty()>
                <p class="notice" role="alert" data-custom-load-error="true">{move || custom_error.get()}</p>
            </Show>
            <ul class="providers-list plain">
                {move || rows.get().into_iter().map(|row| {
                    view! {
                        <ProviderCard
                            row=row
                            customs=customs
                            custom_error=custom_error
                            drafts=drafts
                            notes=notes
                            base_drafts=base_drafts
                            base=base
                            token=token
                            bump=bump
                        />
                    }
                }).collect_view()}
            </ul>
            <section class="providers-card" data-add-host="true">
                <h2>"OpenAI-compatible or local host"</h2>
                <p class="hint">"The daemon stores the host. A key pasted here is sent once and cleared from this form."</p>
                <label>
                    "Name"
                    <input
                        autocomplete="off"
                        spellcheck="false"
                        aria-label="Provider name"
                        data-new-name="true"
                        prop:value=move || new_name.get()
                        on:input=move |ev| new_name.set(event_target_value(&ev))
                    />
                </label>
                <label>
                    "Base URL"
                    <input
                        type="url"
                        autocomplete="off"
                        spellcheck="false"
                        aria-label="Base URL"
                        placeholder="http://127.0.0.1:11434/v1"
                        data-new-url="true"
                        prop:value=move || new_url.get()
                        on:input=move |ev| new_url.set(event_target_value(&ev))
                    />
                </label>
                <label>
                    "Wire"
                    <select
                        aria-label="Wire dialect"
                        data-new-api="true"
                        on:change=move |ev| new_api.set(event_target_value(&ev))
                    >
                        {WIRE_APIS.into_iter().map(|api| view! { <option value=api>{api}</option> }).collect_view()}
                    </select>
                </label>
                <label>
                    "Model id"
                    <input
                        autocomplete="off"
                        spellcheck="false"
                        aria-label="Model id"
                        data-new-model="true"
                        prop:value=move || new_model.get()
                        on:input=move |ev| new_model.set(event_target_value(&ev))
                    />
                </label>
                <label>
                    "API key"
                    <input
                        type="password"
                        autocomplete="off"
                        spellcheck="false"
                        name="strike-new-provider-key"
                        aria-label="API key for the new host"
                        data-new-key="true"
                        prop:value=move || new_key.get()
                        on:input=move |ev| new_key.set(event_target_value(&ev))
                    />
                </label>
                <button type="button" class="primary" data-add-provider="true" on:click=move |_| add_host()>"Add provider"</button>
                <Show when=move || !new_note.get().is_empty()>
                    <p class="providers-note" role="status">{move || new_note.get()}</p>
                </Show>
            </section>
        </main>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn env_note_names_the_variable_only() {
        assert_eq!(
            env_note("OPENAI_API_KEY"),
            Some("Found OPENAI_API_KEY in environment.".into())
        );
        assert_eq!(
            env_note("OPENAI_API_KEY · openai · api.openai.com"),
            Some("Found OPENAI_API_KEY in environment.".into())
        );
        assert_eq!(env_note("api key"), None);
        assert_eq!(env_note("none"), None);
        assert_eq!(env_note("oauth+key"), None);
        assert_eq!(env_note("offline dev provider"), None);
        assert_eq!(
            env_note("GOOGLE_API_KEY"),
            Some("Found GOOGLE_API_KEY in environment.".into())
        );
    }

    #[test]
    fn scrub_removes_the_pasted_key() {
        let key = "sk-test-secret-value";
        let message = format!("HTTP 400 rejected {key}");
        let scrubbed = scrub_secret(&message, key);
        assert!(!scrubbed.contains(key));
        assert!(scrubbed.contains("••••"));
        assert_eq!(scrub_secret("untouched", ""), "untouched");
        assert_eq!(scrub_secret("abc", "ab"), "abc");
    }

    #[test]
    fn connection_label_never_calls_a_stored_key_connected() {
        assert_eq!(connection_label(false, ""), "Not connected");
        assert_eq!(connection_label(false, "stored"), "Not connected");
        // #1531: a pasted key is stored, not connected, until probed.
        assert_eq!(connection_label(true, "stored"), "Key stored — not tested");
        assert_eq!(connection_label(true, "rejected"), "Key rejected");
        assert_eq!(connection_label(true, "verified"), "Connected");
        // Keyless auth (OAuth, device, builtin) is connected as before.
        assert_eq!(connection_label(true, ""), "Connected");
    }

    #[test]
    fn probe_note_reports_each_verdict_plainly() {
        assert_eq!(
            probe_note("valid", "the provider accepted the stored key"),
            "Key verified — the provider accepted the stored key."
        );
        let rejected = probe_note("invalid", "the provider refused the stored key (HTTP 401)");
        assert!(rejected.starts_with("Key rejected"));
        assert!(rejected.contains("HTTP 401"));
        let unreachable = probe_note("unreachable", "could not reach the provider");
        assert!(unreachable.starts_with("Could not verify the key"));
        assert_eq!(
            probe_note("no-key", "no API key is stored for this provider"),
            "No key stored yet — paste a key and Save key first."
        );
        let oauth = probe_note("unsupported", "signed in with OAuth — there is no API key to test");
        assert!(oauth.starts_with("Nothing to test"));
    }

    #[test]
    fn custom_removal_note_claims_only_what_happened() {
        // Full removal: honest success matching the button's label.
        assert_eq!(
            custom_removal_note(&Ok(()), Some(&Ok(()))),
            "Provider removed."
        );
        // The issue's partial case, verbatim shape: credential gone,
        // definition still there, and the note says exactly that.
        assert_eq!(
            custom_removal_note(&Ok(()), Some(&Err("no such provider".into()))),
            "Stored credential removed, but the provider definition could not be deleted: no such provider"
        );
        // Credential failure stops the flow: definition never attempted,
        // and the note must not claim a removal.
        assert_eq!(
            custom_removal_note(&Err("HTTP 400".into()), None),
            "Could not remove the stored credential: HTTP 400 The provider was not removed."
        );
        // Credential-only path (no definition step) keeps the old copy.
        assert_eq!(
            custom_removal_note(&Ok(()), None),
            "Stored credential removed."
        );
    }

    #[test]
    fn edit_host_miss_blames_the_load_not_the_host() {
        assert!(edit_host_miss_note(true).contains("could not be loaded"));
        assert_eq!(
            edit_host_miss_note(false),
            "This host is not an editable custom provider."
        );
    }

    #[test]
    fn error_message_reads_the_daemon_error() {
        assert_eq!(
            error_message(r#"HTTP 400 {"error":"api key is empty"}"#),
            "api key is empty"
        );
        assert_eq!(error_message("HTTP 502"), "HTTP 502");
    }
}
