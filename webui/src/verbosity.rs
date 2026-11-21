//! Response style (#1369): a per-session Concise / Default / Detailed
//! control in the composer chip row. How much the agent says is a separate
//! axis from effort (how hard it thinks). Choosing a style posts
//! `set.verbosity`; the chip changes only on the daemon's
//! `verbosity.selected` confirmation, which also says how the style reaches
//! the model (`instruction`, or `none` for the default). A style is never
//! shown as active while it does nothing.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::{json, Value};

use crate::api::post_op;

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct VerbosityState {
    /// "" (default) | concise | detailed
    pub level: String,
    /// "" (not reported yet) | none | instruction
    pub delivery: String,
}

thread_local! {
    // Reference counted, so the chip and the event loop share it without
    // either owning it.
    static STATE: ArcRwSignal<VerbosityState> = ArcRwSignal::new(VerbosityState::default());
}

pub fn state() -> ArcRwSignal<VerbosityState> {
    STATE.with(|s| s.clone())
}

fn set(next: VerbosityState) {
    STATE.with(|s| {
        if s.get_untracked() != next {
            s.set(next);
        }
    });
}

/// A session switch: the next session's style is unknown until its
/// status or replayed confirmation lands. Never carry one across.
pub fn reset() {
    set(VerbosityState::default());
}

/// Seed from `GET /v1/status` / bootstrap (`verbosity`, `verbosityDelivery`).
pub fn seed(level: &str, delivery: &str) {
    set(VerbosityState {
        level: normalize(level).to_string(),
        delivery: delivery.trim().to_string(),
    });
}

/// Known style id, or "" for default/unknown input.
pub fn normalize(level: &str) -> &'static str {
    match level.trim().to_ascii_lowercase().as_str() {
        "concise" => "concise",
        "detailed" => "detailed",
        _ => "",
    }
}

/// Reduce one event envelope. Only a root `verbosity.selected` changes the
/// state; child sessions (depth > 0) keep their own style.
pub fn reduce(state: VerbosityState, raw: &str) -> VerbosityState {
    if !raw.contains("verbosity.selected") {
        return state;
    }
    let Ok(v) = serde_json::from_str::<Value>(raw) else {
        return state;
    };
    if v.get("type").and_then(Value::as_str) != Some("verbosity.selected") {
        return state;
    }
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    if data.get("depth").and_then(Value::as_i64).unwrap_or(0) > 0 {
        return state;
    }
    let level = data.get("level").and_then(Value::as_str).unwrap_or("");
    let delivery = data.get("delivery").and_then(Value::as_str).unwrap_or("");
    VerbosityState {
        level: normalize(level).to_string(),
        delivery: delivery.trim().to_string(),
    }
}

pub fn observe(raw: &str) {
    let next = reduce(STATE.with(|s| s.get_untracked()), raw);
    set(next);
}

pub fn label(level: &str) -> &'static str {
    match normalize(level) {
        "concise" => "Concise",
        "detailed" => "Detailed",
        _ => "Default",
    }
}

/// How the active style reaches the model, in words for the chip title.
pub fn delivery_note(st: &VerbosityState) -> String {
    match st.delivery.as_str() {
        "instruction" => format!(
            "{} style is sent to the model as a session instruction. Applies from the next turn.",
            label(&st.level)
        ),
        "none" => "Default style: nothing is sent; the model answers at its own length.".into(),
        _ if st.level.is_empty() => "Default style: nothing is sent; the model answers at its own length.".into(),
        _ => "Waiting for the daemon to confirm how this style is delivered.".into(),
    }
}

/// Chip text: the style, plus how it is delivered when it does anything.
pub fn chip_text(st: &VerbosityState) -> String {
    match (normalize(&st.level), st.delivery.as_str()) {
        ("", _) => "Style · Default".into(),
        (_, "instruction") => format!("Style · {} (instruction)", label(&st.level)),
        _ => format!("Style · {}", label(&st.level)),
    }
}

/// Palette command id; the chip opens the palette at it.
pub const COMMAND: &str = "session.verbosity";

