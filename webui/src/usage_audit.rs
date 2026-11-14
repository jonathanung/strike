//! Usage dashboard and tamper-evident audit log.
//! Dollars are whatever GET /v1/usage reports. This module does not invent a price.

use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::Value;
use wasm_bindgen::JsCast;

use crate::api::{auth_header, get_json};
use load_guard::{commit_labeled, commit_usage, LoadEpoch};

#[derive(Clone, Deserialize, Default)]
struct UsageReport {
    #[serde(default)]
    sessions: Vec<SessionRow>,
    #[serde(default)]
    days: Vec<DayRow>,
    #[serde(default)]
    totals: Totals,
    #[serde(rename = "costUsd", default)]
    cost_usd: f64,
    #[serde(rename = "costKnown", default)]
    cost_known: bool,
    #[serde(rename = "costPartial", default)]
    cost_partial: bool,
    #[serde(rename = "costNote", default)]
    cost_note: String,
    #[serde(rename = "unpricedSessions", default)]
    unpriced_sessions: i32,
    #[serde(default)]
    alerts: Vec<Alert>,
    #[serde(rename = "budgetSet", default)]
    budget_set: bool,
    #[serde(rename = "dailyUsd", default)]
    daily_usd: f64,
    #[serde(rename = "pricingNote", default)]
    pricing_note: String,
}

#[derive(Clone, Deserialize, Default)]
struct Totals {
    #[serde(default)]
    input: i64,
    #[serde(default)]
    output: i64,
    #[serde(rename = "cacheRead", default)]
    cache_read: i64,
    #[serde(rename = "cacheCreation", default)]
    cache_creation: i64,
    #[serde(rename = "inputKnown", default)]
    input_known: bool,
    #[serde(rename = "outputKnown", default)]
    output_known: bool,
    #[serde(rename = "cacheReadKnown", default)]
    cache_read_known: bool,
    #[serde(rename = "cacheCreationKnown", default)]
    cache_creation_known: bool,
    #[serde(default)]
    reports: i32,
}

#[derive(Clone, Deserialize, Default)]
struct SessionRow {
    #[serde(default)]
    id: String,
    #[serde(default)]
    title: String,
    #[serde(rename = "parentId", default)]
    parent_id: String,
    #[serde(default)]
    provider: String,
    #[serde(default)]
    model: String,
    #[serde(rename = "firstDay", default)]
    first_day: String,
    #[serde(rename = "lastDay", default)]
    last_day: String,
    #[serde(default)]
    source: String,
    #[serde(default)]
    input: i64,
    #[serde(default)]
    output: i64,
    #[serde(rename = "cacheRead", default)]
    cache_read: i64,
    #[serde(rename = "cacheCreation", default)]
    cache_creation: i64,
    #[serde(rename = "inputKnown", default)]
    input_known: bool,
    #[serde(rename = "outputKnown", default)]
    output_known: bool,
    #[serde(rename = "cacheReadKnown", default)]
    cache_read_known: bool,
    #[serde(rename = "cacheCreationKnown", default)]
    cache_creation_known: bool,
    #[serde(default)]
    reports: i32,
    #[serde(rename = "costUsd", default)]
    cost_usd: f64,
    #[serde(rename = "costKnown", default)]
    cost_known: bool,
    #[serde(rename = "costPartial", default)]
    cost_partial: bool,
    #[serde(rename = "costNote", default)]
    cost_note: String,
}

#[derive(Clone, Deserialize, Default)]
struct DayRow {
    #[serde(default)]
    day: String,
    #[serde(default)]
    sessions: i32,
    #[serde(default)]
    input: i64,
    #[serde(default)]
    output: i64,
    #[serde(rename = "cacheRead", default)]
    cache_read: i64,
    #[serde(rename = "cacheCreation", default)]
    cache_creation: i64,
    #[serde(rename = "inputKnown", default)]
    input_known: bool,
    #[serde(rename = "outputKnown", default)]
    output_known: bool,
    #[serde(rename = "cacheReadKnown", default)]
    cache_read_known: bool,
    #[serde(rename = "cacheCreationKnown", default)]
    cache_creation_known: bool,
    #[serde(default)]
    reports: i32,
    #[serde(rename = "costUsd", default)]
    cost_usd: f64,
    #[serde(rename = "costKnown", default)]
    cost_known: bool,
    #[serde(rename = "costPartial", default)]
    cost_partial: bool,
    #[serde(rename = "unpricedEvents", default)]
    unpriced_events: i32,
    #[serde(rename = "costNote", default)]
    cost_note: String,
}

