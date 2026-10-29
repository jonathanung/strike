//! Transcript pane: markdown messages, tool cards, minimap, rename, export.
//! Composer send and queue stay in the shell. File tabs stay in the editor
//! stream; this pane only emits `path:line` links.

use std::cell::{Cell, RefCell};
use std::rc::Rc;

use leptos::callback::{Callable, Callback};
use leptos::prelude::*;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;
use wasm_bindgen::JsValue;

use crate::shell::Choice;
use crate::status::{StatusKind, StatusMark, StatusSlot};

thread_local! {
    static MESSAGES: RefCell<Vec<Choice>> = RefCell::new(Vec::new());
}

pub fn message_choices() -> Vec<Choice> {
    MESSAGES.with(|slot| slot.borrow().clone())
}
use crate::transcript::{
    anchored_scroll_top, empty_headline, parse_markdown, stick_to_bottom, Block, Chip, ChipKind,
    Inline, Message, Role, Transcript,
};

#[derive(Clone, Copy)]
pub struct TranscriptHandlers {
    pub send: Callback<(String,)>,
    pub rename: Callback<(String, String)>,
    pub regenerate: Callback<()>,
    pub export_timeline: Callback<()>,
    pub export_markdown: Callback<()>,
    pub retry: Callback<(String,)>,
    pub fork: Callback<(i32,)>,
    pub open_file: Callback<(String, u32)>,
}

/// Find-in-transcript state republished by the shell (#1342, unioned
/// with this pane in the #1310 re-land): matches over the transcript's
/// ChatLine mirror — index-aligned with `Transcript::messages`, since
/// the mirror is `Transcript::chat_lines()` — plus the current match.
/// Empty when the find bar is closed or the query is blank.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct FindView {
    pub hits: Vec<crate::transcript_find::TMatch>,
    pub current: Option<crate::transcript_find::TMatch>,
}

