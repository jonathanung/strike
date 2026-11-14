//! Download diagnostics (#1489): the Settings control for the daemon's
//! redacted diagnostic bundle, `GET /v1/diag`.
//!
//! This is the diagnostic *bundle* route — not `/v1/diagnostics`, which is
//! the LSP diagnostics surface (#1404) and stays untouched.
//!
//! Filename choice: unlike the audit export's `download_text` idiom (a
//! client-chosen name for client-built text), the bundle arrives as an
//! attachment whose name the server chooses. The fetch therefore reads
//! the response's `Content-Disposition` header
//! ([`crate::model::diag_filename`]) and saves the raw response text
//! under that name via the Blob download in
//! [`crate::transcript_view::save_download`]. The body is never parsed
//! or re-serialised by the cockpit, so the daemon's redaction posture is
//! exactly what lands on disk, and nothing is logged or cached here.

use leptos::prelude::*;
use leptos::task::spawn_local;

use crate::api::{api, auth_header};
use crate::model::{diag_error_message, diag_filename};
use crate::transcript_view::save_download;

/// The daemon's 503 wording, shown as the disabled control's hint before
/// any click (attach-only / no-live hosts). Kept verbatim from
/// `internal/frontend/server/diag.go`.
pub const DIAG_UNAVAILABLE: &str = "diag capability unavailable on this host";

async fn fetch_diag(base: &str, token: &str, root: &str) -> Result<String, String> {
    let path = if root.is_empty() {
        "/v1/diag".to_string()
    } else {
        format!("/v1/diag?root={}", crate::api::js_encode(root))
    };
    let mut req = gloo_net::http::Request::get(&api(base, &path))
        .credentials(web_sys::RequestCredentials::Include);
    let token = token.trim();
    if !token.is_empty() {
        req = req.header("Authorization", &auth_header(token));
    }
    let resp = req.send().await.map_err(|e| e.to_string())?;
    let status = resp.status();
    let filename = diag_filename(resp.headers().get("content-disposition").as_deref());
    let text = resp.text().await.unwrap_or_default();
    if !(200..300).contains(&status) {
        return Err(diag_error_message(status, &text));
    }
    save_download(&filename, "application/json", &text);
    Ok(filename)
}

#[component]
pub fn DiagDownload(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    status: RwSignal<String>,
    diag_capable: RwSignal<bool>,
) -> impl IntoView {
    let busy = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let done = RwSignal::new(false);

    let download = move || {
        if busy.get_untracked() || !diag_capable.get_untracked() {
            return;
        }
        let (b, t, r) = (
            base.get_untracked(),
            token.get_untracked(),
            root.get_untracked(),
        );
        busy.set(true);
        error.set(String::new());
        done.set(false);
        spawn_local(async move {
            match fetch_diag(&b, &t, &r).await {
                Ok(_name) => {
                    error.set(String::new());
                    done.set(true);
                    status.set("diagnostic bundle downloaded".into());
                }
                Err(e) => {
                    error.set(e.clone());
                    status.set(format!("diagnostic download failed: {e}"));
                }
            }
            busy.set(false);
        });
    };

    view! {
        <div class="diag-download">
            <button
                type="button"
                class="secondary"
                data-action="download-diagnostics"
                disabled=move || !diag_capable.get() || busy.get()
                on:click=move |_| download()
            >
                {move || if busy.get() { "Downloading…" } else { "Download diagnostics" }}
            </button>
            <Show when=move || !diag_capable.get()>
                <p class="hint">{DIAG_UNAVAILABLE}</p>
            </Show>
            <Show when=move || diag_capable.get() && error.get().is_empty()>
                <p class="hint">
                    "Redacted bundle from the live engine (same as the TUI /diag). Nothing is previewed or stored in the browser."
                </p>
            </Show>
            <Show when=move || done.get()>
                <p class="hint" role="status">"diagnostic bundle downloaded"</p>
            </Show>
            <Show when=move || !error.get().is_empty()>
                <p class="banner" role="alert">{move || error.get()}</p>
                <button
                    type="button"
                    class="secondary"
                    data-action="retry-diagnostics"
                    disabled=move || busy.get()
                    on:click=move |_| download()
                >
                    "Retry"
                </button>
            </Show>
        </div>
    }
}
