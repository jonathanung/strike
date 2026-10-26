//! Transcript pane: markdown messages, tool cards, minimap, rename, export.
//! Composer send and queue stay in the shell. File tabs stay in the editor
//! stream; this pane only emits `path:line` links.

use leptos::callback::{Callable, Callback};
use leptos::prelude::*;
use wasm_bindgen::JsCast;
use wasm_bindgen::JsValue;

use crate::status_shim::Badge;
use crate::transcript::{
    empty_headline, parse_markdown, show_minimap, stick_to_bottom, suggestions, Block, Chip,
    ChipKind, Inline, Message, Role, Transcript,
};

#[derive(Clone, Copy)]
pub struct TranscriptHandlers {
    pub send: Callback<(String,)>,
    pub rename: Callback<(String,)>,
    pub regenerate: Callback<()>,
    pub export_timeline: Callback<()>,
    pub export_markdown: Callback<()>,
    pub retry: Callback<(String,)>,
    pub fork: Callback<(i32,)>,
    pub open_file: Callback<(String, u32)>,
}

#[component]
pub fn TranscriptPane(
    transcript: RwSignal<Transcript>,
    loading: RwSignal<bool>,
    load_error: RwSignal<String>,
    tick: RwSignal<u32>,
    note: RwSignal<String>,
    handlers: TranscriptHandlers,
) -> impl IntoView {
    let editing = RwSignal::new(false);
    let draft = RwSignal::new(String::new());
    let pinned = RwSignal::new(true);
    let cursor = RwSignal::new(None::<u32>);
    let scroller = NodeRef::<leptos::html::Div>::new();
    let scroller_fx = scroller;

    Effect::new(move |_| {
        let count = transcript.with(|t| t.messages.len());
        let stick = pinned.get();
        if !stick {
            return;
        }
        let _ = count;
        if let Some(el) = scroller_fx.get() {
            el.set_scroll_top(el.scroll_height());
        }
    });

    let on_scroll = move |_| {
        if let Some(el) = scroller_fx.get() {
            let next = stick_to_bottom(
                el.scroll_top() as f64,
                el.scroll_height() as f64,
                el.client_height() as f64,
            );
            if pinned.get_untracked() != next {
                pinned.set(next);
            }
        }
    };

    let jump = move |_| {
        pinned.set(true);
        if let Some(el) = scroller_fx.get() {
            el.set_scroll_top(el.scroll_height());
        }
    };

    let rename = handlers.rename;
    let open_file = handlers.open_file;
    let retry = handlers.retry;
    let fork = handlers.fork;
    let export_timeline = handlers.export_timeline;
    let export_markdown = handlers.export_markdown;
    let regenerate = handlers.regenerate;
    let send = handlers.send;

    let start_rename = move |_| {
        let current = transcript.with_untracked(|t| t.title.clone());
        draft.set(current);
        editing.set(true);
    };
    let cancel_rename = move |_| editing.set(false);
    let commit_rename = move || {
        editing.set(false);
        rename.run((draft.get(),));
    };
    let on_key = move |ev: web_sys::KeyboardEvent| {
        if ev.alt_key() || ev.ctrl_key() || ev.meta_key() {
            return;
        }
        if let Some(target) = ev.target() {
            if let Ok(el) = target.dyn_into::<web_sys::Element>() {
                let tag = el.tag_name().to_ascii_lowercase();
                if tag == "input" || tag == "textarea" {
                    return;
                }
            }
        }
        let key = ev.key();
        let ids = transcript.with(|t| t.messages.iter().map(|m| m.id).collect::<Vec<_>>());
        let move_cursor = |delta: isize| {
            if ids.is_empty() {
                return;
            }
            let next = match cursor.get_untracked() {
                Some(id) => {
                    let idx = ids.iter().position(|x| *x == id).unwrap_or(0);
                    let n = idx as isize + delta;
                    let n = n.clamp(0, ids.len() as isize - 1) as usize;
                    ids[n]
                }
                None => {
                    if delta < 0 {
                        *ids.last().unwrap()
                    } else {
                        ids[0]
                    }
                }
            };
            cursor.set(Some(next));
            pinned.set(false);
            scroll_message(next);
        };
        match key.as_str() {
            "j" | "ArrowDown" => {
                ev.prevent_default();
                move_cursor(1);
            }
            "k" | "ArrowUp" => {
                ev.prevent_default();
                move_cursor(-1);
            }
            "Home" => {
                ev.prevent_default();
                if let Some(id) = ids.first().copied() {
                    cursor.set(Some(id));
                    scroll_message(id);
                }
            }
            "End" => {
                ev.prevent_default();
                if let Some(id) = ids.last().copied() {
                    cursor.set(Some(id));
                    pinned.set(true);
                    scroll_message(id);
                }
            }
            "c" => {
                ev.prevent_default();
                if let Some(text) = current_copy(&transcript, cursor.get_untracked()) {
                    let _ = write_clipboard(&text);
                }
            }
            "t" => {
                ev.prevent_default();
                if let Some(text) = current_retry(&transcript, cursor.get_untracked()) {
                    retry.run((text,));
                }
            }
            "f" => {
                ev.prevent_default();
                if let Some(keep) = current_keep(&transcript, cursor.get_untracked()) {
                    fork.run((keep,));
                }
            }
            "r" => {
                ev.prevent_default();
                let current = transcript.with_untracked(|t| t.title.clone());
                draft.set(current);
                editing.set(true);
            }
            "g" => {
                ev.prevent_default();
                regenerate.run(());
            }
            "e" => {
                ev.prevent_default();
                export_timeline.run(());
            }
            _ => {}
        }
    };

    view! {
        <div class="sx-head">
            <div class="sx-title-row">
                <Show
                    when=move || editing.get()
                    fallback=move || {
                        let label = transcript.with(|t| display_title(&t.title));
                        view! { <h2 class="sx-title">{label}</h2> }
                    }
                >
                    <input
                        class="sx-rename"
                        aria-label="Session title"
                        prop:value=move || draft.get()
                        on:input=move |ev| draft.set(event_target_value(&ev))
                        on:keydown=move |ev: web_sys::KeyboardEvent| {
                            if ev.key() == "Enter" {
                                ev.prevent_default();
                                commit_rename();
                            } else if ev.key() == "Escape" {
                                ev.prevent_default();
                                editing.set(false);
                            }
                        }
                    />
                    <button type="button" class="sx-quiet" on:click=move |_| commit_rename()>"Save"</button>
                    <button type="button" class="sx-quiet" on:click=cancel_rename>"Cancel"</button>
                </Show>
            </div>
            <div class="sx-toolbar">
                <button type="button" class="sx-quiet" on:click=start_rename>"Rename"</button>
                <button type="button" class="sx-quiet" on:click=move |_| regenerate.run(())>"Regenerate"</button>
                <button type="button" class="sx-quiet" on:click=move |_| export_timeline.run(())>"Export"</button>
                <button type="button" class="sx-quiet" on:click=move |_| export_markdown.run(())>"Markdown"</button>
            </div>
            <p class="sx-keys">"j and k move. c copies. t retries. f forks after the turn. r renames. g regenerates. e exports."</p>
            <Show when=move || !note.get().is_empty()>
                <p class="sx-note" role="alert">{move || note.get()}</p>
            </Show>
        </div>
        <div class="sx-stage">
            <div
                class="transcript sx-transcript"
                id="transcript"
                tabindex="0"
                aria-label="Transcript"
                node_ref=scroller_fx
                on:scroll=on_scroll
                on:keydown=on_key
            >
                <Show when=move || loading.get()>
                    <div class="skeleton" aria-hidden="true">
                        <div class="skeleton-line"></div>
                        <div class="skeleton-line short"></div>
                    </div>
                </Show>
                <Show when=move || !loading.get() && transcript.with(|t| t.messages.is_empty()) && load_error.get().is_empty()>
                    <div class="sx-empty">
                        <strong>{move || empty_headline(tick.get())}</strong>
                        <p>"Start with a card. The line under each one is what gets sent."</p>
                        <div class="sx-cards">
                            {suggestions().iter().map(|s| {
                                let prompt = s.prompt.to_string();
                                let shown = prompt.clone();
                                let attr = prompt.clone();
                                let title = s.title;
                                let detail = s.detail;
                                view! {
                                    <button type="button" class="sx-card" title=detail data-prompt=attr on:click=move |_| send.run((prompt.clone(),))>
                                        <span class="sx-card-title">{title}</span>
                                        <span class="sx-card-detail">{shown}</span>
                                    </button>
                                }
                            }).collect_view()}
                        </div>
                    </div>
                </Show>
                {move || transcript.get().messages.into_iter().map(|msg| {
                    message_view(msg, cursor, open_file, retry, fork)
                }).collect_view()}
            </div>
            <Show when=move || show_minimap(transcript.with(|t| t.messages.len()))>
                <div class="sx-map" aria-label="Transcript map">
                    {move || transcript.get().messages.into_iter().map(|msg| {
                        let id = msg.id;
                        let label = format!("{} {}", msg.title, msg.body.chars().take(48).collect::<String>());
                        let class = format!("sx-tick sx-tick-{}", msg.role.class());
                        view! {
                            <button type="button" class=class aria-label=label on:click=move |_| {
                                cursor.set(Some(id));
                                pinned.set(false);
                                scroll_message(id);
                            }></button>
                        }
                    }).collect_view()}
                </div>
            </Show>
            <Show when=move || !pinned.get() && transcript.with(|t| !t.messages.is_empty())>
                <button type="button" class="sx-jump" on:click=jump>"Latest"</button>
            </Show>
        </div>
    }
}

