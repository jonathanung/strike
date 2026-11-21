//! Model fallback (#1350): the session's fallback model. The pick is a
//! palette command (`model.fallback`) posting `set.fallback_model`; the
//! composer chip appears only once the daemon confirms a fallback with
//! `fallback.selected`. A turn the fallback answered is marked in the
//! transcript from the daemon's `model.fallback` event (see
//! `transcript.rs`), naming both models.
//!
//! #1922: when the primary refuses, the session stays on the fallback
//! (`model.fallback` with `sticky`, `fallback.selected` with `active`). The
//! chip then reads "On fallback" and offers Return to primary, which posts
//! `fallback.return`.
//!
//! #1963: the sticky notice in the transcript carries Return to primary
//! too (the chip binds `RETURN_ACTION`, the notice dispatches it); a
//! delegated child's fallback turn is marked in the parent's transcript
//! with child wording; the palette's fallback choices mark the current
//! pick.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde_json::{json, Value};

use crate::api::post_op;

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FallbackState {
    pub provider: String,
    pub model: String,
    /// The session runs on the fallback until Return to primary (#1922).
    pub active: bool,
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

/// Seed from status (`fallbackProvider`, `fallbackModel`,
/// `fallbackActive`). An unreported fallback leaves the state alone so it
/// cannot undo a replayed pick.
pub fn seed(provider: &str, model: &str, active: bool) {
    if provider.trim().is_empty() {
        return;
    }
    set(FallbackState {
        provider: provider.trim().to_string(),
        model: model.trim().to_string(),
        active,
    });
}

/// Reduce one envelope: a root `fallback.selected` replaces the state; a
/// root sticky `model.fallback` (#1922) marks the session as running on the
/// fallback.
pub fn reduce(state: FallbackState, raw: &str) -> FallbackState {
    if !raw.contains("fallback") {
        return state;
    }
    let Ok(v) = serde_json::from_str::<Value>(raw) else {
        return state;
    };
    let typ = v.get("type").and_then(Value::as_str).unwrap_or("");
    if typ != "fallback.selected" && typ != "model.fallback" {
        return state;
    }
    let data = v.get("data").cloned().unwrap_or(Value::Null);
    if data.get("depth").and_then(Value::as_i64).unwrap_or(0) > 0 {
        return state;
    }
    let flag = |k: &str| data.get(k).and_then(Value::as_bool).unwrap_or(false);
    if typ == "model.fallback" {
        if !flag("sticky") {
            return state;
        }
        return FallbackState { active: true, ..state };
    }
    let text = |k: &str| data.get(k).and_then(Value::as_str).unwrap_or("").trim().to_string();
    FallbackState {
        provider: text("provider"),
        model: text("model"),
        active: flag("active"),
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

/// Transcript notice for a `model.fallback` event: title and body. A
/// delegated child's notice (depth > 0, #1963) says so; only the root's
/// own sticky notice points at Return to primary.
pub fn notice_text(data: &Value) -> (String, String) {
    let text = |k: &str| data.get(k).and_then(Value::as_str).unwrap_or("").to_string();
    let fallback = model_label(&text("fallbackProvider"), &text("fallbackModel"));
    let primary = model_label(&text("primaryProvider"), &text("primaryModel"));
    let child = is_child(data);
    let title = if child { "Model fallback · delegated agent" } else { "Model fallback" };
    let lead = if child { "A delegated agent was answered by" } else { "Answered by" };
    let body = match text("reason").as_str() {
        "unavailable" | "" => format!("{lead} {fallback} — {primary} unavailable"),
        "refused" if child => format!(
            "{lead} {fallback} — {primary} refused. That agent stays on {fallback} for the rest of its task."
        ),
        "refused" => format!(
            "{lead} {fallback} — {primary} refused. The session stays on {fallback} until you use Return to primary."
        ),
        other => format!("{lead} {fallback} — {primary} {other}"),
    };
    (title.into(), body)
}

fn is_child(data: &Value) -> bool {
    data.get("depth").and_then(Value::as_i64).unwrap_or(0) > 0
}

/// Transcript key for a root sticky `model.fallback` notice (#1963): the
/// notice offers Return to primary while the session is still on the
/// fallback. Every other fallback notice has no key.
pub const STICKY_KEY: &str = "fallback.sticky";

pub fn notice_key(data: &Value) -> String {
    let sticky = data.get("sticky").and_then(Value::as_bool).unwrap_or(false);
    if sticky && !is_child(data) {
        STICKY_KEY.into()
    } else {
        String::new()
    }
}

/// Page binding behind the notice's Return to primary (#1963). The chip
/// binds it, because only the page knows the session.
pub const RETURN_ACTION: &str = "fallback.return_to_primary";

/// Palette choice detail (#1963): marks the row matching the current
/// fallback so the menu shows what is set before you change it.
pub fn choice_detail(current: &FallbackState, choice_id: &str, base: &str) -> String {
    // The daemon strips a matching "provider/" prefix from the model it
    // confirms; compare bare ids so a prefixed catalog id still matches.
    let bare = |p: &str, m: &str| {
        let (p, m) = (p.trim(), m.trim());
        let prefix = format!("{p}/");
        m.strip_prefix(prefix.as_str()).unwrap_or(m).to_string()
    };
    let is_current = match choice_id.split_once('\t') {
        Some((p, m)) => {
            current.is_set()
                && current.provider == p.trim()
                && bare(p, m) == bare(&current.provider, &current.model)
        }
        None => !current.is_set(),
    };
    if is_current {
        format!("Current · {base}")
    } else {
        base.to_string()
    }
}

/// Palette command id; the chip opens the palette at it.
pub const COMMAND: &str = "model.fallback";

/// Op that ends a session-sticky fallback (#1922).
pub const RETURN_OP: &str = "fallback.return";

/// Composer chip label: says plainly when the session is running on the
/// fallback rather than merely having one configured.
pub fn chip_text(st: &FallbackState) -> String {
    if st.active {
        format!("On fallback · {}", st.label())
    } else {
        format!("Fallback · {}", st.label())
    }
}

fn chip_title(st: &FallbackState) -> String {
    if st.active {
        format!(
            "The session model refused a request, so {} answers every turn until you use Return to primary. Click to change or remove the fallback.",
            st.label()
        )
    } else {
        format!(
            "If the session model is unavailable after retries, {} answers that turn and the next turn tries the session model again. If it refuses, the session stays on {} until you return. Click to change or remove.",
            st.label(),
            st.label()
        )
    }
}

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
        move || chip_text(&st.get())
    };
    let active = {
        let st = st.clone();
        move || st.get().active
    };
    let active_attr = {
        let st = st.clone();
        move || if st.get().active { "true" } else { "false" }
    };
    let title = move || chip_title(&st.get());
    let go_back = move || {
        let root = active_root.get_untracked();
        if root.is_empty() {
            return;
        }
        if busy.get_untracked() {
            status.set("This turn is still running. Return to primary waits until it finishes.".into());
            return;
        }
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            if let Err(e) = post_op(&b, &t, &root, RETURN_OP, json!({})).await {
                status.set(format!("Still on the fallback model: {e}"));
            }
        });
    };
    // The transcript's sticky notice dispatches this (#1963).
    crate::shell::bind(
        RETURN_ACTION,
        std::rc::Rc::new(move |_: &str| {
            go_back();
            Ok(())
        }),
    );
    on_cleanup(|| crate::shell::unbind(RETURN_ACTION));
    let return_to_primary = move |_: web_sys::MouseEvent| go_back();
    view! {
        <Show when=shown>
            <button
                type="button"
                class="composer-glance"
                data-fallback-status="true"
                data-fallback-active=active_attr.clone()
                title=title.clone()
                on:click=move |_| crate::shell::show_palette_at(COMMAND)
            >{text.clone()}</button>
            <Show when=active.clone()>
                <button
                    type="button"
                    class="composer-glance"
                    data-fallback-return="true"
                    title="Send the next turn to the session model again"
                    on:click=return_to_primary
                >"Return to primary"</button>
            </Show>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fs(p: &str, m: &str) -> FallbackState {
        FallbackState { provider: p.into(), model: m.into(), active: false }
    }

    fn on(p: &str, m: &str) -> FallbackState {
        FallbackState { active: true, ..fs(p, m) }
    }

    #[test]
    fn reduce_tracks_sticky_fallback() {
        let cases = [
            (fs("openai", "gpt"), r#"{"type":"model.fallback","data":{"reason":"refused","sticky":true}}"#, on("openai", "gpt")),
            (fs("openai", "gpt"), r#"{"type":"model.fallback","data":{"reason":"unavailable"}}"#, fs("openai", "gpt")),
            (fs("openai", "gpt"), r#"{"type":"model.fallback","data":{"sticky":true,"depth":2}}"#, fs("openai", "gpt")),
            (on("openai", "gpt"), r#"{"type":"fallback.selected","data":{"provider":"openai","model":"gpt"}}"#, fs("openai", "gpt")),
            (fs("openai", "gpt"), r#"{"type":"fallback.selected","data":{"provider":"openai","model":"gpt","active":true}}"#, on("openai", "gpt")),
        ];
        for (start, raw, want) in cases {
            assert_eq!(reduce(start, raw), want, "{raw}");
        }
    }

    #[test]
    fn refused_notice_says_the_session_stays() {
        let data = serde_json::json!({
            "primaryProvider": "anthropic", "primaryModel": "a1",
            "fallbackProvider": "openai", "fallbackModel": "g1",
            "reason": "refused", "sticky": true
        });
        let (_, body) = notice_text(&data);
        assert!(body.starts_with("Answered by openai/g1 — anthropic/a1 refused."), "{body}");
        assert!(body.contains("Return to primary"), "{body}");
    }

    #[test]
    fn child_notice_says_delegated_agent() {
        let data = serde_json::json!({
            "primaryProvider": "anthropic", "primaryModel": "a1",
            "fallbackProvider": "openai", "fallbackModel": "g1",
            "reason": "unavailable", "depth": 1
        });
        let (title, body) = notice_text(&data);
        assert_eq!(title, "Model fallback · delegated agent");
        assert_eq!(body, "A delegated agent was answered by openai/g1 — anthropic/a1 unavailable");
        let refused = serde_json::json!({
            "primaryProvider": "anthropic", "fallbackProvider": "openai",
            "reason": "refused", "sticky": true, "depth": 2
        });
        let (_, body) = notice_text(&refused);
        assert!(body.contains("That agent stays on openai"), "{body}");
        assert!(!body.contains("Return to primary"), "{body}");
    }

    #[test]
    fn only_root_sticky_notices_get_the_return_key() {
        let cases = [
            (serde_json::json!({"reason": "refused", "sticky": true}), STICKY_KEY),
            (serde_json::json!({"reason": "refused", "sticky": true, "depth": 1}), ""),
            (serde_json::json!({"reason": "unavailable"}), ""),
            (serde_json::json!({}), ""),
        ];
        for (data, want) in cases {
            assert_eq!(notice_key(&data), want, "{data}");
        }
    }

    #[test]
    fn choice_detail_marks_the_current_fallback() {
        let set = fs("openai", "gpt");
        assert_eq!(choice_detail(&set, "openai\tgpt", "d"), "Current · d");
        assert_eq!(choice_detail(&set, "openai\tother", "d"), "d");
        assert_eq!(choice_detail(&set, "none", "d"), "d");
        assert_eq!(choice_detail(&set, "openai\topenai/gpt", "d"), "Current · d");
        let unset = fs("", "");
        assert_eq!(choice_detail(&unset, "none", "d"), "Current · d");
        assert_eq!(choice_detail(&unset, "openai\tgpt", "d"), "d");
    }

    #[test]
    fn chip_text_says_when_on_fallback() {
        assert_eq!(chip_text(&fs("openai", "gpt")), "Fallback · openai/gpt");
        assert_eq!(chip_text(&on("openai", "gpt")), "On fallback · openai/gpt");
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
