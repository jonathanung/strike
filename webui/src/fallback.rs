//! Model fallback (#1350): the session's fallback model. The pick is a
//! palette command (`model.fallback`) posting `set.fallback_model`; the
//! composer chip appears only once the daemon confirms a fallback with
//! `fallback.selected`. A turn the fallback answered is marked in the
//! transcript from the daemon's `model.fallback` event (see
//! `transcript.rs`), naming both models.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::{json, Value};

use crate::api::post_op;

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FallbackState {
    pub provider: String,
    pub model: String,
}

impl FallbackState {
    pub fn is_set(&self) -> bool {
        !self.provider.trim().is_empty()
    }

    pub fn label(&self) -> String {
        model_label(&self.provider, &self.model)
    }
}

thread_local! {
    static STATE: ArcRwSignal<FallbackState> = ArcRwSignal::new(FallbackState::default());
}

pub fn state() -> ArcRwSignal<FallbackState> {
    STATE.with(|s| s.clone())
}

fn set(next: FallbackState) {
    STATE.with(|s| {
        if s.get_untracked() != next {
            s.set(next);
        }
    });
}

/// Session switch: the next session's fallback is unknown until its status
/// or replayed confirmation lands.
pub fn reset() {
    set(FallbackState::default());
}

/// Seed from status (`fallbackProvider`, `fallbackModel`). An unreported
/// fallback leaves the state alone so it cannot undo a replayed pick.
pub fn seed(provider: &str, model: &str) {
    if provider.trim().is_empty() {
        return;
    }
    set(FallbackState {
        provider: provider.trim().to_string(),
        model: model.trim().to_string(),
    });
}

/// Reduce one envelope: only a root `fallback.selected` changes the state.
pub fn reduce(state: FallbackState, raw: &str) -> FallbackState {
    if !raw.contains("fallback.selected") {
        return state;
    }
    let Ok(v) = serde_json::from_str::<Value>(raw) else {
        return state;
    };
    if v.get("type").and_then(Value::as_str) != Some("fallback.selected") {
        return state;
    }
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    if data.get("depth").and_then(Value::as_i64).unwrap_or(0) > 0 {
        return state;
    }
    let text = |k: &str| data.get(k).and_then(Value::as_str).unwrap_or("").trim().to_string();
    FallbackState {
        provider: text("provider"),
        model: text("model"),
    }
}

pub fn observe(raw: &str) {
    let next = reduce(STATE.with(|s| s.get_untracked()), raw);
    set(next);
}

pub fn model_label(provider: &str, model: &str) -> String {
    match (provider.trim(), model.trim()) {
        ("", "") => String::new(),
        (p, "") => p.to_string(),
        ("", m) => m.to_string(),
        (p, m) => format!("{p}/{m}"),
    }
}

/// Transcript notice for a `model.fallback` event: title and body.
pub fn notice_text(data: &Value) -> (String, String) {
    let text = |k: &str| data.get(k).and_then(Value::as_str).unwrap_or("").to_string();
    let fallback = model_label(&text("fallbackProvider"), &text("fallbackModel"));
    let primary = model_label(&text("primaryProvider"), &text("primaryModel"));
    let why = match text("reason").as_str() {
        "unavailable" | "" => "unavailable",
        other => return ("Model fallback".into(), format!("Answered by {fallback} — {primary} {other}")),
    };
    ("Model fallback".into(), format!("Answered by {fallback} — {primary} {why}"))
}

/// Palette command id; the chip opens the palette at it.
pub const COMMAND: &str = "model.fallback";

/// Decode the palette's dispatch argument: "" clears, otherwise
/// `provider` + tab + `model` (the catalog choice id).
pub fn decode_pick(args: &str) -> Option<(String, String)> {
    let args = args.trim_matches(|c: char| c == ' ' || c == '\n');
    if args.is_empty() {
        return Some((String::new(), String::new()));
    }
    let (p, m) = args.split_once('\t')?;
    if p.trim().is_empty() {
        return None;
    }
    Some((p.trim().to_string(), m.trim().to_string()))
}

