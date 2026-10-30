//! Usage dashboard and tamper-evident audit log.
//! Dollars are whatever GET /v1/usage reports. This module does not invent a price.

use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::Value;
use wasm_bindgen::JsCast;

use crate::api::{auth_header, get_json};

#[derive(Clone, Deserialize, Default)]
struct UsageReport {
    #[serde(default)]
    sessions: Vec<SessionRow>,
    #[serde(default)]
    days: Vec<DayRow>,
    #[serde(default)]
    models: Vec<ModelRow>,
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

/// One provider+model row of the By model breakdown (#1470). Mirrors
/// `usageaudit.ModelUsage`; the daemon aggregates per event, so a
/// multi-model session's spend is already split before it reaches here.
#[derive(Clone, Deserialize, Default)]
struct ModelRow {
    #[serde(default)]
    provider: String,
    #[serde(default)]
    model: String,
    #[serde(rename = "firstDay", default)]
    first_day: String,
    #[serde(rename = "lastDay", default)]
    last_day: String,
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

// --- Usage export (#1481) -------------------------------------------------
// Client-side by design, following the Audit precedent in this file: the
// page already fetched the full report, so export serialises exactly that
// report — no /v1/usage/export route, no second source of truth. JSON is
// the fetched payload itself (kept as a Value at load time so fields this
// module does not model, e.g. skippedLines, survive); CSVs are built from
// the same structs the page renders, one file per breakdown.

fn csv_escape(field: &str) -> String {
    if field.contains([',', '"', '\n', '\r']) {
        format!("\"{}\"", field.replace('"', "\"\""))
    } else {
        field.to_string()
    }
}

fn csv_row(fields: &[String]) -> String {
    fields
        .iter()
        .map(|f| csv_escape(f))
        .collect::<Vec<_>>()
        .join(",")
}

/// Token cell for CSV: empty when the count was never reported, so a
/// spreadsheet never reads an unreported part as a measured zero.
fn csv_tok(n: i64, known: bool) -> String {
    if known {
        n.to_string()
    } else {
        String::new()
    }
}

fn csv_cost(known: bool, usd: f64) -> String {
    if known {
        format!("{usd}")
    } else {
        String::new()
    }
}

/// (first, last) day covered by the report, from the day rows when there
/// are any, else from session/model ranges. ("", "") when nothing is dated.
fn usage_range(rep: &UsageReport) -> (String, String) {
    let mut first = String::new();
    let mut last = String::new();
    let mut take = |day: &str| {
        if day.is_empty() {
            return;
        }
        if first.is_empty() || day < first.as_str() {
            first = day.to_string();
        }
        if day > last.as_str() {
            last = day.to_string();
        }
    };
    for d in &rep.days {
        take(&d.day);
    }
    if first.is_empty() {
        for s in &rep.sessions {
            take(&s.first_day);
            take(&s.last_day);
        }
        for m in &rep.models {
            take(&m.first_day);
            take(&m.last_day);
        }
    }
    (first, last)
}

/// `strike-usage[-kind]-YYYYMMDD-YYYYMMDD.ext`; the range segment is
/// dropped when the report carries no dates.
fn usage_filename(rep: &UsageReport, kind: &str, ext: &str) -> String {
    let (first, last) = usage_range(rep);
    let mut name = "strike-usage".to_string();
    if !kind.is_empty() {
        name.push('-');
        name.push_str(kind);
    }
    if !first.is_empty() {
        name.push('-');
        name.push_str(&first.replace('-', ""));
        name.push('-');
        name.push_str(&last.replace('-', ""));
    }
    name.push('.');
    name.push_str(ext);
    name
}

fn days_csv(rep: &UsageReport) -> String {
    let mut out = csv_row(&[
        "day".into(),
        "sessions".into(),
        "reports".into(),
        "input".into(),
        "output".into(),
        "cacheRead".into(),
        "cacheCreation".into(),
        "costUsd".into(),
        "costKnown".into(),
        "costPartial".into(),
        "unpricedEvents".into(),
        "costNote".into(),
    ]);
    out.push('\n');
    for d in &rep.days {
        out.push_str(&csv_row(&[
            d.day.clone(),
            d.sessions.to_string(),
            d.reports.to_string(),
            csv_tok(d.input, d.input_known),
            csv_tok(d.output, d.output_known),
            csv_tok(d.cache_read, d.cache_read_known),
            csv_tok(d.cache_creation, d.cache_creation_known),
            csv_cost(d.cost_known, d.cost_usd),
            d.cost_known.to_string(),
            d.cost_partial.to_string(),
            d.unpriced_events.to_string(),
            d.cost_note.clone(),
        ]));
        out.push('\n');
    }
    out
}

fn sessions_csv(rep: &UsageReport) -> String {
    let mut out = csv_row(&[
        "id".into(),
        "title".into(),
        "parentId".into(),
        "provider".into(),
        "model".into(),
        "firstDay".into(),
        "lastDay".into(),
        "reports".into(),
        "input".into(),
        "output".into(),
        "cacheRead".into(),
        "cacheCreation".into(),
        "costUsd".into(),
        "costKnown".into(),
        "costPartial".into(),
        "costNote".into(),
    ]);
    out.push('\n');
    for s in &rep.sessions {
        out.push_str(&csv_row(&[
            s.id.clone(),
            s.title.clone(),
            s.parent_id.clone(),
            s.provider.clone(),
            s.model.clone(),
            s.first_day.clone(),
            s.last_day.clone(),
            s.reports.to_string(),
            csv_tok(s.input, s.input_known),
            csv_tok(s.output, s.output_known),
            csv_tok(s.cache_read, s.cache_read_known),
            csv_tok(s.cache_creation, s.cache_creation_known),
            csv_cost(s.cost_known, s.cost_usd),
            s.cost_known.to_string(),
            s.cost_partial.to_string(),
            s.cost_note.clone(),
        ]));
        out.push('\n');
    }
    out
}

fn models_csv(rep: &UsageReport) -> String {
    let mut out = csv_row(&[
        "provider".into(),
        "model".into(),
        "firstDay".into(),
        "lastDay".into(),
        "sessions".into(),
        "reports".into(),
        "input".into(),
        "output".into(),
        "cacheRead".into(),
        "cacheCreation".into(),
        "costUsd".into(),
        "costKnown".into(),
        "costPartial".into(),
        "unpricedEvents".into(),
        "costNote".into(),
    ]);
    out.push('\n');
    for m in &rep.models {
        out.push_str(&csv_row(&[
            m.provider.clone(),
            m.model.clone(),
            m.first_day.clone(),
            m.last_day.clone(),
            m.sessions.to_string(),
            m.reports.to_string(),
            csv_tok(m.input, m.input_known),
            csv_tok(m.output, m.output_known),
            csv_tok(m.cache_read, m.cache_read_known),
            csv_tok(m.cache_creation, m.cache_creation_known),
            csv_cost(m.cost_known, m.cost_usd),
            m.cost_known.to_string(),
            m.cost_partial.to_string(),
            m.unpriced_events.to_string(),
            m.cost_note.clone(),
        ]));
        out.push('\n');
    }
    out
}

/// By-session order for the sort control (#1457). "server" keeps the
/// report's own order untouched; the other keys sort descending with a
/// token total as the tiebreak, then id so the order is stable to read.
fn sorted_sessions(rows: &[SessionRow], sort: &str) -> Vec<SessionRow> {
    let mut out = rows.to_vec();
    let tokens = |s: &SessionRow| s.input + s.output + s.cache_read + s.cache_creation;
    match sort {
        "cost" => out.sort_by(|a, b| {
            b.cost_usd
                .partial_cmp(&a.cost_usd)
                .unwrap_or(std::cmp::Ordering::Equal)
                .then_with(|| tokens(b).cmp(&tokens(a)))
                .then_with(|| a.id.cmp(&b.id))
        }),
        "tokens" => out.sort_by(|a, b| {
            tokens(b)
                .cmp(&tokens(a))
                .then_with(|| a.id.cmp(&b.id))
        }),
        "last" => out.sort_by(|a, b| {
            b.last_day
                .cmp(&a.last_day)
                .then_with(|| b.first_day.cmp(&a.first_day))
                .then_with(|| a.id.cmp(&b.id))
        }),
        _ => {}
    }
    out
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
    let mime = if filename.ends_with(".csv") {
        "text/csv"
    } else {
        "application/json"
    };
    let href = format!(
        "data:{mime};charset=utf-8,{}",
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
    open_session: impl Fn(String) + Copy + Send + Sync + 'static,
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
                <UsagePage base=base token=token status=status open_session=open_session />
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
    open_session: impl Fn(String) + Copy + Send + Sync + 'static,
) -> impl IntoView {
    let report = RwSignal::new(UsageReport::default());
    // The fetched payload verbatim, for the JSON export (#1481): kept as a
    // Value so the export is the report exactly as fetched, including
    // fields this module does not model.
    let fetched = RwSignal::new(Value::Null);
    let error = RwSignal::new(String::new());
    let budget = RwSignal::new(String::new());
    let loaded = RwSignal::new(false);
    let refresh = RwSignal::new(0u32);
    let session_sort = RwSignal::new("server".to_string());

    Effect::new(move |_| {
        refresh.get();
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match get_json::<Value>(&b, &t, "/v1/usage").await {
                Ok(body) => match serde_json::from_value::<UsageReport>(body.clone()) {
                    Ok(rep) => {
                        if rep.budget_set {
                            budget.set(format!("{}", rep.daily_usd));
                        }
                        fetched.set(body);
                        report.set(rep);
                        error.set(String::new());
                        loaded.set(true);
                    }
                    Err(e) => {
                        error.set(e.to_string());
                        loaded.set(true);
                    }
                },
                Err(e) => {
                    error.set(e);
                    loaded.set(true);
                }
            }
        });
    });

