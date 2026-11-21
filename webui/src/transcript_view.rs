//! Transcript pane: markdown messages, tool cards, minimap, rename, export.
//! Composer send and queue stay in the shell. File tabs stay in the editor
//! stream; this pane only emits `path:line` links.

use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::rc::Rc;

use leptos::callback::{Callable, Callback};
use leptos::prelude::*;
use leptos::task::spawn_local;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;
use wasm_bindgen::JsValue;

use crate::shell::Choice;
use crate::status::{StatusKind, StatusMark, StatusSlot};
use transcript_window as tw;

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
    /// Attach message `id` to the composer as a quote (#1362).
    pub quote: Callback<(u32,)>,
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
    // Controlled fold open-state (#1441): message id -> open override.
    // Absent = collapsed (today's default). Bulk actions are a snapshot:
    // they only write entries for folds present at click time, so a fold
    // streamed in afterwards has no entry and starts collapsed. Cleared
    // on session switch below (bulk state does not survive sessions).
    let folds = FoldState {
        map: RwSignal::new(HashMap::<u32, bool>::new()),
        dismissed: RwSignal::new(None),
    };
    let draft = RwSignal::new(String::new());
    let pinned = RwSignal::new(true);
    let cursor = RwSignal::new(None::<u32>);
    let cursor_session = RwSignal::new(String::new());
    let rename_for = RwSignal::new(String::new());
    let seen_session = RwSignal::new(None::<String>);
    let win_start = RwSignal::new(tw::TAIL);
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
        win_start.set(tw::TAIL);
        folds.map.set(HashMap::new());
        folds.dismissed.update_untracked(|d| *d = None);
    });
    let scroller = NodeRef::<leptos::html::Div>::new();
    let scroller_fx = scroller;

    // Windowed rendering (#1515): only `messages[win_start..]` is
    // mounted. A following reader's window slides forward in steps so
    // the DOM stays bounded; a reader scrolled up keeps every row above
    // them. Row indices stay absolute (find hits, `tf-line-<idx>`).
    Effect::new(move |_| {
        let len = transcript.with(|t| t.messages.len());
        let stick = pinned.get();
        // Tracked so a reset to TAIL (session switch) resolves even when
        // the new list happens to have the same length.
        let cur = win_start.get();
        let next = tw::follow(cur, len, stick);
        if next != cur {
            win_start.set(next);
        }
    });
    // Mounts row `idx` if it sits above the window. Callers scroll to
    // it after render (`scroll_message` / find's `scroll_to_line_soon`
    // both defer a task, after the list has re-rendered).
    let reveal_row = move |idx: usize| {
        let len = transcript.with_untracked(|t| t.messages.len());
        let base = tw::follow(win_start.get_untracked(), len, false);
        let next = tw::reveal(base, idx);
        if next != base {
            pinned.set(false);
            win_start.set(next);
        }
    };
    // Find-in-transcript: the current match's row must be mounted
    // before the shell scrolls to its `tf-line-<idx>` article. The
    // window (and follow state) from before find opened is put back when
    // it closes, because the shell then restores the scroll offset it
    // saved at open, which only means the same place over the same
    // mounted rows.
    let find_base = StoredValue::new(None::<(usize, bool)>);
    Effect::new(move |_| {
        let (active, line) =
            find.with(|f| (!f.hits.is_empty() || f.current.is_some(), f.current.map(|h| h.line)));
        if active {
            if find_base.get_value().is_none() {
                let len = transcript.with_untracked(|t| t.messages.len());
                let start = tw::follow(win_start.get_untracked(), len, false);
                find_base.set_value(Some((start, pinned.get_untracked())));
            }
            if let Some(line) = line {
                reveal_row(line);
            }
        } else if let Some((start, was_pinned)) = find_base.get_value() {
            find_base.set_value(None);
            pinned.set(was_pinned);
            win_start.set(start);
        }
    });
    // Mounts one page of earlier rows, keeping the first row that was
    // already mounted at the same place on screen.
    let earlier_busy = StoredValue::new(false);
    let show_earlier = move || {
        if earlier_busy.get_value() {
            return;
        }
        let len = transcript.with_untracked(|t| t.messages.len());
        let cur = tw::follow(win_start.get_untracked(), len, false);
        if cur == 0 {
            return;
        }
        let anchor_y = |idx: usize| -> Option<f64> {
            let doc = web_sys::window()?.document()?;
            let el = doc.get_element_by_id(&format!("tf-line-{idx}"))?;
            Some(el.get_bounding_client_rect().top())
        };
        let before = anchor_y(cur);
        earlier_busy.set_value(true);
        win_start.set(tw::earlier(cur));
        after_render(move || {
            if let (Some(before), Some(after), Some(el)) =
                (before, anchor_y(cur), scroller_fx.get_untracked())
            {
                let top = el.scroll_top() as f64;
                let next = tw::anchor_scroll_top(top, before, after);
                if (next - top).abs() > 0.5 {
                    el.set_scroll_top(next as i32);
                }
            }
            earlier_busy.set_value(false);
        });
    };

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
                let top = el.scroll_top() as f64;
                if !next && tw::wants_earlier(win_start.get_untracked(), top) {
                    show_earlier();
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

    let expand_all_folds = move |_| {
        let ids = transcript.with_untracked(|t| fold_ids(&t.messages));
        folds.map.update(|map| apply_bulk_fold(map, &ids, true));
    };
    let collapse_all_folds = move |_| {
        let ids = transcript.with_untracked(|t| fold_ids(&t.messages));
        folds.map.update(|map| apply_bulk_fold(map, &ids, false));
    };

    let rename = handlers.rename;
    let open_file = handlers.open_file;
    let retry = handlers.retry;
    let fork = handlers.fork;
    let quote = handlers.quote;
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
                    reveal_row(n);
                    ids[n]
                }
                None => {
                    if delta < 0 {
                        *ids.last().unwrap()
                    } else {
                        reveal_row(0);
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
                    reveal_row(0);
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
                    spawn_local(async move {
                        let ok = crate::clipboard::copy_text(&text).await.is_ok();
                        note.set(crate::clipboard::transcript_copy_feedback(ok).to_string());
                    });
                }
            }
            "q" => {
                // #1362: quote the cursor line into the composer.
                if let Some(id) = cursor.get_untracked() {
                    if cursor_session.get_untracked() == session.get_untracked() {
                        ev.prevent_default();
                        quote.run((id,));
                    }
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
            let idx = transcript.with_untracked(|t| t.messages.iter().position(|m| m.id == id));
            if let Some(idx) = idx {
                reveal_row(idx);
            }
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

    let hidden_rows = move || {
        let len = transcript.with(|t| t.messages.len());
        tw::mounted(win_start.get(), len).start
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
            <Show when=move || !note.get().is_empty()>
                <p class="sx-note" role="alert">{move || note.get()}</p>
            </Show>
            {move || {
                // Count what is visibly open among the folds that exist
                // now: recorded state plus the fold find forces open
                // (#1576), unless the user dismissed that one.
                let ids = transcript.with(|t| fold_ids(&t.messages));
                let count = ids.len();
                let forced = find.with(|fv| {
                    fv.current
                        .filter(|m| m.field == crate::transcript_find::Field::Detail)
                        .and_then(|m| transcript.with(|t| t.messages.get(m.line).map(|x| (x.id, m))))
                });
                let dismissed = folds.dismissed.get_untracked();
                let open_count = folds.map.with(|map| {
                    ids.iter()
                        .filter(|id| {
                            let f = forced.is_some_and(|(fid, _)| fid == **id);
                            resolve_fold_open(fold_is_open(map, **id), f, f && dismissed == forced)
                        })
                        .count()
                });
                if count == 0 {
                    view! { <span></span> }.into_any()
                } else {
                    view! {
                        <div class="sx-toolbar sx-fold-toolbar" data-fold-all="true" data-fold-count=count.to_string() data-fold-open-count=open_count.to_string()>
                            <button type="button" class="sx-quiet" data-fold-expand-all="true" on:click=expand_all_folds>"Expand all"</button>
                            <button type="button" class="sx-quiet" data-fold-collapse-all="true" on:click=collapse_all_folds>"Collapse all"</button>
                            <span class="sx-fold-count">{format!("{open_count}/{count} folds open")}</span>
                        </div>
                    }.into_any()
                }
            }}
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
                <Show when=move || hidden_rows() != 0>
                    <button type="button" class="sx-quiet sx-earlier" on:click=move |_| show_earlier()>
                        {move || {
                            let n = hidden_rows();
                            let page = n.min(tw::PAGE);
                            if page == n {
                                format!("Show {n} earlier")
                            } else {
                                format!("Show {page} earlier ({n} hidden)")
                            }
                        }}
                    </button>
                </Show>
                // Keyed by absolute index: a streaming delta re-renders
                // only the row whose data changed (each row memoizes its
                // own slice), not the whole list.
                <For
                    each=move || {
                        let len = transcript.with(|t| t.messages.len());
                        tw::mounted(win_start.get(), len)
                    }
                    key=|idx: &usize| *idx
                    children=move |idx: usize| {
                        let row = Memo::new(move |_| {
                            transcript.with(|t| find.with(|fv| row_at(t, fv, idx)))
                        });
                        move || {
                            row.get().map(|r| {
                                // Tracked here, not in the row memo: a fold
                                // `notify()` re-renders every mounted row so
                                // each `<details open>` re-syncs with the
                                // fold state (see `FoldState`).
                                let fold_open = folds.map.with(|m| fold_is_open(m, r.msg.id));
                                view! {
                                    {r.sep.map(|label| view! { <div class="day-sep" role="separator">{label}</div> })}
                                    {message_view(r.msg, idx, cursor, open_file, retry, fork, quote, r.quotable, r.quote, r.hits, r.current, r.elapsed, r.usage, r.verify, r.tail, fold_open, folds)}
                                }
                            })
                        }
                    }
                />
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

/// Everything one transcript row renders from. Compared per row on
/// each transcript change so an unchanged row keeps its DOM.
#[derive(Clone, PartialEq)]
struct RowData {
    msg: Message,
    sep: Option<String>,
    hits: Vec<crate::transcript_find::TMatch>,
    current: Option<crate::transcript_find::TMatch>,
    elapsed: Option<String>,
    usage: Option<(String, String)>,
    /// The line can be quoted now (settled, has text) (#1362).
    quotable: bool,
    /// Quote this message carries and the loaded line it resolves to.
    quote: Option<(quote_message::Quote, Option<u32>)>,
    verify: Option<crate::turn_verify::TurnVerify>,
    tail: Option<crate::tool_tail::ToolTail>,
}

fn row_at(t: &Transcript, fv: &FindView, idx: usize) -> Option<RowData> {
    let msg = t.messages.get(idx)?;
    // A day separator precedes the first timed message of each local
    // day (#1349). Untimed messages never open a new day; the previous
    // day comes from the nearest earlier timed message, mounted or not.
    let sep = if msg.at_ms > 0.0 {
        let day = line_day(msg.at_ms);
        let prev = t.messages[..idx]
            .iter()
            .rev()
            .find(|m| m.at_ms > 0.0)
            .map(|m| line_day(m.at_ms));
        (prev != Some(day)).then(|| line_day_label(msg.at_ms))
    } else {
        None
    };
    Some(RowData {
        msg: msg.clone(),
        sep,
        hits: fv.hits.iter().filter(|h| h.line == idx).copied().collect(),
        current: fv.current.filter(|h| h.line == idx),
        elapsed: t.elapsed_for(msg.id).map(str::to_owned),
        usage: t
            .usage_for(msg.id)
            .and_then(|u| u.label().map(|label| (label, u.detail()))),
        quotable: t.quotable_at(idx),
        quote: t
            .quote_for(msg.id)
            .map(|q| (q.clone(), t.resolve_quote(q))),
        verify: t.verify_for(msg.id).cloned(),
        tail: t.tail_for(msg.id).cloned(),
    })
}

/// Runs `f` in a task after the current one, by which point pending
/// reactive updates (the windowed list) have rendered.
fn after_render(f: impl FnOnce() + 'static) {
    let Some(w) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(f);
    let _ = w.set_timeout_with_callback_and_timeout_and_arguments_0(
        cb.as_ref().unchecked_ref(),
        0,
    );
    cb.forget();
}

/// Scrolls to message `id` after render, so a row just mounted by
/// `reveal_row` exists when the lookup runs.
/// Quote card above a user message that quoted a line (#1362). Click
/// (or Enter) jumps back to the original through the same path as the
/// palette's jump, which mounts a windowed-out row and puts the cursor
/// highlight on it. When the original is not in the loaded transcript
/// the card keeps the excerpt and says so instead of jumping nowhere.
fn quote_card_view(q: &quote_message::Quote, target: Option<u32>) -> impl IntoView {
    let label = quote_message::label(&q.role, &q.text);
    match target {
        Some(id) => view! {
            <button
                type="button"
                class="sx-quote-card"
                data-quote-target=id.to_string()
                title="Jump to the quoted message"
                on:click=move |_| {
                    let _ = crate::shell::dispatch("transcript.jump", &id.to_string());
                }
            >
                <span class="sx-quote-text">{label}</span>
            </button>
        }
        .into_any(),
        None => view! {
            <div class="sx-quote-card sx-quote-missing" data-quote-target="">
                <span class="sx-quote-text">{label}</span>
                <span class="sx-quote-note">"The original message is not in this view."</span>
            </div>
        }
        .into_any(),
    }
}

fn scroll_message(id: u32) {
    after_render(move || scroll_message_now(id));
}

fn scroll_message_now(id: u32) {
    if let Some(doc) = web_sys::window().and_then(|w| w.document()) {
        // Articles carry their find-in-transcript id (`tf-line-<idx>`) as
        // the element id, so the pane's own cursor scroll keys off the
        // stable message id in `data-msg` instead.
        if let Ok(Some(el)) = doc.query_selector(&format!("[data-msg=\"{id}\"]")) {
            el.scroll_into_view();
        }
    }
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

/// Ids of messages that actually render a fold (#1441): only a
/// non-empty `detail` produces a `<details class="sx-fold">`.
fn fold_ids(messages: &[Message]) -> Vec<u32> {
    messages
        .iter()
        .filter(|m| !m.detail.is_empty())
        .map(|m| m.id)
        .collect()
}

/// A fold is open only when a bulk action or a per-fold toggle wrote
/// an explicit `true` for it. No entry = collapsed, which is both
/// today's default and the state a fold streamed in after a bulk
/// Expand all starts in (bulk actions are a snapshot, not a mode).
fn fold_is_open(overrides: &HashMap<u32, bool>, id: u32) -> bool {
    overrides.get(&id).copied().unwrap_or(false)
}

/// Apply a bulk Expand/Collapse to exactly the folds that exist at
/// click time. Entries for other ids are untouched; ids created
/// later get no entry and therefore stay collapsed.
fn apply_bulk_fold(overrides: &mut HashMap<u32, bool>, ids: &[u32], open: bool) {
    for id in ids {
        overrides.insert(*id, open);
    }
}

/// Fold signals shared by the list and each message (#1441).
#[derive(Clone, Copy)]
struct FoldState {
    /// Recorded open state per message id. Every write notifies, so
    /// each render's `open` matches the DOM: tachys diffs a static
    /// attribute against its previous render value, and a user toggle
    /// that skipped the rebuild would leave that value stale and make
    /// the next bulk action a no-op on the DOM.
    map: RwSignal<HashMap<u32, bool>>,
    /// The (message id, find match) whose forced-open fold the user
    /// closed; only that exact pairing stays closed (#1576).
    dismissed: RwSignal<Option<(u32, crate::transcript_find::TMatch)>>,
}

/// Rendered open state: the recorded/bulk state, plus the current find
/// match's fold unless the user closed it while it was forced open. A
/// recorded `false` never overrides the forced open (Collapse all must
/// not stop find from revealing folded hits).
fn resolve_fold_open(recorded: bool, find_forced: bool, dismissed: bool) -> bool {
    recorded || (find_forced && !dismissed)
}

#[derive(Debug, PartialEq, Eq)]
enum FoldToggle {
    /// DOM state equals the rendered state (our own re-render).
    Ignore,
    /// A user change: record it.
    Record,
    /// The user closed a find-forced fold: record closed and dismiss the
    /// forced open for this match.
    Dismiss,
}

fn fold_toggle_action(dom_open: bool, rendered_open: bool, find_forced: bool) -> FoldToggle {
    if dom_open == rendered_open {
        FoldToggle::Ignore
    } else if find_forced && !dom_open {
        FoldToggle::Dismiss
    } else {
        FoldToggle::Record
    }
}

/// Apply one native `toggle` of fold `id` against the live state:
/// the currently rendered open state is recomputed from `map` and
/// `dismissed` (never a captured render-time value), then the change is
/// ignored, recorded, or recorded as a dismissal of the forced open.
/// Returns whether anything was written.
fn handle_fold_toggle(
    map: &mut HashMap<u32, bool>,
    dismissed: &mut Option<(u32, crate::transcript_find::TMatch)>,
    id: u32,
    current_key: Option<(u32, crate::transcript_find::TMatch)>,
    find_forced: bool,
    dom_open: bool,
) -> bool {
    let is_dismissed = find_forced && current_key.is_some() && *dismissed == current_key;
    let rendered = resolve_fold_open(fold_is_open(map, id), find_forced, is_dismissed);
    match fold_toggle_action(dom_open, rendered, find_forced) {
        FoldToggle::Ignore => false,
        FoldToggle::Record => {
            record_fold_toggle(map, id, dom_open);
            true
        }
        FoldToggle::Dismiss => {
            record_fold_toggle(map, id, false);
            *dismissed = current_key;
            true
        }
    }
}

/// Record a single fold's native toggle. Returns without writing
/// when the state already matches, so the `toggle` event fired by
/// our own controlled re-render does not loop.
fn record_fold_toggle(overrides: &mut HashMap<u32, bool>, id: u32, open: bool) {
    if fold_is_open(overrides, id) != open {
        overrides.insert(id, open);
    }
}

fn message_view(
    msg: Message,
    idx: usize,
    cursor: RwSignal<Option<u32>>,
    open_file: Callback<(String, u32)>,
    retry: Callback<(String,)>,
    _fork: Callback<(i32,)>,
    quote: Callback<(u32,)>,
    quotable: bool,
    quote_card: Option<(quote_message::Quote, Option<u32>)>,
    find_hits: Vec<crate::transcript_find::TMatch>,
    find_current: Option<crate::transcript_find::TMatch>,
    turn_elapsed: Option<String>,
    turn_usage: Option<(String, String)>,
    turn_verify: Option<crate::turn_verify::TurnVerify>,
    tail: Option<crate::tool_tail::ToolTail>,
    fold_open: bool,
    folds: FoldState,
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
    // #1546: only the daemon's provider_auth code makes a notice
    // actionable — never a match on the message text.
    let auth_notice =
        msg.role == Role::Notice && msg.badge == crate::transcript::PROVIDER_AUTH_CODE;
    let reconnect = msg.reconnect.clone();
    // #1371: end-of-turn failure markers carry their stop reason as a
    // hook for scoped styling and browser checks; every other line has
    // no attribute.
    let turn_stop: Option<String> =
        crate::turn_failure::stop_reason_of(&msg.key).map(str::to_string);
    let auth_retry = retry_text.trim().to_string();
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
    let detail_ranges: Vec<(usize, usize)> = find_hits
        .iter()
        .filter(|h| h.field == crate::transcript_find::Field::Detail)
        .map(|h| (h.start, h.end))
        .collect();
    let cur_body = find_current
        .filter(|h| h.field == crate::transcript_find::Field::Body)
        .map(|h| (h.start, h.end));
    let cur_title = find_current
        .filter(|h| h.field == crate::transcript_find::Field::Title)
        .map(|h| (h.start, h.end));
    let cur_detail = find_current
        .filter(|h| h.field == crate::transcript_find::Field::Detail)
        .map(|h| (h.start, h.end));
    let find_body = !body_ranges.is_empty();
    let find_title = !title_ranges.is_empty();
    let find_detail = !detail_ranges.is_empty();
    // #1576: the fold opens when the current match sits inside it, so
    // stepping onto a folded hit lands somewhere visible. Only the
    // current match's fold is forced open; other folds keep the user's
    // own open/closed state, which #1441 records in `folds` (per-fold
    // toggles and Expand all / Collapse all) so the controlled `open`
    // attribute never closes a fold the user opened.
    let find_forced = cur_detail.is_some();
    let current_key = find_current.map(|m| (id, m));
    let dismissed = find_forced
        && current_key.is_some()
        && folds.dismissed.get_untracked() == current_key;
    let fold_open = resolve_fold_open(fold_open, find_forced, dismissed);
    let _ = (prose, retry_text, keep);
    let class = move || {
        let on = cursor.get() == Some(id);
        format!("sx-msg sx-{role}{}", if on { " sx-on" } else { "" })
    };
    let copy_btn = copy_text.clone();
    // Lessons capture (#1376): "Remember this" on the user's own messages,
    // shown only while the page's capture gate (memory mode) allows it.
    let lesson_gate = use_context::<crate::lessons::CaptureGate>();
    let lesson_text = (msg.role == Role::You && !copy_text.trim().is_empty()).then(|| copy_text.clone());
    // Per-button copy feedback (#1582): the label only reads "Copied"
    // once the awaited clipboard write resolves; a rejection reads
    // "Copy failed" instead of silence. Reverts after 2s.
    let copy_state = RwSignal::new(None::<bool>);
    view! {
        <article id=format!("tf-line-{idx}") data-msg=id class=class data-role=role data-turn-stop=turn_stop>
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
                            // Badge text keeps its space ("no result"); the
                            // class token cannot, so it is dashed (#1506).
                            let class = format!("sx-status sx-status-{}", badge.replace(' ', "-"));
                            view! { <span class=class data-status=badge.clone()>{badge.clone()}</span> }.into_any()
                        }
                    }}
                </Show>
                {has_stamp.then(|| view! { <span class="stamp" title=stamp_full.clone()>{stamp.clone()}</span> })}
                {turn_elapsed.clone().map(|label| view! { <span class="turn-elapsed" title="Turn elapsed time">{label}</span> })}
                {turn_usage.clone().map(|(label, detail)| view! { <span class="turn-usage" title=detail>{label}</span> })}
                {turn_verify.clone().map(|v| view! {
                    <span class="turn-verify" data-verify=v.state.label() title=v.detail.clone() aria-label=v.detail>{v.state.label()}</span>
                })}
                {diff.clone().map(|d| view! {
                    <span class="sx-diff" aria-label=format!("+{} -{}", d.added, d.deleted)>
                        <span class="sx-add">{format!("+{}", d.added)}</span>
                        <span class="sx-del">{format!("-{}", d.deleted)}</span>
                    </span>
                })}
                <span class="sx-actions">
                    <button type="button" class="sx-quiet" on:click=move |_| {
                        let text = copy_btn.clone();
                        spawn_local(async move {
                            let ok = crate::clipboard::copy_text(&text).await.is_ok();
                            copy_state.set(Some(ok));
                            crate::clipboard::after_ms(2_000, move || copy_state.set(None));
                        });
                    }>{move || crate::clipboard::transcript_copy_label(copy_state.get())}</button>
                    {quotable.then(|| view! {
                        <button
                            type="button"
                            class="sx-quiet sx-quote-btn"
                            title="Quote this message in your next send (q)"
                            on:click=move |_| quote.run((id,))
                        >"Quote"</button>
                    })}
                    {lesson_text.zip(lesson_gate).map(|(text, gate)| view! {
                        <Show when=move || gate.0.get()>
                            <button type="button" class="sx-quiet" data-action="lesson-remember"
                                title="Save this message as a lesson future sessions load"
                                on:click={
                                    let text = text.clone();
                                    move |_| { let _ = crate::shell::dispatch("lesson-remember", &text); }
                                }>"Remember this"</button>
                        </Show>
                    })}
                </span>
            </header>
            {quote_card.map(|(q, target)| quote_card_view(&q, target))}
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
            {tail.map(crate::tool_tail::tail_view)}
            {auth_notice.then(|| {
                let target = reconnect.clone();
                let resend = auth_retry.clone();
                let can_retry = !resend.is_empty();
                view! {
                    <div class="sx-auth-actions">
                        <button
                            type="button"
                            class="sx-auth-open"
                            on:click=move |_| {
                                // Same navigation as the empty state's
                                // Add provider; then focus the named row.
                                let _ = crate::shell::set_view("providers");
                                crate::providers::focus_provider_row(target.clone());
                            }
                        >"Open Providers"</button>
                        {can_retry.then(|| view! {
                            <button
                                type="button"
                                class="sx-quiet"
                                title="Re-send the failed message after fixing the credential"
                                on:click=move |_| retry.run((resend.clone(),))
                            >"Retry"</button>
                        })}
                    </div>
                }
            })}
            {if !detail.is_empty() {
                let summary = if diff.is_some() {
                    "Diff"
                } else if auth_notice {
                    "Details"
                } else {
                    "Output"
                };
                // Detail hits highlight through MarkedText, exactly
                // like body hits (#1576); without hits the fold is the
                // plain <pre> it has always been.
                let detail_inner = if find_detail {
                    view! { <crate::transcript_find::MarkedText text=detail.clone() kind="pre" ranges=detail_ranges current=cur_detail /> }.into_any()
                } else {
                    view! { <pre>{detail.clone()}</pre> }.into_any()
                };
                view! {
                    <details
                        class="sx-fold"
                        open=fold_open
                        data-fold-open=if fold_open { "true" } else { "false" }
                        data-fold-id=id.to_string()
                        data-tf-fold-current=if find_forced { "true" } else { "false" }
                        on:toggle=move |ev: web_sys::Event| {
                            let Some(target) = ev.target() else { return };
                            let Ok(el) = target.dyn_into::<web_sys::Element>() else { return };
                            let open = js_sys::Reflect::get(&el, &JsValue::from_str("open"))
                                .ok()
                                .and_then(|v| v.as_bool())
                                .unwrap_or(fold_open);
                            // Decided against live map/dismissed state, not
                            // the render-time `fold_open`. A real change
                            // notifies `map` so the rendered `open` is
                            // re-synced with the DOM (see `FoldState`);
                            // our own re-render's toggle writes nothing.
                            let mut dismissed = folds.dismissed.get_untracked();
                            let mut changed = false;
                            folds.map.update_untracked(|map| {
                                changed = handle_fold_toggle(
                                    map,
                                    &mut dismissed,
                                    id,
                                    current_key,
                                    find_forced,
                                    open,
                                );
                            });
                            if changed {
                                folds.dismissed.update_untracked(|d| *d = dismissed);
                                folds.map.notify();
                            }
                        }
                    >
                        <summary>{summary}</summary>
                        {detail_inner}
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
                    // #1575: known fence languages get the file viewer's
                    // `ev-*` token spans; unknown/empty stay plain text.
                    let code = crate::editor::highlighted_code(&lang, &text);
                    // Same honest per-button feedback as the message
                    // Copy button (#1582).
                    let copy_state = RwSignal::new(None::<bool>);
                    view! {
                        <div class="sx-fence">
                            <div class="sx-fence-bar">
                                <span class="sx-lang">{lang}</span>
                                <button type="button" class="sx-quiet" on:click=move |_| {
                                    let text = copy.clone();
                                    spawn_local(async move {
                                        let ok = crate::clipboard::copy_text(&text).await.is_ok();
                                        copy_state.set(Some(ok));
                                        crate::clipboard::after_ms(2_000, move || copy_state.set(None));
                                    });
                                }>{move || crate::clipboard::transcript_copy_label(copy_state.get())}</button>
                            </div>
                            <pre class="sx-code">{code}</pre>
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
    fn fold_bulk_is_a_snapshot_and_per_fold_toggle_still_works() {
        // Default: no entry = collapsed, as before #1441.
        let mut map: HashMap<u32, bool> = HashMap::new();
        assert!(!fold_is_open(&map, 1));
        // Expand all over the folds present at click time.
        apply_bulk_fold(&mut map, &[1, 2, 3], true);
        assert!(fold_is_open(&map, 1) && fold_is_open(&map, 2) && fold_is_open(&map, 3));
        // A fold streamed in after Expand all has no entry: collapsed,
        // not force-opened (bulk is a snapshot, not a mode).
        assert!(!fold_is_open(&map, 4));
        // Per-fold toggle after a bulk action.
        record_fold_toggle(&mut map, 2, false);
        assert!(!fold_is_open(&map, 2));
        assert!(fold_is_open(&map, 1));
        // Recording the same state is a no-op (own re-render's toggle).
        record_fold_toggle(&mut map, 1, true);
        assert!(fold_is_open(&map, 1));
        // Collapse all hides every present fold; the streamed-in fold
        // stays collapsed too.
        apply_bulk_fold(&mut map, &[1, 2, 3, 4], false);
        assert!(!fold_is_open(&map, 1) && !fold_is_open(&map, 4));
        // Session switch resets by clearing the map.
        map.clear();
        assert!(!fold_is_open(&map, 1));
    }

    #[test]
    fn find_forced_fold_can_be_closed_and_collapse_all_does_not_hide_hits() {
        // Collapse all recorded false: the current match still opens.
        assert!(resolve_fold_open(false, true, false));
        // Recorded open (Expand all), user closes the forced fold:
        // dismiss, and the fold renders closed.
        assert_eq!(fold_toggle_action(false, true, true), FoldToggle::Dismiss);
        assert!(!resolve_fold_open(false, true, true));
        // Own re-render toggles are ignored.
        assert_eq!(fold_toggle_action(true, true, true), FoldToggle::Ignore);
        assert_eq!(fold_toggle_action(false, false, false), FoldToggle::Ignore);
        // Plain user toggles are recorded; reopening a dismissed fold
        // records open.
        assert_eq!(fold_toggle_action(true, false, false), FoldToggle::Record);
        assert_eq!(fold_toggle_action(false, true, false), FoldToggle::Record);
        assert_eq!(fold_toggle_action(true, false, true), FoldToggle::Record);
        assert!(resolve_fold_open(true, true, true));
        // No find: recorded state rules.
        assert!(!resolve_fold_open(false, false, false));
        assert!(resolve_fold_open(true, false, false));
    }

    #[test]
    fn repeated_toggles_without_rebuild_use_live_state() {
        let m = crate::transcript_find::TMatch {
            line: 3,
            field: crate::transcript_find::Field::Detail,
            start: 0,
            end: 2,
        };
        let key = Some((7, m));
        let shown = |map: &HashMap<u32, bool>, d: &Option<(u32, crate::transcript_find::TMatch)>, forced: bool| {
            resolve_fold_open(fold_is_open(map, 7), forced, forced && *d == key)
        };
        // Plain fold: open then close, no rebuild in between.
        let (mut map, mut d) = (HashMap::new(), None);
        assert!(handle_fold_toggle(&mut map, &mut d, 7, None, false, true));
        assert!(shown(&map, &d, false));
        assert!(handle_fold_toggle(&mut map, &mut d, 7, None, false, false));
        assert!(!shown(&map, &d, false), "second toggle must not be ignored");
        // A rebuild's own toggle (DOM equals state) writes nothing.
        assert!(!handle_fold_toggle(&mut map, &mut d, 7, None, false, false));
        // Forced fold: dismiss, reopen, close again.
        let (mut map, mut d) = (HashMap::new(), None);
        assert!(shown(&map, &d, true));
        assert!(handle_fold_toggle(&mut map, &mut d, 7, key, true, false));
        assert!(!shown(&map, &d, true), "dismissed");
        assert!(handle_fold_toggle(&mut map, &mut d, 7, key, true, true));
        assert!(shown(&map, &d, true), "reopen after dismiss sticks");
        assert!(handle_fold_toggle(&mut map, &mut d, 7, key, true, false));
        assert!(!shown(&map, &d, true));
        // After Collapse all the forced fold still shows (no dismissal).
        let (mut map, d) = (HashMap::new(), None);
        apply_bulk_fold(&mut map, &[7], false);
        assert!(shown(&map, &d, true));
    }

    #[test]
    fn fold_ids_only_count_messages_with_detail() {
        let msg = |id: u32, detail: &str| Message {
            id,
            role: Role::Tool,
            title: String::new(),
            body: String::new(),
            badge: String::new(),
            detail: detail.into(),
            key: String::new(),
            call_id: String::new(),
            diff: None,
            chips: Vec::new(),
            keep_events: -1,
            retry_text: String::new(),
            copy_text: String::new(),
            at_ms: 0.0,
        };
        let msgs = vec![msg(1, "out"), msg(2, ""), msg(3, "diff")];
        assert_eq!(fold_ids(&msgs), vec![1, 3]);
        assert!(fold_ids(&[]).is_empty());
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