/// Composer chip, shown only while a fallback is confirmed. It also binds
/// the palette command's dispatch, since only the page knows the session.
#[component]
pub fn FallbackChip(
    base: RwSignal<String>,
    token: RwSignal<String>,
    active_root: RwSignal<String>,
    status: RwSignal<String>,
    busy: Signal<bool>,
) -> impl IntoView {
    let st = state();
    let pick = move |args: &str| {
        let Some((provider, model)) = decode_pick(args) else {
            status.set("Pick a model from the list for the fallback.".into());
            return;
        };
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("Open a session to choose a fallback model.".into());
            return;
        }
        if busy.get_untracked() {
            status.set("This turn is still running. Fallback changes wait until it finishes.".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let body = json!({ "provider": provider, "model": model });
            if let Err(e) = post_op(&b, &t, &root, "set.fallback_model", body).await {
                status.set(format!("Fallback was not changed: {e}"));
            }
        });
    };
    crate::shell::bind(
        COMMAND,
        std::rc::Rc::new(move |args: &str| {
            pick(args);
            Ok(())
        }),
    );
    on_cleanup(|| crate::shell::unbind(COMMAND));
    let shown = {
        let st = st.clone();
        move || st.get().is_set()
    };
    let text = {
        let st = st.clone();
        move || format!("Fallback · {}", st.get().label())
    };
    let title = move || {
        format!(
            "If the session model is unavailable after retries, {} answers that turn and the next turn tries the session model again. Click to change or remove.",
            st.get().label()
        )
    };
    view! {
        <Show when=shown>
            <button
                type="button"
                class="composer-glance"
                data-fallback-status="true"
                title=title.clone()
                on:click=move |_| crate::shell::show_palette_at(COMMAND)
            >{text.clone()}</button>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fs(p: &str, m: &str) -> FallbackState {
        FallbackState { provider: p.into(), model: m.into() }
    }

    #[test]
    fn reduce_applies_root_confirmations() {
        let cases = [
            (r#"{"type":"fallback.selected","data":{"provider":"openai","model":"gpt"}}"#, fs("openai", "gpt")),
            (r#"{"type":"fallback.selected","data":{"provider":"","model":""}}"#, fs("", "")),
            (r#"{"type":"fallback.selected","data":{"provider":"x","model":"y","depth":1}}"#, fs("anthropic", "a")),
            (r#"{"type":"model.selected","data":{"provider":"x","model":"y"}}"#, fs("anthropic", "a")),
        ];
        for (raw, want) in cases {
            assert_eq!(reduce(fs("anthropic", "a"), raw), want, "{raw}");
        }
    }

    #[test]
    fn notice_names_both_models() {
        let data = serde_json::json!({
            "primaryProvider": "anthropic", "primaryModel": "a1",
            "fallbackProvider": "openai", "fallbackModel": "g1",
            "reason": "unavailable", "error": "overloaded"
        });
        let (title, body) = notice_text(&data);
        assert_eq!(title, "Model fallback");
        assert_eq!(body, "Answered by openai/g1 — anthropic/a1 unavailable");
    }

    #[test]
    fn decode_pick_reads_catalog_ids() {
        assert_eq!(decode_pick(""), Some((String::new(), String::new())));
        assert_eq!(decode_pick("openai\tgpt-5"), Some(("openai".into(), "gpt-5".into())));
        assert_eq!(decode_pick("free text"), None);
        assert_eq!(decode_pick("\tgpt"), None);
    }

    #[test]
    fn label_and_set() {
        assert!(!fs("", "").is_set());
        assert_eq!(fs("openai", "gpt").label(), "openai/gpt");
        assert_eq!(model_label("echo", ""), "echo");
    }
}
