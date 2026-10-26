//! Top bar, command palette, and shortcut overlay.

use std::rc::Rc;

use leptos::html::Input;
use leptos::prelude::*;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use super::host::{self, status_badge_class, status_label};
use super::icon::icon;
use super::registry::{self, slot_inline, TopRegion};
use super::storage::{load_recents, remember_command};

const BACK_COLLAPSE: u32 = 1100;
const FOCUS_COLLAPSE: u32 = 1280;

fn viewport_width() -> u32 {
    web_sys::window()
        .and_then(|window| window.inner_width().ok())
        .and_then(|value| value.as_f64())
        .map(|width| width as u32)
        .unwrap_or(1280)
}

fn editable_target() -> bool {
    let Some(doc) = web_sys::window().and_then(|window| window.document()) else {
        return false;
    };
    let Some(el) = doc.active_element() else {
        return false;
    };
    let tag = el.tag_name().to_ascii_lowercase();
    if tag == "input" || tag == "textarea" || tag == "select" {
        return true;
    }
    matches!(
        el.get_attribute("contenteditable").as_deref(),
        Some("true") | Some("")
    )
}

#[component]
fn HistoryButton(back: bool, labeled: bool) -> impl IntoView {
    let label = if back { "Back" } else { "Forward" };
    let keys = if back { "Alt+Left" } else { "Alt+Right" };
    let mark = icon(if back { "back" } else { "forward" });
    let which = if back { "back" } else { "forward" };
    let class = if labeled { "nav-menu-item" } else { "nav-icon" };
    view! {
        <button
            type="button"
            class=class
            title=format!("{label} ({keys})")
            aria-label=label
            data-nav=which
            prop:disabled=move || if back { !host::can_back() } else { !host::can_forward() }
            on:click=move |_| {
                let _ = host::travel(back);
            }
        >
            <span class="icon-slot" aria-hidden="true" inner_html=mark></span>
            {labeled.then(|| view! { <span class="name">{label}</span> <kbd>{keys}</kbd> })}
        </button>
    }
}

#[component]
fn FocusButton(labeled: bool) -> impl IntoView {
    let mark = icon("focus");
    let class = move || {
        let on = host::focus_on();
        if labeled {
            if on {
                "nav-menu-item on"
            } else {
                "nav-menu-item"
            }
        } else if on {
            "nav-icon on"
        } else {
            "nav-icon"
        }
    };
    view! {
        <button
            type="button"
            class=class
            title="Focus mode (Alt+\\)"
            aria-label="Focus mode"
            aria-pressed=move || if host::focus_on() { "true" } else { "false" }
            data-nav="focus"
            on:click=move |_| {
                let _ = host::toggle_focus();
            }
        >
            <span class="icon-slot" aria-hidden="true" inner_html=mark></span>
            {labeled.then(|| view! { <span class="name">"Focus mode"</span> <kbd>"Alt+\\"</kbd> })}
        </button>
    }
}

#[component]
fn SessionChip() -> impl IntoView {
    view! {
        <span
            class=move || status_badge_class(&host::session_kind())
            title="Session status"
            data-nav="session-status"
        >
            {move || status_label(&host::session_kind())}
        </span>
    }
}