#[derive(Clone, Deserialize, Default)]
struct Alert {
    #[serde(default)]
    level: String,
    #[serde(default)]
    scope: String,
    #[serde(rename = "sessionId", default)]
    session_id: String,
    #[serde(default)]
    message: String,
}

#[derive(Clone, Deserialize, Default)]
struct AuditList {
    #[serde(default)]
    configured: bool,
    #[serde(default)]
    verified: bool,
    #[serde(default)]
    error: String,
    #[serde(default)]
    note: String,
    #[serde(default)]
    entries: Vec<AuditEntry>,
}

#[derive(Clone, Deserialize, Default)]
struct AuditEntry {
    #[serde(default)]
    seq: i64,
    #[serde(default)]
    time: String,
    #[serde(default)]
    action: String,
    #[serde(rename = "sessionId", default)]
    session_id: String,
    #[serde(default)]
    summary: String,
}

fn tok(n: i64, known: bool) -> String {
    if !known {
        return "unknown".into();
    }
    n.to_string()
}

fn money(known: bool, partial: bool, usd: f64, note: &str) -> String {
    if !known {
        if note.is_empty() {
            return "not priced".into();
        }
        return format!("not priced — {note}");
    }
    let shown = if usd > 0.0 && usd < 0.01 {
        "<$0.01".to_string()
    } else if usd == 0.0 {
        "$0".to_string()
    } else {
        format!("${usd:.2}")
    };
    if partial {
        if note.is_empty() {
            format!("{shown} partial")
        } else {
            format!("{shown} partial — {note}")
        }
    } else {
        shown
    }
}

async fn put_json(base: &str, token: &str, path: &str, body: Value) -> Result<Value, String> {
    let resp = gloo_net::http::Request::put(&crate::api::api(base, path))
        .header("Authorization", &auth_header(token))
        .json(&body)
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

fn download_text(filename: &str, text: &str) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let Some(doc) = win.document() else {
        return;
    };
    let Ok(anchor) = doc.create_element("a") else {
        return;
    };
    let href = format!(
        "data:application/json;charset=utf-8,{}",
        crate::api::js_encode(text)
    );
    let _ = anchor.set_attribute("href", &href);
    let _ = anchor.set_attribute("download", filename);
    if let Some(el) = anchor.dyn_ref::<web_sys::HtmlElement>() {
        el.click();
    }
}

#[component]
pub fn UsageAuditPages(
    mode: RwSignal<String>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    view! {
        <div class="usage-audit-shell">
            <div class="segmented" role="tablist" aria-label="Usage and audit">
                <button
                    type="button"
                    data-view="usage"
                    class=move || if mode.get() == "audit" { "seg" } else { "seg on" }
                    on:click=move |_| mode.set("usage".into())
                >
                    "Usage"
                </button>
                <button
                    type="button"
                    data-view="audit"
                    class=move || if mode.get() == "audit" { "seg on" } else { "seg" }
                    on:click=move |_| mode.set("audit".into())
                >
                    "Audit"
                </button>
            </div>
            <Show when=move || mode.get() != "audit">
                <UsagePage base=base token=token status=status />
            </Show>
            <Show when=move || mode.get() == "audit">
                <AuditPage base=base token=token status=status />
            </Show>
        </div>
    }
}

