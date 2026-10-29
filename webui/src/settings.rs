//! Settings page. Browser keys and daemon config are labeled separately.
//! Model, approval, notification, and gateway editing stay with their streams.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::Value;

use crate::api::{get_json, patch_json};
use crate::appearance::{
    clear_strike_keys, download, export_browser_json, known_theme, ChoiceRow, ThemeChoices,
};
use crate::localfiles::{auth_sentence, fetch_local_files, FileList, LocalFiles};

pub fn follow_stream(mode: RwSignal<String>, id: &str) {
    if let Some(w) = web_sys::window() {
        let _ = w.location().set_hash(id);
    }
    match id {
        "gateway" => mode.set("daemon".into()),
        "providers" => mode.set("providers".into()),
        "models" | "approvals" => mode.set("chat".into()),
        _ => {}
    }
}

fn field(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(|x| x.as_str())
        .unwrap_or("")
        .trim()
        .to_string()
}

fn shown(v: &str, empty: &str) -> String {
    if v.is_empty() {
        empty.to_string()
    } else {
        v.to_string()
    }
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
    let dials = RwSignal::new(Value::Null);
    let dials_error = RwSignal::new(String::new());
    let files = RwSignal::new(LocalFiles::default());
    let files_error = RwSignal::new(String::new());
    let loading_files = RwSignal::new(true);
    let theme_note = RwSignal::new(String::new());
    let armed = RwSignal::new(false);

    let refresh = move || {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        let (b2, t2) = (b.clone(), t.clone());
        loading_files.set(true);
        files_error.set(String::new());
        dials_error.set(String::new());
        spawn_local(async move {
            match get_json::<Value>(&b, &t, "/v1/settings").await {
                Ok(v) => dials.set(v),
                Err(e) => dials_error.set(format!("Could not read daemon settings: {e}")),
            }
        });
        spawn_local(async move {
            match fetch_local_files(&b2, &t2).await {
                Ok(doc) => {
                    files.set(doc);
                    files_error.set(String::new());
                }
                Err(e) => files_error.set(format!("Could not read the file list: {e}")),
            }
            loading_files.set(false);
        });
    };
    refresh();

    let save_theme = move |id: String| {
        on_theme(id.clone());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match patch_json(&b, &t, "/v1/settings", serde_json::json!({"theme": id})).await {
                Ok(()) => {
                    theme_note.set("Saved the theme id to ~/.strike/config.".into());
                    refresh();
                }
                Err(e) => theme_note.set(format!(
                    "Theme is saved in this browser. Daemon config was not updated: {e}"
                )),
            }
        });
    };

    let use_daemon_theme = move |_| {
        let id = field(&dials.get_untracked(), "theme");
        if known_theme(&id) {
            save_theme(id);
        }
    };

    let export_browser = move |_| {
        download("strike-browser.json", &export_browser_json());
        status.set("Downloaded browser settings. The token was left out.".into());
    };
    let export_daemon = move |_| {
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            match get_json::<Value>(&b, &t, "/v1/settings").await {
                Ok(v) => {
                    let body = serde_json::to_string_pretty(&v).unwrap_or_else(|_| "{}".into());
                    download("strike-daemon-settings.json", &body);
                    status.set("Downloaded the daemon settings dials.".into());
                }
                Err(e) => status.set(format!("Could not export daemon settings: {e}")),
            }
        });
    };
    let clear = move |_| {
        if !armed.get_untracked() {
            armed.set(true);
            return;
        }
        clear_strike_keys();
        armed.set(false);
        on_cleared();
        status.set("Cleared Strike keys in this browser. Daemon files were left in place.".into());
    };

    let gateway_label = move || match gateway.get().as_str() {
        "connected" => "Connected",
        "unreachable" => "Unreachable",
        _ => "Checking",
    };
    let token_label = move || {
        if token.get().is_empty() {
            "No token is stored in this browser. Same-origin calls still send the cookie from /?token= when that cookie is set."
        } else {
            "A token is stored in this browser under strike.token. It is not shown here."
        }
    };

    view! {
        <main class="settings-page">
            <header class="settings-lead">
                <h2 class="panel-title">"Settings"</h2>
                <p class="hint" data-action="settings-telemetry">"No telemetry. Appearance on this page is stored in this browser. The theme id is also written to ~/.strike/config when the daemon accepts it. Model, approval mode, and notify are daemon config and are changed on their own pages."</p>
            </header>

            <section class="card settings-section">
                <h2 class="panel-title">"Appearance"</h2>
                <p class="hint">"Stored in this browser: strike.theme, strike.scheme, strike.font, strike.density. The theme id is the field the TUI reads from ~/.strike/config. Light or dark, type size, and density are not daemon settings."</p>
                <ThemeChoices group="settings-theme" current=theme on_pick=save_theme />
                <Show when=move || dials_error.get().is_empty()>
                    <p class="hint">{move || {
                        let saved = field(&dials.get(), "theme");
                        if saved.is_empty() {
                            "Daemon config has no theme id. The TUI uses the stock Strike palette until one is saved.".into()
                        } else {
                            format!("Daemon config theme: {saved}")
                        }
                    }}</p>
                </Show>
                <Show when=move || {
                    let saved = field(&dials.get(), "theme");
                    known_theme(&saved) && saved != theme.get()
                }>
                    <button type="button" class="secondary" on:click=use_daemon_theme>"Use the daemon theme in this browser"</button>
                </Show>
                <Show when=move || !theme_note.get().is_empty()>
                    <p class="hint" role="status">{move || theme_note.get()}</p>
                </Show>
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

            <section class="card settings-section">
                <h2 class="panel-title">"Defaults"</h2>
                <p class="hint">"Read from ~/.strike/config through GET /v1/settings. This page does not edit them."</p>
                <Show when=move || !dials_error.get().is_empty()>
                    <p class="banner" role="alert">{move || dials_error.get()}</p>
                </Show>
                <ul class="plain">
                    <li class="file-item">
                        <span class="name">{move || {
                            if !dials_error.get().is_empty() {
                                return "Model · Unavailable".into();
                            }
                            let v = dials.get();
                            let p = field(&v, "provider");
                            let m = field(&v, "model");
                            format!("Model · {}", match (p.is_empty(), m.is_empty()) {
                                (true, true) => "Unset in daemon config".into(),
                                (false, true) => p,
                                (true, false) => m,
                                (false, false) => format!("{p} · {m}"),
                            })
                        }}</span>
                        <span class="path">"The composer model chip changes the live session. The model menu is the models page."</span>
                        <button type="button" class="secondary" data-stream="models" on:click=move |_| follow_stream(mode, "models")>"Open model menu"</button>
                    </li>
                    <li class="file-item">
                        <span class="name">{move || {
                            if !dials_error.get().is_empty() {
                                "Approval · Unavailable".into()
                            } else {
                                format!("Approval · {}", shown(&field(&dials.get(), "mode"), "Unset in daemon config"))
                            }
                        }}</span>
                        <span class="path">"This is the saved permission mode. The composer approval chip sets the live session. Rules and approval cards are the approvals page."</span>
                        <button type="button" class="secondary" data-stream="approvals" on:click=move |_| follow_stream(mode, "approvals")>"Open approval settings"</button>
                    </li>
                    <li class="file-item">
                        <span class="name">{move || {
                            if !dials_error.get().is_empty() {
                                "Notify · Unavailable".into()
                            } else {
                                format!("Notify · {}", shown(&field(&dials.get(), "notify"), "Unset in daemon config"))
                            }
                        }}</span>
                        <span class="path">"This is the notify dial in daemon config. The notification centre is the notifications page."</span>
                        <button type="button" class="secondary" data-stream="notifications" on:click=move |_| follow_stream(mode, "notifications")>"Open notification centre"</button>
                    </li>
                </ul>
            </section>

            <section class="card settings-section">
                <h2 class="panel-title">"Keyboard"</h2>
                <ul class="shortcut-list">
                    <li><kbd>"Ctrl"</kbd>" or "<kbd>"Cmd"</kbd>" + "<kbd>"K"</kbd><span>"Command palette"</span></li>
                    <li><kbd>"?"</kbd><span>"Shortcut list, when the cursor is not in a field"</span></li>
                    <li><kbd>"Ctrl"</kbd>" or "<kbd>"Cmd"</kbd>" + "<kbd>","</kbd><span>"Settings. A browser may reserve this chord."</span></li>
                    <li><kbd>"Alt"</kbd>" + "<kbd>"Left"</kbd><span>"Previous session"</span></li>
                    <li><kbd>"Alt"</kbd>" + "<kbd>"Right"</kbd><span>"Next session"</span></li>
                    <li><kbd>"Alt"</kbd>" + "<kbd>"\\"</kbd><span>"Focus the transcript"</span></li>
                    <li><kbd>"Esc"</kbd><span>"Close the command palette and the menus"</span></li>
                    <li><kbd>"Enter"</kbd><span>"Send. Queue waits for the turn. Steer inserts into the current turn. Shift+Enter adds a line."</span></li>
                    <li><kbd>"Enter"</kbd><span>"In the command palette, run the first match"</span></li>
                </ul>
            </section>

            <section class="card settings-section">
                <h2 class="panel-title">"Gateway"</h2>
                <p class="name">{gateway_label}</p>
                <p class="hint">{token_label}</p>
                <p class="hint">{move || auth_sentence(&files.get().auth).to_string()}</p>
                <p class="path">{move || {
                    let b = base.get();
                    if b.is_empty() { "Base URL is this daemon.".into() } else { format!("Base URL {b}") }
                }}</p>
                <button type="button" class="secondary" data-stream="gateway" on:click=move |_| follow_stream(mode, "gateway")>"Open gateway"</button>
                <button type="button" class="secondary" data-stream="providers" on:click=move |_| follow_stream(mode, "providers")>"Open providers"</button>
                <p class="hint">"The Daemon view holds the token and saved daemon URLs. SSH profiles, port forwards, and token rotation are the gateway page (#gateway)."</p>
            </section>

            <section class="card settings-section">
                <h2 class="panel-title">"Data locations"</h2>
                <p class="hint">{move || {
                    let doc = files.get();
                    match (doc.work_dir.is_empty(), doc.global_root.is_empty(), doc.project_key.is_empty()) {
                        (true, _, _) => "The working directory is reported with the file list.".into(),
                        (false, true, true) => format!("Process working directory {}.", doc.work_dir),
                        (false, false, true) => format!("Process working directory {}. Global root {}.", doc.work_dir, doc.global_root),
                        (false, true, false) => format!("Process working directory {}. Project {}.", doc.work_dir, doc.project_key),
                        (false, false, false) => format!("Process working directory {}. Project {}. Global root {}.", doc.work_dir, doc.project_key, doc.global_root),
                    }
                }}</p>
                <Show when=move || loading_files.get()>
                    <p class="hint">"Reading the paths this process uses…"</p>
                </Show>
                <Show when=move || !files_error.get().is_empty()>
                    <p class="banner" role="alert">{move || files_error.get()}</p>
                    <button type="button" class="secondary" data-action="reload-files" on:click=move |_| refresh()>"Retry"</button>
                </Show>
                <Show when=move || !loading_files.get() && files_error.get().is_empty()>
                    {move || view! { <FileList doc=files.get() /> }}
                </Show>
            </section>

            <section class="card settings-section">
                <h2 class="panel-title">"This browser"</h2>
                <p class="hint">"Export and clear affect this browser. Export leaves strike.token out of the file. Clear removes every strike.* localStorage key and the gateway tokens kept for this tab. Neither one deletes daemon files. The HttpOnly cookie from /?token= stays until the browser drops it."</p>
                <div class="row">
                    <button type="button" class="secondary" data-action="export-browser" on:click=export_browser>"Download browser data"</button>
                    <button type="button" class="secondary" data-action="export-daemon" on:click=export_daemon>"Download daemon settings"</button>
                </div>
                <div class="row">
                    <button type="button" class="danger" data-action="clear-local" on:click=clear>
                        {move || if armed.get() { "Confirm clear" } else { "Clear browser data" }}
                    </button>
                    <Show when=move || armed.get()>
                        <button type="button" class="secondary" on:click=move |_| armed.set(false)>"Cancel"</button>
                    </Show>
                </div>
            </section>
        </main>
    }
}