#[component]
pub fn Chrome() -> impl IntoView {
    let palette_open = RwSignal::new(false);
    let help_open = RwSignal::new(false);
    let more_open = RwSignal::new(false);
    let query = RwSignal::new(String::new());
    let arg_id = RwSignal::new(String::new());
    let arg_text = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let cursor = RwSignal::new(0usize);
    let width = RwSignal::new(viewport_width());
    let query_ref = NodeRef::<Input>::new();

    let open_palette = move || {
        query.set(String::new());
        arg_id.set(String::new());
        arg_text.set(String::new());
        error.set(String::new());
        cursor.set(0);
        more_open.set(false);
        help_open.set(false);
        palette_open.set(true);
    };
    let close_palette = move || {
        palette_open.set(false);
        arg_id.set(String::new());
        error.set(String::new());
    };

    host::set_open_help(Rc::new(move || {
        palette_open.set(false);
        arg_id.set(String::new());
        error.set(String::new());
        more_open.set(false);
        help_open.set(true);
    }));

    let flat_ids = move || -> Vec<String> {
        registry::palette(&query.get_untracked(), &load_recents())
            .into_iter()
            .flat_map(|group| group.hits.into_iter().map(|hit| hit.id))
            .collect()
    };
    let activate = move |id: String| {
        let Some(cmd) = registry::command(&id) else {
            return;
        };
        let in_arg = arg_id.get_untracked() == id;
        if cmd.argument.is_some() && !in_arg {
            arg_id.set(id);
            arg_text.set(String::new());
            error.set(String::new());
            query.set(String::new());
            cursor.set(0);
            return;
        }
        let args = if cmd.argument.is_some() {
            arg_text.get_untracked()
        } else {
            String::new()
        };
        if let Some(argument) = cmd.argument {
            if argument.required && args.trim().is_empty() {
                error.set("This command needs an argument.".into());
                return;
            }
        }
        match (cmd.run)(&args) {
            Ok(()) => {
                remember_command(&id);
                close_palette();
            }
            Err(message) => error.set(message),
        }
    };

    Effect::new(move |_| {
        let _ = query.get();
        cursor.set(0);
    });
    Effect::new(move |_| {
        let open = palette_open.get();
        let _ = arg_id.get();
        if open {
            if let Some(el) = query_ref.get() {
                let _ = el.focus();
            }
        }
    });
    Effect::new(move |_| {
        if collapsed_count(width.get()) == 0 {
            more_open.set(false);
        }
    });

    let on_resize = Closure::<dyn FnMut()>::new(move || width.set(viewport_width()));
    let on_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            let key = ev.key();
            if (ev.meta_key() || ev.ctrl_key()) && key.eq_ignore_ascii_case("k") && !ev.alt_key() {
                ev.prevent_default();
                if palette_open.get_untracked() {
                    close_palette();
                } else {
                    open_palette();
                }
                return;
            }
            if palette_open.get_untracked() && key == "Escape" {
                ev.prevent_default();
                if !arg_id.get_untracked().is_empty() {
                    arg_id.set(String::new());
                    arg_text.set(String::new());
                    error.set(String::new());
                    return;
                }
                close_palette();
                return;
            }
            if help_open.get_untracked() && key == "Escape" {
                ev.prevent_default();
                help_open.set(false);
                return;
            }
            if key == "Escape" {
                more_open.set(false);
                host::dismiss_popups();
                return;
            }
            if ev.alt_key() && !ev.meta_key() && !ev.ctrl_key() && key == "ArrowLeft" {
                ev.prevent_default();
                if !palette_open.get_untracked() {
                    let _ = host::travel(true);
                }
                return;
            }
            if ev.alt_key() && !ev.meta_key() && !ev.ctrl_key() && key == "ArrowRight" {
                ev.prevent_default();
                if !palette_open.get_untracked() {
                    let _ = host::travel(false);
                }
                return;
            }
            if ev.alt_key() && !ev.meta_key() && !ev.ctrl_key() && key == "\\" {
                ev.prevent_default();
                let _ = host::toggle_focus();
                return;
            }
            if key == "?" && !ev.meta_key() && !ev.ctrl_key() && !ev.alt_key() {
                if palette_open.get_untracked() || editable_target() {
                    return;
                }
                ev.prevent_default();
                help_open.update(|open| *open = !*open);
                more_open.set(false);
            }
        });
    if let Some(window) = web_sys::window() {
        let _ =
            window.add_event_listener_with_callback("resize", on_resize.as_ref().unchecked_ref());
        let _ = window.add_event_listener_with_callback("keydown", on_key.as_ref().unchecked_ref());
    }
    on_resize.forget();
    on_key.forget();

    let command_mark = icon("command");
    let more_mark = icon("more");

    view! {
        <header class="page-header nav-bar">
            <div class="nav-leading">
                {move || {
                    (width.get() >= BACK_COLLAPSE).then(|| view! {
                        <HistoryButton back=true labeled=false />
                        <HistoryButton back=false labeled=false />
                    })
                }}
                <nav class="crumbs" aria-label="Location" data-nav="crumbs">
                    <span class="brand-mark" title=move || host::version_label()>"Strike"</span>
                    <span class="crumb-sep" aria-hidden="true">">"</span>
                    <span class="crumb">{move || host::workspace_name()}</span>
                    <span class="crumb-sep" aria-hidden="true">">"</span>
                    <span class="crumb current">{move || host::session_name()}</span>
                </nav>
                <span class="brand-status nav-version">{move || host::version_label()}</span>
                {move || {
                    let found = registry::slots(TopRegion::Status);
                    if found.is_empty() {
                        view! { <SessionChip /> }.into_any()
                    } else {
                        found
                            .into_iter()
                            .filter(|slot| slot_inline(slot, width.get()))
                            .map(slot_view)
                            .collect_view()
                            .into_any()
                    }
                }}
                {move || {
                    registry::slots(TopRegion::Leading)
                        .into_iter()
                        .filter(|slot| slot_inline(slot, width.get()))
                        .map(slot_view)
                        .collect_view()
                }}
                {move || (width.get() >= FOCUS_COLLAPSE).then(|| view! { <FocusButton labeled=false /> })}
            </div>
            <button
                type="button"
                class="command-pill"
                data-nav="palette"
                title="Ctrl+K or Cmd+K opens commands. Alt+Left goes back. Alt+Right goes forward. Alt+\\ is focus mode. ? lists shortcuts."
                on:click=move |_| open_palette()
            >
                <span class="pill-label">
                    <span class="icon-slot" aria-hidden="true" inner_html=command_mark></span>
                    <span>"Run a command"</span>
                </span>
                <kbd>"Ctrl+K"</kbd>
            </button>
            <div class="header-actions nav-trailing">
                {move || {
                    registry::slots(TopRegion::Trailing)
                        .into_iter()
                        .filter(|slot| slot_inline(slot, width.get()))
                        .map(slot_view)
                        .collect_view()
                }}
                <Show when=move || { collapsed_count(width.get()) > 0 }>
                    <div class="nav-more">
                        <button
                            type="button"
                            class="nav-icon"
                            aria-label="More"
                            aria-haspopup="menu"
                            aria-expanded=move || if more_open.get() { "true" } else { "false" }
                            data-nav="more"
                            on:click=move |ev: web_sys::MouseEvent| {
                                ev.stop_propagation();
                                more_open.update(|open| *open = !*open);
                            }
                        >
                            <span class="icon-slot" aria-hidden="true" inner_html=more_mark></span>
                        </button>
                        <Show when=move || more_open.get()>
                            <div
                                class="menu nav-pop"
                                role="menu"
                                data-nav="more-menu"
                                on:click=move |_| more_open.set(false)
                            >
                                {move || (width.get() < BACK_COLLAPSE).then(|| view! {
                                    <HistoryButton back=true labeled=true />
                                    <HistoryButton back=false labeled=true />
                                })}
                                {move || (width.get() < FOCUS_COLLAPSE).then(|| view! { <FocusButton labeled=true /> })}
                                {move || {
                                    overflow_slots(width.get())
                                        .into_iter()
                                        .map(|slot| {
                                            let label = slot.overflow_label.to_string();
                                            let view = (slot.render)();
                                            view! {
                                                <div class="nav-overflow-slot">
                                                    <span class="path">{label}</span>
                                                    {view}
                                                </div>
                                            }
                                        })
                                        .collect_view()
                                }}
                            </div>
                        </Show>
                    </div>
                </Show>
            </div>
        </header>
        <Show when=move || palette_open.get()>
            <div class="palette-backdrop" on:click=move |_| close_palette()>
                <div
                    class="palette"
                    role="dialog"
                    aria-modal="true"
                    aria-label="Command palette"
                    data-nav="palette-dialog"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                >
                    <Show when=move || !arg_id.get().is_empty()>
                        <div class="palette-arg">
                            <button
                                type="button"
                                class="secondary"
                                on:click=move |_| {
                                    arg_id.set(String::new());
                                    arg_text.set(String::new());
                                    error.set(String::new());
                                }
                            >
                                "Back"
                            </button>
                            <span class="name">{move || {
                                registry::command(&arg_id.get()).map(|cmd| cmd.label.to_string()).unwrap_or_default()
                            }}</span>
                        </div>
                    </Show>
                    <input
                        node_ref=query_ref
                        placeholder=move || arg_placeholder(&arg_id.get())
                        prop:value=move || if arg_id.get().is_empty() { query.get() } else { arg_text.get() }
                        autocomplete="off"
                        spellcheck="false"
                        aria-label="Command"
                        on:input=move |ev| {
                            let value = event_target_value(&ev);
                            error.set(String::new());
                            if arg_id.get_untracked().is_empty() {
                                query.set(value);
                            } else {
                                arg_text.set(value);
                            }
                        }
                        on:keydown=move |ev: web_sys::KeyboardEvent| {
                            let key = ev.key();
                            if key == "ArrowDown" || key == "ArrowUp" {
                                if !arg_id.get_untracked().is_empty() {
                                    return;
                                }
                                ev.prevent_default();
                                let ids = flat_ids();
                                if ids.is_empty() {
                                    return;
                                }
                                let cur = cursor.get_untracked();
                                let next = if key == "ArrowDown" {
                                    (cur + 1) % ids.len()
                                } else if cur == 0 {
                                    ids.len() - 1
                                } else {
                                    cur - 1
                                };
                                cursor.set(next);
                                return;
                            }
                            if key == "Enter" {
                                ev.prevent_default();
                                if !arg_id.get_untracked().is_empty() {
                                    activate(arg_id.get_untracked());
                                    return;
                                }
                                let ids = flat_ids();
                                if let Some(id) = ids.get(cursor.get_untracked()) {
                                    activate(id.clone());
                                }
                            }
                        }
                    />
                    <Show when=move || !error.get().is_empty()>
                        <p class="notice palette-error" role="alert">{move || error.get()}</p>
                    </Show>
                    <div class="palette-list" role="listbox">
                        {move || {
                            if !arg_id.get().is_empty() {
                                let detail = registry::command(&arg_id.get())
                                    .map(|cmd| cmd.detail.to_string())
                                    .unwrap_or_default();
                                return view! { <p class="hint">{detail}</p> }.into_any();
                            }
                            let groups = registry::palette(&query.get(), &load_recents());
                            if groups.is_empty() {
                                return view! { <p class="hint">"No matching commands."</p> }.into_any();
                            }
                            let mut blocks = Vec::new();
                            let mut index = 0usize;
                            let selected = cursor.get();
                            for group in groups {
                                let heading = group.section.clone();
                                blocks.push(view! { <h3 class="palette-section">{heading}</h3> }.into_any());
                                for hit in group.hits {
                                    let id = hit.id.clone();
                                    let label = hit.label.clone();
                                    let detail = hit.detail.clone();
                                    let shortcut = hit.shortcut.clone();
                                    let on = index == selected;
                                    index += 1;
                                    let class = if on { "palette-row on" } else { "palette-row" };
                                    let activate_id = id.clone();
                                    let run = activate;
                                    blocks.push(view! {
                                        <button
                                            type="button"
                                            class=class
                                            role="option"
                                            aria-selected=on.to_string()
                                            data-command-id=id
                                            on:click=move |_| run(activate_id.clone())
                                        >
                                            <span class="palette-copy">
                                                <span class="name">{label}</span>
                                                <span class="path">{detail}</span>
                                            </span>
                                            {(!shortcut.is_empty()).then(|| view! { <kbd>{shortcut}</kbd> })}
                                        </button>
                                    }.into_any());
                                }
                            }
                            blocks.into_any()
                        }}
                    </div>
                    <p class="hint keys">"Up and Down move. Enter runs. A command with … asks for an argument. Esc closes. ? lists shortcuts."</p>
                </div>
            </div>
        </Show>
        <Show when=move || help_open.get()>
            <div class="palette-backdrop" on:click=move |_| help_open.set(false)>
                <div
                    class="palette help-sheet"
                    role="dialog"
                    aria-modal="true"
                    aria-label="Keyboard shortcuts"
                    data-nav="help"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                >
                    <div class="help-head">
                        <h2 class="panel-title">"Keyboard shortcuts"</h2>
                        <button type="button" class="secondary" on:click=move |_| help_open.set(false)>"Close"</button>
                    </div>
                    <div class="palette-list">
                        {move || shortcut_groups()}
                    </div>
                </div>
            </div>
        </Show>
    }
}

