//! Read-only side chat. Questions go to `/v1/sidechat/ask` and never to
//! `user.input` or `permission.reply`. The panel mirrors the session events
//! the app already subscribes to; it has no approval control.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;
use serde_json::{json, Value};
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::api::{get_json, post_json_value};
use crate::model::{strip_echo_tip, ChatLine};

#[derive(Clone, Debug, Deserialize)]
struct SideChatTurn {
    id: String,
    question: String,
    #[serde(default)]
    answer: String,
    #[serde(default, rename = "focusName")]
    focus_name: String,
    #[serde(default)]
    provider: String,
    #[serde(default)]
    model: String,
}

#[derive(Deserialize)]
struct SideChatList {
    #[serde(default, rename = "readOnly")]
    read_only: bool,
    #[serde(default)]
    turns: Vec<SideChatTurn>,
}

#[derive(Deserialize)]
struct SideChatAnswer {
    id: String,
    question: String,
    answer: String,
    #[serde(default, rename = "readOnly")]
    read_only: bool,
    #[serde(default, rename = "focusName")]
    focus_name: String,
    #[serde(default)]
    provider: String,
    #[serde(default)]
    model: String,
}

/// Body for a side-chat question. None when the text is blank. The body has
/// no protocol op type, so it cannot be a permission reply.
pub fn side_chat_ask_body(text: &str, focus_id: &str, focus_name: &str) -> Option<Value> {
    let text = text.trim();
    if text.is_empty() {
        return None;
    }
    let mut body = json!({ "text": text });
    let focus_id = focus_id.trim();
    let focus_name = focus_name.trim();
    if !focus_id.is_empty() {
        body["focusSessionId"] = json!(focus_id);
    }
    if !focus_name.is_empty() {
        body["focusName"] = json!(focus_name);
    }
    Some(body)
}

/// Label when the answer's model is not the live root's model. Empty when
/// they match, so an echo boot does not stamp every turn.
fn answered_by_other(
    provider: &str,
    model: &str,
    live_provider: &str,
    live_model: &str,
) -> Option<String> {
    let provider = provider.trim();
    let model = model.trim();
    if provider.is_empty() && model.is_empty() {
        return None;
    }
    let same_provider = provider.eq_ignore_ascii_case(live_provider.trim()) || provider.is_empty();
    let same_model = model.eq_ignore_ascii_case(live_model.trim())
        || (model.is_empty() && live_model.trim().is_empty());
    if same_provider && same_model {
        return None;
    }
    let label = match (provider.is_empty(), model.is_empty()) {
        (false, false) => format!("{provider} · {model}"),
        (false, true) => provider.to_string(),
        _ => model.to_string(),
    };
    Some(format!("Answered by {label}"))
}

const SIDE_NOT_READONLY: &str = "Side chat list was not marked read-only.";

/// Which failure the banner's Retry button is looking at.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SideErrorKind {
    None,
    Load,
    Ask,
}

/// Per-session side-chat memory. Reset when the root changes so a question
/// asked in one session cannot be replayed into another.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SideChatMemory {
    pub draft: String,
    pub error: String,
    pub kind: SideErrorKind,
    pub last_ask: String,
    pub last_ask_root: String,
    pub pending: bool,
    pub loaded_for: String,
    pub error_root: String,
}