    // Export closures (#1481): client-side from the already-fetched
    // report, mirroring the Audit page's Export idiom below.
    let export_json = move |_| {
        let body = fetched.get_untracked();
        if body.is_null() {
            status.set("usage export failed: no report loaded".into());
            return;
        }
        let text = serde_json::to_string_pretty(&body).unwrap_or_default();
        let name = usage_filename(&report.get_untracked(), "", "json");
        download_text(&name, &text);
        status.set("usage export downloaded".into());
    };
    let export_days = move |_| {
        let rep = report.get_untracked();
        let name = usage_filename(&rep, "days", "csv");
        download_text(&name, &days_csv(&rep));
        status.set("usage days CSV downloaded".into());
    };
    let export_sessions = move |_| {
        let rep = report.get_untracked();
        let name = usage_filename(&rep, "sessions", "csv");
        download_text(&name, &sessions_csv(&rep));
        status.set("usage sessions CSV downloaded".into());
    };
    let export_models = move |_| {
        let rep = report.get_untracked();
        let name = usage_filename(&rep, "models", "csv");
        download_text(&name, &models_csv(&rep));
        status.set("usage models CSV downloaded".into());
    };

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
                <div class="ua-form usage-export">
                    <span class="hint">"Export"</span>
                    <button type="button" on:click=export_json>"Export JSON"</button>
                    <button type="button" on:click=export_days>"Export CSV (by day)"</button>
                    <button type="button" on:click=export_sessions>"Export CSV (by session)"</button>
                    <button type="button" on:click=export_models>"Export CSV (by model)"</button>
                    <span class="hint">"Exports the report on this page, exactly as fetched — no server export."</span>
                </div>
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
                <Show when=move || !report.get().models.is_empty()>
                    <h2>"By model"</h2>
                    <div class="ua-table" role="table">
                        <div class="ua-row ua-head" role="row">
                            <span>"Model"</span><span>"Sessions"</span><span>"In"</span><span>"Out"</span><span>"Cache read"</span><span>"Cache write"</span><span>"Cost"</span>
                        </div>
                        {move || report.get().models.into_iter().map(|m| {
                            let label = if m.model.is_empty() && m.provider.is_empty() {
                                "unknown model".to_string()
                            } else if m.provider.is_empty() {
                                m.model.clone()
                            } else if m.model.is_empty() {
                                m.provider.clone()
                            } else {
                                format!("{} / {}", m.provider, m.model)
                            };
                            let cost = money(m.cost_known, m.cost_partial || m.unpriced_events > 0, m.cost_usd, &m.cost_note);
                            let meta = format!("{}–{} · {} reports", m.first_day, m.last_day, m.reports);
                            view! {
                                <div class="ua-row" role="row">
                                    <span><strong>{label}</strong><span class="hint">{" "}{meta}</span></span>
                                    <span>{m.sessions}</span>
                                    <span>{tok(m.input, m.input_known)}</span>
                                    <span>{tok(m.output, m.output_known)}</span>
                                    <span>{tok(m.cache_read, m.cache_read_known)}</span>
                                    <span>{tok(m.cache_creation, m.cache_creation_known)}</span>
                                    <span>{cost}</span>
                                </div>
                            }
                        }).collect_view()}
                    </div>
                </Show>
                <Show when=move || !report.get().sessions.is_empty()>
                    <h2>"By session"</h2>
                    <div class="ua-form usage-session-sort">
                        <label for="usage-session-sort">"Sort sessions"</label>
                        <select id="usage-session-sort" prop:value=move || session_sort.get() on:change=move |ev| session_sort.set(event_target_value(&ev))>
                            <option value="server">"Server order"</option>
                            <option value="cost">"Cost (highest first)"</option>
                            <option value="tokens">"Tokens (highest first)"</option>
                            <option value="last">"Last active (newest first)"</option>
                        </select>
                    </div>
                    <div class="ua-table" role="table">
                        <div class="ua-row ua-head" role="row">
                            <span>"Session"</span><span>"Role"</span><span>"In"</span><span>"Out"</span><span>"Cache read"</span><span>"Cache write"</span><span>"Cost"</span>
                        </div>
                        {move || sorted_sessions(&report.get().sessions, &session_sort.get()).into_iter().map(|s| {
                            let role = if s.parent_id.is_empty() { "root".to_string() } else { format!("child of {}", short_id(&s.parent_id)) };
                            let label = if s.title.is_empty() { short_id(&s.id) } else { s.title.clone() };
                            let model = if s.model.is_empty() { s.source.clone() } else { format!("{} / {}", s.provider, s.model) };
                            let cost = money(s.cost_known, s.cost_partial, s.cost_usd, &s.cost_note);
                            // #1457: an explicit, focusable Open control in
                            // the Session cell (Q1) — not a whole-row click.
                            // It goes through the shell's open_session path,
                            // which resumes a non-live session and reports a
                            // deleted one on the status line instead of
                            // silently doing nothing.
                            let open_id = s.id.clone();
                            let open_title = s.id.clone();
                            let open_aria = format!("Open session {}", s.id);
                            view! {
                                <div class="ua-row" role="row">
                                    <span><strong>{label}</strong>{" "}<button type="button" class="linkish session-open" title=open_title aria-label=open_aria on:click=move |_| open_session(open_id.clone())>"Open"</button><span class="hint">{" "}{model}{" "}{s.first_day.clone()}{"–"}{s.last_day.clone()}{" · "}{s.reports}{" reports"}</span></span>
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

    Effect::new(move |_| {
        refresh.get();
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            match get_json::<AuditList>(&b, &t, "/v1/audit").await {
                Ok(body) => {
                    list.set(body);
                    error.set(String::new());
                    loaded.set(true);
                }
                Err(e) => {
                    error.set(e);
                    loaded.set(true);
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

#[cfg(test)]
mod tests {
    use super::*;

    fn report_fixture() -> UsageReport {
        let mut rep = UsageReport::default();
        rep.days = vec![DayRow {
            day: "2026-10-26".into(),
            sessions: 2,
            reports: 3,
            input: 100,
            input_known: true,
            output: 50,
            output_known: true,
            cost_usd: 1.5,
            cost_known: true,
            cost_note: "note, with comma".into(),
            ..Default::default()
        }];
        rep.sessions = vec![
            SessionRow {
                id: "cheap".into(),
                title: "Cheap".into(),
                first_day: "2026-10-25".into(),
                last_day: "2026-10-25".into(),
                reports: 1,
                input: 10,
                input_known: true,
                cost_usd: 0.25,
                cost_known: true,
                ..Default::default()
            },
            SessionRow {
                id: "pricey".into(),
                title: "Pricey".into(),
                first_day: "2026-10-24".into(),
                last_day: "2026-10-26".into(),
                reports: 2,
                input: 999,
                input_known: true,
                cost_usd: 9.0,
                cost_known: true,
                ..Default::default()
            },
        ];
        rep.models = vec![ModelRow {
            provider: "anthropic".into(),
            model: "opus".into(),
            first_day: "2026-10-25".into(),
            last_day: "2026-10-26".into(),
            sessions: 1,
            reports: 2,
            input: 1009,
            input_known: true,
            cost_usd: 9.25,
            cost_known: true,
            ..Default::default()
        }];
        rep
    }

    #[test]
    fn filename_carries_the_report_range() {
        let rep = report_fixture();
        assert_eq!(usage_filename(&rep, "", "json"), "strike-usage-20261026-20261026.json");
        assert_eq!(usage_filename(&rep, "days", "csv"), "strike-usage-days-20261026-20261026.csv");
        assert_eq!(usage_filename(&UsageReport::default(), "", "json"), "strike-usage.json");
    }

    #[test]
    fn days_csv_matches_the_page_row_and_escapes_notes() {
        let csv = days_csv(&report_fixture());
        let mut lines = csv.lines();
        assert_eq!(
            lines.next().unwrap(),
            "day,sessions,reports,input,output,cacheRead,cacheCreation,costUsd,costKnown,costPartial,unpricedEvents,costNote"
        );
        let row = lines.next().unwrap();
        assert!(row.starts_with("2026-10-26,2,3,100,50,,,1.5,true,false,0,"), "row: {row}");
        assert!(row.ends_with("\"note, with comma\""), "row: {row}");
    }

    #[test]
    fn csv_leaves_unknown_tokens_and_costs_empty() {
        // An unreported token part is not a measured zero, and an
        // unpriced row has no dollars to export (#1481 honesty rule).
        let mut rep = UsageReport::default();
        rep.days = vec![DayRow {
            day: "2026-10-26".into(),
            ..Default::default()
        }];
        let csv = days_csv(&rep);
        let row = csv.lines().nth(1).unwrap();
        assert_eq!(row, "2026-10-26,0,0,,,,,,false,false,0,");
    }

    #[test]
    fn sessions_and_models_csv_carry_their_rows() {
        let rep = report_fixture();
        let sessions = sessions_csv(&rep);
        assert!(sessions.contains("pricey,Pricey"), "sessions: {sessions}");
        let models = models_csv(&rep);
        assert!(models.contains("anthropic,opus,2026-10-25,2026-10-26,1,2,1009"), "models: {models}");
    }

    #[test]
    fn session_sort_keeps_server_order_by_default() {
        let rep = report_fixture();
        let ids: Vec<String> = sorted_sessions(&rep.sessions, "server")
            .iter()
            .map(|s| s.id.clone())
            .collect();
        assert_eq!(ids, vec!["cheap".to_string(), "pricey".to_string()]);
        let by_cost: Vec<String> = sorted_sessions(&rep.sessions, "cost")
            .iter()
            .map(|s| s.id.clone())
            .collect();
        assert_eq!(by_cost, vec!["pricey".to_string(), "cheap".to_string()]);
        let by_tokens: Vec<String> = sorted_sessions(&rep.sessions, "tokens")
            .iter()
            .map(|s| s.id.clone())
            .collect();
        assert_eq!(by_tokens, vec!["pricey".to_string(), "cheap".to_string()]);
        let by_last: Vec<String> = sorted_sessions(&rep.sessions, "last")
            .iter()
            .map(|s| s.id.clone())
            .collect();
        assert_eq!(by_last, vec!["pricey".to_string(), "cheap".to_string()]);
        // Sorting never mutates the report's own order.
        assert_eq!(rep.sessions[0].id, "cheap");
    }

    #[test]
    fn model_row_deserialises_the_daemon_payload() {
        let body = r#"{"provider":"openai","model":"priced","firstDay":"2026-10-25","lastDay":"2026-10-26","sessions":2,"input":10,"output":5,"cacheRead":1,"cacheCreation":0,"inputKnown":true,"outputKnown":true,"cacheReadKnown":true,"cacheCreationKnown":false,"reports":3,"costUsd":1.25,"costKnown":true,"costPartial":false,"unpricedEvents":0}"#;
        let row: ModelRow = serde_json::from_str(body).unwrap();
        assert_eq!(row.provider, "openai");
        assert_eq!(row.sessions, 2);
        assert!(row.cost_known && row.cost_usd == 1.25);
    }
}