fn slot_view(slot: super::registry::TopSlot) -> AnyView {
    let title = slot.title.to_string();
    let body = (slot.render)();
    view! { <span class="nav-slot" title=title>{body}</span> }.into_any()
}

fn arg_placeholder(id: &str) -> String {
    if id.is_empty() {
        return "Type a command".into();
    }
    registry::command(id)
        .and_then(|cmd| cmd.argument.map(|arg| arg.placeholder.to_string()))
        .unwrap_or_else(|| "Argument".into())
}

fn overflow_slots(width: u32) -> Vec<super::registry::TopSlot> {
    [TopRegion::Status, TopRegion::Leading, TopRegion::Trailing]
        .into_iter()
        .flat_map(registry::slots)
        .filter(|slot| !slot_inline(slot, width))
        .collect()
}

fn collapsed_count(width: u32) -> usize {
    let mut count = overflow_slots(width).len();
    if width < BACK_COLLAPSE {
        count += 2;
    }
    if width < FOCUS_COLLAPSE {
        count += 1;
    }
    count
}

fn shortcut_groups() -> Vec<AnyView> {
    let mut blocks = Vec::new();
    let mut current = String::new();
    for shortcut in registry::shortcuts() {
        if shortcut.group != current {
            current = shortcut.group.to_string();
            let heading = current.clone();
            blocks.push(view! { <h3 class="palette-section">{heading}</h3> }.into_any());
        }
        let keys = shortcut.keys.to_string();
        let label = shortcut.label.to_string();
        blocks.push(
            view! {
                <div class="shortcut-row">
                    <span class="name">{label}</span>
                    <kbd>{keys}</kbd>
                </div>
            }
            .into_any(),
        );
    }
    blocks
}
