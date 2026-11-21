//! Dashboards (#1628): agent-authored project pages.
//!
//! Index: `GET /v1/dashboards?root=` (dashboard artifacts the active root
//! may read). Page: `GET /v1/dashboards/{id}?root=` — the daemon fills
//! measured blocks from its records on every open, so reopening a page
//! re-measures; agent-written blocks change only when the agent saves the
//! page. Start: `POST /v1/dashboards` with a template. Asking in chat
//! ("make me a standup page") reaches the same artifact through the
//! agent's `artifact_write`. Rendering rules (dash-not-zero, the "agent
//! wrote this" marker) live in the `webui/dashboard-view` crate.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::json;

use crate::api::{get_json, js_encode, post_json_value};
use dashboard_view::{AGENT_MARK, TEMPLATES};

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
struct DashMeta {
    #[serde(default)]
    id: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    version: i64,
    #[serde(default, rename = "updatedAt")]
    updated_at: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
struct DashList {
    #[serde(default)]
    dashboards: Vec<DashMeta>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
struct Column {
    #[serde(default)]
    title: String,
    #[serde(default)]
    cards: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
struct Block {
    #[serde(default)]
    kind: String,
    #[serde(default)]
    provenance: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    metric: String,
    #[serde(default)]
    value: Option<f64>,
    #[serde(default)]
    measured: bool,
    #[serde(default)]
    unit: String,
    #[serde(default)]
    note: String,
    #[serde(default)]
    body: String,
    #[serde(default)]
    items: Vec<String>,
    #[serde(default)]
    columns: Vec<Column>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
struct Page {
    #[serde(default)]
    id: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    version: i64,
    #[serde(default)]
    template: String,
    #[serde(default)]
    blocks: Vec<Block>,
    #[serde(default, rename = "renderedAt")]
    rendered_at: String,
}

fn columns_view(cols: Vec<Column>) -> impl IntoView {
    view! {
        <div class="dash-kanban">
            {cols.into_iter().map(|c| {
                let count = c.cards.len();
                view! {
                    <div class="dash-col">
                        <h4>{c.title}" "<span class="hint">{count}</span></h4>
                        <ul class="plain">
                            {c.cards.into_iter().map(|card| view! { <li class="dash-card">{card}</li> }).collect_view()}
                        </ul>
                    </div>
                }
            }).collect_view()}
        </div>
    }
}

fn block_view(b: Block) -> AnyView {
    let agent = dashboard_view::is_agent(&b.provenance);
    let class = if agent { "dash-block card dash-agent" } else { "dash-block card dash-measured" };
    let provenance = if agent { "agent" } else { "measured" };
    let title = b.title.clone();
    let kind = b.kind.clone();
    let body: AnyView = match b.kind.as_str() {
        "metric" => {
            let text = dashboard_view::metric_text(b.value, b.measured, &b.unit);
            let measured = if b.measured { "true" } else { "false" };
            let note = b.note.clone();
            let metric = b.metric.clone();
            view! {
                <p class="dash-value" data-measured=measured title=metric>{text}</p>
                {(!note.is_empty()).then(|| view! { <p class="hint">{note.clone()}</p> })}
            }
            .into_any()
        }
        "sessions_kanban" | "kanban" => {
            if b.columns.is_empty() {
                let msg = if agent { dashboard_view::empty_agent_text().to_string() } else { b.note.clone() };
                view! { <p class="hint">{msg}</p> }.into_any()
            } else {
                columns_view(b.columns.clone()).into_any()
            }
        }
        "list" => {
            if b.items.is_empty() {
                view! { <p class="hint">{dashboard_view::empty_agent_text()}</p> }.into_any()
            } else {
                view! {
                    <ul class="dash-list">
                        {b.items.clone().into_iter().map(|it| view! { <li>{it}</li> }).collect_view()}
                    </ul>
                }
                .into_any()
            }
        }
        _ => {
            if b.body.trim().is_empty() {
                view! { <p class="hint">{dashboard_view::empty_agent_text()}</p> }.into_any()
            } else {
                view! { <p class="dash-body">{b.body.clone()}</p> }.into_any()
            }
        }
    };
    view! {
        <section class=class data-provenance=provenance data-kind=kind>
            <header class="dash-block-head">
                <h3>{title}</h3>
                {agent.then(|| view! { <span class="dash-mark" title="Written by the agent, not measured by the daemon">{AGENT_MARK}</span> })}
            </header>
            {body}
        </section>
    }
    .into_any()
}

/// The Dashboards page: index on the left, the open page on the right.
#[component]
pub fn DashboardsPane(
    base: RwSignal<String>,
    token: RwSignal<String>,
    active_root: RwSignal<String>,
) -> impl IntoView {
    let list = RwSignal::new(Vec::<DashMeta>::new());
    let page = RwSignal::new(None::<Page>);
    let selected = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let loading = RwSignal::new(false);
    let generation = RwSignal::new(0u32);

    let load_list = move || {
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        if root.is_empty() {
            list.set(Vec::new());
            return;
        }
        spawn_local(async move {
            let path = format!("/v1/dashboards?root={}", js_encode(&root));
            match get_json::<DashList>(&b, &t, &path).await {
                Ok(body) => {
                    list.set(body.dashboards);
                    error.set(String::new());
                }
                Err(e) => error.set(format!("Could not load dashboards: {e}")),
            }
        });
    };

    // Opening a page always re-fetches, so measured blocks are re-measured.
    let open = move |id: String| {
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        let gen = generation.get_untracked().wrapping_add(1);
        generation.set(gen);
        selected.set(id.clone());
        page.set(None);
        loading.set(true);
        spawn_local(async move {
            let path = format!("/v1/dashboards/{}?root={}", js_encode(&id), js_encode(&root));
            let res = get_json::<Page>(&b, &t, &path).await;
            if generation.get_untracked() != gen {
                return;
            }
            loading.set(false);
            match res {
                Ok(p) => {
                    page.set(Some(p));
                    error.set(String::new());
                }
                Err(e) => error.set(format!("Could not open the dashboard: {e}")),
            }
        });
    };

    let start = move |template: &'static str| {
        let (b, t, root) = (base.get_untracked(), token.get_untracked(), active_root.get_untracked());
        if root.is_empty() {
            error.set("Open a session first: a dashboard belongs to the session that starts it.".into());
            return;
        }
        spawn_local(async move {
            let path = format!("/v1/dashboards?root={}", js_encode(&root));
            match post_json_value(&b, &t, &path, json!({ "template": template })).await {
                Ok(v) => {
                    let id = v.get("id").and_then(|x| x.as_str()).unwrap_or("").to_string();
                    load_list();
                    if !id.is_empty() {
                        open(id);
                    }
                }
                Err(e) => error.set(format!("Could not start the dashboard: {e}")),
            }
        });
    };

