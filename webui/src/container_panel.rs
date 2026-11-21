//! Container posture (#1584): the Settings → Isolation panel over the
//! daemon's read-only `GET /v1/container/status`.
//!
//! It answers "is this agent on the host or in a container, and is that
//! container healthy?" with the same facts as the TUI `/container` and
//! `strike container status|drift|detect`. Host mode is a first-class
//! posture (#1290): no runtime is stated plainly, never as an error. A
//! route failure is shown as an error, never as an empty panel. No actions.

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::api::{api, auth_header};
use crate::model::{container_status_outcome, ContainerStatusReply};

/// Hint shown while bootstrap reports `container: false`.
pub const CONTAINER_UNAVAILABLE: &str =
    "Container posture needs a daemon with a workspace. Use /container in the TUI or strike container status.";

fn status_path(root: &str) -> String {
    if root.is_empty() {
        "/v1/container/status".to_string()
    } else {
        format!("/v1/container/status?root={}", crate::api::js_encode(root))
    }
}

async fn fetch_status(base: &str, token: &str, root: &str) -> Result<ContainerStatusReply, String> {
    let mut builder = gloo_net::http::Request::get(&api(base, &status_path(root)))
        .credentials(web_sys::RequestCredentials::Include);
    let token = token.trim();
    if !token.is_empty() {
        builder = builder.header("Authorization", &auth_header(token));
    }
    let resp = builder.send().await.map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    container_status_outcome(status, &text)
}

#[component]
pub fn ContainerPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    capable: RwSignal<bool>,
) -> impl IntoView {
    let reply = RwSignal::new(None::<ContainerStatusReply>);
    let error = RwSignal::new(String::new());
    let loading = RwSignal::new(false);
    // Bumped by Refresh; the effect re-reads on it, the root, or the flag.
    let nonce = RwSignal::new(0u32);

    Effect::new(move |_| {
        let _ = nonce.get();
        let ok = capable.get();
        let r = root.get();
        error.set(String::new());
        if !ok {
            reply.set(None);
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        loading.set(true);
        spawn_local(async move {
            match fetch_status(&b, &t, &r).await {
                Ok(v) => reply.set(Some(v)),
                Err(e) => {
                    reply.set(None);
                    error.set(format!("Could not read container status: {e}"));
                }
            }
            loading.set(false);
        });
    });

    view! {
        <div class="container-panel" data-panel="container">
            <Show when=move || !capable.get()>
                <p class="hint">{CONTAINER_UNAVAILABLE}</p>
            </Show>
            <Show when=move || capable.get()>
                <Show when=move || loading.get() && reply.get().is_none() && error.get().is_empty()>
                    <p class="hint" role="status">"Reading isolation posture…"</p>
                </Show>
                <Show when=move || !error.get().is_empty()>
                    <p class="banner" role="alert">{move || error.get()}</p>
                </Show>
                {move || reply.get().map(|v| {
                    let ladder = v.ladder.clone();
                    let posture_note = v.posture_note();
                    let runtime_line = v.runtime_line();
                    let container_line = v.container_line();
                    let drift_line = v.drift_line();
                    let session_model = v.container.session_model.clone();
                    let markers = v.detect.markers.join(", ");
                    let scope_note = if v.config_scope == "project" {
                        "Container config: global + this workspace."
                    } else {
                        "Container config: global only (workspace untrusted or no repo config)."
                    };
                    let errors = v.errors.clone();
                    view! {
                        <p class="container-posture">
                            <span class="container-posture-label" data-posture=v.posture.clone()>{v.posture.clone()}</span>
                            " "
                            <span>{v.describe.clone()}</span>
                        </p>
                        <p class="hint">{posture_note}</p>
                        <ol class="container-ladder" aria-label="Isolation ladder, least to most host isolation">
                            {ladder.into_iter().map(|rung| {
                                let class = if rung.active { "rung on" } else { "rung" };
                                let current = if rung.active { "step" } else { "false" };
                                view! {
                                    <li class=class aria-current=current title=rung.describe.clone()>
                                        <span class="rung-label">{rung.label.clone()}</span>
                                    </li>
                                }
                            }).collect_view()}
                        </ol>
                        <dl class="container-facts">
                            <dt>"Runtime"</dt>
                            <dd data-fact="runtime">{runtime_line}</dd>
                            <dt>"Workspace container"</dt>
                            <dd data-fact="container">{container_line}</dd>
                            <dt>"Dockerfile drift"</dt>
                            <dd data-fact="drift">{drift_line}</dd>
                            <dt>"Detected manifests"</dt>
                            <dd data-fact="detect">{if markers.is_empty() { "none".to_string() } else { markers }}</dd>
                            <dt>"Session model"</dt>
                            <dd>{session_model}</dd>
                        </dl>
                        <p class="hint">{scope_note}</p>
                        {errors.into_iter().map(|e| view! { <p class="banner" role="alert">{e}</p> }).collect_view()}
                    }
                })}
                <button
                    type="button"
                    class="secondary"
                    data-action="container-refresh"
                    disabled=move || loading.get()
                    on:click=move |_| nonce.update(|n| *n = n.wrapping_add(1))
                >
                    {move || if loading.get() { "Refreshing…" } else { "Refresh" }}
                </button>
            </Show>
        </div>
    }
}
