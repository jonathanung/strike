//! Top bar, command palette, and shortcut overlay.

use std::rc::Rc;

use leptos::html::Input;
use leptos::prelude::*;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use super::host::{self, status_badge_class, status_label};
use super::icon::icon;
use super::registry::{self, TopRegion};
use super::storage::{load_recents, remember_command};

/// Centre-zone pages. Cap is 5. Secondary pages stay in the menu.
const PRIMARY: &[(&str, &str)] = &[("chat", "Chat"), ("daemon", "Daemon")];

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
    let phone_open = RwSignal::new(false);
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
        more_open.set(false);
        phone_open.set(false);
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
        phone_open.set(false);
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
        if !phone.get() {
            phone_open.set(false);
        }
    });

    let on_resize = Closure::<dyn FnMut()>::new(move || {
        phone.set(phone_layout());
    });
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
                phone_open.set(false);
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
        <header class="page-header nav-bar" data-nav="header">
            {move || if phone.get() {
                phone_bar(open_palette, phone_open, more_mark, command_mark)
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
                    let mut status = registry::slots(TopRegion::Status);
                    status.extend(
                        registry::slots(TopRegion::Trailing)
                            .into_iter()
                            .filter(|slot| slot.id == "gateway"),
                    );
                    if status.is_empty() {
                        view! { <SessionChip /> }.into_any()
                    } else {
                        status.into_iter().map(slot_view).collect_view().into_any()
                    }
                }}
                <span class="brand-status nav-version">{move || host::version_label()}</span>
                <button
                    type="button"
                    class="nav-menu-btn"
                    data-nav="more"
                    aria-haspopup="menu"
                    aria-expanded=move || if more_open.get() { "true" } else { "false" }
                    aria-controls="nav-menu"
                    on:click=move |ev: web_sys::MouseEvent| {
                        ev.stop_propagation();
                        more_open.update(|open| *open = !*open);
                    }
                >
                    "Menu"
                </button>
            </div>
                }
                .into_any()
            }}
        </header>
        <button
            type="button"
            class="nav-backdrop"
            data-open=move || if more_open.get() { "1" } else { "0" }
            aria-label="Close menu"
            tabindex="-1"
            on:click=move |_| more_open.set(false)
        ></button>
        <div
            id="nav-menu"
            class="nav-pop"
            role="menu"
            data-nav="more-menu"
            data-open=move || if more_open.get() { "1" } else { "0" }
        >
            <p class="nav-menu-workspace">{move || host::workspace_name()}</p>
            <p class="hint">{move || host::session_name()}</p>
            <button
                type="button"
                class="nav-menu-item"
                data-nav="palette"
                on:click=move |_| open_palette()
            >
                <span class="name">"Run a command"</span>
                <kbd>"Ctrl+K"</kbd>
            </button>
            <button
                type="button"
                class="nav-menu-item"
                data-nav="providers"
                on:click=move |_| {
                    more_open.set(false);
                    let _ = host::set_view("providers");
                }
            >
                <span class="name">"Providers"</span>
            </button>
            <HistoryButton back=true labeled=true />
            <HistoryButton back=false labeled=true />
            <FocusButton labeled=true />
            <button
                type="button"
                class="nav-menu-item"
                on:click=move |_| {
                    more_open.set(false);
                    help_open.set(true);
                }
            >
                <span class="name">"Keyboard shortcuts"</span>
                <kbd>"?"</kbd>
            </button>
            {move || {
                menu_slots()
                    .into_iter()
                    .map(|slot| {
                        // overflow_label is the accessible name when the slot
                        // control is an icon. collapse_below_px no longer
                        // moves slots; the menu holds them at every width.
                        let title = slot.overflow_label.to_string();
                        let body = (slot.render)();
                        view! { <div class="nav-menu-slot" title=title>{body}</div> }
                    })
                    .collect_view()
            }}
        </div>
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