/// Wire level for a palette choice id, or None when unknown.
pub fn wire_level(choice: &str) -> Option<&'static str> {
    match choice.trim().to_ascii_lowercase().as_str() {
        "" | "default" => Some("default"),
        "concise" => Some("concise"),
        "detailed" => Some("detailed"),
        _ => None,
    }
}

/// Composer chip. `busy` is the active session's running-turn flag: the
/// daemon refuses the op mid-turn, so the chip says so up front. The chip
/// opens the palette at [`COMMAND`] (the chip strip scrolls, so a popover
/// inside it would be clipped); the command dispatches back here.
#[component]
pub fn VerbosityChip(
    base: RwSignal<String>,
    token: RwSignal<String>,
    active_root: RwSignal<String>,
    status: RwSignal<String>,
    busy: Signal<bool>,
) -> impl IntoView {
    let st = state();
    let pick = move |choice: &str| {
        let Some(wire) = wire_level(choice) else {
            status.set(format!("Unknown response style {choice:?}."));
            return;
        };
        let root = active_root.get_untracked();
        if root.is_empty() {
            status.set("Open a session to choose a response style.".into());
            return;
        }
        if busy.get_untracked() {
            status.set("This turn is still running. Style changes wait until it finishes.".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, "set.verbosity", json!({ "level": wire })).await {
                status.set(format!("Style was not changed: {e}"));
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
    let text = {
        let st = st.clone();
        move || chip_text(&st.get())
    };
    let title = {
        let st = st.clone();
        move || delivery_note(&st.get())
    };
    let label = text.clone();
    let current = move || normalize(&st.get().level).to_string();
    view! {
        <button
            type="button"
            class="composer-glance"
            data-verbosity-status=current
            title=title
            aria-label=move || format!("{}. Change response style", label())
            on:click=move |_| crate::shell::show_palette_at(COMMAND)
        >{text}</button>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn st(level: &str, delivery: &str) -> VerbosityState {
        VerbosityState { level: level.into(), delivery: delivery.into() }
    }

    #[test]
    fn reduce_applies_root_confirmations_only() {
        let cases = [
            (r#"{"type":"verbosity.selected","data":{"level":"concise","delivery":"instruction"}}"#, st("concise", "instruction")),
            (r#"{"type":"verbosity.selected","data":{"level":"","delivery":"none"}}"#, st("", "none")),
            (r#"{"type":"verbosity.selected","data":{"level":"loud","delivery":"instruction"}}"#, st("", "instruction")),
            (r#"{"type":"verbosity.selected","data":{"level":"detailed","delivery":"instruction","depth":1}}"#, st("detailed", "instruction")),
            (r#"{"type":"effort.selected","data":{"level":"high"}}"#, st("detailed", "instruction")),
        ];
        let mut cur = st("detailed", "instruction");
        for (raw, want) in cases {
            let next = reduce(st("detailed", "instruction"), raw);
            assert_eq!(next, want, "{raw}");
            cur = next;
        }
        let _ = cur;
    }

    #[test]
    fn chip_says_how_the_style_is_delivered() {
        assert_eq!(chip_text(&st("", "none")), "Style · Default");
        assert_eq!(chip_text(&st("concise", "instruction")), "Style · Concise (instruction)");
        assert_eq!(chip_text(&st("detailed", "")), "Style · Detailed");
        assert!(delivery_note(&st("concise", "instruction")).contains("session instruction"));
        assert!(delivery_note(&st("", "none")).contains("nothing is sent"));
        assert!(delivery_note(&st("concise", "")).contains("Waiting"));
    }

    #[test]
    fn wire_level_maps_palette_choices() {
        for (raw, want) in [("default", Some("default")), ("", Some("default")), ("Concise", Some("concise")), ("detailed", Some("detailed")), ("loud", None)] {
            assert_eq!(wire_level(raw), want, "{raw}");
        }
    }

    #[test]
    fn normalize_known_levels() {
        for (raw, want) in [("Concise", "concise"), (" detailed ", "detailed"), ("default", ""), ("", ""), ("x", "")] {
            assert_eq!(normalize(raw), want, "{raw}");
        }
    }
}