fn display_title(title: &str) -> String {
    let title = title.trim();
    if title.is_empty() {
        "Untitled session".into()
    } else {
        title.to_string()
    }
}

fn current_copy(transcript: &RwSignal<Transcript>, id: Option<u32>) -> Option<String> {
    transcript.with_untracked(|t| {
        let msg = match id {
            Some(id) => t.messages.iter().find(|m| m.id == id),
            None => t.messages.last(),
        }?;
        let text = if msg.copy_text.is_empty() {
            msg.body.clone()
        } else {
            msg.copy_text.clone()
        };
        if text.is_empty() {
            None
        } else {
            Some(text)
        }
    })
}

fn current_retry(transcript: &RwSignal<Transcript>, id: Option<u32>) -> Option<String> {
    transcript.with_untracked(|t| {
        let msg = id
            .and_then(|id| t.messages.iter().find(|m| m.id == id))
            .or_else(|| t.messages.iter().rev().find(|m| !m.retry_text.is_empty()))?;
        if msg.retry_text.trim().is_empty() {
            None
        } else {
            Some(msg.retry_text.clone())
        }
    })
}

fn current_keep(transcript: &RwSignal<Transcript>, id: Option<u32>) -> Option<i32> {
    transcript.with_untracked(|t| {
        let msg = id.and_then(|id| t.messages.iter().find(|m| m.id == id))?;
        if msg.keep_events < 0 {
            None
        } else {
            Some(msg.keep_events)
        }
    })
}

