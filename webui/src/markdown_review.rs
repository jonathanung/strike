//! Markdown Review (#1366): comment on exact passages of a rendered `.md`
//! file in the document pane, then send every comment as one turn.
//!
//! Anchors, stale detection, grouping and the turn text live in the pure
//! `md-review` crate (tested in CI). This module is the browser side:
//! reading the selection inside the rendered view, keeping unsent comments
//! in browser storage per session + file (so a reload loses nothing), and
//! sending through the normal `user.input` path. Sending is refused while
//! a turn runs.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::{Deserialize, Serialize};

use md_review::{Anchor, Block, Comment, PlainDoc, Status};

use crate::editor::{heading_plain_text, markdown_blocks, MdBlock};

/// DOM id of the wrapper around the rendered Markdown the selection must
/// fall inside.
pub const HOST_ID: &str = "mdr-host";

/// The rendered document's visible text, one entry per block, using the
/// same parse and inline stripping as the rendered view.
pub fn plain_blocks(src: &str) -> Vec<Block> {
    let plain = |t: &str| heading_plain_text(t);
    markdown_blocks(src)
        .into_iter()
        .map(|b| match b {
            MdBlock::Heading(_, t) => Block::heading(&plain(&t)),
            MdBlock::Paragraph(t) | MdBlock::Quote(t) => Block::text(&plain(&t)),
            MdBlock::Code { text, .. } => Block::text(&text),
            MdBlock::List(items) => Block::text(&items.iter().map(|i| plain(i.as_str())).collect::<Vec<_>>().join(" ")),
            MdBlock::Rule => Block::text(""),
            MdBlock::Table { header, rows, .. } => {
                let mut cells: Vec<String> = header.iter().map(|c| plain(c.as_str())).collect();
                for row in &rows {
                    cells.extend(row.iter().map(|c| plain(c.as_str())));
                }
                Block::text(&cells.join(" "))
            }
        })
        .collect()
}

/// The current selection's text when it starts inside the rendered view.
pub fn selection_in_host() -> Option<String> {
    let win = web_sys::window()?;
    let sel = win.get_selection().ok()??;
    if sel.is_collapsed() {
        return None;
    }
    let host = win.document()?.get_element_by_id(HOST_ID)?;
    let node = sel.anchor_node()?;
    if !host.contains(Some(&node)) {
        return None;
    }
    let text: String = sel.to_string().into();
    if text.trim().is_empty() {
        None
    } else {
        Some(text)
    }
}

