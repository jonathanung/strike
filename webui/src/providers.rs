//! Providers and API keys. The list comes from `GET /v1/providers`.
//! Keys are posted to the daemon and never written to localStorage,
//! never copied into a status string, and never logged.

use std::collections::HashMap;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::Value;

use crate::api::{self, delete_json, get_text, patch_json, post_json_value};

/// Wire dialects `config.ValidateWireAPI` accepts. Not a provider catalog.
const WIRE_APIS: [&str; 3] = ["openai", "responses", "anthropic"];

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

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct SettingsSnap {
    #[serde(default)]
    provider: String,
    #[serde(default)]
    model: String,
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

pub fn connection_label(authed: bool) -> &'static str {
    if authed {
        "Connected"
    } else {
        "Not connected"
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

fn request_reload(bump: RwSignal<u32>) {
    bump.update(|n| *n = n.wrapping_add(1));
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
    drafts: RwSignal<HashMap<String, String>>,
    notes: RwSignal<HashMap<String, String>>,
    models: RwSignal<HashMap<String, Vec<String>>>,
    picked: RwSignal<HashMap<String, String>>,
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
    let badge = if authed { "chip ok" } else { "chip" };
    let state = connection_label(authed);

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
                            "Key stored on the daemon. It is not shown again.".into(),
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
            let path = format!("/v1/models?provider={}", api::js_encode(&name));
            match get_text(&b, &t, &path).await {
                Ok(body) => match serde_json::from_str::<ModelList>(&body) {
                    Ok(list) => {
                        let ids: Vec<String> = list.models.into_iter().map(|row| row.id).collect();
                        let n = ids.len();
                        if let Some(first) = ids.first() {
                            let id = first.clone();
                            picked.update(|map| {
                                map.entry(name.clone()).or_insert(id);
                            });
                        }
                        models.update(|map| {
                            map.insert(name.clone(), ids);
                        });
                        map_insert(&notes, name, format!("Listed {n} models."));
                    }
                    Err(err) => map_insert(&notes, name, note_from(&err.to_string(), &key)),
                },
                Err(err) => map_insert(&notes, name, note_from(&err, &key)),
            }
        });
    };

    let remove_stored = move |name: String| {
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
                    "This host is not an editable custom provider.".into(),
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

    let set_default = move |name: String| {
        let model = picked
            .get_untracked()
            .get(&name)
            .cloned()
            .unwrap_or_default();
        if model.trim().is_empty() {
            map_insert(&notes, name, "Pick a model first.".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match patch_json(
                &b,
                &t,
                "/v1/settings",
                serde_json::json!({ "provider": name, "model": model }),
            )
            .await
            {
                Ok(()) => {
                    map_insert(&notes, name, "Default model saved.".into());
                    request_reload(bump);
                }
                Err(err) => map_insert(&notes, name, note_from(&err, "")),
            }
        });
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
    let model_name = name.clone();
    let select_name = name.clone();
    let pick_name = name.clone();

    view! {
        <li class="providers-card" data-provider=name_attr>
            <div class="providers-card-head">
                <h2>{heading}</h2>
                <span class=badge data-provider-state=state>{state}</span>
            </div>
            <p class="hint">{detail}</p>
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
                    <button type="button" class="secondary" data-test-provider="true" on:click=move |_| test_provider(name_test_key.clone())>"Test"</button>
                    <button type="button" class="secondary" data-remove-key="true" on:click=move |_| remove_stored(name_remove_key.clone())>
                        {if custom { "Remove provider" } else { "Remove stored key" }}
                    </button>
                </div>
                </div>
            })}
            {(!api_key).then(|| view! {
                <div class="providers-actions">
                    <button type="button" class="secondary" data-test-provider="true" on:click=move |_| test_provider(name_test_plain.clone())>"Test"</button>
                    {(custom && !builtin).then(|| view! {
                        <button type="button" class="secondary" data-remove-key="true" on:click=move |_| remove_stored(name_remove_plain.clone())>"Remove provider"</button>
                    })}
                </div>
            })}
            {move || {
                let ids = models.with(|map| map.get(&model_name).cloned().unwrap_or_default());
                if ids.is_empty() {
                    return view! { <span></span> }.into_any();
                }
                let label = select_name.clone();
                let pick = pick_name.clone();
                let default_name = model_name.clone();
                view! {
                    <div class="providers-block">
                    <label>
                        "Default model"
                        <select
                            aria-label=format!("Default model for {label}")
                            data-default-select="true"
                            on:change=move |ev| {
                                map_insert(&picked, pick.clone(), event_target_value(&ev));
                            }
                        >
                            {ids.into_iter().map(|id| {
                                let attr = id.clone();
                                view! { <option value=attr>{id}</option> }
                            }).collect_view()}
                        </select>
                    </label>
                    <button type="button" class="primary" data-set-default="true" on:click=move |_| set_default(default_name.clone())>"Set as default"</button>
                    </div>
                }.into_any()
            }}
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
    let load_error = RwSignal::new(String::new());
    let loading = RwSignal::new(true);
    let defaults = RwSignal::new(SettingsSnap {
        provider: String::new(),
        model: String::new(),
    });
    let drafts = RwSignal::new(HashMap::<String, String>::new());
    let notes = RwSignal::new(HashMap::<String, String>::new());
    let models = RwSignal::new(HashMap::<String, Vec<String>>::new());
    let picked = RwSignal::new(HashMap::<String, String>::new());
    let base_drafts = RwSignal::new(HashMap::<String, String>::new());

    let new_name = RwSignal::new(String::new());
    let new_url = RwSignal::new(String::new());
    let new_api = RwSignal::new("openai".to_string());
    let new_model = RwSignal::new(String::new());
    let new_key = RwSignal::new(String::new());
    let new_note = RwSignal::new(String::new());

    let bump = RwSignal::new(0u32);
    Effect::new(move |_| {
        let _ = base.get();
        let _ = bump.get();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        loading.set(true);
        spawn_local(async move {
            let listed = get_text(&b, &t, "/v1/providers").await;
            let custom = get_text(&b, &t, "/v1/custom-providers").await;
            let settings = get_text(&b, &t, "/v1/settings").await;
            match listed {
                Ok(body) => match serde_json::from_str::<ProviderList>(&body) {
                    Ok(list) => {
                        rows.set(list.providers);
                        load_error.set(String::new());
                    }
                    Err(err) => load_error.set(format!("Could not read providers: {err}")),
                },
                Err(err) => load_error.set(note_from(&err, "")),
            }
            if let Ok(body) = custom {
                if let Ok(list) = serde_json::from_str::<CustomList>(&body) {
                    customs.set(list.providers);
                }
            }
            if let Ok(body) = settings {
                if let Ok(snap) = serde_json::from_str::<SettingsSnap>(&body) {
                    defaults.set(snap);
                }
            }
            loading.set(false);
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
            new_note.set("Host saved. The key, if you pasted one, stays on the daemon.".into());
            request_reload(bump);
        });
    };

    view! {
        <main class="providers-page" data-providers-root="true">
            <header class="providers-head">
                <h1 class="panel-title">"Providers"</h1>
                <p class="hint">"Keys stay on the daemon. This page does not put them in browser storage."</p>
                <p data-default-model="true">
                    {move || {
                        let snap = defaults.get();
                        if snap.provider.is_empty() && snap.model.is_empty() {
                            "Default model: none yet.".to_string()
                        } else {
                            format!("Default model: {} / {}", snap.provider, snap.model)
                        }
                    }}
                </p>
                <button type="button" class="secondary" on:click=move |_| request_reload(bump)>"Refresh"</button>
            </header>
            <Show when=move || loading.get()>
                <p class="hint">"Loading providers from the daemon."</p>
            </Show>
            <Show when=move || !load_error.get().is_empty()>
                <p class="notice" role="alert">{move || load_error.get()}</p>
            </Show>
            <ul class="providers-list plain">
                {move || rows.get().into_iter().map(|row| {
                    view! {
                        <ProviderCard
                            row=row
                            customs=customs
                            drafts=drafts
                            notes=notes
                            models=models
                            picked=picked
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
    fn connection_label_is_the_two_states() {
        assert_eq!(connection_label(true), "Connected");
        assert_eq!(connection_label(false), "Not connected");
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
