//! Triggers page. The daemon registry is the source of truth; this module
//! only renders it. Secrets are shown once, when the daemon mints them.

use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};
use wasm_bindgen::JsCast;

use load_guard::{commit_load, Commit, ErrPolicy, LoadEpoch};

use crate::load_epoch::begin;

use crate::api::{api, auth_header, get_json, now_ms, post_json_value};

#[derive(Clone, Deserialize)]
struct TriggerList {
    triggers: Vec<TriggerRow>,
}

#[derive(Clone, Deserialize)]
struct TriggerRow {
    id: String,
    name: String,
    kind: String,
    enabled: bool,
    #[serde(rename = "secretSet")]
    secret_set: bool,
    prompt: String,
    #[serde(rename = "permissionMode")]
    permission_mode: String,
    #[serde(rename = "resolvedPermissionMode")]
    resolved_permission_mode: String,
    #[serde(default, rename = "continueSessionId")]
    continue_session_id: String,
    #[serde(rename = "ratePerMinute")]
    rate_per_minute: i32,
    #[serde(rename = "dedupeSeconds")]
    dedupe_seconds: i32,
    #[serde(default)]
    events: Vec<String>,
    #[serde(default)]
    adapter: String,
    #[serde(default)]
    channel: String,
    #[serde(rename = "hookPath")]
    hook_path: String,
}

#[derive(Clone, Deserialize)]
struct DeliveryList {
    deliveries: Vec<DeliveryRow>,
}

#[derive(Clone, Deserialize)]
struct DeliveryRow {
    id: String,
    at: String,
    outcome: String,
    #[serde(default, rename = "sessionId")]
    session_id: String,
    #[serde(default)]
    event: String,
    #[serde(default)]
    reason: String,
}

#[derive(Copy, Clone)]
struct Form {
    selected: RwSignal<String>,
    name: RwSignal<String>,
    kind: RwSignal<String>,
    prompt: RwSignal<String>,
    perm: RwSignal<String>,
    continue_id: RwSignal<String>,
    rate_text: RwSignal<String>,
    dedupe_text: RwSignal<String>,
    events_text: RwSignal<String>,
    adapter: RwSignal<String>,
    channel: RwSignal<String>,
    enabled: RwSignal<bool>,
    secret_input: RwSignal<String>,
    hook_path: RwSignal<String>,
    notice: RwSignal<String>,
    generation: RwSignal<u64>,
    secret_once: RwSignal<Option<SecretOnce>>,
}

impl Form {
    fn new() -> Self {
        Self {
            selected: RwSignal::new(String::new()),
            name: RwSignal::new(String::new()),
            kind: RwSignal::new("webhook".to_string()),
            prompt: RwSignal::new(String::new()),
            perm: RwSignal::new(String::new()),
            continue_id: RwSignal::new(String::new()),
            rate_text: RwSignal::new("6".to_string()),
            dedupe_text: RwSignal::new("600".to_string()),
            events_text: RwSignal::new(String::new()),
            adapter: RwSignal::new(String::new()),
            channel: RwSignal::new(String::new()),
            enabled: RwSignal::new(true),
            secret_input: RwSignal::new(String::new()),
            hook_path: RwSignal::new(String::new()),
            notice: RwSignal::new(String::new()),
            generation: RwSignal::new(0),
            secret_once: RwSignal::new(None),
        }
    }

    fn fill(self, row: &TriggerRow) {
        self.selected.set(row.id.clone());
        self.name.set(row.name.clone());
        self.kind.set(row.kind.clone());
        self.prompt.set(row.prompt.clone());
        self.perm.set(row.permission_mode.clone());
        self.continue_id.set(row.continue_session_id.clone());
        self.rate_text.set(row.rate_per_minute.to_string());
        self.dedupe_text.set(row.dedupe_seconds.to_string());
        self.events_text.set(row.events.join(", "));
        self.adapter.set(row.adapter.clone());
        self.channel.set(row.channel.clone());
        self.enabled.set(row.enabled);
        self.secret_input.set(String::new());
        self.hook_path.set(row.hook_path.clone());
        self.notice.set(String::new());
        self.secret_once.set(None);
        self.generation.update(|n| *n = n.wrapping_add(1));
    }

