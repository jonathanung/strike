//! Settings page. Appearance stays here. Everything else is a palette command.

use std::cell::{Cell, RefCell};
use std::rc::Rc;

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::Value;

use crate::api::{get_json, patch_json};
use crate::appearance::{known_theme, ChoiceRow, ThemeChoices};
use crate::localfiles::{fetch_local_files, FileList, LocalFiles};

thread_local! {
    static THEME_MISMATCH: Cell<bool> = const { Cell::new(false) };
    static DAEMON_THEME: RefCell<String> = RefCell::new(String::new());
}

pub fn daemon_theme_mismatch() -> bool {
    THEME_MISMATCH.with(|cell| cell.get())
}

fn field(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(|x| x.as_str())
        .unwrap_or("")
        .trim()
        .to_string()
}

#[component]
pub fn SettingsPage(
    base: RwSignal<String>,
    token: RwSignal<String>,
    gateway: RwSignal<String>,
    mode: RwSignal<String>,
    status: RwSignal<String>,
    theme: RwSignal<String>,
    scheme: RwSignal<String>,
    font: RwSignal<String>,
    density: RwSignal<String>,
    on_theme: impl Fn(String) + Copy + Send + Sync + 'static,
    on_scheme: impl Fn(String) + Copy + Send + Sync + 'static,
    on_font: impl Fn(String) + Copy + Send + Sync + 'static,
    on_density: impl Fn(String) + Copy + Send + Sync + 'static,
    on_cleared: impl Fn() + Copy + Send + Sync + 'static,
) -> impl IntoView {
    let _ = (gateway, mode, on_cleared);
    let dials = RwSignal::new(Value::Null);
    let files = RwSignal::new(LocalFiles::default());
    let files_error = RwSignal::new(String::new());
    let loading_files = RwSignal::new(true);
    let show_files = RwSignal::new(false);

    let refresh_files = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        loading_files.set(true);
        files_error.set(String::new());
        spawn_local(async move {
            match fetch_local_files(&b, &t).await {
                Ok(doc) => {
                    files.set(doc);
                    files_error.set(String::new());
                }
                Err(e) => files_error.set(format!("Could not read the file list: {e}")),
            }
            loading_files.set(false);
        });
    };
    let refresh_dials = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Ok(v) = get_json::<Value>(&b, &t, "/v1/settings").await {
                dials.set(v);
            }
        });
    };
    refresh_files();
    refresh_dials();

    Effect::new(move |_| {
        let saved = field(&dials.get(), "theme");
        let browser = theme.get();
        let mismatch = known_theme(&saved) && saved != browser;
        THEME_MISMATCH.with(|cell| cell.set(mismatch));
        DAEMON_THEME.with(|slot| *slot.borrow_mut() = saved);
    });

    let save_theme = move |id: String| {
        on_theme(id.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match patch_json(&b, &t, "/v1/settings", serde_json::json!({"theme": id})).await {
                Ok(()) => {
                    status.set("Saved the theme id to ~/.strike/config.".into());
                    refresh_dials();
                }
                Err(e) => status.set(format!(
                    "Theme is saved in this browser. Daemon config was not updated: {e}"
                )),
            }
        });
    };

    let open_files = move || {
        show_files.set(true);
        refresh_files();
    };
    crate::shell::bind(
        "settings.files",
        Rc::new(move |_| {
            open_files();
            Ok(())
        }),
    );
    crate::shell::bind(
        "settings.use-daemon-theme",
        Rc::new(move |_| {
            let id = DAEMON_THEME.with(|slot| slot.borrow().clone());
            if known_theme(&id) {
                save_theme(id);
            }
            Ok(())
        }),
    );
    if crate::park::take("settings.files").is_some() {
        open_files();
    }
    if crate::park::take("settings.use-daemon-theme").is_some() {
        let id = DAEMON_THEME.with(|slot| slot.borrow().clone());
        if known_theme(&id) {
            save_theme(id);
        }
    }

    view! {
        <main class="settings-page">
            <header class="settings-lead">
                <h2 class="panel-title">"Settings"</h2>
            </header>
            <section class="card settings-section">
                <h2 class="panel-title">"Appearance"</h2>
                <ThemeChoices group="settings-theme" current=theme on_pick=save_theme />
                <ChoiceRow
                    group="settings-scheme"
                    legend="Scheme"
                    options=vec![("dark", "Dark"), ("light", "Light")]
                    current=scheme
                    on_pick=on_scheme
                />
                <ChoiceRow
                    group="settings-font"
                    legend="Type size"
                    options=vec![("sm", "Small"), ("md", "Default"), ("lg", "Large")]
                    current=font
                    on_pick=on_font
                />
                <ChoiceRow
                    group="settings-density"
                    legend="Density"
                    options=vec![("comfortable", "Comfortable"), ("compact", "Compact")]
                    current=density
                    on_pick=on_density
                />
            </section>
            <Show when=move || show_files.get()>
                <section class="card settings-section" id="settings-files">
                    <h2 class="panel-title">"Data locations"</h2>
                    <p class="hint">"No telemetry."</p>
                    <Show when=move || loading_files.get()>
                        <p class="hint">"Reading the paths this process uses…"</p>
                    </Show>
                    <Show when=move || !files_error.get().is_empty()>
                        <p class="banner" role="alert">{move || files_error.get()}</p>
                        <button type="button" class="secondary" data-action="reload-files" on:click=move |_| refresh_files()>"Retry"</button>
                    </Show>
                    <Show when=move || !loading_files.get() && files_error.get().is_empty()>
                        {move || view! { <FileList doc=files.get() /> }}
                    </Show>
                </section>
            </Show>
        </main>
    }
}