    Effect::new(move |_| {
        let _ = (base.get(), token.get(), active_root.get());
        page.set(None);
        selected.set(String::new());
        load_list();
    });

    view! {
        <div class="dash-shell" id="dashboards-root">
            <aside class="dash-index">
                <div class="section-head">
                    <h2 class="panel-title">"Dashboards"</h2>
                    <button type="button" class="secondary" on:click=move |_| load_list()>"Refresh"</button>
                </div>
                <p class="hint">"Ask the agent in chat for a page (a standup, a roadmap, a kanban of the work), or start one here."</p>
                <div class="row-actions dash-templates">
                    {TEMPLATES.into_iter().map(|(id, label)| view! {
                        <button type="button" class="secondary dash-template" data-template=id on:click=move |_| start(id)>{format!("New {label}")}</button>
                    }).collect_view()}
                </div>
                <Show when=move || !error.get().is_empty()>
                    <div class="notice" role="alert">{move || error.get()}</div>
                </Show>
                {move || {
                    let rows = list.get();
                    if rows.is_empty() {
                        return view! { <p class="hint" data-dash-empty="true">"No dashboards yet."</p> }.into_any();
                    }
                    view! {
                        <ul class="plain dash-rows">
                            {rows.into_iter().map(|m| {
                                let id = m.id.clone();
                                let id_click = m.id.clone();
                                let on = move || selected.get() == id;
                                let label = if m.title.is_empty() { m.id.clone() } else { m.title.clone() };
                                let meta = format!("v{} · {}", m.version, m.updated_at.split('T').next().unwrap_or(""));
                                view! {
                                    <li class=move || if on() { "row card on" } else { "row card" } data-dashboard=m.id.clone()>
                                        <button type="button" class="link" on:click=move |_| open(id_click.clone())>
                                            <span class="name">{label}</span>
                                            <span class="path">{meta}</span>
                                        </button>
                                    </li>
                                }
                            }).collect_view()}
                        </ul>
                    }.into_any()
                }}
            </aside>
            <main class="dash-main">
                {move || {
                    if loading.get() {
                        return view! { <p class="hint">"Loading…"</p> }.into_any();
                    }
                    let Some(p) = page.get() else {
                        return view! { <p class="hint">"Pick a dashboard, or start one from a template."</p> }.into_any();
                    };
                    let id = p.id.clone();
                    let rendered = format!("Measured {} · v{}", p.rendered_at.replace('T', " ").split('.').next().unwrap_or(""), p.version);
                    let suffix = if p.template.is_empty() { String::new() } else { format!(" · {}", p.template) };
                    view! {
                        <div class="dash-page" data-dashboard-page=p.id.clone()>
                            <div class="section-head">
                                <h2 class="panel-title">{p.title.clone()}</h2>
                                <button type="button" class="secondary dash-reopen" on:click=move |_| open(id.clone())>"Re-measure"</button>
                            </div>
                            <p class="hint">{rendered}{suffix}</p>
                            <div class="dash-blocks">
                                {p.blocks.into_iter().map(block_view).collect_view()}
                            </div>
                        </div>
                    }.into_any()
                }}
            </main>
        </div>
    }
}