#[component]
pub fn UsagePage(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let report = RwSignal::new(UsageReport::default());
    let error = RwSignal::new(String::new());
    let budget = RwSignal::new(String::new());
    let loaded = RwSignal::new(false);
    let refresh = RwSignal::new(0u32);
    let usage_epoch = RwSignal::new(LoadEpoch::new());

    Effect::new(move |_| {
        refresh.get();
        let (b, t) = (base.get(), token.get());
        let ticket = crate::load_epoch::begin(usage_epoch);
        spawn_local(async move {
            let result = match get_json::<UsageReport>(&b, &t, "/v1/usage").await {
                Ok(rep) => Ok((rep.budget_set, format!("{}", rep.daily_usd), rep)),
                Err(e) => Err(e),
            };
            match result {
                Ok((set, daily, rep)) => {
                    let mut budget_v = budget.get_untracked();
                    let mut error_v = error.get_untracked();
                    let mut loaded_v = loaded.get_untracked();
                    if commit_usage(
                        &usage_epoch.get_untracked(),
                        ticket,
                        &mut budget_v,
                        &mut error_v,
                        &mut loaded_v,
                        Ok((set, daily)),
                    ) {
                        budget.set(budget_v);
                        error.set(error_v);
                        loaded.set(loaded_v);
                        report.set(rep);
                    }
                }
                Err(e) => {
                    let mut budget_v = budget.get_untracked();
                    let mut error_v = error.get_untracked();
                    let mut loaded_v = loaded.get_untracked();
                    if commit_usage(
                        &usage_epoch.get_untracked(),
                        ticket,
                        &mut budget_v,
                        &mut error_v,
                        &mut loaded_v,
                        Err(e),
                    ) {
                        budget.set(budget_v);
                        error.set(error_v);
                        loaded.set(loaded_v);
                    }
                }
            }
        });
    });

    let save_budget = move |ev: web_sys::SubmitEvent| {
        ev.prevent_default();
        let raw = budget.get();
        let parsed: Result<f64, _> = raw.trim().parse();
        let usd = match parsed {
            Ok(n) if n >= 0.0 && n.is_finite() => n,
            _ => {
                error.set("Enter a daily budget of 0 or more. 0 clears it.".into());
                status.set("budget not saved".into());
                return;
            }
        };
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match put_json(
                &b,
                &t,
                "/v1/usage/budget",
                serde_json::json!({"dailyUsd": usd}),
            )
            .await
            {
                Ok(_) => {
                    error.set(String::new());
                    status.set("daily budget saved".into());
                    refresh.update(|n| *n += 1);
                }
                Err(e) => {
                    error.set(e);
                    status.set("budget rejected".into());
                }
            }
        });
    };

    view! {
        <main class="usage-audit">
            <div class="ua-wrap">
                <h2 class="panel-title">"Usage"</h2>
                <p class="hint">"Token totals are summed from usage.reported events, including cache tokens. Dollars appear only when the model catalog published a price. Cache tokens without a published cache rate stay out of the dollar figure."</p>
                <Show when=move || !error.get().is_empty()>
                    <p class="ua-alert bad" role="alert">{move || error.get()}</p>
                </Show>
                {move || {
                    report.get().alerts.into_iter().map(|a| {
                        let class = if a.level == "100" || a.level == "incomplete" { "ua-alert bad" } else { "ua-alert" };
                        let mut text = a.message.clone();
                        if !a.scope.is_empty() {
                            text = format!("{} · {}", a.scope, text);
                        }
                        if !a.session_id.is_empty() {
                            text = format!("{} ({})", text, short_id(&a.session_id));
                        }
                        view! { <p class=class role="status">{text}</p> }
                    }).collect_view()
                }}
                <Show when=move || !report.get().pricing_note.is_empty()>
                    <p class="ua-alert">{move || report.get().pricing_note}</p>
                </Show>
                <Show when=move || { report.get().unpriced_sessions > 0 }>
                    <p class="ua-alert">{move || {
                        let n = report.get().unpriced_sessions;
                        let noun = if n == 1 { "session has" } else { "sessions have" };
                        format!("{n} {noun} no published price. Those tokens are listed and left out of any dollar total.")
                    }}</p>
                </Show>
                <p class="hint">{move || format!("{} usage reports", report.get().totals.reports)}</p>
                <div class="ua-grid">
                    <div class="stat card"><div class="stat-value">{move || tok(report.get().totals.input, report.get().totals.input_known)}</div><div class="stat-label">"Input tokens"</div></div>
                    <div class="stat card"><div class="stat-value">{move || tok(report.get().totals.output, report.get().totals.output_known)}</div><div class="stat-label">"Output tokens"</div></div>
                    <div class="stat card"><div class="stat-value">{move || tok(report.get().totals.cache_read, report.get().totals.cache_read_known)}</div><div class="stat-label">"Cache read"</div></div>
                    <div class="stat card"><div class="stat-value">{move || tok(report.get().totals.cache_creation, report.get().totals.cache_creation_known)}</div><div class="stat-label">"Cache write"</div></div>
                    <div class="stat card"><div class="stat-value">{move || {
                        let r = report.get();
                        money(r.cost_known, r.cost_partial, r.cost_usd, &r.cost_note)
                    }}</div><div class="stat-label">"Cost"</div></div>
                </div>
                <form class="ua-form" on:submit=save_budget>
                    <label for="daily-budget">"Daily budget (USD)"</label>
                    <input id="daily-budget" inputmode="decimal" prop:value=move || budget.get() on:input=move |ev| budget.set(event_target_value(&ev)) />
                    <button type="submit">"Save budget"</button>
                    <span class="hint">{move || if report.get().budget_set { format!("Ceiling ${}", report.get().daily_usd) } else { "No daily ceiling".into() }}</span>
                </form>
                <p class="hint">"Ctrl+K, then Usage or Audit. Alt+U opens Usage. Alt+A opens Audit."</p>
                <Show when=move || loaded.get() && report.get().sessions.is_empty() && error.get().is_empty()>
                    <div class="empty-state">
                        <strong>"No provider usage recorded"</strong>
                        <p>"A completed model turn writes usage.reported. Until then this page stays empty rather than showing a made-up cost."</p>
                    </div>
                </Show>
                <Show when=move || !report.get().days.is_empty()>
                    <h2>"By day (UTC)"</h2>
                    <div class="ua-table" role="table">
                        <div class="ua-row ua-head" role="row">
                            <span>"Day"</span><span>"Sessions"</span><span>"In"</span><span>"Out"</span><span>"Cache read"</span><span>"Cache write"</span><span>"Cost"</span>
                        </div>
                        {move || report.get().days.into_iter().map(|d| {
                            let cost = money(d.cost_known, d.cost_partial || d.unpriced_events > 0, d.cost_usd, &d.cost_note);
                            let sessions = format!("{} · {} reports", d.sessions, d.reports);
                            view! {
                                <div class="ua-row" role="row">
                                    <span>{d.day}</span>
                                    <span>{sessions}</span>
                                    <span>{tok(d.input, d.input_known)}</span>
                                    <span>{tok(d.output, d.output_known)}</span>
                                    <span>{tok(d.cache_read, d.cache_read_known)}</span>
                                    <span>{tok(d.cache_creation, d.cache_creation_known)}</span>
                                    <span>{cost}</span>
                                </div>
                            }
                        }).collect_view()}
                    </div>
                </Show>
                <Show when=move || !report.get().sessions.is_empty()>
                    <h2>"By session"</h2>
                    <div class="ua-table" role="table">
                        <div class="ua-row ua-head" role="row">
                            <span>"Session"</span><span>"Role"</span><span>"In"</span><span>"Out"</span><span>"Cache read"</span><span>"Cache write"</span><span>"Cost"</span>
                        </div>
                        {move || report.get().sessions.into_iter().map(|s| {
                            let role = if s.parent_id.is_empty() { "root".to_string() } else { format!("child of {}", short_id(&s.parent_id)) };
                            let label = if s.title.is_empty() { short_id(&s.id) } else { s.title.clone() };
                            let model = if s.model.is_empty() { s.source.clone() } else { format!("{} / {}", s.provider, s.model) };
                            let cost = money(s.cost_known, s.cost_partial, s.cost_usd, &s.cost_note);
                            view! {
                                <div class="ua-row" role="row">
                                    <span><strong>{label}</strong><span class="hint">{" "}{model}{" "}{s.first_day.clone()}{"–"}{s.last_day.clone()}{" · "}{s.reports}{" reports"}</span></span>
                                    <span>{role}</span>
                                    <span>{tok(s.input, s.input_known)}</span>
                                    <span>{tok(s.output, s.output_known)}</span>
                                    <span>{tok(s.cache_read, s.cache_read_known)}</span>
                                    <span>{tok(s.cache_creation, s.cache_creation_known)}</span>
                                    <span>{cost}</span>
                                </div>
                            }
                        }).collect_view()}
                    </div>
                </Show>
            </div>
        </main>
    }
}

