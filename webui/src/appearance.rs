//! Browser appearance. Theme ids match the bundled TUI themes painted in
//! webui/style.css. Strike is the stock palette and the default.

use leptos::prelude::*;
use wasm_bindgen::JsCast;

pub struct ThemeChoice {
    pub id: &'static str,
    pub label: &'static str,
    pub detail: &'static str,
}

pub const THEMES: &[ThemeChoice] = &[
    ThemeChoice {
        id: "strike",
        label: "Strike",
        detail: "Original palette. Royal purple, cyan while work is in flight.",
    },
    ThemeChoice {
        id: "nord",
        label: "Nord",
        detail: "Bundled theme. Surfaces it omits stay on the Strike ladder.",
    },
    ThemeChoice {
        id: "dracula",
        label: "Dracula",
        detail: "Bundled theme. Surfaces it omits stay on the Strike ladder.",
    },
    ThemeChoice {
        id: "gruvbox",
        label: "Gruvbox",
        detail: "Bundled theme. Surfaces it omits stay on the Strike ladder.",
    },
    ThemeChoice {
        id: "monokai",
        label: "Monokai",
        detail: "Bundled theme. Surfaces it omits stay on the Strike ladder.",
    },
    ThemeChoice {
        id: "tokyo-night",
        label: "Tokyo Night",
        detail: "Bundled theme. Surfaces it omits stay on the Strike ladder.",
    },
    ThemeChoice {
        id: "catppuccin",
        label: "Catppuccin",
        detail: "Bundled theme. Surfaces it omits stay on the Strike ladder.",
    },
];

pub fn known_theme(id: &str) -> bool {
    THEMES.iter().any(|t| t.id == id)
}

pub fn normalize_font(v: &str) -> String {
    if v == "sm" || v == "lg" {
        v.to_string()
    } else {
        "md".into()
    }
}

pub fn normalize_density(v: &str) -> String {
    if v == "compact" {
        "compact".into()
    } else {
        "comfortable".into()
    }
}

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

pub fn save(key: &str, value: &str) {
    if let Some(s) = storage() {
        let _ = s.set_item(key, value);
    }
}

pub fn apply_type(font: &str, density: &str) {
    let font = normalize_font(font);
    let density = normalize_density(density);
    if let Some(doc) = web_sys::window().and_then(|w| w.document()) {
        if let Some(el) = doc.document_element() {
            let _ = el.set_attribute("data-font", &font);
            let _ = el.set_attribute("data-density", &density);
        }
    }
    save("strike.font", &font);
    save("strike.density", &density);
}

/// Removes every `strike.*` localStorage key, including the saved token.
/// Does not touch daemon files or the HttpOnly cookie.
pub fn clear_strike_keys() {
    let Some(s) = storage() else {
        return;
    };
    let n = s.length().unwrap_or(0);
    let mut keys = Vec::new();
    for i in 0..n {
        if let Ok(Some(k)) = s.key(i) {
            if k.starts_with("strike.") {
                keys.push(k);
            }
        }
    }
    for k in keys {
        let _ = s.remove_item(&k);
    }
}

/// Browser export. The daemon token is omitted so a downloaded file is not a credential.
pub fn export_browser_json() -> String {
    let mut keys = serde_json::Map::new();
    if let Some(s) = storage() {
        let n = s.length().unwrap_or(0);
        for i in 0..n {
            let Ok(Some(k)) = s.key(i) else { continue };
            if !k.starts_with("strike.") || k == "strike.token" {
                continue;
            }
            let v = s.get_item(&k).ok().flatten().unwrap_or_default();
            keys.insert(k, serde_json::Value::String(v));
        }
    }
    serde_json::to_string_pretty(&serde_json::json!({
        "storage": "localStorage",
        "omitted": ["strike.token"],
        "keys": keys,
    }))
    .unwrap_or_else(|_| "{}".into())
}

pub fn download(filename: &str, body: &str) {
    let Some(win) = web_sys::window() else { return };
    let Some(doc) = win.document() else { return };
    let Ok(el) = doc.create_element("a") else { return };
    let href = format!(
        "data:application/json;charset=utf-8,{}",
        crate::api::js_encode(body)
    );
    let _ = el.set_attribute("href", &href);
    let _ = el.set_attribute("download", filename);
    if let Some(body_el) = doc.body() {
        let _ = body_el.append_child(&el);
        if let Some(html) = el.dyn_ref::<web_sys::HtmlElement>() {
            html.click();
        }
        let _ = body_el.remove_child(&el);
    }
}

#[component]
pub fn ThemeChoices(
    group: &'static str,
    current: RwSignal<String>,
    on_pick: impl Fn(String) + Copy + Send + Sync + 'static,
) -> impl IntoView {
    view! {
        <div class="theme-grid" role="radiogroup" aria-label="Theme">
            {THEMES.iter().map(|theme| {
                let id = theme.id.to_string();
                let id_for = id.clone();
                let label = theme.label.to_string();
                let detail = theme.detail.to_string();
                let badge = if theme.id == "strike" { "Default" } else { "" };
                let on_pick = on_pick;
                view! {
                    <label class="theme-choice">
                        <input
                            type="radio"
                            name=group
                            data-theme-choice=id.clone()
                            prop:checked=move || current.get() == id
                            on:change=move |_| on_pick(id_for.clone())
                        />
                        <span class="swatch" data-theme-id=theme.id></span>
                        <span class="theme-copy">
                            <span class="name">{label} " " <span class="pill-tag">{badge}</span></span>
                            <span class="path">{detail}</span>
                        </span>
                    </label>
                }
            }).collect_view()}
        </div>
    }
}

#[component]
pub fn ChoiceRow(
    group: &'static str,
    legend: &'static str,
    options: Vec<(&'static str, &'static str)>,
    current: RwSignal<String>,
    on_pick: impl Fn(String) + Copy + Send + Sync + 'static,
) -> impl IntoView {
    view! {
        <fieldset class="choice-row">
            <legend>{legend}</legend>
            <div class="segmented" role="radiogroup" aria-label=legend>
                {options.into_iter().map(|(id, label)| {
                    let id_s = id.to_string();
                    let id_for = id_s.clone();
                    let attr = id.to_string();
                    let on_pick = on_pick;
                    view! {
                        <label class="seg-choice">
                            <input
                                type="radio"
                                name=group
                                data-choice=attr
                                prop:checked=move || current.get() == id_s
                                on:change=move |_| on_pick(id_for.clone())
                            />
                            <span>{label}</span>
                        </label>
                    }
                }).collect_view()}
            </div>
        </fieldset>
    }
}
