//! Project init (#1581): the Settings control for the daemon's
//! `GET/POST /v1/init`, which writes a starter AGENTS.md through the same
//! host `ProjectInit` service as the TUI `/init`.
//!
//! The browser path creates only. When AGENTS.md already exists the daemon
//! answers 409 and this control says so; replacing the file stays a TUI
//! `/init` confirm.

use leptos::prelude::*;
use crate::error_notice::humanize;
use leptos::task::spawn_local;

use crate::api::{api, auth_header};
use crate::model::{project_init_outcome, ProjectInitReply};

/// Hint shown while bootstrap reports `projectInit: false`.
pub const PROJECT_INIT_UNAVAILABLE: &str =
    "Project init needs a live, writable daemon. Use /init in the TUI instead.";

fn init_path(root: &str) -> String {
    if root.is_empty() {
        "/v1/init".to_string()
    } else {
        format!("/v1/init?root={}", crate::api::js_encode(root))
    }
}

async fn call_init(
    base: &str,
    token: &str,
    root: &str,
    write: bool,
) -> Result<ProjectInitReply, String> {
    let url = api(base, &init_path(root));
    let mut builder = if write {
        gloo_net::http::Request::post(&url)
    } else {
        gloo_net::http::Request::get(&url)
    }
    .credentials(web_sys::RequestCredentials::Include);
    let token = token.trim();
    if !token.is_empty() {
        builder = builder.header("Authorization", &auth_header(token));
    }
    let resp = if write {
        builder
            .header("Content-Type", "application/json")
            .body("{}".to_string())
            .map_err(|e| e.to_string())?
            .send()
            .await
    } else {
        builder.send().await
    }
    .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    project_init_outcome(status, &text)
}

#[component]
pub fn ProjectInit(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    status: RwSignal<String>,
    capable: RwSignal<bool>,
) -> impl IntoView {
    let busy = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let reply = RwSignal::new(None::<ProjectInitReply>);
    let created = RwSignal::new(false);

    // Preview the target whenever the capability or the root changes.
    Effect::new(move |_| {
        let ok = capable.get();
        let r = root.get();
        created.set(false);
        reply.set(None);
        error.set(String::new());
        if !ok {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match call_init(&b, &t, &r, false).await {
                Ok(v) => reply.set(Some(v)),
                Err(e) => error.set(e),
            }
        });
    });

    let create = move || {
        if busy.get_untracked() || !capable.get_untracked() {
            return;
        }
        let (b, t, r) = (
            base.get_untracked(),
            token.get_untracked(),
            root.get_untracked(),
        );
        busy.set(true);
        error.set(String::new());
        spawn_local(async move {
            match call_init(&b, &t, &r, true).await {
                Ok(v) => {
                    if v.error.is_empty() {
                        created.set(true);
                        status.set(format!("wrote {}", v.path));
                    } else {
                        status.set(format!("project init refused: {}", v.error));
                    }
                    reply.set(Some(v));
                }
                Err(e) => {
                    error.set(e.clone());
                    status.set(format!("project init failed: {e}"));
                }
            }
            busy.set(false);
        });
    };

    view! {
        <div class="project-init">
            <Show when=move || !capable.get()>
                <p class="hint">{PROJECT_INIT_UNAVAILABLE}</p>
            </Show>
            <Show when=move || capable.get()>
                <p class="hint" role="status">
                    {move || match reply.get() {
                        Some(v) if created.get() => {
                            format!("Created {}. Review it before your next turn.", v.path)
                        }
                        Some(v) if v.exists => {
                            format!(
                                "{} already exists. The browser never overwrites it; use /init in the TUI to replace it.",
                                v.path,
                            )
                        }
                        Some(v) => {
                            format!(
                                "Writes a starter AGENTS.md (same as the TUI /init) to {}.",
                                v.path,
                            )
                        }
                        None => "Checking for AGENTS.md…".to_string(),
                    }}
                </p>
                <button
                    type="button"
                    class="secondary"
                    data-action="project-init"
                    disabled=move || busy.get() || reply.get().map(|v| v.exists).unwrap_or(true)
                    on:click=move |_| create()
                >
                    {move || if busy.get() { "Creating…" } else { "Create AGENTS.md" }}
                </button>
            </Show>
            <Show when=move || !error.get().is_empty()>
                <p class="banner" role="alert">{move || humanize(&error.get())}</p>
            </Show>
        </div>
    }
}
