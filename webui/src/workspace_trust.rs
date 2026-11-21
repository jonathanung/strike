//! Workspace trust (#1781): the cockpit surface for the daemon's
//! `GET/POST /v1/workspace/trust`, on top of the #1352 trust gate.
//!
//! An untrusted workspace runs on builtin + global config only. This shows
//! a persistent "Untrusted workspace" badge while the repo ships config the
//! gate is holding back, and a card listing exactly what trusting would load
//! (the daemon's load-free enumeration: names and command lines, never env
//! or headers), with Grant / Keep untrusted. The workspace is always the
//! live root's own directory; the browser never names a path.

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::api::{api, auth_header};
use crate::model::{workspace_trust_outcome, WorkspaceTrustReply};

fn trust_path(root: &str) -> String {
    if root.is_empty() {
        "/v1/workspace/trust".to_string()
    } else {
        format!("/v1/workspace/trust?root={}", crate::api::js_encode(root))
    }
}

/// GET when `action` is None, else POST `{"action": action}`.
async fn call_trust(
    base: &str,
    token: &str,
    root: &str,
    action: Option<&str>,
) -> Result<WorkspaceTrustReply, String> {
    let url = api(base, &trust_path(root));
    let mut builder = if action.is_some() {
        gloo_net::http::Request::post(&url)
    } else {
        gloo_net::http::Request::get(&url)
    }
    .credentials(web_sys::RequestCredentials::Include);
    let token = token.trim();
    if !token.is_empty() {
        builder = builder.header("Authorization", &auth_header(token));
    }
    let resp = match action {
        Some(a) => {
            builder
                .header("Content-Type", "application/json")
                .body(serde_json::json!({ "action": a }).to_string())
                .map_err(|e| e.to_string())?
                .send()
                .await
        }
        None => builder.send().await,
    }
    .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    workspace_trust_outcome(status, &text)
}

#[component]
pub fn WorkspaceTrust(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    status: RwSignal<String>,
    capable: RwSignal<bool>,
) -> impl IntoView {
    let reply = RwSignal::new(None::<WorkspaceTrustReply>);
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let card_open = RwSignal::new(false);
    let granted = RwSignal::new(false);

    // Re-read whenever the capability or the live root changes. The card
    // only opens from the badge: it is an overlay and never takes the
    // composer's room (phone keyboard viewport).
    Effect::new(move |_| {
        let ok = capable.get();
        let r = root.get();
        reply.set(None);
        error.set(String::new());
        granted.set(false);
        card_open.set(false);
        if !ok {
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match call_trust(&b, &t, &r, None).await {
                Ok(v) => reply.set(Some(v)),
                Err(e) => error.set(e),
            }
        });
    });

    let grant = move || {
        if busy.get_untracked() {
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
            match call_trust(&b, &t, &r, Some("grant")).await {
                Ok(v) => {
                    if v.error.is_empty() {
                        granted.set(true);
                        status.set(format!("trusted {}", v.root));
                    } else {
                        error.set(v.error.clone());
                        status.set(format!("workspace trust refused: {}", v.error));
                    }
                    reply.set(Some(v));
                }
                Err(e) => {
                    error.set(e.clone());
                    status.set(format!("workspace trust failed: {e}"));
                }
            }
            busy.set(false);
        });
    };

    // Keep untrusted changes nothing on disk: it closes the card and the
    // badge stays up.
    let keep_untrusted = move || card_open.set(false);

    let badge_visible = move || reply.get().map(|v| v.badge_visible()).unwrap_or(false);

    view! {
        <Show when=move || capable.get() && (badge_visible() || card_open.get())>
            <div class="workspace-trust">
                <Show when=badge_visible>
                    <button
                        type="button"
                        class="trust-badge"
                        data-action="workspace-trust-badge"
                        aria-expanded=move || if card_open.get() { "true" } else { "false" }
                        title=move || reply.get().map(|v| format!("Repo config is inert: {}", v.summary)).unwrap_or_default()
                        on:click=move |_| card_open.update(|o| *o = !*o)
                    >
                        "Untrusted workspace"
                    </button>
                </Show>
                <Show when=move || card_open.get()>
                    <section class="trust-card card" role="region" aria-label="Workspace trust">
                        {move || reply.get().map(|v| {
                            let sections = v.surfaces.sections();
                            let root_text = v.root.clone();
                            let restart = v.restart_required;
                            let mutable = v.mutable && !v.untrustable;
                            view! {
                                <h2 class="panel-title">
                                    {if granted.get() { "Workspace trusted" } else { "Trust this workspace?" }}
                                </h2>
                                <p class="path">{root_text}</p>
                                <Show
                                    when=move || granted.get()
                                    fallback=move || view! {
                                        <p class="hint">
                                            "This repo's own config is not loaded. Trusting it loads exactly this list. Tools, MCP calls and hooks still ask for permission as before."
                                        </p>
                                    }
                                >
                                    <p class="hint" role="status">
                                        {if restart {
                                            "Trust saved. Restart strike serve to load this config; running sessions keep the config they started with."
                                        } else {
                                            "Trust saved. New sessions load this config."
                                        }}
                                    </p>
                                </Show>
                                <dl class="trust-surfaces">
                                    {sections.into_iter().map(|s| view! {
                                        <dt>{s.title}</dt>
                                        {s.lines.into_iter().map(|l| view! { <dd><code>{l}</code></dd> }).collect_view()}
                                    }).collect_view()}
                                </dl>
                                <Show when=move || !mutable>
                                    <p class="hint">
                                        "This daemon cannot change trust (read-only or attach-only, or the home directory). Run `strike trust` in the workspace instead."
                                    </p>
                                </Show>
                            }
                        })}
                        <div class="row">
                            <Show when=move || !granted.get()>
                                <button
                                    type="button"
                                    class="primary"
                                    data-action="workspace-trust-grant"
                                    disabled=move || busy.get() || !reply.get().map(|v| v.mutable && !v.untrustable).unwrap_or(false)
                                    on:click=move |_| grant()
                                >
                                    {move || if busy.get() { "Trusting…" } else { "Grant" }}
                                </button>
                                <button
                                    type="button"
                                    class="secondary"
                                    data-action="workspace-trust-keep"
                                    on:click=move |_| keep_untrusted()
                                >
                                    "Keep untrusted"
                                </button>
                            </Show>
                            <Show when=move || granted.get()>
                                <button
                                    type="button"
                                    class="secondary"
                                    data-action="workspace-trust-close"
                                    on:click=move |_| card_open.set(false)
                                >
                                    "Close"
                                </button>
                            </Show>
                        </div>
                        <Show when=move || !error.get().is_empty()>
                            <p class="banner" role="alert">{move || error.get()}</p>
                        </Show>
                    </section>
                </Show>
            </div>
        </Show>
    }
}
