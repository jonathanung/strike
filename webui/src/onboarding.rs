//! First-run card. Acknowledgement is daemon-scoped (#1552): completing
//! or dismissing the card writes ~/.strike/onboarding.json through
//! POST /v1/onboarding/acknowledge, and the app checks GET /v1/onboarding
//! before showing this card, so a new browser against an onboarded daemon
//! skips it. localStorage (`strike.onboarded`) is the per-browser fast
//! path and the fallback when the daemon record cannot be written.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::Value;

use crate::api::{get_json, patch_json};
use crate::appearance::ThemeChoices;
use crate::localfiles::{auth_sentence, fetch_local_files, FileList, LocalFiles};
use crate::providers::ProvidersPage;

/// First-run steps, in order (#1510).
const STEPS: [&str; 3] = ["Stays local", "Connect a provider", "Pick a look"];

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
    let dismissed = RwSignal::new(false);
    let settings_ok = RwSignal::new(false);
    let daemon_theme = RwSignal::new(String::new());
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
            let (ok, saved) = match get_json::<Value>(&b_settings, &t_settings, "/v1/settings").await {
                Ok(v) => (
                    true,
                    v.get("theme")
                        .and_then(|x| x.as_str())
                        .unwrap_or("")
                        .trim()
                        .to_string(),
                ),
                Err(_) => (false, String::new()),
            };
            // Click handling is synchronous. A dismiss that already ran wins.
            if !dismissed.get_untracked() {
                settings_ok.set(ok);
                daemon_theme.set(saved.clone());
                if ok
                    && !user_picked.get_untracked()
                    && crate::appearance::known_theme(&saved)
                    && saved != theme.get_untracked()
                {
                    on_theme(saved);
                }
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
        dismissed.set(true);
        on_theme(revert_to.clone());
        on_done();
    };
    let continue_on = move |_| {
        let chosen = theme.get_untracked();
        let picked = user_picked.get_untracked();
        let saved = daemon_theme.get_untracked();
        let ok = settings_ok.get_untracked();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        on_done();
        // A custom theme id (not one of the bundled accents) stays until the
        // user picks a swatch. A failed settings read does not invent "empty".
        let write = picked || (ok && (saved.is_empty() || saved == chosen));
        if !write {
            status.set("Kept the theme already in daemon config.".into());
            return;
        }
        spawn_local(async move {
            match patch_json(&b, &t, "/v1/settings", serde_json::json!({"theme": chosen})).await {
                Ok(()) => status.set("Saved the theme id to ~/.strike/config.".into()),
                Err(e) => status.set(format!(
                    "Theme is saved in this browser. Daemon config was not updated: {e}"
                )),
            }
        });
    };

    // #1510: three short steps instead of one long wall. Every step stays
    // mounted (hidden, not unmounted) so a provider form or theme pick
    // survives Back/Next, and Finish setup (onboarding-continue) is always present so
    // finishing never needs a scroll to the bottom of a long page.
    let step = RwSignal::new(1u8);
    let next = move |_| step.update(|s| *s = (*s + 1).min(STEPS.len() as u8));
    let back = move |_| step.update(|s| *s = s.saturating_sub(1).max(1));

    view! {
        <div class="onboarding-scrim" role="dialog" aria-modal="true" aria-labelledby="onboarding-title">
            <div class="onboarding-card">
                <div class="onboarding-lead">
                    <p class="eyebrow">{move || format!("First run · step {} of {}", step.get(), STEPS.len())}</p>
                    <h2 id="onboarding-title">"Welcome to Strike"</h2>
                    <p>"A coding agent that runs on this machine. Three short steps, and every choice can be changed later in Settings."</p>
                    <ol class="onboarding-steps">
                        {STEPS.iter().enumerate().map(|(i, label)| {
                            let n = i as u8 + 1;
                            view! {
                                <li
                                    class=move || if step.get() == n { "on" } else { "" }
                                    aria-current=move || if step.get() == n { "step" } else { "false" }
                                >{*label}</li>
                            }
                        }).collect_view()}
                    </ol>
                </div>
                <div class="onboarding-body">
                    <section class="onboarding-step" data-step="local" hidden=move || step.get() != 1>
                        <h3>"What stays on this machine"</h3>
                        <p class="telemetry" data-action="telemetry-note">"No telemetry. This daemon does not phone home. A model request goes to the provider you configure, and only when a turn runs."</p>
                        <Show when=move || telemetry_warn.get()>
                            <p class="banner" role="alert">"This daemon reported telemetry. This build does not send any."</p>
                        </Show>
                        <p class="hint">{move || {
                            let doc = files.get();
                            let where_ = if doc.work_dir.is_empty() {
                                String::new()
                            } else if doc.project_key.is_empty() {
                                format!("Strike works in {}. ", doc.work_dir)
                            } else {
                                format!("Strike works in {} (project {}). ", doc.work_dir, doc.project_key)
                            };
                            let root = if doc.global_root.is_empty() { "~/.strike".to_string() } else { doc.global_root.clone() };
                            format!("{where_}Config and credentials live under {root}. {}", auth_sentence(&doc.auth))
                        }}</p>
                        <Show when=move || loading.get()>
                            <p class="hint">"Reading the paths this process uses…"</p>
                        </Show>
                        <Show when=move || !files_error.get().is_empty()>
                            <p class="banner" role="alert">{move || files_error.get()}</p>
                            <button type="button" class="secondary" data-action="reload-files" on:click=reload>"Retry"</button>
                        </Show>
                        <Show when=move || !loading.get() && files_error.get().is_empty()>
                            <details class="onboarding-files">
                                <summary>{move || {
                                    let doc = files.get();
                                    let present = doc.files.iter().filter(|f| f.exists).count();
                                    format!("Every path the daemon reads ({present} on disk)")
                                }}</summary>
                                {move || view! { <FileList doc=files.get() /> }}
                            </details>
                        </Show>
                    </section>
                    <section class="onboarding-step" data-step="provider" hidden=move || step.get() != 2>
                        <h3>"Connect a provider"</h3>
                        <p class="hint">"A provider runs the model, for example Anthropic or OpenAI. Add its API key and pick a default model here. Until one is connected, a turn cannot reach a model. Skip this step and do it later from Menu, Providers."</p>
                        <ProvidersPage base=base token=token />
                    </section>
                    <section class="onboarding-step" data-step="look" hidden=move || step.get() != 3>
                        <h3>"Pick a look"</h3>
                        <Show when=move || theme_ready.get() fallback=move || view! { <p class="hint">"Checking the theme saved in daemon config…"</p> }>
                            <ThemeChoices group="onboarding-accent" current=theme on_pick=pick />
                            <Show when=move || {
                                let saved = daemon_theme.get();
                                !saved.is_empty() && !crate::appearance::known_theme(&saved)
                            }>
                                <p class="hint">{move || format!("Daemon config theme: {}. It is not a bundled accent. Finishing leaves it in place until you pick one.", daemon_theme.get())}</p>
                            </Show>
                        </Show>
                        <p class="hint">"Strike is the original palette. Light or dark, type size, and density live in Settings and stay in this browser."</p>
                    </section>
                    <p class="hint onboarding-note">"Not now keeps this browser's theme and writes no daemon config. Finish setup saves a bundled theme id only when you picked one or daemon config has none. Either button marks onboarding done on this daemon (~/.strike/onboarding.json), so other browsers connected to it skip this card."</p>
                    <div class="onboarding-actions">
                        <button type="button" class="secondary" data-action="onboarding-dismiss" on:click=dismiss>"Not now"</button>
                        <span class="onboarding-spacer"></span>
                        <Show when=move || { step.get() > 1 }>
                            <button type="button" class="secondary" data-action="onboarding-back" on:click=back>"Back"</button>
                        </Show>
                        <Show when=move || { (step.get() as usize) < STEPS.len() }>
                            <button type="button" class="primary" data-action="onboarding-next" on:click=next>"Next"</button>
                        </Show>
                        <button
                            type="button"
                            class=move || if step.get() as usize == STEPS.len() { "primary" } else { "secondary" }
                            data-action="onboarding-continue"
                            disabled=move || !theme_ready.get() || (loading.get() && files_error.get().is_empty())
                            on:click=continue_on
                        >"Finish setup"</button>
                    </div>
                </div>
            </div>
        </div>
    }
}
