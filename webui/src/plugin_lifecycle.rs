//! Plugin lifecycle for installed plugins (#1570), shown in Customize's
//! Plugins section beside its enable/disable list.
//!
//! Verbs: trust (after its preview renders), untrust, remove and update
//! (each a two-step action that sends the daemon's `confirm:true`), and an
//! outdated badge from `GET /v1/plugins/outdated` — no auto-update. Catalog
//! search/install and panes are out of this slice. Every state shown comes
//! from the daemon; after any action the list is fetched again.

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::api::{get_json, post_json_value};
use crate::plugin_flow::{
    confirm, flow_id, mcp_lines, outdated, step, Event, Flow, Plugin, PluginList, TrustPreview,
    UpdateReview,
};

/// The daemon's wording when `Services.Plugins` is not wired.
pub const PLUGINS_UNAVAILABLE: &str = "plugins capability unavailable on this host";

fn scope_query(scope: &str) -> String {
    if scope.is_empty() {
        String::new()
    } else {
        format!("?scope={}", crate::api::js_encode(scope))
    }
}

#[component]
pub fn PluginLifecycle(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    capable: RwSignal<bool>,
) -> impl IntoView {
    let plugins = RwSignal::new(Vec::<Plugin>::new());
    let stale = RwSignal::new(Vec::<(String, String)>::new());
    let list_error = RwSignal::new(String::new());
    let outdated_note = RwSignal::new(String::new());
    let loaded = RwSignal::new(false);
    let flow = RwSignal::new(Flow::Idle);
    let flow_error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);

    let refresh = move || {
        if !capable.get_untracked() {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let list = get_json::<PluginList>(&b, &t, "/v1/plugins").await;
            let rows = match list {
                Ok(l) => {
                    list_error.set(String::new());
                    l.plugins
                }
                Err(e) => {
                    list_error.set(format!("Could not list plugins: {e}"));
                    Vec::new()
                }
            };
            plugins.set(rows.clone());
            loaded.set(true);
            match get_json::<PluginList>(&b, &t, "/v1/plugins/outdated").await {
                Ok(o) => {
                    outdated_note.set(String::new());
                    stale.set(outdated(&rows, &o.plugins));
                }
                Err(e) => {
                    outdated_note.set(format!("Could not check for updates: {e}"));
                    stale.set(outdated(&rows, &[]));
                }
            }
        });
    };
    Effect::new(move |_| {
        if capable.get() {
            refresh();
        }
    });

    let cancel = move || {
        flow.update(|f| *f = step(std::mem::take(f), Event::Reset));
        flow_error.set(String::new());
    };
    let ask_trust = move |id: String, scope: String| {
        flow_error.set(String::new());
        flow.update(|f| *f = step(std::mem::take(f), Event::AskTrust { id: id.clone(), scope: scope.clone() }));
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let path = format!("/v1/plugins/{}/trust-preview{}", crate::api::js_encode(&id), scope_query(&scope));
        spawn_local(async move {
            match get_json::<TrustPreview>(&b, &t, &path).await {
                Ok(preview) => flow.update(|f| *f = step(std::mem::take(f), Event::PreviewLoaded(preview))),
                Err(e) => {
                    if flow_id(&flow.get_untracked()) == Some(id.as_str()) {
                        flow.set(Flow::Idle);
                        flow_error.set(format!("Could not load the trust preview for {id}: {e}"));
                    }
                }
            }
        });
    };
    let ask_update = move |id: String, scope: String| {
        flow_error.set(String::new());
        flow.update(|f| *f = step(std::mem::take(f), Event::AskUpdate { id: id.clone(), scope: scope.clone() }));
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let body = serde_json::json!({"id": id, "scope": scope});
        spawn_local(async move {
            let res = post_json_value(&b, &t, "/v1/plugins/preview-update", body).await;
            let review = res.and_then(|v| serde_json::from_value::<UpdateReview>(v).map_err(|e| e.to_string()));
            match review {
                Ok(review) => flow.update(|f| *f = step(std::mem::take(f), Event::ReviewLoaded(review))),
                Err(e) => {
                    if flow_id(&flow.get_untracked()) == Some(id.as_str()) {
                        flow.set(Flow::Idle);
                        flow_error.set(format!("Could not preview the update for {id}: {e}"));
                    }
                }
            }
        });
    };
    let run_confirm = move || {
        if busy.get_untracked() {
            return;
        }
        let Some(post) = confirm(&flow.get_untracked()) else {
            return;
        };
        busy.set(true);
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match post_json_value(&b, &t, post.path(), post.body()).await {
                Ok(_) => {
                    flow.set(Flow::Idle);
                    flow_error.set(String::new());
                    status.set(post.done());
                }
                Err(e) => flow_error.set(format!("The daemon refused: {e}")),
            }
            busy.set(false);
            // Trust and install state render from the daemon, never assumed.
            refresh();
        });
    };

    let row_view = move |p: Plugin| {
        let id = p.id.clone();
        let scope = p.scope.clone();
        let update = stale.with(|s| s.iter().find(|(sid, _)| sid == &id).map(|(_, v)| v.clone()));
        let mine = {
            let id = id.clone();
            move || flow.with(|f| flow_id(f) == Some(id.as_str()))
        };
        let other_busy = {
            let mine = mine.clone();
            move || busy.get() || (flow.with(|f| *f != Flow::Idle) && !mine())
        };
        let (id_t, sc_t) = (id.clone(), scope.clone());
        let (id_u, sc_u) = (id.clone(), scope.clone());
        let (id_r, sc_r) = (id.clone(), scope.clone());
        let (id_up, sc_up) = (id.clone(), scope.clone());
        let can_trust = p.can_trust();
        let can_untrust = p.can_untrust();
        let has_update = update.is_some();
        let update_label = match update.as_deref() {
            Some("") | None => "Update available".to_string(),
            Some(v) => format!("Update available: {v}"),
        };
        let meta = [
            (!p.version.is_empty()).then(|| format!("v{}", p.version)),
            (!p.scope.is_empty()).then(|| p.scope.clone()),
            Some(if p.enabled { "enabled".to_string() } else { "disabled".to_string() }),
            (!p.source_label.is_empty()).then(|| p.source_label.clone()),
        ]
        .into_iter()
        .flatten()
        .collect::<Vec<_>>()
        .join(" · ");
        let load_error = p.load_error.clone();
        let other_busy2 = other_busy.clone();
        let other_busy3 = other_busy.clone();
        let other_busy4 = other_busy.clone();
        view! {
            <li class="plugin-life-row" data-plugin-id=id.clone() data-trust-state=p.trust_state.clone()>
                <div class="plugin-life-head">
                    <span class="name">{p.title().to_string()}</span>
                    <span class="badge" data-plugin-trust="">{p.trust_label()}</span>
                    <Show when=move || has_update>
                        <span class="badge" data-plugin-outdated="">{update_label.clone()}</span>
                    </Show>
                </div>
                <p class="hint">{meta}</p>
                <Show when={let e = load_error.clone(); move || !e.is_empty()}>
                    <p class="cz-error">{load_error.clone()}</p>
                </Show>
                <div class="cz-actions">
                    <Show when=move || can_trust>
                        <button type="button" class="secondary" data-plugin-action="trust-preview"
                            disabled={let o = other_busy.clone(); move || o()}
                            on:click={let (i, s) = (id_t.clone(), sc_t.clone()); move |_| ask_trust(i.clone(), s.clone())}>
                            "Review trust…"
                        </button>
                    </Show>
                    <Show when=move || can_untrust>
                        <button type="button" class="secondary" data-plugin-action="untrust"
                            disabled={let o = other_busy2.clone(); move || o()}
                            on:click={let (i, s) = (id_u.clone(), sc_u.clone()); move |_| {
                                flow_error.set(String::new());
                                flow.update(|f| *f = step(std::mem::take(f), Event::ArmUntrust { id: i.clone(), scope: s.clone() }));
                            }}>
                            "Untrust…"
                        </button>
                    </Show>
                    <Show when=move || has_update>
                        <button type="button" class="secondary" data-plugin-action="update-preview"
                            disabled={let o = other_busy3.clone(); move || o()}
                            on:click={let (i, s) = (id_up.clone(), sc_up.clone()); move |_| ask_update(i.clone(), s.clone())}>
                            "Review update…"
                        </button>
                    </Show>
                    <button type="button" class="secondary" data-plugin-action="remove"
                        disabled={let o = other_busy4.clone(); move || o()}
                        on:click={let (i, s) = (id_r.clone(), sc_r.clone()); move |_| {
                            flow_error.set(String::new());
                            flow.update(|f| *f = step(std::mem::take(f), Event::ArmRemove { id: i.clone(), scope: s.clone() }));
                        }}>
                        "Remove…"
                    </button>
                </div>
                <Show when=mine.clone()>
                    <PluginFlowPanel flow=flow busy=busy on_confirm=run_confirm on_cancel=cancel />
                </Show>
            </li>
        }
    };

    view! {
        <section class="plugin-life" data-plugin-lifecycle=""
            on:keydown=move |ev: web_sys::KeyboardEvent| {
                if ev.key() == "Escape" && flow.with_untracked(|f| *f != Flow::Idle) {
                    ev.prevent_default();
                    ev.stop_propagation();
                    cancel();
                }
            }>
            <h3>"Trust, updates, and removal"</h3>
            <Show
                when=move || capable.get()
                fallback=move || view! { <p class="hint" data-plugins-unavailable="">{PLUGINS_UNAVAILABLE}</p> }
            >
                <p class="hint">"Executable parts (MCP servers, harnesses, shell hooks, process panes) run only after you trust a plugin. Review what it may run first. Install and catalog search stay on the CLI and TUI for now."</p>
                <Show when=move || !list_error.get().is_empty()>
                    <p class="cz-error" role="alert">{move || list_error.get()}</p>
                </Show>
                <Show when=move || !outdated_note.get().is_empty()>
                    <p class="hint" data-plugins-outdated-note="">{move || outdated_note.get()}</p>
                </Show>
                <Show when=move || !flow_error.get().is_empty()>
                    <p class="cz-error" role="alert" data-plugin-flow-error="">{move || flow_error.get()}</p>
                </Show>
                <Show when=move || loaded.get() && plugins.with(Vec::is_empty) && list_error.get().is_empty()>
                    <p class="hint">"No plugins installed."</p>
                </Show>
                <ul class="plugin-life-list">
                    {move || plugins.get().into_iter().map(row_view).collect_view()}
                </ul>
                <button type="button" class="secondary" data-plugin-action="refresh" on:click=move |_| refresh()>"Refresh"</button>
            </Show>
        </section>
    }
}