#[component]
pub fn TranscriptPane(
    transcript: RwSignal<Transcript>,
    session: RwSignal<String>,
    loading: RwSignal<bool>,
    load_error: RwSignal<String>,
    tick: RwSignal<u32>,
    note: RwSignal<String>,
    handlers: TranscriptHandlers,
    find: RwSignal<FindView>,
) -> impl IntoView {
    let editing = RwSignal::new(false);
    let draft = RwSignal::new(String::new());
    let pinned = RwSignal::new(true);
    let cursor = RwSignal::new(None::<u32>);
    let cursor_session = RwSignal::new(String::new());
    let rename_for = RwSignal::new(String::new());
    let seen_session = RwSignal::new(None::<String>);
    Effect::new(move |_| {
        let next = session.get();
        let prev = seen_session.get_untracked();
        if prev.as_deref() == Some(next.as_str()) {
            return;
        }
        let switched = prev.is_some();
        seen_session.set(Some(next));
        if !switched {
            return;
        }
        editing.set(false);
        draft.set(String::new());
        cursor.set(None);
        cursor_session.set(String::new());
        rename_for.set(String::new());
    });
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

    // Geometry at the last scroll position the reader chose:
    // (scroll_top, scroll_height, client_height). The phone keyboard
    // resizes the shell around the transcript; `settle_scroll` compares
    // against this so the resize keeps a pinned reader at the bottom and
    // never yanks a reader who scrolled up. Returns true when it handled
    // a resize, in which case it has already restored the anchor and the
    // follow state.
    let geom = Rc::new(Cell::new((0.0f64, 0.0f64, 0.0f64)));
    let settle_scroll: Rc<dyn Fn(&web_sys::Element) -> bool> = {
        let geom = Rc::clone(&geom);
        Rc::new(move |el: &web_sys::Element| -> bool {
            let top = el.scroll_top() as f64;
            let height = el.scroll_height() as f64;
            let client = el.client_height() as f64;
            let (prev_top, prev_height, prev_client) = geom.get();
            if prev_client <= 0.0 || (client - prev_client).abs() <= 0.5 {
                geom.set((top, height, client));
                return false;
            }
            let next = anchored_scroll_top(prev_top, prev_height, prev_client, client, height);
            if (next - top).abs() > 0.5 {
                el.set_scroll_top(next as i32);
            }
            pinned.set(stick_to_bottom(prev_top, prev_height, prev_client));
            geom.set((next, height, client));
            true
        })
    };

    let on_scroll = {
        let settle_scroll = Rc::clone(&settle_scroll);
        move |_| {
            if let Some(el) = scroller_fx.get() {
                if settle_scroll(&el) {
                    return;
                }
                let next = stick_to_bottom(
                    el.scroll_top() as f64,
                    el.scroll_height() as f64,
                    el.client_height() as f64,
                );
                if pinned.get_untracked() != next {
                    pinned.set(next);
                }
            }
        }
    };

    let jump = move |_| {
        pinned.set(true);
        if let Some(el) = scroller_fx.get() {
            el.set_scroll_top(el.scroll_height());
        }
    };

    // The scroller's own box is what the keyboard actually resizes, so
    // watch it directly; the scroll handler above covers engines that
    // clamp the offset before the observer fires.
    struct ScrollWatch {
        obs: web_sys::ResizeObserver,
        _cb: Closure<dyn FnMut(js_sys::Array, web_sys::ResizeObserver)>,
    }
    let ro_slot = StoredValue::new_local(None::<ScrollWatch>);
    on_cleanup(move || {
        ro_slot.update_value(|slot| {
            if let Some(watch) = slot.take() {
                watch.obs.disconnect();
            }
        });
    });
    scroller.on_load({
        let settle_scroll = Rc::clone(&settle_scroll);
        move |el| {
            let settle = Rc::clone(&settle_scroll);
            let cb = Closure::wrap(
                Box::new(move |_: js_sys::Array, _: web_sys::ResizeObserver| {
                    if let Some(el) = scroller_fx.get() {
                        settle(&el);
                    }
                }) as Box<dyn FnMut(js_sys::Array, web_sys::ResizeObserver)>,
            );
            let Ok(obs) = web_sys::ResizeObserver::new(cb.as_ref().unchecked_ref()) else {
                return;
            };
            let el: &web_sys::Element = &el;
            obs.observe(el);
            ro_slot.update_value(|slot| {
                if let Some(old) = slot.replace(ScrollWatch { obs, _cb: cb }) {
                    old.obs.disconnect();
                }
            });
        }
    });

    let rename = handlers.rename;
    let open_file = handlers.open_file;
    let retry = handlers.retry;
    let fork = handlers.fork;
    let export_timeline = handlers.export_timeline;
    let _ = (handlers.export_markdown, handlers.send);
    let regenerate = handlers.regenerate;

    let cancel_rename = move |_| editing.set(false);
    let commit_rename = move || {
        let started = rename_for.get_untracked();
        let active = session.get_untracked();
        if !rename_applies(&started, &active) {
            editing.set(false);
            note.set("That rename was for another session.".into());
            return;
        }
        editing.set(false);
        rename.run((started, draft.get()));
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
            cursor_session.set(session.get_untracked());
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
                    cursor_session.set(session.get_untracked());
                    scroll_message(id);
                }
            }
            "End" => {
                ev.prevent_default();
                if let Some(id) = ids.last().copied() {
                    cursor.set(Some(id));
                    cursor_session.set(session.get_untracked());
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
                let bind = cursor.get_untracked().map(|id| CursorBind {
                    session: cursor_session.get_untracked(),
                    id,
                });
                let msgs = cursor_messages(&transcript);
                if let Some(text) = resolve_retry(&session.get_untracked(), bind.as_ref(), &msgs) {
                    retry.run((text,));
                }
            }
            "f" => {
                ev.prevent_default();
                let bind = cursor.get_untracked().map(|id| CursorBind {
                    session: cursor_session.get_untracked(),
                    id,
                });
                let msgs = cursor_messages(&transcript);
                if let Some(keep) = resolve_fork(&session.get_untracked(), bind.as_ref(), &msgs) {
                    fork.run((keep,));
                }
            }
            "r" => {
                ev.prevent_default();
                let current = transcript.with_untracked(|t| t.title.clone());
                rename_for.set(session.get_untracked());
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

    Effect::new(move |_| {
        let rows = transcript.with(|t| {
            t.messages
                .iter()
                .map(|msg| {
                    let preview: String = msg.body.chars().take(72).collect();
                    Choice {
                        id: msg.id.to_string(),
                        label: if msg.title.is_empty() {
                            preview.clone()
                        } else {
                            msg.title.clone()
                        },
                        detail: preview,
                    }
                })
                .collect()
        });
        MESSAGES.with(|slot| *slot.borrow_mut() = rows);
    });
    crate::shell::bind(
        "transcript.regenerate",
        Rc::new(move |_| {
            regenerate.run(());
            Ok(())
        }),
    );
    crate::shell::bind(
        "transcript.retry",
        Rc::new(move |_| {
            let active = session.get_untracked();
            let bound = cursor.get_untracked().map(|id| CursorBind {
                session: cursor_session.get_untracked(),
                id,
            });
            let text = palette_retry(&active, bound.as_ref(), &cursor_messages(&transcript))
                .ok_or_else(|| "No message to retry.".to_string())?;
            retry.run((text,));
            Ok(())
        }),
    );
    crate::shell::bind(
        "transcript.fork-message",
        Rc::new(move |_| {
            let active = session.get_untracked();
            let bound = cursor.get_untracked().map(|id| CursorBind {
                session: cursor_session.get_untracked(),
                id,
            });
            let keep = palette_fork(&active, bound.as_ref(), &cursor_messages(&transcript))
                .ok_or_else(|| "No message to fork from.".to_string())?;
            fork.run((keep,));
            Ok(())
        }),
    );
    crate::shell::bind(
        "transcript.jump",
        Rc::new(move |args| {
            let id: u32 = args
                .trim()
                .parse()
                .map_err(|_| "Pick a message.".to_string())?;
            cursor.set(Some(id));
            cursor_session.set(session.get_untracked());
            pinned.set(false);
            scroll_message(id);
            Ok(())
        }),
    );
    on_cleanup(|| {
        for id in [
            "transcript.regenerate",
            "transcript.retry",
            "transcript.fork-message",
            "transcript.jump",
        ] {
            crate::shell::unbind(id);
        }
        MESSAGES.with(|slot| slot.borrow_mut().clear());
    });

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
            <Show when=move || !note.get().is_empty()>
                <p class="sx-note" role="alert">{move || note.get()}</p>
            </Show>
        </div>
        <div class="sx-stage">
            <div
                class="transcript sx-transcript"
                id="tf-transcript"
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
                        <p>"Send a message to start."</p>
                        <button
                            type="button"
                            class="secondary"
                            data-add-provider="true"
                            on:click=move |_| {
                                let _ = crate::shell::set_view("providers");
                            }
                        >
                            "Add provider"
                        </button>
                    </div>
                </Show>
                {move || {
                    let fv = find.get();
                    let snap = transcript.get();
                    let msgs = snap.messages.clone();
                    // A day separator precedes the first timed message of
                    // each local day (#1349). Untimed messages never open
                    // a new day.
                    let mut prev_day: Option<(u32, u32, u32)> = None;
                    let seps: Vec<Option<String>> = msgs
                        .iter()
                        .map(|msg| {
                            if msg.at_ms <= 0.0 {
                                return None;
                            }
                            let day = line_day(msg.at_ms);
                            let label = if prev_day == Some(day) {
                                None
                            } else {
                                Some(line_day_label(msg.at_ms))
                            };
                            prev_day = Some(day);
                            label
                        })
                        .collect();
                    msgs.into_iter()
                        .zip(seps)
                        .enumerate()
                        .map(|(idx, (msg, sep))| {
                            let msg_hits: Vec<crate::transcript_find::TMatch> = fv
                                .hits
                                .iter()
                                .filter(|h| h.line == idx)
                                .copied()
                                .collect();
                            let msg_current = fv.current.filter(|h| h.line == idx);
                            let msg_elapsed = snap.elapsed_for(msg.id).map(str::to_owned);
                            view! {
                                {sep.map(|label| view! { <div class="day-sep" role="separator">{label}</div> })}
                                {message_view(msg, idx, cursor, open_file, retry, fork, msg_hits, msg_current, msg_elapsed)}
                            }
                        })
                        .collect_view()
                }}
            </div>
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

struct CursorMsg {
    id: u32,
    retry_text: String,
    keep_events: i32,
}

struct CursorBind {
    session: String,
    id: u32,
}

fn cursor_messages(transcript: &RwSignal<Transcript>) -> Vec<CursorMsg> {
    transcript.with_untracked(|t| {
        t.messages
            .iter()
            .map(|m| CursorMsg {
                id: m.id,
                retry_text: m.retry_text.clone(),
                keep_events: m.keep_events,
            })
            .collect()
    })
}

/// Retry only the message the cursor was set on, in that session.
/// A missing id does not fall through to the newest prompt.
fn resolve_retry(
    active: &str,
    cursor: Option<&CursorBind>,
    messages: &[CursorMsg],
) -> Option<String> {
    let cursor = cursor?;
    if cursor.session != active {
        return None;
    }
    let msg = messages.iter().find(|m| m.id == cursor.id)?;
    let text = msg.retry_text.trim();
    if text.is_empty() {
        None
    } else {
        Some(text.to_string())
    }
}

fn resolve_fork(active: &str, cursor: Option<&CursorBind>, messages: &[CursorMsg]) -> Option<i32> {
    let cursor = cursor?;
    if cursor.session != active {
        return None;
    }
    let msg = messages.iter().find(|m| m.id == cursor.id)?;
    if msg.keep_events < 0 {
        None
    } else {
        Some(msg.keep_events)
    }
}

fn rename_applies(started: &str, active: &str) -> bool {
    !started.is_empty() && started == active
}

/// Palette retry: the cursor message when it belongs to this session,
/// otherwise the latest retryable turn in the transcript on screen.
fn palette_retry(
    active: &str,
    cursor: Option<&CursorBind>,
    messages: &[CursorMsg],
) -> Option<String> {
    if cursor.is_some_and(|c| c.session == active) {
        return resolve_retry(active, cursor, messages);
    }
    messages.iter().rev().find_map(|m| {
        let text = m.retry_text.trim();
        if text.is_empty() {
            None
        } else {
            Some(text.to_string())
        }
    })
}

fn palette_fork(active: &str, cursor: Option<&CursorBind>, messages: &[CursorMsg]) -> Option<i32> {
    if cursor.is_some_and(|c| c.session == active) {
        return resolve_fork(active, cursor, messages);
    }
    messages.iter().rev().find_map(|m| {
        if m.keep_events < 0 {
            None
        } else {
            Some(m.keep_events)
        }
    })
}

fn scroll_message(id: u32) {
    if let Some(doc) = web_sys::window().and_then(|w| w.document()) {
        // Articles carry their find-in-transcript id (`tf-line-<idx>`) as
        // the element id, so the pane's own cursor scroll keys off the
        // stable message id in `data-msg` instead.
        if let Ok(Some(el)) = doc.query_selector(&format!("[data-msg=\"{id}\"]")) {
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
    idx: usize,
    cursor: RwSignal<Option<u32>>,
    open_file: Callback<(String, u32)>,
    _retry: Callback<(String,)>,
    _fork: Callback<(i32,)>,
    find_hits: Vec<crate::transcript_find::TMatch>,
    find_current: Option<crate::transcript_find::TMatch>,
    turn_elapsed: Option<String>,
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
    let stamp = line_stamp(msg.at_ms);
    let stamp_full = line_full_time(msg.at_ms);
    let has_stamp = !stamp.is_empty();
    let prose = matches!(msg.role, Role::You | Role::Strike);
    // Find-in-transcript (#1342): this message's match ranges, split by
    // field. When a field has ranges it renders through MarkedText —
    // the same trade #1398 made on the old bubble path: markdown / link
    // rendering pauses for that message while its matches are marked.
    let body_ranges: Vec<(usize, usize)> = find_hits
        .iter()
        .filter(|h| h.field == crate::transcript_find::Field::Body)
        .map(|h| (h.start, h.end))
        .collect();
    let title_ranges: Vec<(usize, usize)> = find_hits
        .iter()
        .filter(|h| h.field == crate::transcript_find::Field::Title)
        .map(|h| (h.start, h.end))
        .collect();
    let cur_body = find_current
        .filter(|h| h.field == crate::transcript_find::Field::Body)
        .map(|h| (h.start, h.end));
    let cur_title = find_current
        .filter(|h| h.field == crate::transcript_find::Field::Title)
        .map(|h| (h.start, h.end));
    let find_body = !body_ranges.is_empty();
    let find_title = !title_ranges.is_empty();
    let _ = (prose, retry_text, keep);
    let class = move || {
        let on = cursor.get() == Some(id);
        format!("sx-msg sx-{role}{}", if on { " sx-on" } else { "" })
    };
    let copy_btn = copy_text.clone();
    view! {
        <article id=format!("tf-line-{idx}") data-msg=id class=class data-role=role>
            <header>
                {if find_title {
                    view! { <span class="sx-who"><crate::transcript_find::MarkedText text=title.clone() kind="inline" ranges=title_ranges current=cur_title /></span> }.into_any()
                } else {
                    view! { <span class="sx-who">{title.clone()}</span> }.into_any()
                }}
                <Show when=move || show_badge>
                    {match StatusKind::parse_known(&badge) {
                        Some(kind) => view! { <StatusMark kind=kind place=StatusSlot::Badge /> }.into_any(),
                        None => {
                            let class = format!("sx-status sx-status-{badge}");
                            view! { <span class=class data-status=badge.clone()>{badge.clone()}</span> }.into_any()
                        }
                    }}
                </Show>
                {has_stamp.then(|| view! { <span class="stamp" title=stamp_full.clone()>{stamp.clone()}</span> })}
                {turn_elapsed.clone().map(|label| view! { <span class="turn-elapsed" title="Turn elapsed time">{label}</span> })}
                {diff.clone().map(|d| view! {
                    <span class="sx-diff" aria-label=format!("+{} -{}", d.added, d.deleted)>
                        <span class="sx-add">{format!("+{}", d.added)}</span>
                        <span class="sx-del">{format!("-{}", d.deleted)}</span>
                    </span>
                })}
                <span class="sx-actions">
                    <button type="button" class="sx-quiet" on:click=move |_| { let _ = write_clipboard(&copy_btn); }>"Copy"</button>
                </span>
            </header>
            {if find_body {
                view! { <crate::transcript_find::MarkedText text=body.clone() kind="prose" ranges=body_ranges current=cur_body /> }.into_any()
            } else if prose && crate::editor::has_markdown_table(&body) {
                // Table-bearing prose renders through the shared markdown
                // view (#1344), as on the shell's bubble path.
                view! { <crate::editor::MarkdownView text=body.clone() /> }.into_any()
            } else if prose {
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
                    data-path=path_attr.clone()
                    data-line=line.to_string()
                    data-ev-path=path_attr
                    data-ev-line=line.to_string()
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

/// Header stamp for a transcript message (#1349): relative while the
/// message is under a day old (the session list's `fmt_ago` vocabulary),
/// local clock time after that. Empty when the time is unknown.
fn line_stamp(at_ms: f64) -> String {
    if at_ms <= 0.0 {
        return String::new();
    }
    let now = crate::api::now_ms();
    if now - at_ms < 86_400_000.0 {
        let rel = crate::status::fmt_ago((at_ms / 1000.0) as i64, (now / 1000.0) as i64);
        if !rel.is_empty() {
            return rel;
        }
    }
    let d = js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(at_ms));
    format!("{:02}:{:02}", d.get_hours() as u32, d.get_minutes() as u32)
}

/// Full local timestamp for the stamp's tooltip. Empty when unknown.
fn line_full_time(at_ms: f64) -> String {
    if at_ms <= 0.0 {
        return String::new();
    }
    js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(at_ms))
        .to_string()
        .as_string()
        .unwrap_or_default()
}

/// Local calendar day of a message, as (year, month 0-based, day of month).
fn line_day(at_ms: f64) -> (u32, u32, u32) {
    let d = js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(at_ms));
    (
        d.get_full_year() as u32,
        d.get_month() as u32,
        d.get_date() as u32,
    )
}

/// Separator label shown when the local day changes between messages.
fn line_day_label(at_ms: f64) -> String {
    const DAYS: [&str; 7] = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
    const MONTHS: [&str; 12] = [
        "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
    ];
    let d = js_sys::Date::new(&wasm_bindgen::JsValue::from_f64(at_ms));
    format!(
        "{} {} {}, {}",
        DAYS[(d.get_day() as usize) % 7],
        MONTHS[(d.get_month() as usize) % 12],
        d.get_date() as u32,
        d.get_full_year() as u32
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(id: u32, retry: &str, keep: i32) -> CursorMsg {
        CursorMsg {
            id,
            retry_text: retry.into(),
            keep_events: keep,
        }
    }

    #[test]
    fn rename_and_keys_stay_on_the_session_they_started_on() {
        assert!(rename_applies("sess-a", "sess-a"));
        assert!(!rename_applies("sess-a", "sess-b"));
        assert!(!rename_applies("", "sess-b"));
        let b = vec![row(1, "prompt b", 9)];
        let from_a = CursorBind {
            session: "sess-a".into(),
            id: 1,
        };
        assert!(resolve_retry("sess-b", Some(&from_a), &b).is_none());
        assert!(resolve_fork("sess-b", Some(&from_a), &b).is_none());
        let missing = CursorBind {
            session: "sess-a".into(),
            id: 9,
        };
        let a = vec![row(2, "newest prompt", 4)];
        assert!(resolve_retry("sess-a", Some(&missing), &a).is_none());
        assert!(resolve_retry("sess-a", None, &a).is_none());
        let hit = CursorBind {
            session: "sess-a".into(),
            id: 2,
        };
        assert_eq!(
            resolve_retry("sess-a", Some(&hit), &a).as_deref(),
            Some("newest prompt")
        );
        assert_eq!(resolve_fork("sess-a", Some(&hit), &a), Some(4));
    }

    #[test]
    fn palette_message_commands_prefer_this_session_cursor() {
        let a = vec![row(1, "older", 1), row(2, "newest", 4)];
        assert_eq!(palette_retry("sess-a", None, &a).as_deref(), Some("newest"));
        assert_eq!(palette_fork("sess-a", None, &a), Some(4));
        let stale = CursorBind {
            session: "sess-b".into(),
            id: 1,
        };
        assert_eq!(
            palette_retry("sess-a", Some(&stale), &a).as_deref(),
            Some("newest")
        );
        let hit = CursorBind {
            session: "sess-a".into(),
            id: 1,
        };
        assert_eq!(
            palette_retry("sess-a", Some(&hit), &a).as_deref(),
            Some("older")
        );
        assert_eq!(palette_fork("sess-a", Some(&hit), &a), Some(1));
        let missing = CursorBind {
            session: "sess-a".into(),
            id: 9,
        };
        assert!(palette_retry("sess-a", Some(&missing), &a).is_none());
        assert!(palette_fork("sess-a", Some(&missing), &a).is_none());
    }
}