fn scroll_message(id: u32) {
    if let Some(doc) = web_sys::window().and_then(|w| w.document()) {
        if let Some(el) = doc.get_element_by_id(&format!("sx-m-{id}")) {
            el.scroll_into_view();
        }
    }
}

fn write_clipboard(text: &str) -> Result<(), String> {
    let win = web_sys::window().ok_or_else(|| "no window".to_string())?;
    let _ = win.navigator().clipboard().write_text(text);
    Ok(())
}

pub fn dispatch_open_file(path: &str, line: u32) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let detail = js_sys::Object::new();
    let _ = js_sys::Reflect::set(
        &detail,
        &JsValue::from_str("path"),
        &JsValue::from_str(path),
    );
    let _ = js_sys::Reflect::set(
        &detail,
        &JsValue::from_str("line"),
        &JsValue::from_f64(line as f64),
    );
    let init = web_sys::CustomEventInit::new();
    init.set_detail(&detail);
    init.set_bubbles(true);
    if let Ok(ev) = web_sys::CustomEvent::new_with_event_init_dict("strike:open-file", &init) {
        let _ = win.dispatch_event(&ev);
    }
}

pub fn save_download(filename: &str, mime: &str, text: &str) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let Some(doc) = win.document() else {
        return;
    };
    let bytes = js_sys::Uint8Array::from(text.as_bytes());
    let parts = js_sys::Array::new();
    parts.push(&bytes);
    let bag = web_sys::BlobPropertyBag::new();
    bag.set_type(mime);
    let Ok(blob) = web_sys::Blob::new_with_u8_array_sequence_and_options(&parts, &bag) else {
        return;
    };
    let Ok(url) = web_sys::Url::create_object_url_with_blob(&blob) else {
        return;
    };
    let Ok(el) = doc.create_element("a") else {
        return;
    };
    let Ok(a) = el.dyn_into::<web_sys::HtmlAnchorElement>() else {
        return;
    };
    a.set_href(&url);
    a.set_download(filename);
    if let Some(body) = doc.body() {
        let _ = body.append_child(&a);
        a.click();
        a.remove();
    }
    let _ = web_sys::Url::revoke_object_url(&url);
}