/// Put a non-empty selection in `quote`; an empty one leaves it alone so
/// a stray click does not discard a half-written comment.
pub fn capture(quote: RwSignal<String>) {
    if let Some(text) = selection_in_host() {
        quote.set(text);
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
struct Stored {
    id: String,
    quote: String,
    #[serde(default)]
    heading: String,
    #[serde(default)]
    offset: usize,
    body: String,
}

fn to_stored(c: &Comment) -> Stored {
    Stored {
        id: c.id.clone(),
        quote: c.anchor.quote.clone(),
        heading: c.anchor.heading.clone(),
        offset: c.anchor.offset,
        body: c.body.clone(),
    }
}

fn from_stored(s: Stored) -> Comment {
    Comment {
        id: s.id,
        anchor: Anchor { quote: s.quote, heading: s.heading, offset: s.offset },
        body: s.body,
    }
}

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

fn load_comments(key: &str) -> Vec<Comment> {
    storage()
        .and_then(|s| s.get_item(key).ok().flatten())
        .and_then(|raw| serde_json::from_str::<Vec<Stored>>(&raw).ok())
        .map(|list| list.into_iter().map(from_stored).collect())
        .unwrap_or_default()
}

fn save_comments(key: &str, comments: &[Comment]) {
    let Some(s) = storage() else {
        return;
    };
    if comments.is_empty() {
        let _ = s.remove_item(key);
        return;
    }
    let list: Vec<Stored> = comments.iter().map(to_stored).collect();
    if let Ok(raw) = serde_json::to_string(&list) {
        let _ = s.set_item(key, &raw);
    }
}

fn new_id() -> String {
    format!("m{}", (js_sys::Math::random() * 1e12) as u64)
}

#[component]
pub fn MdReviewPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    busy: Memo<bool>,
    path: RwSignal<String>,
    content: RwSignal<String>,
    quote: RwSignal<String>,
) -> impl IntoView {
    let comments = RwSignal::new(Vec::<Comment>::new());
    let draft = RwSignal::new(String::new());
    let note = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let sending = RwSignal::new(false);
    let editing = RwSignal::new(String::new());
    let edit_draft = RwSignal::new(String::new());
    let key = Memo::new(move |_| md_review::storage_key(&root.get(), &path.get()));
    let doc = Memo::new(move |_| PlainDoc::new(&plain_blocks(&content.get())));

    // Each session + file has its own unsent comments.
    Effect::new(move |_| {
        let k = key.get();
        comments.set(load_comments(&k));
        quote.set(String::new());
        draft.set(String::new());
        editing.set(String::new());
        note.set(String::new());
        error.set(String::new());
    });

    let commit = move |list: Vec<Comment>| {
        save_comments(&key.get_untracked(), &list);
        comments.set(list);
    };

    let add = move || {
        let body = draft.get_untracked();
        if body.trim().is_empty() {
            error.set("Write a comment first.".into());
            return;
        }
        let Some(anchor) = doc.with_untracked(|d| d.anchor(&quote.get_untracked(), 0)) else {
            error.set("That selection is not text of this document. Select a passage in the rendered view.".into());
            return;
        };
        let mut list = comments.get_untracked();
        list.push(Comment { id: new_id(), anchor, body: body.trim().to_string() });
        commit(list);
        quote.set(String::new());
        draft.set(String::new());
        error.set(String::new());
        note.set("Comment added.".into());
    };

    let send = move || {
        let list = comments.get_untracked();
        let r = root.get_untracked();
        if let Some(why) = md_review::send_block(list.len(), busy.get_untracked(), !r.is_empty()) {
            error.set(why.into());
            return;
        }
        if sending.get_untracked() {
            return;
        }
        let rows: Vec<(Comment, Status)> = doc.with_untracked(|d| {
            md_review::grouped(d, &list).into_iter().flat_map(|(_, g)| g).collect()
        });
        let text = md_review::review_turn(&path.get_untracked(), &rows);
        let sent_ids: Vec<String> = list.iter().map(|c| c.id.clone()).collect();
        let sent_key = key.get_untracked();
        sending.set(true);
        error.set(String::new());
        note.set(String::new());
        let (b, t) = (base.get_untracked(), token.get_untracked());
        spawn_local(async move {
            let res = crate::api::post_op(&b, &t, &r, "user.input", serde_json::json!({"text": text})).await;
            sending.set(false);
            match res {
                Ok(()) => {
                    // Clear only what was sent, and only for that file.
                    if key.get_untracked() == sent_key {
                        let kept: Vec<Comment> = comments
                            .get_untracked()
                            .into_iter()
                            .filter(|c| !sent_ids.contains(&c.id))
                            .collect();
                        commit(kept);
                    } else {
                        let kept: Vec<Comment> = load_comments(&sent_key)
                            .into_iter()
                            .filter(|c| !sent_ids.contains(&c.id))
                            .collect();
                        save_comments(&sent_key, &kept);
                    }
                    note.set(format!("Sent {} comment{} as one turn.", sent_ids.len(), if sent_ids.len() == 1 { "" } else { "s" }));
                }
                Err(e) => error.set(format!("Send failed; your comments are kept. {e}")),
            }
        });
    };

    view! {
        <section class="mdr-panel" data-md-review="true" aria-label="Review comments">
            <header class="mdr-head">
                <span class="hint">"Review comments"</span>
                <span class="mdr-count" data-mdr-count="true">{move || comments.with(|c| c.len()).to_string()}</span>
            </header>
            <Show when=move || quote.with(|q| !q.trim().is_empty())>
                <div class="mdr-composer" data-mdr-composer="true">
                    <blockquote class="mdr-quote">{move || md_review::normalize(&quote.get())}</blockquote>
                    <textarea class="mdr-draft" aria-label="Comment on the selected passage" placeholder="Comment on this passage"
                        prop:value=move || draft.get()
                        on:input=move |ev| draft.set(event_target_value(&ev))
                        on:keydown=move |ev: web_sys::KeyboardEvent| {
                            if ev.key() == "Enter" && (ev.ctrl_key() || ev.meta_key()) {
                                ev.prevent_default();
                                add();
                            } else if ev.key() == "Escape" {
                                quote.set(String::new());
                            }
                        }></textarea>
                    <span class="mdr-actions">
                        <button type="button" class="mdr-add" on:click=move |_| add()>"Add comment"</button>
                        <button type="button" class="secondary" on:click=move |_| { quote.set(String::new()); draft.set(String::new()); }>"Cancel"</button>
                    </span>
                </div>
            </Show>
            <Show when=move || comments.with(|c| c.is_empty()) && quote.with(|q| q.trim().is_empty())>
                <p class="hint mdr-empty">"Select a passage in the rendered document to comment on it."</p>
            </Show>
            {move || {
                let list = comments.get();
                let groups = doc.with(|d| md_review::grouped(d, &list));
                groups.into_iter().map(|(heading, rows)| {
                    let title = if heading.is_empty() { "Before the first heading".to_string() } else { heading };
                    view! {
                        <div class="mdr-group">
                            <h5 class="mdr-heading">{title}</h5>
                            <ul class="mdr-list">
                                {rows.into_iter().map(|(c, status)| {
                                    let stale = status == Status::Stale;
                                    let (id_edit, id_save, id_del, id_show) = (c.id.clone(), c.id.clone(), c.id.clone(), c.id.clone());
                                    let body = c.body.clone();
                                    view! {
                                        <li class={if stale { "mdr-item mdr-stale" } else { "mdr-item" }} data-mdr-comment={c.id.clone()} data-mdr-stale={stale.to_string()}>
                                            <blockquote class="mdr-quote">{c.anchor.quote.clone()}</blockquote>
                                            {stale.then(|| view! { <span class="mdr-badge" data-mdr-stale-badge="true">"Anchor moved/lost: this passage is no longer in the file"</span> })}
                                            <Show when=move || editing.get() != id_show
                                                fallback=move || view! {
                                                    <textarea class="mdr-draft" aria-label="Edit comment"
                                                        prop:value=move || edit_draft.get()
                                                        on:input=move |ev| edit_draft.set(event_target_value(&ev))></textarea>
                                                    <span class="mdr-actions">
                                                        <button type="button" class="mdr-save" on:click={
                                                            let id = id_save.clone();
                                                            move |_| {
                                                                let text = edit_draft.get_untracked();
                                                                if text.trim().is_empty() {
                                                                    error.set("A comment cannot be empty; delete it instead.".into());
                                                                    return;
                                                                }
                                                                let list: Vec<Comment> = comments.get_untracked().into_iter().map(|mut x| {
                                                                    if x.id == id { x.body = text.trim().to_string(); }
                                                                    x
                                                                }).collect();
                                                                commit(list);
                                                                editing.set(String::new());
                                                            }
                                                        }>"Save"</button>
                                                        <button type="button" class="secondary" on:click=move |_| editing.set(String::new())>"Cancel"</button>
                                                    </span>
                                                }>
                                                <p class="mdr-body">{body.clone()}</p>
                                                <span class="mdr-actions">
                                                    <button type="button" class="secondary mdr-edit" on:click={
                                                        let id = id_edit.clone();
                                                        let body = body.clone();
                                                        move |_| { edit_draft.set(body.clone()); editing.set(id.clone()); }
                                                    }>"Edit"</button>
                                                    <button type="button" class="secondary mdr-delete" on:click={
                                                        let id = id_del.clone();
                                                        move |_| {
                                                            let list: Vec<Comment> = comments.get_untracked().into_iter().filter(|x| x.id != id).collect();
                                                            commit(list);
                                                        }
                                                    }>"Delete"</button>
                                                </span>
                                            </Show>
                                        </li>
                                    }
                                }).collect_view()}
                            </ul>
                        </div>
                    }
                }).collect_view()
            }}
            <div class="mdr-send">
                <button type="button" class="mdr-send-btn" data-mdr-send="true"
                    disabled={move || comments.with(|c| c.is_empty()) || sending.get()}
                    on:click=move |_| send()>
                    {move || {
                        let n = comments.with(|c| c.len());
                        if n == 0 { "Send to agent".to_string() } else { format!("Send {n} to agent") }
                    }}
                </button>
                <Show when=move || busy.get() && comments.with(|c| !c.is_empty())>
                    <span class="hint mdr-why">"A turn is running. Send once the session is idle."</span>
                </Show>
            </div>
            <Show when=move || !error.get().is_empty()>
                <p class="hint mdr-error" role="alert" data-mdr-error="true">{move || error.get()}</p>
            </Show>
            <Show when=move || !note.get().is_empty()>
                <p class="hint mdr-note" role="status">{move || note.get()}</p>
            </Show>
        </section>
    }
}