/// Phone header (#1490 slice 2): one compact row — workspace name and
/// session status on the left, a single overflow button on the right.
/// Everything the desktop bar shows inline (Gateways, Phone, layout,
/// view, notifications, the gateway chip) moves into the sheet, next to
/// Run a command, Back/Forward, and Focus mode. The sheet closes on a
/// selection, on Escape, and on a backdrop tap — except clicks inside
/// slots that own a popover (gateway panel, notification centre): those
/// panels render inside the slot subtree and manage their own lifetime,
/// so a click there is not a sheet selection.
fn phone_bar(
    open_palette: impl Fn() + Copy + Send + Sync + 'static,
    phone_open: RwSignal<bool>,
    more_mark: &'static str,
    command_mark: &'static str,
) -> AnyView {
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
            <button
                type="button"
                class="nav-icon phone-more-btn"
                aria-label="More actions"
                aria-haspopup="menu"
                aria-expanded=move || if phone_open.get() { "true" } else { "false" }
                data-nav="phone-more"
                on:click=move |ev: web_sys::MouseEvent| {
                    ev.stop_propagation();
                    phone_open.update(|open| *open = !*open);
                }
            >
                <span class="icon-slot" aria-hidden="true" inner_html=more_mark></span>
            </button>
            <Show when=move || phone_open.get()>
                <div
                    class="phone-menu-backdrop"
                    data-nav="phone-menu-backdrop"
                    on:click=move |_| phone_open.set(false)
                ></div>
                <div
                    class="menu phone-menu"
                    role="menu"
                    data-nav="phone-menu"
                    on:click=move |ev: web_sys::MouseEvent| {
                        if !in_popover_slot(&ev) {
                            phone_open.set(false);
                        }
                    }
                >
                    <button
                        type="button"
                        class="nav-menu-item"
                        data-nav="phone-run-command"
                        on:click=move |_| open_palette()
                    >
                        <span class="icon-slot" aria-hidden="true" inner_html=command_mark></span>
                        <span class="name">"Run a command"</span>
                        <kbd>"Ctrl+K"</kbd>
                    </button>
                    <button
                        type="button"
                        class="nav-menu-item"
                        data-nav="providers"
                        on:click=move |_| {
                            phone_open.set(false);
                            let _ = host::set_view("providers");
                        }
                    >
                        <span class="name">"Providers"</span>
                    </button>
                    <HistoryButton back=true labeled=true />
                    <HistoryButton back=false labeled=true />
                    <FocusButton labeled=true />
                    {move || {
                        phone_slots()
                            .into_iter()
                            .map(|slot| {
                                let label = slot.overflow_label.to_string();
                                let id = slot.id;
                                let body = (slot.render)();
                                view! {
                                    <div class="nav-overflow-slot" data-slot=id>
                                        <span class="path">{label}</span>
                                        {body}
                                    </div>
                                }
                            })
                            .collect_view()
                    }}
                </div>
            </Show>
        </div>
    }
    .into_any()
}

/// Slots that live in the phone overflow sheet: everything the desktop
/// bar renders inline except the session status, which stays in the bar.
fn phone_slots() -> Vec<super::registry::TopSlot> {
    [TopRegion::Leading, TopRegion::Trailing]
        .into_iter()
        .flat_map(registry::slots)
        .collect()
}

/// True when a click inside the phone sheet landed in a slot that owns
/// a popover (the gateway panel, the notification centre). Those panels
/// render inside the slot subtree, so unmounting the sheet would kill
/// the panel the click just opened.
fn in_popover_slot(ev: &web_sys::MouseEvent) -> bool {
    let Some(target) = ev.target() else {
        return false;
    };
    let Ok(el) = target.dyn_into::<web_sys::Element>() else {
        return false;
    };
    el.closest("[data-slot=\"gateway-switcher\"], [data-slot=\"notifications\"]")
        .map(|found| found.is_some())
        .unwrap_or(false)
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

/// Everything that is not the gateway status chip or the Chat/Daemon
/// switch (the centre zone owns that). The menu stays in the DOM so
/// existing `data-view` clicks still find their buttons.
fn menu_slots() -> Vec<super::registry::TopSlot> {
    [TopRegion::Leading, TopRegion::Trailing]
        .into_iter()
        .flat_map(registry::slots)
        .filter(|slot| slot.id != "view" && slot.id != "gateway")
        .collect()
}

#[cfg(test)]
mod tests {
    use super::PRIMARY;

    #[test]
    fn primary_views_stay_within_the_cap() {
        assert!(PRIMARY.len() <= 5);
        assert!(PRIMARY.len() >= 1);
        assert!(PRIMARY.iter().any(|(id, _)| *id == "chat"));
        assert!(PRIMARY.iter().any(|(id, _)| *id == "daemon"));
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
