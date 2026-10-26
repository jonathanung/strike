//! First-run card. Browser acknowledgement lives in localStorage
//! (`strike.onboarded`). strike serve does not write ~/.strike/onboarding.json.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::Value;

use crate::api::{get_json, patch_json};
use crate::appearance::ThemeChoices;
use crate::localfiles::{auth_sentence, fetch_local_files, FileList, LocalFiles};

#[component]
pub fn OnboardingCard(
    base: RwSignal<String>,
    token: RwSignal<String>,
    status: RwSignal<String>,
    theme: RwSignal<String>,
    on_theme: impl Fn(String) + Copy + Send + Sync + 'static,
    on_done: impl Fn() + Copy + Send + Sync + 'static,
) -> impl IntoView {
    let files = RwSignal::new(LocalFiles::default());
    let files_error = RwSignal::new(String::new());
    let telemetry_warn = RwSignal::new(false);
    let loading = RwSignal::new(true);
    let theme_ready = RwSignal::new(false);
    let user_picked = RwSignal::new(false);
    let revert_to = theme.get_untracked();

    let load = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let (b_settings, t_settings) = (b.clone(), t.clone());
        loading.set(true);
        files_error.set(String::new());
        spawn_local(async move {
            match fetch_local_files(&b, &t).await {
                Ok(doc) => {
                    telemetry_warn.set(doc.telemetry);
                    files.set(doc);
                    files_error.set(String::new());
                }
                Err(e) => {
                    telemetry_warn.set(false);
                    files_error.set(format!("Could not read the file list: {e}"));
                }
            }
            loading.set(false);
        });
        spawn_local(async move {
            let saved = match get_json::<Value>(&b_settings, &t_settings, "/v1/settings").await {
                Ok(v) => v
                    .get("theme")
                    .and_then(|x| x.as_str())
                    .unwrap_or("")
                    .to_string(),
                Err(_) => String::new(),
            };
            if !user_picked.get_untracked()
                && crate::appearance::known_theme(&saved)
                && saved != theme.get_untracked()
            {
                on_theme(saved);
            }
            theme_ready.set(true);
        });
    };
    load();

    let pick = move |id: String| {
        user_picked.set(true);
        on_theme(id);
    };
    let reload = move |_| load();
    let dismiss = move |_| {
        on_theme(revert_to.clone());
        on_done();
    };
    let continue_on = move |_| {
        let chosen = theme.get_untracked();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        on_done();
        spawn_local(async move {
            match patch_json(&b, &t, "/v1/settings", serde_json::json!({"theme": chosen})).await {
                Ok(()) => status.set("Saved the theme id to ~/.strike/config.".into()),
                Err(e) => status.set(format!(
                    "Theme is saved in this browser. Daemon config was not updated: {e}"
                )),
            }
        });
    };

    view! {
        <div class="onboarding-scrim" role="dialog" aria-modal="true" aria-labelledby="onboarding-title">
            <div class="onboarding-card">
                <div class="onboarding-lead">
                    <p class="eyebrow">"First run"</p>
                    <h2 id="onboarding-title">"Strike is local"</h2>
                    <p>"The daemon reads files on this machine. The accent you pick is a bundled theme. Strike is the original palette."</p>
                </div>
                <div class="onboarding-body">
                    <p class="telemetry" data-action="telemetry-note">"No telemetry. This daemon does not phone home. A model request goes to the provider you configure, and only when a turn runs."</p>
                    <Show when=move || telemetry_warn.get()>
                        <p class="banner" role="alert">"This daemon reported telemetry. This build does not send any."</p>
                    </Show>
                    <p class="hint">{move || {
                        let doc = files.get();
                        let where_ = if doc.work_dir.is_empty() {
                            String::new()
                        } else if doc.project_key.is_empty() {
                            format!("Working directory {}. ", doc.work_dir)
                        } else {
                            format!("Working directory {}. Project {}. ", doc.work_dir, doc.project_key)
                        };
                        format!("{where_}{}", auth_sentence(&doc.auth))
                    }}</p>
                    <Show when=move || loading.get()>
                        <p class="hint">"Reading the paths this process uses…"</p>
                    </Show>
                    <Show when=move || !files_error.get().is_empty()>
                        <p class="banner" role="alert">{move || files_error.get()}</p>
                        <button type="button" class="secondary" data-action="reload-files" on:click=reload>"Retry"</button>
                    </Show>
                    <Show when=move || !loading.get() && files_error.get().is_empty()>
                        {move || view! { <FileList doc=files.get() /> }}
                    </Show>
                    <h3 class="panel-title">"Providers"</h3>
                    <p class="hint">"This is the Providers page. Keys stay on the daemon and are not copied into this browser."</p>
                    <providers::ProvidersPage base=base token=token />
                    <Show when=move || theme_ready.get() fallback=move || view! { <p class="hint">"Checking the theme saved in daemon config…"</p> }>
                        <ThemeChoices group="onboarding-accent" current=theme on_pick=pick />
                    </Show>
                    <div class="onboarding-actions">
                        <button type="button" class="secondary" data-action="onboarding-dismiss" on:click=dismiss>"Not now"</button>
                        <button type="button" class="primary" data-action="onboarding-continue" disabled=move || !theme_ready.get() on:click=continue_on>"Continue"</button>
                    </div>
                    <p class="hint">"Not now keeps the theme this browser already had and does not write daemon config. Continue stores the accent here and writes the theme id to ~/.strike/config when the daemon accepts it. Light or dark, type size, and density stay in this browser."</p>
                </div>
            </div>
        </div>
    }
}