impl Default for SideChatMemory {
    fn default() -> Self {
        Self {
            draft: String::new(),
            error: String::new(),
            kind: SideErrorKind::None,
            last_ask: String::new(),
            last_ask_root: String::new(),
            pending: false,
            loaded_for: String::new(),
            error_root: String::new(),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SideLoadPlan {
    /// Already loaded for this root. Leave the draft and the banner.
    Stay,
    /// Root changed. Drop the previous session's draft, error, and question.
    Switch,
    /// Same root, load failed. Fetch again without wiping the draft.
    Reload,
}

pub fn plan_side_load(mem: &SideChatMemory, session: &str) -> SideLoadPlan {
    if !session.is_empty() && mem.loaded_for == session {
        return SideLoadPlan::Stay;
    }
    if !session.is_empty()
        && mem.loaded_for.is_empty()
        && mem.kind == SideErrorKind::Load
        && mem.error_root == session
    {
        return SideLoadPlan::Reload;
    }
    SideLoadPlan::Switch
}

pub fn apply_side_plan(mem: &mut SideChatMemory, session: &str, plan: SideLoadPlan) {
    match plan {
        SideLoadPlan::Stay => {}
        SideLoadPlan::Reload => {
            mem.loaded_for = session.to_string();
            mem.error.clear();
            mem.kind = SideErrorKind::None;
        }
        SideLoadPlan::Switch => {
            mem.draft.clear();
            mem.error.clear();
            mem.kind = SideErrorKind::None;
            mem.last_ask.clear();
            mem.last_ask_root.clear();
            mem.pending = false;
            mem.error_root.clear();
            if session.is_empty() {
                mem.loaded_for.clear();
            } else {
                mem.loaded_for = session.to_string();
            }
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SideRetry {
    Ignore,
    Replay,
    Reload,
}

pub fn side_retry(mem: &SideChatMemory, root: &str) -> SideRetry {
    if mem.pending || root.is_empty() {
        return SideRetry::Ignore;
    }
    match mem.kind {
        SideErrorKind::Load if mem.error_root == root => SideRetry::Reload,
        SideErrorKind::Ask if mem.last_ask_root == root && !mem.last_ask.trim().is_empty() => {
            SideRetry::Replay
        }
        _ => SideRetry::Ignore,
    }
}

/// A failed or rejected load must not stay latched, or Retry and the next
/// open of the same root cannot fetch again.
pub fn mark_side_load_failed(mem: &mut SideChatMemory, root: &str, message: &str) {
    mem.loaded_for.clear();
    mem.error = message.to_string();
    mem.kind = SideErrorKind::Load;
    mem.error_root = root.to_string();
}

fn clip_line(s: &str) -> String {
    let count = s.chars().count();
    if count <= 180 {
        return s.to_string();
    }
    let clipped: String = s.chars().take(180).collect();
    format!("{clipped}…")
}

#[component]
pub fn SideChatPanel(
    open: RwSignal<bool>,
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    lines: RwSignal<Vec<ChatLine>>,
    focus_id: RwSignal<String>,
    focus_name: RwSignal<String>,
    live_provider: RwSignal<String>,
    live_model: RwSignal<String>,
) -> impl IntoView {
    let draft = RwSignal::new(String::new());
    let turns = RwSignal::new(Vec::<SideChatTurn>::new());
    let error = RwSignal::new(String::new());
    let pending = RwSignal::new(false);
    let loaded_for = RwSignal::new(String::new());
    let last_ask = RwSignal::new(String::new());
    let last_ask_root = RwSignal::new(String::new());
    let error_kind = RwSignal::new(SideErrorKind::None);
    let error_root = RwSignal::new(String::new());
    let reload_gen = RwSignal::new(0u32);

    let read_memory = move || SideChatMemory {
        draft: draft.get_untracked(),
        error: error.get_untracked(),
        kind: error_kind.get_untracked(),
        last_ask: last_ask.get_untracked(),
        last_ask_root: last_ask_root.get_untracked(),
        pending: pending.get_untracked(),
        loaded_for: loaded_for.get_untracked(),
        error_root: error_root.get_untracked(),
    };
    let write_memory = move |mem: SideChatMemory| {
        draft.set(mem.draft);
        error.set(mem.error);
        error_kind.set(mem.kind);
        last_ask.set(mem.last_ask);
        last_ask_root.set(mem.last_ask_root);
        pending.set(mem.pending);
        loaded_for.set(mem.loaded_for);
        error_root.set(mem.error_root);
    };

    Effect::new(move |_| {
        let is_open = open.get();
        let session = root.get();
        let _reload = reload_gen.get();
        if !is_open {
            return;
        }
        if let Some(window) = web_sys::window() {
            let focus_input = Closure::once(move || {
                let Some(doc) = web_sys::window().and_then(|w| w.document()) else {
                    return;
                };
                let Some(el) = doc.get_element_by_id("side-chat-input") else {
                    return;
                };
                if let Ok(input) = el.dyn_into::<web_sys::HtmlTextAreaElement>() {
                    let _ = input.focus();
                }
            });
            let _ = window.set_timeout_with_callback_and_timeout_and_arguments_0(
                focus_input.as_ref().unchecked_ref(),
                0,
            );
            focus_input.forget();
        }
        let mut mem = read_memory();
        let plan = plan_side_load(&mem, &session);
        if plan == SideLoadPlan::Stay {
            return;
        }
        apply_side_plan(&mut mem, &session, plan);
        write_memory(mem);
        turns.set(Vec::new());
        if session.is_empty() {
            return;
        }
        let expected = session.clone();
        let (b, t) = (base.get(), token.get());
        spawn_local(async move {
            let path = format!("/v1/sidechat?root={}", crate::api::js_encode(&expected));
            match get_json::<SideChatList>(&b, &t, &path).await {
                Ok(list) => {
                    if root.get_untracked() != expected {
                        return;
                    }
                    if list.read_only {
                        turns.set(list.turns);
                        error.set(String::new());
                        error_kind.set(SideErrorKind::None);
                    } else {
                        let mut mem = read_memory();
                        mark_side_load_failed(&mut mem, &expected, SIDE_NOT_READONLY);
                        write_memory(mem);
                    }
                }
                Err(e) => {
                    if root.get_untracked() != expected {
                        return;
                    }
                    let mut mem = read_memory();
                    mark_side_load_failed(&mut mem, &expected, &e);
                    write_memory(mem);
                }
            }
        });
    });

    let ask = move || {
        if pending.get_untracked() {
            return;
        }
        let Some(body) = side_chat_ask_body(
            &draft.get_untracked(),
            &focus_id.get_untracked(),
            &focus_name.get_untracked(),
        ) else {
            return;
        };
        let session = root.get_untracked();
        if session.is_empty() {
            error.set("No live session yet.".into());
            return;
        }
        last_ask.set(body["text"].as_str().unwrap_or_default().to_string());
        last_ask_root.set(session.clone());
        draft.set(String::new());
        pending.set(true);
        error.set(String::new());
        error_kind.set(SideErrorKind::None);
        let expected = session.clone();
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let path = format!("/v1/sidechat/ask?root={}", crate::api::js_encode(&expected));
            let result = post_json_value(&b, &t, &path, body).await;
            pending.set(false);
            if root.get_untracked() != expected {
                return;
            }
            match result {
                Ok(value) => match serde_json::from_value::<SideChatAnswer>(value) {
                    Ok(answer) if answer.read_only => {
                        turns.update(|list| {
                            list.push(SideChatTurn {
                                id: answer.id,
                                question: answer.question,
                                answer: answer.answer,
                                focus_name: answer.focus_name,
                                provider: answer.provider,
                                model: answer.model,
                            });
                        });
                    }
                    Ok(_) => {
                        error.set("Side chat answer was not marked read-only.".into());
                        error_kind.set(SideErrorKind::Ask);
                        error_root.set(expected.clone());
                    }
                    Err(e) => {
                        error.set(e.to_string());
                        error_kind.set(SideErrorKind::Ask);
                        error_root.set(expected.clone());
                    }
                },
                Err(e) => {
                    error.set(e);
                    error_kind.set(SideErrorKind::Ask);
                    error_root.set(expected.clone());
                }
            }
        });
    };

    let retry = move || match side_retry(&read_memory(), &root.get_untracked()) {
        SideRetry::Ignore => {}
        SideRetry::Replay => {
            draft.set(last_ask.get_untracked());
            ask();
        }
        SideRetry::Reload => {
            reload_gen.update(|n| *n = n.wrapping_add(1));
        }
    };

    view! {
        <Show when=move || open.get()>
            <aside class="side-chat" data-side-chat="open" role="dialog" aria-label="Side chat" on:keydown=move |ev: web_sys::KeyboardEvent| {
                if ev.key() == "Escape" {
                    ev.prevent_default();
                    open.set(false);
                }
            }>
                <header class="side-chat-head">
                    <h2>"Side chat"</h2>
                    <span class="side-chat-readonly">"Read-only"</span>
                    <button type="button" class="secondary" on:click=move |_| open.set(false)>"Close"</button>
                </header>
                <p class="hint">"Questions stay in this chat. They do not steer the session or approve a tool."</p>
                <Show when=move || !focus_name.get().is_empty()>
                    <p class="hint">{move || format!("About {}", focus_name.get())}</p>
                </Show>
                <div class="side-chat-scroll">
                    <div class="side-chat-mirror" aria-label="Session events">
                        <p class="hint">"This session"</p>
                        {move || {
                            let all = lines.get();
                            let start = all.len().saturating_sub(8);
                            let shown: Vec<_> = all[start..].to_vec();
                            if shown.is_empty() {
                                view! { <p class="hint">"No session events yet."</p> }.into_any()
                            } else {
                                shown.into_iter().map(|line| {
                                    let title = line.title.clone();
                                    let body = clip_line(&line.body);
                                    view! {
                                        <p class="side-chat-event">
                                            <span class="who">{title}</span>
                                            <span>{body}</span>
                                        </p>
                                    }
                                }).collect_view().into_any()
                            }
                        }}
                    </div>
                    {move || {
                        let live_p = live_provider.get();
                        let live_m = live_model.get();
                        turns.get().into_iter().map(|turn| {
                        let question = turn.question.clone();
                        let answer = strip_echo_tip(&turn.answer);
                        let about = turn.focus_name.clone();
                        let answered_by = answered_by_other(&turn.provider, &turn.model, &live_p, &live_m);
                        let _ = turn.id.clone();
                        view! {
                            <article class="side-chat-turn" data-side-chat-turn="true">
                                <p class="side-chat-you">{question}</p>
                                {(!about.is_empty()).then(|| {
                                    let label = about.clone();
                                    view! { <p class="hint">{label}</p> }
                                })}
                                <p class="side-chat-answer" data-side-chat-answer="true">{answer}</p>
                                {answered_by.map(|label| view! { <p class="hint">{label}</p> })}
                            </article>
                        }
                    }).collect_view()
                    }}
                </div>
                <Show when=move || !error.get().is_empty()>
                    <div class="notice" role="alert" data-side-chat-error="true">
                        <span>{move || error.get()}</span>
                        <button type="button" class="secondary" on:click=move |_| retry()>"Retry"</button>
                    </div>
                </Show>
                <form on:submit=move |ev: web_sys::SubmitEvent| { ev.prevent_default(); ask(); }>
                    <textarea
                        id="side-chat-input"
                        placeholder="Ask about this session"
                        prop:value=move || draft.get()
                        disabled=move || pending.get()
                        on:input=move |ev| draft.set(event_target_value(&ev))
                        on:keydown=move |ev: web_sys::KeyboardEvent| {
                            if ev.key() == "Enter" && !ev.shift_key() {
                                ev.prevent_default();
                                ask();
                            } else if ev.key() == "Escape" {
                                ev.prevent_default();
                                open.set(false);
                            }
                        }
                    ></textarea>
                    <div class="side-chat-bar">
                        <span class="hint">"Enter asks. Shift+Enter adds a line. Esc closes."</span>
                        <button class="primary send" type="submit" disabled=move || pending.get()>"Ask"</button>
                    </div>
                </form>
            </aside>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ask_body_is_not_an_op() {
        let body = side_chat_ask_body("  why this file? ", "child-1", "scout").unwrap();
        assert_eq!(body["text"], "why this file?");
        assert_eq!(body["focusSessionId"], "child-1");
        assert_eq!(body["focusName"], "scout");
        assert!(body.get("type").is_none());
        assert!(body.get("decision").is_none());
        assert!(body.get("data").is_none());
        assert!(side_chat_ask_body("   ", "", "").is_none());
        let plain = side_chat_ask_body("hello", "", "  ").unwrap();
        assert!(plain.get("focusSessionId").is_none());
        assert!(plain.get("focusName").is_none());
        assert!(answered_by_other("echo", "echo", "echo", "echo").is_none());
        assert_eq!(
            answered_by_other("anthropic", "claude-opus", "echo", "echo").as_deref(),
            Some("Answered by anthropic · claude-opus")
        );
    }

    fn errored(root: &str) -> SideChatMemory {
        SideChatMemory {
            draft: "draft for A".into(),
            error: "boom".into(),
            kind: SideErrorKind::Ask,
            last_ask: "why this file?".into(),
            last_ask_root: root.into(),
            pending: true,
            loaded_for: root.into(),
            error_root: root.into(),
        }
    }

    #[test]
    fn switching_roots_drops_the_previous_question() {
        let mut mem = errored("session-a");
        assert_eq!(plan_side_load(&mem, "session-b"), SideLoadPlan::Switch);
        apply_side_plan(&mut mem, "session-b", SideLoadPlan::Switch);
        assert!(mem.draft.is_empty());
        assert!(mem.error.is_empty());
        assert!(mem.last_ask.is_empty());
        assert!(!mem.pending);
        assert_eq!(mem.loaded_for, "session-b");
        assert_eq!(mem.kind, SideErrorKind::None);
        // A's question must not replay into B even if the clear was skipped.
        let mut stale = errored("session-a");
        stale.pending = false;
        assert_eq!(side_retry(&stale, "session-b"), SideRetry::Ignore);
        assert_eq!(side_retry(&stale, "session-a"), SideRetry::Replay);
    }

    #[test]
    fn load_error_retry_reloads_without_wiping_the_draft() {
        let mut mem = SideChatMemory {
            draft: "keep me".into(),
            error: "HTTP 500".into(),
            kind: SideErrorKind::Load,
            loaded_for: String::new(),
            error_root: "session-b".into(),
            ..SideChatMemory::default()
        };
        assert_eq!(side_retry(&mem, "session-b"), SideRetry::Reload);
        assert_eq!(plan_side_load(&mem, "session-b"), SideLoadPlan::Reload);
        apply_side_plan(&mut mem, "session-b", SideLoadPlan::Reload);
        assert_eq!(mem.draft, "keep me");
        assert!(mem.error.is_empty());
        assert_eq!(mem.loaded_for, "session-b");
        // A rejected read-only list is a load failure and unlatches.
        mem.loaded_for = "session-b".into();
        mark_side_load_failed(&mut mem, "session-b", SIDE_NOT_READONLY);
        assert!(mem.loaded_for.is_empty());
        assert_eq!(mem.kind, SideErrorKind::Load);
        assert_eq!(plan_side_load(&mem, "session-b"), SideLoadPlan::Reload);
        assert_eq!(side_retry(&mem, "session-b"), SideRetry::Reload);
        // Same root, already loaded: do not reset.
        mem.loaded_for = "session-b".into();
        mem.kind = SideErrorKind::None;
        mem.draft = "typed".into();
        assert_eq!(plan_side_load(&mem, "session-b"), SideLoadPlan::Stay);
    }
}