fn message_view(
    msg: Message,
    cursor: RwSignal<Option<u32>>,
    open_file: Callback<(String, u32)>,
    retry: Callback<(String,)>,
    fork: Callback<(i32,)>,
) -> impl IntoView {
    let id = msg.id;
    let role = msg.role.class();
    let title = msg.title.clone();
    let badge = msg.badge.clone();
    let show_badge = !badge.is_empty();
    let body = msg.body.clone();
    let detail = msg.detail.clone();
    let diff = msg.diff.clone();
    let chips = msg.chips.clone();
    let copy_text = if msg.copy_text.is_empty() {
        body.clone()
    } else {
        msg.copy_text.clone()
    };
    let retry_text = msg.retry_text.clone();
    let keep = msg.keep_events;
    let prose = matches!(msg.role, Role::You | Role::Strike);
    let show_retry = prose && !retry_text.trim().is_empty();
    let show_fork = prose && keep >= 0;
    let class = move || {
        let on = cursor.get() == Some(id);
        format!("sx-msg sx-{role}{}", if on { " sx-on" } else { "" })
    };
    let copy_btn = copy_text.clone();
    let retry_btn = retry_text.clone();
    view! {
        <article id=format!("sx-m-{id}") class=class data-role=role>
            <header>
                <span class="sx-who">{title}</span>
                <Show when=move || show_badge>
                    <Badge kind=badge.clone() label=badge.clone() />
                </Show>
                {diff.clone().map(|d| view! {
                    <span class="sx-diff" aria-label=format!("+{} -{}", d.added, d.deleted)>
                        <span class="sx-add">{format!("+{}", d.added)}</span>
                        <span class="sx-del">{format!("-{}", d.deleted)}</span>
                    </span>
                })}
                <span class="sx-actions">
                    <button type="button" class="sx-quiet" on:click=move |_| { let _ = write_clipboard(&copy_btn); }>"Copy"</button>
                    <Show when=move || show_retry>
                        <button type="button" class="sx-quiet" on:click={
                            let text = retry_btn.clone();
                            move |_| retry.run((text.clone(),))
                        }>"Retry"</button>
                    </Show>
                    <Show when=move || show_fork>
                        <button type="button" class="sx-quiet" on:click=move |_| fork.run((keep,))>"Fork"</button>
                    </Show>
                </span>
            </header>
            {if prose {
                blocks_view(parse_markdown(&body), open_file).into_any()
            } else if body.is_empty() {
                view! { <span></span> }.into_any()
            } else {
                view! { <p class="sx-body">{body.clone()}</p> }.into_any()
            }}
            {if !prose && !chips.is_empty() {
                view! {
                    <div class="sx-chips">
                        {chips.into_iter().map(|chip| chip_view(chip, open_file)).collect_view()}
                    </div>
                }.into_any()
            } else {
                view! { <span></span> }.into_any()
            }}
            {if !detail.is_empty() {
                let summary = if diff.is_some() { "Diff" } else { "Output" };
                view! {
                    <details class="sx-fold">
                        <summary>{summary}</summary>
                        <pre>{detail}</pre>
                    </details>
                }.into_any()
            } else {
                view! { <span></span> }.into_any()
            }}
        </article>
    }
}

