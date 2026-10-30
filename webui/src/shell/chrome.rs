//! Top bar, command palette, and shortcut overlay.

use std::rc::Rc;

use leptos::html::Input;
use leptos::prelude::*;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use super::host::{self, status_badge_class, status_label};
use super::icon::icon;
use super::registry::{self, TopRegion};
use super::storage::{load_usage, remember_command};

/// Centre-zone pages. Cap is 5. Chat is the only primary page: Daemon
/// and every other destination live behind the command palette (#1590);
/// on phone the Daemon view stays reachable from the bottom nav.
const PRIMARY: &[(&str, &str)] = &[("chat", "Chat")];

/// Phone layout, keyed on the same media query that sets `data-phone`
/// (mobile.rs), so the Rust header and the phone CSS never disagree.
fn phone_layout() -> bool {
    web_sys::window()
        .and_then(|window| window.match_media(crate::mobile::PHONE_MQ).ok())
        .flatten()
        .map(|mql| mql.matches())
        .unwrap_or(false)
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
    let query = RwSignal::new(String::new());
    let arg_id = RwSignal::new(String::new());
    let arg_text = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let cursor = RwSignal::new(0usize);
    let phone = RwSignal::new(phone_layout());
    let query_ref = NodeRef::<Input>::new();

    let open_palette = move || {
        query.set(String::new());
        arg_id.set(String::new());
        arg_text.set(String::new());
        error.set(String::new());
        cursor.set(0);
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
        help_open.set(true);
    }));

    let flat_ids = move || -> Vec<String> {
        if !arg_id.get_untracked().is_empty() {
            return choice_rows(&arg_id.get_untracked(), &arg_text.get_untracked())
                .into_iter()
                .map(|choice| choice.id)
                .collect();
        }
        registry::palette(&query.get_untracked(), &load_usage())
            .into_iter()
            .flat_map(|group| group.hits.into_iter().map(|hit| hit.id))
            .collect()
    };
    let run_ready = move |id: String, args: String| {
        let Some(cmd) = registry::command(&id) else {
            return;
        };
        if !cmd.is_enabled() {
            error.set("That command is unavailable.".into());
            return;
        }
        if let Some(argument) = cmd.argument {
            let choosing = cmd.choices.is_some() && !args.trim().is_empty();
            if argument.required && args.trim().is_empty() && !choosing {
                error.set("This command needs an argument.".into());
                return;
            }
        }
        if let Some(spec) = cmd.confirm {
            let perform = cmd.run.clone();
            let remembered = id.clone();
            let payload = args.clone();
            crate::confirm::ask(
                spec.into(),
                Rc::new(move || {
                    if perform(&payload).is_ok() {
                        remember_command(&remembered);
                    }
                }),
            );
            close_palette();
            return;
        }
        match (cmd.run)(&args) {
            Ok(()) => {
                remember_command(&id);
                close_palette();
            }
            Err(message) => error.set(message),
        }
    };
    let activate = move |id: String| {
        let Some(cmd) = registry::command(&id) else {
            return;
        };
        if !cmd.is_enabled() {
            error.set("That command is unavailable.".into());
            return;
        }
        let in_arg = arg_id.get_untracked() == id;
        if cmd.needs_prompt() && !in_arg {
            arg_id.set(id);
            arg_text.set(String::new());
            error.set(String::new());
            query.set(String::new());
            cursor.set(0);
            return;
        }
        let mut args = if in_arg {
            arg_text.get_untracked()
        } else {
            String::new()
        };
        if in_arg {
            if let Some(load) = cmd.choices.clone() {
                let rows = registry::filter_choices(&args, &load());
                if let Some(choice) = rows.get(cursor.get_untracked()) {
                    args = choice.id.clone();
                }
            }
        }
        run_ready(id, args);
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
    let on_resize = Closure::<dyn FnMut()>::new(move || {
        phone.set(phone_layout());
    });
    let on_key =
        Closure::<dyn FnMut(web_sys::KeyboardEvent)>::new(move |ev: web_sys::KeyboardEvent| {
            let key = ev.key();
            if key == "/"
                && !ev.meta_key()
                && !ev.ctrl_key()
                && !ev.alt_key()
                && !palette_open.get_untracked()
                && !editable_target()
            {
                ev.prevent_default();
                open_palette();
                return;
            }
            if palette_open.get_untracked() && key == "Tab" {
                ev.prevent_default();
                trap_palette_focus(ev.shift_key());
                return;
            }
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
            if ev.alt_key() && !ev.meta_key() && !ev.ctrl_key() && key.eq_ignore_ascii_case("s") {
                ev.prevent_default();
                let _ = host::toggle_side_chat();
                return;
            }
            if key == "?" && !ev.meta_key() && !ev.ctrl_key() && !ev.alt_key() {
                if palette_open.get_untracked() || editable_target() {
                    return;
                }
                ev.prevent_default();
                help_open.update(|open| *open = !*open);
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

    view! {
        <header class="page-header nav-bar" data-nav="header">
            {move || if phone.get() {
                phone_bar(open_palette, command_mark)
            } else {
                view! {
            <div class="nav-zone nav-left">
                <nav class="crumbs" aria-label="Location" data-nav="crumbs">
                    <span class="brand-mark" title=move || host::version_label()>"Strike"</span>
                    <span class="crumb-sep" aria-hidden="true">">"</span>
                    <span
                        class="nav-workspace crumb"
                        title=move || host::workspace_name()
                    >
                        {move || host::workspace_name()}
                    </span>
                </nav>
            </div>
            <div class="nav-zone nav-center">
                <div class="segmented" role="tablist" aria-label="View" data-nav="primary">
                    {PRIMARY.iter().map(|(id, label)| {
                        let id = *id;
                        let label = *label;
                        view! {
                            <button
                                type="button"
                                data-view=id
                                class=move || if host::view() == id { "seg on" } else { "seg" }
                                on:click=move |_| {
                                    let _ = host::set_view(id);
                                }
                            >
                                {label}
                            </button>
                        }
                    }).collect_view()}
                </div>
            </div>
            <div class="nav-zone nav-right">
                {move || {
                    let status = registry::slots(TopRegion::Status);
                    if status.is_empty() {
                        view! { <SessionChip /> }.into_any()
                    } else {
                        status.into_iter().map(slot_view).collect_view().into_any()
                    }
                }}
                {move || bell_slot()}
                <button
                    type="button"
                    class="nav-menu-btn nav-palette-btn"
                    data-nav="palette"
                    aria-keyshortcuts="Control+K Meta+K"
                    title="Run a command (Ctrl+K)"
                    on:click=move |_| open_palette()
                >
                    "Run a command"
                    <kbd>"Ctrl+K"</kbd>
                </button>
            </div>
                }
                .into_any()
            }}
        </header>
        <div class="shell-alive" data-nav="alive-tray">
            {move || alive_slots().into_iter().map(slot_view).collect_view()}
        </div>
        <crate::confirm::ConfirmDialog />
        <Show when=move || palette_open.get()>
            <div class="palette-backdrop" on:click=move |_| close_palette()>
                <div
                    class=move || if phone.get() { "palette palette-sheet" } else { "palette" }
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
                        role="combobox"
                        aria-autocomplete="list"
                        aria-expanded="true"
                        aria-controls="palette-list"
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
                    <div id="palette-list" class="palette-list" role="listbox" aria-label="Commands">
                        {move || {
                            if !arg_id.get().is_empty() {
                                let rows = choice_rows(&arg_id.get(), &arg_text.get());
                                if rows.is_empty() {
                                    let detail = registry::command(&arg_id.get())
                                        .map(|cmd| cmd.detail.to_string())
                                        .unwrap_or_default();
                                    return view! {
                                        <p class="palette-empty" role="status">"Nothing matches."</p>
                                        <p class="hint">{detail}</p>
                                    }.into_any();
                                }
                                let selected = cursor.get();
                                let mut blocks = Vec::new();
                                for (index, choice) in rows.into_iter().enumerate() {
                                    let on = index == selected;
                                    let class = if on { "palette-row on" } else { "palette-row" };
                                    let option_id = format!("palette-opt-{index}");
                                    let label = choice.label.clone();
                                    let detail = choice.detail.clone();
                                    let picked = arg_id.get_untracked();
                                    let choice_id = choice.id.clone();
                                    let run = run_ready;
                                    blocks.push(view! {
                                        <button
                                            type="button"
                                            id=option_id
                                            class=class
                                            role="option"
                                            aria-selected=on.to_string()
                                            data-choice-id=choice.id
                                            on:click=move |_| run(picked.clone(), choice_id.clone())
                                        >
                                            <span class="palette-copy">
                                                <span class="name">{label}</span>
                                                <span class="path">{detail}</span>
                                            </span>
                                        </button>
                                    }.into_any());
                                }
                                return blocks.into_any();
                            }
                            let groups = registry::palette(&query.get(), &load_usage());
                            if groups.is_empty() {
                                return view! {
                                    <p class="palette-empty" role="status">"No matching commands."</p>
                                    <p class="hint">"Try a page name, a shortcut, or part of a command."</p>
                                }.into_any();
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
                                    let option_id = format!("palette-opt-{index}");
                                    index += 1;
                                    let class = if on { "palette-row on" } else { "palette-row" };
                                    let enabled = hit.enabled;
                                    let activate_id = id.clone();
                                    let run = activate;
                                    blocks.push(view! {
                                        <button
                                            type="button"
                                            id=option_id
                                            class=class
                                            role="option"
                                            aria-selected=on.to_string()
                                            aria-disabled=(!enabled).to_string()
                                            disabled=!enabled
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

/// Phone header: workspace and status, plus a 44px control that opens
/// the palette as a full-height sheet.
fn phone_bar(open_palette: impl Fn() + Copy + 'static, command_mark: &'static str) -> AnyView {
    view! {
        <div class="nav-leading phone-leading">
            <span
                class="crumb current phone-ws"
                data-nav="phone-workspace"
                title=move || host::workspace_name()
            >
                {move || host::workspace_name()}
            </span>
            {move || {
                let found = registry::slots(TopRegion::Status);
                if found.is_empty() {
                    view! { <SessionChip /> }.into_any()
                } else {
                    found.into_iter().map(slot_view).collect_view().into_any()
                }
            }}
        </div>
        <div class="nav-more phone-more">
            {bell_slot()}
            <button
                type="button"
                class="nav-icon phone-more-btn"
                aria-label="Commands"
                aria-haspopup="dialog"
                aria-keyshortcuts="Control+K Meta+K"
                data-nav="phone-more"
                on:click=move |ev: web_sys::MouseEvent| {
                    ev.stop_propagation();
                    open_palette();
                }
            >
                <span class="icon-slot" aria-hidden="true" inner_html=command_mark></span>
            </button>
        </div>
    }
    .into_any()
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
        .map(|cmd| {
            cmd.argument
                .map(|arg| arg.placeholder.to_string())
                .unwrap_or_else(|| "Filter".into())
        })
        .unwrap_or_else(|| "Argument".into())
}

/// The notifications bell's single live mount (#1590 B9/D6): the header
/// right zone on desktop, the phone bar on phone — the bar is always on
/// screen, so waiting approvals stay visible without opening anything,
/// and the component (which owns the banner poll and the
/// `notifications.open` binding) is never mounted twice (#1527/#1536).
fn bell_slot() -> AnyView {
    match registry::slots(TopRegion::Trailing)
        .into_iter()
        .find(|slot| slot.id == "notifications")
    {
        Some(slot) => slot_view(slot),
        None => view! { <span class="nav-slot nav-slot-empty"></span> }.into_any(),
    }
}

/// Popover-owning slots stay mounted (visually clipped) so their panels
/// can still open from palette commands — there is no menu any more
/// (#1590), the palette is the only launcher. Excluded: `view` (the
/// Chat/Daemon switcher; the centre zone, the palette and the phone
/// bottom nav own view switching), `gateway` (the gateway status chip
/// is not rendered in the header any more — gateway health lives in the
/// Daemon view and the Gateways panel), and `notifications` (the bell's
/// one live mount is `bell_slot`, above).
fn alive_slots() -> Vec<super::registry::TopSlot> {
    [TopRegion::Leading, TopRegion::Trailing]
        .into_iter()
        .flat_map(registry::slots)
        .filter(|slot| slot.id != "view" && slot.id != "gateway" && slot.id != "notifications")
        .collect()
}

fn choice_rows(id: &str, query: &str) -> Vec<super::registry::Choice> {
    let Some(cmd) = registry::command(id) else {
        return Vec::new();
    };
    let Some(load) = cmd.choices else {
        return Vec::new();
    };
    registry::filter_choices(query, &load())
}

fn trap_palette_focus(shift: bool) {
    let Some(doc) = web_sys::window().and_then(|window| window.document()) else {
        return;
    };
    let Some(root) = doc
        .query_selector("[data-nav='palette-dialog']")
        .ok()
        .flatten()
    else {
        return;
    };
    let Ok(list) =
        root.query_selector_all("button, input, [href], [tabindex]:not([tabindex='-1'])")
    else {
        return;
    };
    let count = list.length();
    if count == 0 {
        return;
    }
    let active = doc.active_element();
    let mut index = 0i32;
    for i in 0..count {
        let Some(node) = list.item(i) else { continue };
        let same = active
            .as_ref()
            .and_then(|el| el.dyn_ref::<web_sys::Node>())
            .map(|el| el.is_same_node(Some(&node)))
            .unwrap_or(false);
        if same {
            index = i as i32;
            break;
        }
    }
    let next = if shift {
        if index == 0 {
            count - 1
        } else {
            (index as u32) - 1
        }
    } else {
        ((index as u32) + 1) % count
    };
    if let Some(node) = list.item(next) {
        if let Some(el) = node.dyn_ref::<web_sys::HtmlElement>() {
            let _ = el.focus();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::PRIMARY;

    #[test]
    fn primary_views_stay_within_the_cap() {
        assert!(PRIMARY.len() <= 5);
        assert!(PRIMARY.len() >= 1);
        assert!(PRIMARY.iter().any(|(id, _)| *id == "chat"));
        // #1590 A4: Daemon is palette-only on desktop (the phone bottom
        // nav keeps its Daemon tab); it must not return to the centre
        // segmented control.
        assert!(!PRIMARY.iter().any(|(id, _)| *id == "daemon"));
    }
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