#[component]
fn PluginFlowPanel(
    flow: RwSignal<Flow>,
    busy: RwSignal<bool>,
    on_confirm: impl Fn() + Copy + Send + Sync + 'static,
    on_cancel: impl Fn() + Copy + Send + Sync + 'static,
) -> impl IntoView {
    let body = move || match flow.get() {
        Flow::TrustLoading { id, .. } => view! {
            <p class="hint">{format!("Loading what {id} may run…")}</p>
        }
        .into_any(),
        Flow::TrustReview { id, preview, .. } => {
            let mut lines: Vec<String> = Vec::new();
            if !preview.capabilities.is_empty() {
                lines.push(format!("Capabilities: {}", preview.capabilities.join(", ")));
            }
            lines.extend(mcp_lines(&preview.mcp));
            for h in &preview.harnesses {
                let mut line = format!("Harness {}", h.name);
                if !h.command.is_empty() {
                    line.push_str(&format!(": runs {} {}", h.command, h.args.join(" ")));
                }
                lines.push(line.trim_end().to_string());
            }
            if preview.hooks > 0 {
                lines.push(format!("Shell hooks: {}", preview.hooks));
            }
            lines.extend(preview.review_lines.iter().cloned());
            if !preview.digest.is_empty() {
                lines.push(format!("Content digest: {}", preview.digest));
            }
            view! {
                <p>{format!("Trust {id}? It may run:")}</p>
                <ul class="plugin-life-review" data-plugin-trust-preview="">
                    {lines.into_iter().map(|l| view! { <li>{l}</li> }).collect_view()}
                </ul>
                <p class="hint">"Environment and header values are never shown here, only their keys."</p>
            }
            .into_any()
        }
        Flow::UntrustArmed { id, .. } => view! {
            <p class="cz-error" role="alert">{format!("Revoke trust for {id}? Its executable parts stop on next launch. Passive contributions are unaffected.")}</p>
        }
        .into_any(),
        Flow::RemoveArmed { id, .. } => view! {
            <p class="cz-error" role="alert" data-plugin-remove-confirm="">{format!("Remove {id}? Its installed files are deleted.")}</p>
        }
        .into_any(),
        Flow::UpdateLoading { id, .. } => view! {
            <p class="hint">{format!("Reviewing the update for {id}…")}</p>
        }
        .into_any(),
        Flow::UpdateReview { id, review, .. } => {
            let mut lines: Vec<String> = Vec::new();
            if !review.summary.is_empty() {
                lines.push(review.summary.clone());
            }
            if !review.old_version.is_empty() || !review.new_version.is_empty() {
                lines.push(format!("Version {} → {}", review.old_version, review.new_version));
            }
            if !review.capability_added.is_empty() {
                lines.push(format!("Adds capabilities: {}", review.capability_added.join(", ")));
            }
            if !review.capability_removed.is_empty() {
                lines.push(format!("Drops capabilities: {}", review.capability_removed.join(", ")));
            }
            if !review.contrib_added.is_empty() {
                lines.push(format!("Adds: {}", review.contrib_added.join(", ")));
            }
            if !review.contrib_removed.is_empty() {
                lines.push(format!("Removes: {}", review.contrib_removed.join(", ")));
            }
            if review.executable_changed {
                lines.push("Executable parts change.".into());
                lines.extend(review.executable_diffs.iter().cloned());
            }
            if review.trust_invalidated && review.had_trust {
                lines.push("Your trust grant ends: review trust again after updating.".into());
            }
            view! {
                <p>{format!("Update {id}?")}</p>
                <ul class="plugin-life-review" data-plugin-update-review="">
                    {lines.into_iter().map(|l| view! { <li>{l}</li> }).collect_view()}
                </ul>
            }
            .into_any()
        }
        Flow::Idle => ().into_any(),
    };
    let label = move || match flow.get() {
        Flow::TrustReview { .. } => Some("Trust"),
        Flow::UntrustArmed { .. } => Some("Confirm untrust"),
        Flow::RemoveArmed { .. } => Some("Confirm remove"),
        Flow::UpdateReview { .. } => Some("Confirm update"),
        _ => None,
    };
    view! {
        <div class="plugin-life-flow" role="group" aria-label="Plugin action">
            {body}
            <div class="cz-actions">
                <Show when=move || label().is_some()>
                    <button type="button" class="primary" data-plugin-action="confirm"
                        disabled=move || busy.get()
                        on:click=move |_| on_confirm()>
                        {move || label().unwrap_or("")}
                    </button>
                </Show>
                <button type="button" class="secondary" data-plugin-action="cancel" on:click=move |_| on_cancel()>"Cancel"</button>
            </div>
        </div>
    }
}