    fn clear(self) {
        self.selected.set(String::new());
        self.name.set(String::new());
        self.kind.set("webhook".into());
        self.prompt.set(String::new());
        self.perm.set(String::new());
        self.continue_id.set(String::new());
        self.rate_text.set("6".into());
        self.dedupe_text.set("600".into());
        self.events_text.set(String::new());
        self.adapter.set(String::new());
        self.channel.set(String::new());
        self.enabled.set(true);
        self.secret_input.set(String::new());
        self.hook_path.set(String::new());
        self.notice.set(String::new());
        self.secret_once.set(None);
        self.generation.update(|n| *n = n.wrapping_add(1));
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct SecretOnce {
    trigger_id: String,
    name: String,
    secret: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct SaveStamp {
    generation: u64,
    selected: String,
    submitted_secret: String,
    name: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct SaveResponse {
    id: String,
    hook_path: String,
    secret: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum SaveCommit {
    Here {
        id: String,
        hook_path: String,
        secret: Option<SecretOnce>,
        clear_secret_input: bool,
        notice: String,
    },
    Elsewhere {
        notice: String,
    },
}

fn shown_secret(secret: Option<&SecretOnce>, selected: &str) -> Option<SecretOnce> {
    let secret = secret?;
    if secret.secret.is_empty() || secret.trigger_id != selected {
        None
    } else {
        Some(secret.clone())
    }
}

fn secret_banner_text(name: &str) -> String {
    let name = name.trim();
    if name.is_empty() {
        "Copy this signing secret now. Strike will not show it again.".into()
    } else {
        format!("Copy the signing secret for {name} now. Strike will not show it again.")
    }
}

fn commit_save(
    stamp: &SaveStamp,
    generation_now: u64,
    selected_now: &str,
    secret_input_now: &str,
    response: &SaveResponse,
) -> SaveCommit {
    if stamp.generation != generation_now || stamp.selected != selected_now {
        let name = stamp.name.trim();
        let notice = if name.is_empty() {
            "Saved.".into()
        } else {
            format!("Saved {name}.")
        };
        return SaveCommit::Elsewhere { notice };
    }
    let id = if response.id.is_empty() {
        stamp.selected.clone()
    } else {
        response.id.clone()
    };
    let secret = if response.secret.is_empty() {
        None
    } else {
        Some(SecretOnce {
            trigger_id: id.clone(),
            name: stamp.name.clone(),
            secret: response.secret.clone(),
        })
    };
    SaveCommit::Here {
        id,
        hook_path: response.hook_path.clone(),
        secret,
        clear_secret_input: secret_input_now == stamp.submitted_secret,
        notice: "Saved.".into(),
    }
}

fn refresh(
    base: RwSignal<String>,
    token: RwSignal<String>,
    rows: RwSignal<Vec<TriggerRow>>,
    notice: RwSignal<String>,
) {
    let (b, t) = (base.get(), token.get());
    spawn_local(async move {
        match get_json::<TriggerList>(&b, &t, "/v1/triggers").await {
            Ok(list) => rows.set(list.triggers),
            Err(e) => notice.set(e),
        }
    });
}

fn load_deliveries(
    base: RwSignal<String>,
    token: RwSignal<String>,
    id: String,
    rows: RwSignal<Vec<DeliveryRow>>,
    notice: RwSignal<String>,
    log_error: RwSignal<String>,
    loading: RwSignal<bool>,
    epoch: RwSignal<LoadEpoch>,
) {
    let gen = begin(epoch);
    rows.set(Vec::new());
    log_error.set(String::new());
    if id.is_empty() {
        loading.set(false);
        return;
    }
    loading.set(true);
    let (b, t) = (base.get(), token.get());
    spawn_local(async move {
        let fetched =
            get_json::<DeliveryList>(&b, &t, &format!("/v1/triggers/{id}/deliveries")).await;
        let raw = fetched.as_ref().err().cloned();
        let mapped = match fetched {
            Ok(list) => Ok(list.deliveries),
            Err(_) => Err("Deliveries failed to load for this trigger.".into()),
        };
        match epoch
            .with_untracked(|epoch| commit_load(epoch, gen, mapped, Vec::new(), ErrPolicy::Clear))
        {
            Commit::Drop => return,
            Commit::Apply { value, error } => {
                let failed = !error.is_empty();
                rows.set(value);
                log_error.set(error);
                loading.set(false);
                if failed {
                    if let Some(raw) = raw {
                        notice.set(raw);
                    }
                }
            }
        }
    });
}

async fn send_json(
    method: &str,
    base: &str,
    token: &str,
    path: &str,
    body: Value,
) -> Result<Value, String> {
    let req = match method {
        "PATCH" => gloo_net::http::Request::patch(&api(base, path)),
        "DELETE" => gloo_net::http::Request::delete(&api(base, path)),
        _ => gloo_net::http::Request::post(&api(base, path)),
    };
    let resp = req
        .header("Authorization", &auth_header(token))
        .header("Content-Type", "application/json")
        .body(body.to_string())
        .map_err(|e| e.to_string())?
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status) {
        return Err(format!("HTTP {status} {text}"));
    }
    if text.trim().is_empty() {
        return Ok(Value::Null);
    }
    serde_json::from_str(&text).map_err(|e| e.to_string())
}

#[component]
pub fn TriggersPage(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let rows = RwSignal::new(Vec::<TriggerRow>::new());
    let deliveries = RwSignal::new(Vec::<DeliveryRow>::new());
    let delivery_error = RwSignal::new(String::new());
    let delivery_loading = RwSignal::new(false);
    let delivery_epoch = RwSignal::new(LoadEpoch::new());
    let secret_revealed = RwSignal::new(false);
    let form = Form::new();

    refresh(base, token, rows, form.notice);
    Effect::new(move |_| {
        load_deliveries(
            base,
            token,
            form.selected.get(),
            deliveries,
            form.notice,
            delivery_error,
            delivery_loading,
            delivery_epoch,
        );
    });

    let save = move || {
        let name_v = form.name.get().trim().to_string();
        if name_v.is_empty() {
            form.notice.set("Name is required.".into());
            return;
        }
        let Ok(rate) = form.rate_text.get().trim().parse::<i32>() else {
            form.notice
                .set("Rate must be a number from 1 to 120.".into());
            return;
        };
        if !(1..=120).contains(&rate) {
            form.notice
                .set("Rate must be a number from 1 to 120.".into());
            return;
        }
        let Ok(dedupe) = form.dedupe_text.get().trim().parse::<i32>() else {
            form.notice
                .set("Dedupe window must be 0 to 86400 seconds.".into());
            return;
        };
        if !(0..=86400).contains(&dedupe) {
            form.notice
                .set("Dedupe window must be 0 to 86400 seconds.".into());
            return;
        }
        let events: Vec<String> = form
            .events_text
            .get()
            .split(',')
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
            .collect();
        let mut body = json!({
            "name": name_v,
            "kind": form.kind.get(),
            "prompt": form.prompt.get(),
            "permissionMode": form.perm.get(),
            "continueSessionId": form.continue_id.get().trim(),
            "ratePerMinute": rate,
            "dedupeSeconds": dedupe,
            "events": events,
            "adapter": form.adapter.get().trim(),
            "channel": form.channel.get().trim(),
            "enabled": form.enabled.get(),
        });
        let typed = form.secret_input.get().trim().to_string();
        if !typed.is_empty() {
            body["secret"] = json!(typed);
        }
        let id = form.selected.get();
        form.generation.update(|n| *n = n.wrapping_add(1));
        let stamp = SaveStamp {
            generation: form.generation.get_untracked(),
            selected: id.clone(),
            submitted_secret: typed,
            name: name_v,
        };
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            let result = if id.is_empty() {
                post_json_value(&b, &t, "/v1/triggers", body).await
            } else {
                send_json("PATCH", &b, &t, &format!("/v1/triggers/{id}"), body).await
            };
            match result {
                Ok(v) => {
                    let response = SaveResponse {
                        id: v
                            .get("id")
                            .and_then(|s| s.as_str())
                            .unwrap_or("")
                            .to_string(),
                        hook_path: v
                            .get("hookPath")
                            .and_then(|s| s.as_str())
                            .unwrap_or("")
                            .to_string(),
                        secret: v
                            .get("secret")
                            .and_then(|s| s.as_str())
                            .unwrap_or("")
                            .to_string(),
                    };
                    match commit_save(
                        &stamp,
                        form.generation.get_untracked(),
                        &form.selected.get_untracked(),
                        form.secret_input.get_untracked().trim(),
                        &response,
                    ) {
                        SaveCommit::Here {
                            id,
                            hook_path,
                            secret,
                            clear_secret_input,
                            notice,
                        } => {
                            if secret.is_some() {
                                secret_revealed.set(false);
                            }
                            form.secret_once.set(secret);
                            if !id.is_empty() {
                                form.selected.set(id);
                            }
                            if !hook_path.is_empty() {
                                form.hook_path.set(hook_path);
                            }
                            if clear_secret_input {
                                form.secret_input.set(String::new());
                            }
                            form.notice.set(notice);
                            status.set("Trigger saved".into());
                            refresh(base, token, rows, form.notice);
                        }
                        SaveCommit::Elsewhere { notice } => {
                            form.notice.set(notice);
                            refresh(base, token, rows, form.notice);
                        }
                    }
                }
                Err(e) => {
                    if form.generation.get_untracked() == stamp.generation
                        && form.selected.get_untracked() == stamp.selected
                    {
                        form.notice.set(e);
                    } else {
                        form.notice.set(format!("Save failed for {}.", stamp.name));
                    }
                }
            }
        });
    };

    let send_test = move || {
        let id = form.selected.get();
        if id.is_empty() {
            form.notice
                .set("Save the trigger before sending a test.".into());
            return;
        }
        let kind_v = form.kind.get();
        let delivery = format!("ui{}", now_ms() as i64);
        let body = if kind_v == "github" {
            json!({
                "event": "issues",
                "deliveryId": delivery,
                "body": {
                    "action": "opened",
                    "repository": {"full_name": "example/repo"},
                    "issue": {"number": 1, "title": "Test issue", "body": "Synthetic payload from the Triggers page."},
                    "sender": {"login": "strike"}
                }
            })
        } else if kind_v == "channel" {
            json!({
                "deliveryId": delivery,
                "text": "Synthetic message from the Triggers page.",
                "sender": "you",
                "channel": form.channel.get()
            })
        } else {
            json!({
                "event": "test",
                "deliveryId": delivery,
                "body": "{\"hello\":\"strike\"}"
            })
        };
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match post_json_value(&b, &t, &format!("/v1/triggers/{id}/test"), body).await {
                Ok(v) => {
                    let outcome = v.get("outcome").and_then(|s| s.as_str()).unwrap_or("sent");
                    form.notice.set(format!("Test {outcome}."));
                    status.set(format!("Trigger test {outcome}"));
                    load_deliveries(
                        base,
                        token,
                        id,
                        deliveries,
                        form.notice,
                        delivery_error,
                        delivery_loading,
                        delivery_epoch,
                    );
                }
                Err(e) => form.notice.set(e),
            }
        });
    };

