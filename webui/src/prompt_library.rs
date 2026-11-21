//! Prompt library (#1357): save, name and reuse composer prompts with
//! `{{variable}}` placeholders.
//!
//! Slice 1 is honestly browser-local: the list lives in localStorage
//! under `strike.prompts`, like pins and seen-state, with the same
//! durability (this browser, this origin; clearing site data removes it).
//! The pure rules — validation, placeholders, search, insert — live in the
//! `prompt-library` crate. Inserting only ever puts text in the composer
//! draft (appended, never overwriting); nothing is sent, expanded
//! daemon-side or executed by the library.
//!
//! Entry points: the palette (`prompts.save`, `prompts.insert`,
//! `prompts.manage`, registered in `shell/contrib/prompt_library.rs`),
//! the composer "+" menu, and "Save as prompt" on a sent user message.

use std::rc::Rc;

use leptos::prelude::*;
use prompt_library::{insert_into_draft, placeholders, preview, search, upsert, Prompt};
use serde::{Deserialize, Serialize};
use wasm_bindgen::prelude::*;
use wasm_bindgen::JsCast;

pub const STORAGE_KEY: &str = "strike.prompts";
pub const SAVE_ID: &str = "prompts.save";
pub const INSERT_ID: &str = "prompts.insert";
pub const MANAGE_ID: &str = "prompts.manage";

/// UI copy for the durability caveat, shown in the editor and library.
pub const LOCAL_NOTE: &str =
    "Saved in this browser only, like pins. Clearing site data removes them.";

#[derive(Clone, Debug, Serialize, Deserialize)]
struct Stored {
    id: String,
    name: String,
    body: String,
}

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

/// The saved library. A missing or unreadable entry is an empty library.
pub fn load_prompts() -> Vec<Prompt> {
    storage()
        .and_then(|s| s.get_item(STORAGE_KEY).ok().flatten())
        .and_then(|raw| serde_json::from_str::<Vec<Stored>>(&raw).ok())
        .unwrap_or_default()
        .into_iter()
        .map(|s| Prompt { id: s.id, name: s.name, body: s.body })
        .collect()
}

/// Persist the library. `false` when the browser refused the write.
fn save_prompts(list: &[Prompt]) -> bool {
    let stored: Vec<Stored> = list
        .iter()
        .map(|p| Stored { id: p.id.clone(), name: p.name.clone(), body: p.body.clone() })
        .collect();
    let Ok(raw) = serde_json::to_string(&stored) else {
        return false;
    };
    storage().is_some_and(|s| s.set_item(STORAGE_KEY, &raw).is_ok())
}

fn new_id() -> String {
    let now = js_sys::Date::now() as u64;
    let salt = (js_sys::Math::random() * 1_000_000.0) as u64;
    format!("p{now:x}{salt:x}")
}

/// Palette choices for "Insert saved prompt": one row per prompt.
pub fn choices() -> Vec<crate::shell::Choice> {
    let list = load_prompts();
    search(&list, "")
        .into_iter()
        .map(|p| crate::shell::Choice {
            id: p.id.clone(),
            label: p.name.clone(),
            detail: preview(&p.body, 80),
        })
        .collect()
}

fn later(f: impl FnOnce() + 'static) {
    let Some(win) = web_sys::window() else {
        return;
    };
    let cb = Closure::once(f);
    let _ = win.set_timeout_with_callback_and_timeout_and_arguments_0(cb.as_ref().unchecked_ref(), 0);
    cb.forget();
}

