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
fn answered_by_other(provider: &str, model: &str, live_provider: &str, live_model: &str) -> Option<String> {
    let provider = provider.trim();
    let model = model.trim();
    if provider.is_empty() && model.is_empty() {
        return None;
    }
    let same_provider = provider.eq_ignore_ascii_case(live_provider.trim()) || provider.is_empty();
    let same_model = model.eq_ignore_ascii_case(live_model.trim()) || (model.is_empty() && live_model.trim().is_empty());
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

    Effect::new(move |_| {
        let is_open = open.get();
        let session = root.get();
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
        if session.is_empty() {
            turns.set(Vec::new());
            loaded_for.set(String::new());
            return;
        }
        if loaded_for.get_untracked() == session {
            return;
        }
        turns.set(Vec::new());
        loaded_for.set(session.clone());
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
                    } else {
                        error.set("Side chat list was not marked read-only.".into());
                    }
                }
                Err(e) => {
                    if root.get_untracked() != expected {
                        return;
                    }
                    loaded_for.set(String::new());
                    error.set(e);
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
        draft.set(String::new());
        pending.set(true);
        error.set(String::new());
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
                    Ok(_) => error.set("Side chat answer was not marked read-only.".into()),
                    Err(e) => error.set(e.to_string()),
                },
                Err(e) => error.set(e),
            }
        });
    };

    let retry = move || {
        if pending.get_untracked() {
            return;
        }
        let last = last_ask.get_untracked();
        if last.trim().is_empty() {
            return;
        }
        draft.set(last);
        ask();
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
}
