//! Config sources (#1493): the read-only provenance list from
//! `GET /v1/config-sources`, shown in Settings.
//!
//! Answers "which config files is Strike reading?" in the daemon's order
//! (its precedence story, so the list is never re-sorted here). Read-only:
//! no editing, no create, and only the daemon's `display` path is shown;
//! the daemon withholds absolute host paths and the cockpit never tries to
//! reconstruct them. Editing a config draft belongs to Customize.

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::api::get_json;
use crate::model::{ConfigSource, ConfigSourcesReply};

/// The daemon's wording when `host.Services.ConfigFiles` is not wired
/// (`capabilityUnavailable(w, "configFiles")` in the server).
pub const CONFIG_FILES_UNAVAILABLE: &str = "configFiles capability unavailable on this host";

fn sources_path(root: &str) -> String {
    if root.is_empty() {
        "/v1/config-sources".to_string()
    } else {
        format!("/v1/config-sources?root={}", crate::api::js_encode(root))
    }
}

#[component]
pub fn ConfigSources(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    capable: RwSignal<bool>,
) -> impl IntoView {
    let rows = RwSignal::new(Vec::<ConfigSource>::new());
    let error = RwSignal::new(String::new());
    let loading = RwSignal::new(false);
    let loaded = RwSignal::new(false);
    let generation = RwSignal::new(0u32);

    let refresh = move || {
        if !capable.get_untracked() {
            return;
        }
        let (b, t, r) = (
            base.get_untracked(),
            token.get_untracked(),
            root.get_untracked(),
        );
        let ticket = generation.get_untracked().wrapping_add(1);
        generation.set(ticket);
        loading.set(true);
        error.set(String::new());
        spawn_local(async move {
            let result = get_json::<ConfigSourcesReply>(&b, &t, &sources_path(&r)).await;
            if generation.get_untracked() != ticket {
                return;
            }
            match result {
                Ok(reply) => {
                    rows.set(reply.sources);
                    loaded.set(true);
                }
                Err(e) => {
                    rows.set(Vec::new());
                    loaded.set(false);
                    error.set(format!("Could not read the config sources: {e}"));
                }
            }
            loading.set(false);
        });
    };
    Effect::new(move |_| {
        // Refetch when the capability turns on or the session root moves.
        let (on, _) = (capable.get(), root.get());
        if on {
            refresh();
        }
    });

    view! {
        <div class="config-sources" data-config-sources="">
            <p class="hint">"Which config files this daemon reads, in its precedence order. Read-only: edit configuration in Customize."</p>
            <Show
                when=move || capable.get()
                fallback=move || view! {
                    <p class="hint" data-config-sources-unavailable="">{CONFIG_FILES_UNAVAILABLE}</p>
                }
            >
                <Show when=move || loading.get() && !loaded.get()>
                    <p class="hint">"Reading the config sources…"</p>
                </Show>
                <Show when=move || !error.get().is_empty()>
                    <p class="banner" role="alert">{move || error.get()}</p>
                </Show>
                <Show when=move || loaded.get() && rows.with(Vec::is_empty)>
                    <p class="hint">"The daemon reported no config sources."</p>
                </Show>
                <ol class="config-source-list">
                    <For
                        each=move || rows.get().into_iter().enumerate().collect::<Vec<_>>()
                        key=|(i, row)| (*i, row.clone())
                        children=move |(_, row)| {
                            let present = row.exists;
                            view! {
                                <li
                                    class="config-source"
                                    data-config-source=row.display.clone()
                                    data-exists=if present { "true" } else { "false" }
                                >
                                    <span class="name">{row.title().to_string()}</span>
                                    <code class="path">{row.display.clone()}</code>
                                    <span class="meta">
                                        {format!("{} · {} · {}", row.scope_label(), row.kind_label(), row.state_label())}
                                    </span>
                                </li>
                            }
                        }
                    />
                </ol>
                <button type="button" class="secondary" data-action="reload-config-sources" on:click=move |_| refresh()>
                    "Refresh"
                </button>
            </Show>
        </div>
    }
}