fn blocks_view(blocks: Vec<Block>, open_file: Callback<(String, u32)>) -> impl IntoView {
    blocks
        .into_iter()
        .map(|block| {
            match block {
                Block::Heading(level, inlines) => {
                    let inner = inlines_view(inlines, open_file);
                    match level {
                        1 => view! { <h3 class="sx-h">{inner}</h3> }.into_any(),
                        2 => view! { <h4 class="sx-h">{inner}</h4> }.into_any(),
                        _ => view! { <h5 class="sx-h">{inner}</h5> }.into_any(),
                    }
                }
                Block::Paragraph(inlines) => {
                    view! { <p class="sx-md">{inlines_view(inlines, open_file)}</p> }.into_any()
                }
                Block::Quote(inlines) => {
                    view! { <blockquote class="sx-quote">{inlines_view(inlines, open_file)}</blockquote> }.into_any()
                }
                Block::List { ordered, items } => {
                    let rows = items
                        .into_iter()
                        .map(|item| {
                            view! { <li>{inlines_view(item, open_file)}</li> }
                        })
                        .collect_view();
                    if ordered {
                        view! { <ol class="sx-list">{rows}</ol> }.into_any()
                    } else {
                        view! { <ul class="sx-list">{rows}</ul> }.into_any()
                    }
                }
                Block::Code { lang, text } => {
                    let copy = text.clone();
                    view! {
                        <div class="sx-fence">
                            <div class="sx-fence-bar">
                                <span class="sx-lang">{lang}</span>
                                <button type="button" class="sx-quiet" on:click=move |_| { let _ = write_clipboard(&copy); }>"Copy"</button>
                            </div>
                            <pre class="sx-code">{text}</pre>
                        </div>
                    }.into_any()
                }
            }
        })
        .collect_view()
}

fn inlines_view(inlines: Vec<Inline>, open_file: Callback<(String, u32)>) -> impl IntoView {
    inlines
        .into_iter()
        .map(|inline| match inline {
            Inline::Text(text) => view! { <span>{text}</span> }.into_any(),
            Inline::Code(text) => view! { <code class="sx-inline">{text}</code> }.into_any(),
            Inline::Strong(inner) => {
                view! { <strong>{inlines_view(inner, open_file)}</strong> }.into_any()
            }
            Inline::Em(inner) => view! { <em>{inlines_view(inner, open_file)}</em> }.into_any(),
            Inline::Link { href, label } => {
                view! { <a href=href target="_blank" rel="noreferrer">{label}</a> }.into_any()
            }
            Inline::Chip(chip) => chip_view(chip, open_file).into_any(),
        })
        .collect_view()
}

fn chip_view(chip: Chip, open_file: Callback<(String, u32)>) -> impl IntoView {
    let label = chip.label.clone();
    match chip.kind {
        ChipKind::File => {
            let path = chip.path.clone();
            let line = chip.line;
            let path_attr = path.clone();
            view! {
                <a
                    class="sx-chip sx-file"
                    href="#file"
                    data-path=path_attr
                    data-line=line.to_string()
                    on:click=move |ev: web_sys::MouseEvent| {
                        ev.prevent_default();
                        dispatch_open_file(&path, line);
                        open_file.run((path.clone(), line));
                    }
                >{label}</a>
            }
            .into_any()
        }
        ChipKind::PullRequest | ChipKind::Issue => {
            let class = if chip.kind == ChipKind::PullRequest {
                "sx-chip sx-pr"
            } else {
                "sx-chip sx-issue"
            };
            if chip.href.is_empty() {
                view! { <span class=class>{label}</span> }.into_any()
            } else {
                let href = chip.href.clone();
                view! { <a class=class href=href target="_blank" rel="noreferrer">{label}</a> }
                    .into_any()
            }
        }
    }
}
