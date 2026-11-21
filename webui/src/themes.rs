//! Daemon theme catalog in the browser (#1529).
//!
//! `GET /v1/themes` is the theme universe. Picker rows and the paint
//! decision live in the `theme-catalog` crate (unit-tested in CI); this
//! module fetches and decodes the catalog, caches the last inline theme so
//! a reload paints it before the catalog answers, and writes the paint to
//! the document.

use serde_json::Value;
use theme_catalog::{CatalogState, Color, Entry, Paint, Scheme, ROLES};
use wasm_bindgen::JsCast;

use crate::api::get_json;

const CACHE_KEY: &str = "strike.themeEntry";

fn text(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or("")
        .trim()
        .to_string()
}

/// One catalog entry. Entries without an id are dropped.
pub fn entry_from_value(v: &Value) -> Option<Entry> {
    let id = text(v, "id");
    if id.is_empty() {
        return None;
    }
    let colors = v.get("colors");
    let colors = ROLES
        .iter()
        .filter_map(|(role, _)| {
            let pair = colors?.get(*role)?;
            Some(Color {
                role: (*role).to_string(),
                light: text(pair, "light"),
                dark: text(pair, "dark"),
            })
        })
        .collect();
    Some(Entry {
        id,
        name: text(v, "name"),
        provenance: text(v, "provenance"),
        overrode: text(v, "overrode"),
        colors,
    })
}

fn entry_to_value(e: &Entry) -> Value {
    let mut colors = serde_json::Map::new();
    for c in &e.colors {
        colors.insert(
            c.role.clone(),
            serde_json::json!({"light": c.light, "dark": c.dark}),
        );
    }
    serde_json::json!({
        "id": e.id,
        "name": e.name,
        "provenance": e.provenance,
        "overrode": e.overrode,
        "colors": colors,
    })
}

/// `{themes: [...]}` → entries in daemon order.
pub fn parse_list(v: &Value) -> Vec<Entry> {
    v.get("themes")
        .and_then(Value::as_array)
        .map(|list| list.iter().filter_map(entry_from_value).collect())
        .unwrap_or_default()
}

pub async fn fetch(base: &str, token: &str) -> CatalogState {
    match get_json::<Value>(base, token, "/v1/themes").await {
        Ok(v) => CatalogState::Loaded(parse_list(&v)),
        Err(e) => CatalogState::Failed(e),
    }
}

/// Last inline entry painted in this browser.
pub fn cached() -> Option<Entry> {
    let raw = crate::load(CACHE_KEY);
    if raw.is_empty() {
        return None;
    }
    serde_json::from_str::<Value>(&raw)
        .ok()
        .and_then(|v| entry_from_value(&v))
}

fn set_cache(entry: Option<&Entry>) {
    match entry {
        Some(e) => crate::save(CACHE_KEY, &entry_to_value(e).to_string()),
        None => crate::remove_key(CACHE_KEY),
    }
}

/// Resolve and paint. Returns the paint so callers can show its note.
pub fn paint(id: &str, scheme: &str, catalog: &CatalogState) -> Paint {
    let scheme_v = Scheme::parse(scheme);
    let cached = cached();
    let paint = theme_catalog::resolve(id, scheme_v, catalog, cached.as_ref());
    let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
        return paint;
    };
    let Some(el) = doc.document_element() else {
        return paint;
    };
    let scheme_attr = if scheme_v == Scheme::Light {
        "light"
    } else {
        "dark"
    };
    let style = el
        .dyn_ref::<web_sys::HtmlElement>()
        .map(web_sys::HtmlElement::style);
    if let Some(style) = &style {
        for var in theme_catalog::inline_vars() {
            let _ = style.remove_property(var);
        }
    }
    let (data_theme, chrome) = match &paint {
        Paint::Bundled(id) => (id.clone(), crate::theme_color(id, scheme_attr).to_string()),
        Paint::Inline(e) => {
            if let Some(style) = &style {
                for (var, value) in e.css_vars(scheme_v) {
                    let _ = style.set_property(var, &value);
                }
            }
            let chrome = e
                .chrome_color(scheme_v)
                .map(str::to_string)
                .unwrap_or_else(|| crate::theme_color("strike", scheme_attr).to_string());
            (e.id.clone(), chrome)
        }
        Paint::Fallback { .. } => (
            theme_catalog::DEFAULT_ID.to_string(),
            crate::theme_color("strike", scheme_attr).to_string(),
        ),
    };
    let _ = el.set_attribute("data-theme", &data_theme);
    let _ = el.set_attribute("data-scheme", scheme_attr);
    if let Some(meta) = doc
        .query_selector("meta[name=\"theme-color\"]")
        .ok()
        .flatten()
    {
        let _ = meta.set_attribute("content", &chrome);
    }
    match &paint {
        Paint::Bundled(id) => {
            crate::save("strike.theme", id);
            set_cache(None);
        }
        Paint::Inline(e) => {
            crate::save("strike.theme", &e.id);
            set_cache(Some(e));
        }
        // Keep the saved id while the catalog may still know it; drop it
        // once the catalog has answered without it.
        Paint::Fallback { keep: true, .. } => {}
        Paint::Fallback { keep: false, .. } => {
            crate::save("strike.theme", theme_catalog::DEFAULT_ID);
            set_cache(None);
        }
    }
    crate::save("strike.scheme", scheme_attr);
    paint
}