fn audit_label(list: &AuditList) -> String {
    if !list.configured {
        "Not configured".into()
    } else if list.verified {
        "Chain intact".into()
    } else {
        "Chain broken".into()
    }
}

fn short_id(id: &str) -> String {
    let n = id.chars().count();
    if n <= 10 {
        return id.to_string();
    }
    id.chars().take(8).collect()
}

#[component]
pub fn AuditPage(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
) -> impl IntoView {
    let list = RwSignal::new(AuditList::default());
    let error = RwSignal::new(String::new());
    let filter = RwSignal::new("all".to_string());
    let loaded = RwSignal::new(false);
    let refresh = RwSignal::new(0u32);
    let audit_epoch = RwSignal::new(LoadEpoch::new());

    Effect::new(move |_| {
        refresh.get();
        let (b, t) = (base.get(), token.get());
        let ticket = crate::load_epoch::begin(audit_epoch);
        spawn_local(async move {
            let result = match get_json::<AuditList>(&b, &t, "/v1/audit").await {
                Ok(body) => Ok(body),
                Err(e) => Err(e),
            };
            match result {
                Ok(body) => {
                    let verdict = audit_label(&body);
                    let mut label = audit_label(&list.get_untracked());
                    let mut error_v = error.get_untracked();
                    let mut loaded_v = loaded.get_untracked();
                    if commit_labeled(
                        &audit_epoch.get_untracked(),
                        ticket,
                        &mut label,
                        &mut error_v,
                        &mut loaded_v,
                        Ok(verdict),
                    ) {
                        list.set(body);
                        error.set(error_v);
                        loaded.set(loaded_v);
                    }
                }
                Err(e) => {
                    let mut label = audit_label(&list.get_untracked());
                    let mut error_v = error.get_untracked();
                    let mut loaded_v = loaded.get_untracked();
                    if commit_labeled(
                        &audit_epoch.get_untracked(),
                        ticket,
                        &mut label,
                        &mut error_v,
                        &mut loaded_v,
                        Err(e),
                    ) {
                        error.set(error_v);
                        loaded.set(loaded_v);
                    }
                }
            }
        });
    });

    let verify = move |_| {
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match get_json::<Value>(&b, &t, "/v1/audit/verify").await {
                Ok(_) => {
                    error.set(String::new());
                    status.set("audit chain verified".into());
                    refresh.update(|n| *n += 1);
                }
                Err(e) => {
                    error.set(e);
                    status.set("audit chain failed verification".into());
                    refresh.update(|n| *n += 1);
                }
            }
        });
    };
    let export = move || {
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match get_json::<Value>(&b, &t, "/v1/audit/export").await {
                Ok(body) => {
                    let text = serde_json::to_string_pretty(&body).unwrap_or_default();
                    download_text("strike-audit.json", &text);
                    status.set("audit export downloaded".into());
                }
                Err(e) => {
                    error.set(e);
                    status.set("audit export failed".into());
                }
            }
        });
    };
    crate::shell::bind(
        "audit.export",
        Rc::new(move |_| {
            export();
            Ok(())
        }),
    );
    if crate::park::take("audit.export").is_some() {
        export();
    }

    let actions = [
        "all",
        "approval",
        "permission",
        "shell",
        "terminal",
        "config",
        "token",
    ];

    view! {
        <main class="usage-audit">
            <div class="ua-wrap">
                <h2 class="panel-title">"Audit"</h2>
                <p class="hint">"Append-only hash chain of approvals, permission changes, shell and terminal use, config edits, and token rotation. Entries are redacted before they are hashed. Verify fails if a line was edited or the tail was removed."</p>
                <div class="ua-form">
                    <button on:click=verify>"Verify chain"</button>
                    <span class=move || if list.get().verified { "chip ok" } else { "chip bad" }>{move || if !list.get().configured { "Not configured".to_string() } else if list.get().verified { "Chain intact".to_string() } else { "Chain broken".to_string() }}</span>
                </div>
                <Show when=move || !error.get().is_empty()>
                    <p class="ua-alert bad" role="alert">{move || error.get()}</p>
                </Show>
                <Show when=move || !list.get().error.is_empty()>
                    <p class="ua-alert bad" role="alert">{move || list.get().error}</p>
                </Show>
                <Show when=move || !list.get().note.is_empty()>
                    <p class="hint">{move || list.get().note}</p>
                </Show>
                <div class="chips" role="tablist" aria-label="Audit filter">
                    {actions.into_iter().map(|name| {
                        let id = name.to_string();
                        let label = name.to_string();
                        let id_class = id.clone();
                        view! {
                            <button class=move || if filter.get() == id_class { "chip on" } else { "chip" } on:click=move |_| filter.set(id.clone())>{label}</button>
                        }
                    }).collect_view()}
                </div>
                <Show when=move || loaded.get() && list.get().entries.is_empty() && error.get().is_empty()>
                    <div class="empty-state">
                        <strong>"No audit entries"</strong>
                        <p>"Approvals, permission changes, shell commands, config edits, and token rotation are appended here. POST /v1/audit records terminal and other actions that are not already a protocol event."</p>
                    </div>
                </Show>
                <div class="ua-table" role="table">
                    {move || {
                        let want = filter.get();
                        list.get().entries.into_iter().rev().filter(|e| want == "all" || e.action == want).map(|e| {
                            view! {
                                <div class="ua-row ua-audit" role="row">
                                    <span class="hint">{format!("#{} {}", e.seq, e.time)}</span>
                                    <span class="chip">{e.action}</span>
                                    <span>{e.summary}</span>
                                    <span class="hint">{if e.session_id.is_empty() { String::new() } else { short_id(&e.session_id) }}</span>
                                </div>
                            }
                        }).collect_view()
                    }}
                </div>
            </div>
        </main>
    }
}