    let reject_sig = move || {
        let id = form.selected.get();
        if id.is_empty() {
            form.notice
                .set("Save the trigger before trying a bad signature.".into());
            return;
        }
        let kind_v = form.kind.get();
        let b = base.get();
        spawn_local(async move {
            let ts = ((now_ms() / 1000.0) as i64).to_string();
            let delivery = format!("bad{}", now_ms() as i64);
            let req = gloo_net::http::Request::post(&api(&b, &format!("/hooks/{id}")))
                .header("Content-Type", "application/json");
            let req = if kind_v == "github" {
                req.header("X-GitHub-Delivery", &delivery)
                    .header("X-GitHub-Event", "issues")
                    .header(
                        "X-Hub-Signature-256",
                        "sha256=0000000000000000000000000000000000000000000000000000000000000000",
                    )
            } else {
                req.header("X-Strike-Delivery", &delivery)
                    .header("X-Strike-Timestamp", &ts)
                    .header("X-Strike-Event", "test")
                    .header(
                        "X-Strike-Signature",
                        "sha256=0000000000000000000000000000000000000000000000000000000000000000",
                    )
            };
            match req.body("{}") {
                Ok(built) => match built.send().await {
                    Ok(resp) => {
                        let status_code = resp.status();
                        let text = resp.text().await.unwrap_or_default();
                        form.notice
                            .set(format!("Hook returned {status_code}. {text}"));
                        load_deliveries(
                            base,
                            token,
                            id,
                            deliveries,
                            form.notice,
                            delivery_error,
                            delivery_loading,
                            delivery_epoch,
                        );
                    }
                    Err(e) => form.notice.set(e.to_string()),
                },
                Err(e) => form.notice.set(e.to_string()),
            }
        });
    };