fn focus(sel: &'static str) {
    later(move || {
        if let Some(el) = web_sys::window()
            .and_then(|w| w.document())
            .and_then(|d| d.query_selector(sel).ok().flatten())
            .and_then(|el| el.dyn_into::<web_sys::HtmlElement>().ok())
        {
            let _ = el.focus();
        }
    });
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum Pane {
    Closed,
    /// `id` is `None` for a new prompt.
    Editor { id: Option<String> },
    Library,
    Fill { prompt: Prompt, names: Vec<String> },
}

#[component]
pub fn PromptLibrary(draft: RwSignal<String>) -> impl IntoView {
    let prompts = RwSignal::new(load_prompts());
    let pane = RwSignal::new(Pane::Closed);
    let name = RwSignal::new(String::new());
    let body = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let query = RwSignal::new(String::new());
    let cursor = RwSignal::new(0usize);
    let values = RwSignal::new(Vec::<(String, String)>::new());

    let close = move || {
        pane.set(Pane::Closed);
        error.set(String::new());
        focus("#composer-input");
    };
    let open_editor = move |id: Option<String>, initial_name: String, initial_body: String| {
        name.set(initial_name);
        body.set(initial_body);
        error.set(String::new());
        pane.set(Pane::Editor { id });
        focus("[data-prompt-name]");
    };
    let open_library = move || {
        prompts.set(load_prompts());
        query.set(String::new());
        cursor.set(0);
        error.set(String::new());
        pane.set(Pane::Library);
        focus("[data-prompt-search]");
    };
    // Put a prompt in the composer. Placeholders go through the fill-in
    // form first; nothing is ever sent.
    let use_prompt = move |p: Prompt| match placeholders(&p.body) {
        Ok(names) if names.is_empty() => {
            draft.update(|d| *d = insert_into_draft(d, &p.body));
            close();
        }
        Ok(names) => {
            values.set(names.iter().map(|n| (n.clone(), String::new())).collect());
            pane.set(Pane::Fill { prompt: p, names });
            error.set(String::new());
            focus("[data-prompt-var]");
        }
        // Only reachable for a body edited outside the UI: save-time
        // validation never stores a malformed prompt.
        Err(e) => {
            error.set(e.message());
            pane.set(Pane::Library);
        }
    };

    crate::shell::bind(
        SAVE_ID,
        Rc::new(move |args: &str| {
            // From a sent message the text arrives as the argument;
            // from the composer it is the current draft.
            let text = if args.trim().is_empty() { draft.get_untracked() } else { args.to_string() };
            open_editor(None, String::new(), text);
            Ok(())
        }),
    );
    crate::shell::bind(
        INSERT_ID,
        Rc::new(move |args: &str| {
            let id = args.trim();
            if id.is_empty() {
                open_library();
                return Ok(());
            }
            let list = load_prompts();
            let Some(p) = list.into_iter().find(|p| p.id == id) else {
                return Err("That prompt is no longer in the library.".into());
            };
            use_prompt(p);
            Ok(())
        }),
    );
    crate::shell::bind(
        MANAGE_ID,
        Rc::new(move |_args: &str| {
            open_library();
            Ok(())
        }),
    );
    on_cleanup(|| {
        crate::shell::unbind(SAVE_ID);
        crate::shell::unbind(INSERT_ID);
        crate::shell::unbind(MANAGE_ID);
    });

    let save = move || {
        let Pane::Editor { id } = pane.get_untracked() else {
            return;
        };
        let editing = id.is_some();
        let prompt = Prompt {
            id: id.unwrap_or_else(new_id),
            name: name.get_untracked(),
            body: body.get_untracked(),
        };
        let mut list = load_prompts();
        if let Err(msg) = upsert(&mut list, prompt) {
            error.set(msg);
            return;
        }
        if !save_prompts(&list) {
            error.set("This browser refused to store the prompt (private mode or storage full).".into());
            return;
        }
        prompts.set(list);
        if editing {
            open_library();
        } else {
            close();
        }
    };
    let delete = move |p: Prompt| {
        let id = p.id.clone();
        let asked = crate::confirm::ask(
            crate::confirm::ConfirmRequest {
                title: format!("Delete “{}”?", p.name),
                body: "The prompt is removed from this browser's library. This cannot be undone.".into(),
                confirm_label: "Delete prompt".into(),
            },
            Rc::new(move || {
                let mut list = load_prompts();
                if prompt_library::remove(&mut list, &id) && !save_prompts(&list) {
                    error.set("This browser refused to update the library.".into());
                    return;
                }
                prompts.set(list);
            }),
        );
        if !asked {
            error.set(crate::confirm::BUSY.into());
        }
    };
    let insert_filled = move || {
        let Pane::Fill { prompt, .. } = pane.get_untracked() else {
            return;
        };
        match prompt_library::fill(&prompt.body, &values.get_untracked()) {
            Ok(text) => {
                draft.update(|d| *d = insert_into_draft(d, &text));
                close();
            }
            Err(e) => error.set(e.message()),
        }
    };

    let filtered = move || {
        let list = prompts.get();
        let q = query.get();
        search(&list, &q).into_iter().cloned().collect::<Vec<_>>()
    };

    view! {
        <Show when=move || pane.get() != Pane::Closed>
            <div class="prompt-lib-backdrop" on:click=move |_| close()>
                <div
                    class="prompt-lib-dialog"
                    role="dialog"
                    aria-modal="true"
                    aria-label="Prompt library"
                    data-prompt-library="true"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        if ev.key() == "Escape" {
                            ev.prevent_default();
                            ev.stop_propagation();
                            close();
                        }
                    }
                >
                    {move || match pane.get() {
                        Pane::Closed => ().into_any(),
                        Pane::Editor { id } => view! {
                            <h2>{if id.is_some() { "Edit prompt" } else { "Save as prompt" }}</h2>
                            <label class="prompt-lib-field">
                                <span>"Name"</span>
                                <input
                                    type="text"
                                    data-prompt-name="true"
                                    maxlength=prompt_library::MAX_NAME.to_string()
                                    prop:value=move || name.get()
                                    on:input=move |ev| name.set(event_target_value(&ev))
                                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                                        if ev.key() == "Enter" {
                                            ev.prevent_default();
                                            save();
                                        }
                                    }
                                />
                            </label>
                            <label class="prompt-lib-field">
                                <span>"Prompt"</span>
                                <textarea
                                    data-prompt-body="true"
                                    rows="6"
                                    prop:value=move || body.get()
                                    on:input=move |ev| body.set(event_target_value(&ev))
                                ></textarea>
                            </label>
                            <p class="hint">"Use {{name}} for a blank to fill in when you insert it. "{LOCAL_NOTE}</p>
                            <Show when=move || !error.get().is_empty()>
                                <p class="prompt-lib-error" role="alert" data-prompt-error="true">{move || error.get()}</p>
                            </Show>
                            <div class="prompt-lib-actions">
                                <button type="button" class="secondary" on:click=move |_| close()>"Cancel"</button>
                                <button type="button" data-prompt-save="true" on:click=move |_| save()>"Save prompt"</button>
                            </div>
                        }.into_any(),
                        Pane::Library => view! {
                            <h2>"Prompt library"</h2>
                            <input
                                type="search"
                                class="prompt-lib-search"
                                data-prompt-search="true"
                                placeholder="Search prompts"
                                aria-label="Search prompts"
                                prop:value=move || query.get()
                                on:input=move |ev| {
                                    query.set(event_target_value(&ev));
                                    cursor.set(0);
                                }
                                on:keydown=move |ev: web_sys::KeyboardEvent| {
                                    let rows: Vec<Prompt> = {
                                        let list = prompts.get_untracked();
                                        search(&list, &query.get_untracked()).into_iter().cloned().collect()
                                    };
                                    let n = rows.len();
                                    match ev.key().as_str() {
                                        "ArrowDown" if n > 0 => {
                                            ev.prevent_default();
                                            cursor.set((cursor.get_untracked() + 1) % n);
                                        }
                                        "ArrowUp" if n > 0 => {
                                            ev.prevent_default();
                                            cursor.set((cursor.get_untracked() + n - 1) % n);
                                        }
                                        "Enter" => {
                                            ev.prevent_default();
                                            if let Some(p) = rows.get(cursor.get_untracked()).cloned() {
                                                use_prompt(p);
                                            }
                                        }
                                        _ => {}
                                    }
                                }
                            />
                            <Show when=move || !error.get().is_empty()>
                                <p class="prompt-lib-error" role="alert" data-prompt-error="true">{move || error.get()}</p>
                            </Show>
                            <ul class="prompt-lib-list" role="listbox" aria-label="Saved prompts">
                                {move || {
                                    let rows = filtered();
                                    if rows.is_empty() {
                                        let msg = if prompts.get().is_empty() {
                                            "No saved prompts yet. Use “Save as prompt” on a draft or a sent message."
                                        } else {
                                            "No prompt matches that search."
                                        };
                                        return view! { <li class="hint">{msg}</li> }.into_any();
                                    }
                                    let at = cursor.get();
                                    rows.into_iter().enumerate().map(|(i, p)| {
                                        let (pick, edit, del) = (p.clone(), p.clone(), p.clone());
                                        let klass = if i == at { "prompt-lib-row on" } else { "prompt-lib-row" };
                                        let detail = preview(&p.body, 90);
                                        view! {
                                            <li class=klass role="option" aria-selected=(i == at).to_string() data-prompt-row=p.id.clone()>
                                                <button type="button" class="prompt-lib-pick" data-prompt-insert="true" on:click=move |_| use_prompt(pick.clone())>
                                                    <span class="name">{p.name.clone()}</span>
                                                    <span class="path">{detail}</span>
                                                </button>
                                                <button type="button" class="secondary" data-prompt-edit="true" on:click=move |_| {
                                                    open_editor(Some(edit.id.clone()), edit.name.clone(), edit.body.clone());
                                                }>"Edit"</button>
                                                <button type="button" class="secondary danger" data-prompt-delete="true" on:click=move |_| delete(del.clone())>"Delete"</button>
                                            </li>
                                        }
                                    }).collect_view().into_any()
                                }}
                            </ul>
                            <p class="hint">"↑/↓ to move, Enter to insert, Esc to close. Inserting never sends. "{LOCAL_NOTE}</p>
                            <div class="prompt-lib-actions">
                                <button type="button" class="secondary" data-prompt-new="true" on:click=move |_| open_editor(None, String::new(), String::new())>"New prompt"</button>
                                <button type="button" class="secondary" on:click=move |_| close()>"Close"</button>
                            </div>
                        }.into_any(),
                        Pane::Fill { prompt, names } => {
                            let last = names.len().saturating_sub(1);
                            view! {
                                <h2>{format!("Fill in “{}”", prompt.name)}</h2>
                                {names.into_iter().enumerate().map(|(i, var)| {
                                    let key = var.clone();
                                    let read_key = var.clone();
                                    view! {
                                        <label class="prompt-lib-field">
                                            <span>{var.clone()}</span>
                                            <input
                                                type="text"
                                                data-prompt-var=var.clone()
                                                prop:value=move || values.get().iter().find(|(k, _)| *k == read_key).map(|(_, v)| v.clone()).unwrap_or_default()
                                                on:input=move |ev| {
                                                    let v = event_target_value(&ev);
                                                    values.update(|list| {
                                                        if let Some(slot) = list.iter_mut().find(|(k, _)| *k == key) {
                                                            slot.1 = v;
                                                        }
                                                    });
                                                }
                                                on:keydown=move |ev: web_sys::KeyboardEvent| {
                                                    if ev.key() == "Enter" && i == last {
                                                        ev.prevent_default();
                                                        insert_filled();
                                                    }
                                                }
                                            />
                                        </label>
                                    }
                                }).collect_view()}
                                <Show when=move || !error.get().is_empty()>
                                    <p class="prompt-lib-error" role="alert" data-prompt-error="true">{move || error.get()}</p>
                                </Show>
                                <p class="hint">"The filled-in text goes into the composer. It is not sent."</p>
                                <div class="prompt-lib-actions">
                                    <button type="button" class="secondary" on:click=move |_| close()>"Cancel"</button>
                                    <button type="button" data-prompt-fill-insert="true" on:click=move |_| insert_filled()>"Insert"</button>
                                </div>
                            }.into_any()
                        }
                    }}
                </div>
            </div>
        </Show>
    }
}