    let delete_one = move |id: String| {
        if id.is_empty() {
            form.notice.set("Select a trigger first.".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match send_json("DELETE", &b, &t, &format!("/v1/triggers/{id}"), Value::Null).await {
                Ok(_) => {
                    form.clear();
                    form.notice.set("Deleted.".into());
                    refresh(base, token, rows, form.notice);
                }
                Err(e) => form.notice.set(e),
            }
        });
    };
    crate::shell::bind(
        "trigger.delete",
        Rc::new(move |id| {
            let id = id.trim().to_string();
            if id.is_empty() {
                return Err("Type a trigger id.".into());
            }
            delete_one(id);
            Ok(())
        }),
    );

    view! {
        <main class="triggers">
            <section class="triggers-list" aria-label="Triggers">
                <div class="section-head">
                    <h2 class="panel-title">"Triggers"</h2>
                    <button type="button" class="secondary" on:click=move |_| form.clear()>"New"</button>
                </div>
                <Show when=move || rows.get().is_empty()>
                    <p class="hint">"No triggers yet. Save one on the right. An empty permission mode stays plan, which denies writes."</p>
                </Show>
                <ul class="plain">
                    {move || rows.get().into_iter().map(|row| {
                        let pick = row.clone();
                        let id = row.id.clone();
                        let label = row.name.clone();
                        let meta = format!("{} · {}", row.kind, if row.enabled { "on" } else { "off" });
                        view! {
                            <li>
                                <button type="button" class=move || if form.selected.get() == id { "row card on" } else { "row card" } on:click=move |_| form.fill(&pick)>
                                    <span class="name">{label}</span>
                                    <span class="path">{meta}</span>
                                </button>
                            </li>
                        }
                    }).collect_view()}
                </ul>
            </section>
            <section class="triggers-detail">
                <form on:submit=move |ev: web_sys::SubmitEvent| {
                    ev.prevent_default();
                    save();
                } on:keydown=move |ev: web_sys::KeyboardEvent| {
                    if ev.key() != "Enter" {
                        return;
                    }
                    let Some(target) = ev.target() else { return };
                    if target.dyn_ref::<web_sys::HtmlTextAreaElement>().is_some() {
                        return;
                    }
                    ev.prevent_default();
                    save();
                }>
                    <h2 class="panel-title">{move || if form.selected.get().is_empty() { "New trigger".to_string() } else { "Edit trigger".to_string() }}</h2>
                    <Show when=move || !form.notice.get().is_empty()>
                        <p class="notice" role="alert">{move || form.notice.get()}</p>
                    </Show>
                    <Show when=move || shown_secret(form.secret_once.get().as_ref(), &form.selected.get()).is_some()>
                        <div class="secret-once" role="status">
                            <p>{move || secret_banner_text(&form.secret_once.get().map(|row| row.name).unwrap_or_default())}</p>
                            <Show when=move || secret_revealed.get() fallback=move || view! {
                                <input type="password" readonly autocomplete="off" prop:value=move || form.secret_once.get().map(|row| row.secret).unwrap_or_default() />
                            }>
                                <input type="text" readonly autocomplete="off" prop:value=move || form.secret_once.get().map(|row| row.secret).unwrap_or_default() />
                            </Show>
                            <button type="button" class="secondary" on:click=move |_| secret_revealed.set(!secret_revealed.get())>
                                {move || if secret_revealed.get() { "Mask secret" } else { "Reveal secret" }}
                            </button>
                            <button type="button" class="secondary" on:click=move |_| { secret_revealed.set(false); form.secret_once.set(None); }>"Dismiss secret"</button>
                        </div>
                    </Show>
                    <label for="trigger-name">"Name"</label>
                    <input id="trigger-name" prop:value=move || form.name.get() on:input=move |ev| form.name.set(event_target_value(&ev)) />
                    <label for="trigger-kind">"Kind"</label>
                    <select id="trigger-kind" prop:value=move || form.kind.get() on:change=move |ev| form.kind.set(event_target_value(&ev))>
                        <option value="webhook">"Webhook"</option>
                        <option value="github">"GitHub"</option>
                        <option value="channel">"Channel"</option>
                    </select>
                    <label for="trigger-perm">"Permission mode"</label>
                    <select id="trigger-perm" prop:value=move || form.perm.get() on:change=move |ev| form.perm.set(event_target_value(&ev))>
                        <option value="">"Plan when unset (safest)"</option>
                        <option value="plan">"Plan"</option>
                        <option value="default">"Default"</option>
                        <option value="soft-approve">"Soft approve"</option>
                        <option value="accept-edits">"Accept edits"</option>
                        <option value="yolo">"Yolo"</option>
                    </select>
                    <Show when=move || matches!(form.perm.get().as_str(), "yolo" | "accept-edits" | "soft-approve")>
                        <p class="hint warn">"This posture is looser than plan. Unattended triggers start in plan unless you set one here."</p>
                    </Show>
                    <p class="hint">{move || format!("Resolved at fire time: {}", if form.perm.get().is_empty() { "plan".to_string() } else { form.perm.get() })}</p>
                    <label for="trigger-prompt">"Prompt template"</label>
                    <textarea id="trigger-prompt" rows="5" prop:value=move || form.prompt.get() on:input=move |ev| form.prompt.set(event_target_value(&ev)) placeholder="Leave blank for the built-in template. Placeholders: {{event}} {{action}} {{repo}} {{number}} {{title}} {{body}} {{url}} {{sender}} {{text}}"></textarea>
                    <Show when=move || form.kind.get() != "channel">
                        <label for="trigger-secret">"Signing secret"</label>
                        <input id="trigger-secret" type="password" autocomplete="off" prop:value=move || form.secret_input.get() on:input=move |ev| form.secret_input.set(event_target_value(&ev)) placeholder="Blank keeps the current secret" />
                    </Show>
                    <Show when=move || form.kind.get() == "github">
                        <label for="trigger-events">"GitHub events"</label>
                        <input id="trigger-events" prop:value=move || form.events_text.get() on:input=move |ev| form.events_text.set(event_target_value(&ev)) placeholder="issues, pull_request, issue_comment" />
                    </Show>
                    <Show when=move || form.kind.get() == "channel">
                        <label for="trigger-adapter">"Adapter name"</label>
                        <input id="trigger-adapter" prop:value=move || form.adapter.get() on:input=move |ev| form.adapter.set(event_target_value(&ev)) placeholder="slack" />
                        <label for="trigger-channel">"Channel filter"</label>
                        <input id="trigger-channel" prop:value=move || form.channel.get() on:input=move |ev| form.channel.set(event_target_value(&ev)) placeholder="Blank matches every channel on that adapter" />
                        <p class="hint">"No chat listener is running. The adapter interface is registered in the daemon; Send test injects one message."</p>
                    </Show>
                    <label for="trigger-continue">"Continue session"</label>
                    <input id="trigger-continue" prop:value=move || form.continue_id.get() on:input=move |ev| form.continue_id.set(event_target_value(&ev)) placeholder="Blank starts a new root" />
                    <div class="row">
                        <label>"Per minute"<input prop:value=move || form.rate_text.get() on:input=move |ev| form.rate_text.set(event_target_value(&ev)) /></label>
                        <label>"Dedupe seconds"<input prop:value=move || form.dedupe_text.get() on:input=move |ev| form.dedupe_text.set(event_target_value(&ev)) /></label>
                    </div>
                    <div class="row triggers-actions">
                        <button type="button" class=move || if form.enabled.get() { "secondary on" } else { "secondary" } on:click=move |_| form.enabled.set(!form.enabled.get())>
                            {move || if form.enabled.get() { "Enabled" } else { "Disabled" }}
                        </button>
                        <button type="submit" class="primary">"Save"</button>
                        <button type="button" class="secondary" on:click=move |_| send_test()>"Send test"</button>
                        <button type="button" class="secondary" on:click=move |_| reject_sig()>"Reject signature"</button>
                        <Show when=move || !form.selected.get().is_empty()>
                            <button type="button" class="secondary" data-action="delete-trigger" on:click=move |_| {
                                let id = form.selected.get();
                                let name = form.name.get();
                                let hook = form.hook_path.get();
                                let body = if hook.is_empty() {
                                    format!("Delete {name}. The hook stops accepting events, and a replacement trigger gets a new secret.")
                                } else {
                                    format!("Delete {name}. {}{hook} stops accepting events, and anything sending to it has to be reconfigured with a new secret.", origin_prefix(&base.get_untracked()))
                                };
                                if !crate::confirm::ask(
                                    crate::confirm::ConfirmRequest {
                                        title: "Delete this trigger?".into(),
                                        body,
                                        confirm_label: "Delete trigger".into(),
                                    },
                                    Rc::new(move || delete_one(id.clone())),
                                ) {
                                    form.notice.set(crate::confirm::BUSY.into());
                                }
                            }>"Delete"</button>
                        </Show>
                    </div>
                    <p class="hint keys">"Enter saves. Tab moves through the form. Ctrl+K, then Triggers, opens this page."</p>
                    <Show when=move || !form.hook_path.get().is_empty() && form.kind.get() != "channel">
                        <p class="hint">{move || format!("Hook {}{} · secret {}", origin_prefix(&base.get()), form.hook_path.get(), if rows.get().iter().any(|r| r.id == form.selected.get() && r.secret_set) { "is set" } else { "is not set" })}</p>
                    </Show>
                    <p class="hint">{move || {
                        let mode = rows.get().into_iter().find(|r| r.id == form.selected.get()).map(|r| r.resolved_permission_mode).unwrap_or_else(|| "plan".into());
                        format!("Unattended runs use {mode} unless this form names another mode.")
                    }}</p>
                </form>
            </section>
            <section class="triggers-log" aria-label="Delivery log">
                    <h2 class="panel-title">"Delivery log"</h2>
                    <Show when=move || delivery_loading.get()>
                        <p class="hint">"Loading deliveries…"</p>
                    </Show>
                    <Show when=move || !delivery_error.get().is_empty()>
                        <p class="hint" role="alert">{move || delivery_error.get()}</p>
                    </Show>
                    <Show when=move || deliveries.get().is_empty() && delivery_error.get().is_empty() && !delivery_loading.get()>
                        <p class="hint">"Accepted, rejected, duplicate, and rate-limited deliveries show up here. The log does not store the signing secret."</p>
                    </Show>
                    <ul class="plain">
                        {move || deliveries.get().into_iter().map(|row| {
                            let replay_id = row.id.clone();
                            let headline = format!("{} · {}", row.outcome, row.event);
                            let detail = if row.reason.is_empty() { row.session_id.clone() } else { row.reason.clone() };
                            let when = row.at.clone();
                            let can_replay = row.outcome == "accepted" || row.outcome == "replayed";
                            view! {
                                <li class="row card">
                                    <span>
                                        <span class="name">{headline}</span>
                                        <span class="path">{detail}</span>
                                        <span class="path">{when}</span>
                                    </span>
                                    {can_replay.then(|| {
                                        let delivery_id = replay_id.clone();
                                        view! {
                                            <button type="button" class="secondary" on:click=move |_| {
                                                let trigger_id = form.selected.get();
                                                let delivery = delivery_id.clone();
                                                let (b, t) = (base.get(), token.get());
                                                spawn_local(async move {
                                                    match post_json_value(&b, &t, &format!("/v1/triggers/{trigger_id}/replay"), json!({"deliveryId": delivery})).await {
                                                        Ok(v) => {
                                                            let outcome = v.get("outcome").and_then(|s| s.as_str()).unwrap_or("replayed");
                                                            form.notice.set(format!("Replay {outcome}."));
                                                            load_deliveries(
                                                                base,
                                                                token,
                                                                trigger_id,
                                                                deliveries,
                                                                form.notice,
                                                                delivery_error,
                                                                delivery_loading,
                                                                delivery_epoch,
                                                            );
                                                        }
                                                        Err(e) => form.notice.set(e),
                                                    }
                                                });
                                            }>"Replay"</button>
                                        }
                                    })}
                                </li>
                            }
                        }).collect_view()}
                    </ul>
            </section>
        </main>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn secret_banner_is_tied_to_its_trigger() {
        let banner = SecretOnce {
            trigger_id: "trig-a".into(),
            name: "Alpha".into(),
            secret: "sek-a".into(),
        };
        let shown = shown_secret(Some(&banner), "trig-a").unwrap();
        assert_eq!(shown.secret, "sek-a");
        assert!(shown_secret(Some(&banner), "trig-b").is_none());
        assert!(shown_secret(None, "trig-a").is_none());
        assert!(secret_banner_text("Alpha").contains("Alpha"));
        assert!(!secret_banner_text("Alpha").contains("sek-a"));
    }

    #[test]
    fn save_completion_does_not_hybridize_another_trigger() {
        let stamp = SaveStamp {
            generation: 2,
            selected: "trig-a".into(),
            submitted_secret: "old".into(),
            name: "Alpha".into(),
        };
        let response = SaveResponse {
            id: "trig-a".into(),
            hook_path: "/hooks/a".into(),
            secret: "sek-a".into(),
        };
        match commit_save(&stamp, 3, "trig-b", "beta-secret", &response) {
            SaveCommit::Elsewhere { notice } => assert_eq!(notice, "Saved Alpha."),
            SaveCommit::Here { .. } => panic!("applied A's identity onto B"),
        }
        match commit_save(&stamp, 2, "trig-a", "old", &response) {
            SaveCommit::Here {
                id,
                hook_path,
                secret,
                clear_secret_input,
                notice,
            } => {
                assert_eq!(id, "trig-a");
                assert_eq!(hook_path, "/hooks/a");
                assert_eq!(secret.unwrap().trigger_id, "trig-a");
                assert!(clear_secret_input);
                assert_eq!(notice, "Saved.");
            }
            SaveCommit::Elsewhere { .. } => panic!("same form should commit in place"),
        }
        match commit_save(&stamp, 2, "trig-a", "typed-during-flight", &response) {
            SaveCommit::Here {
                clear_secret_input, ..
            } => assert!(!clear_secret_input),
            SaveCommit::Elsewhere { .. } => panic!("same form should commit in place"),
        }
    }
}

fn origin_prefix(base: &str) -> String {
    let base = base.trim().trim_end_matches('/').to_string();
    if !base.is_empty() {
        return base;
    }
    web_sys::window()
        .and_then(|w| w.location().origin().ok())
        .unwrap_or_default()
}
