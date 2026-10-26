//! Commands and slots the shell itself ships. Another stream replaces one
//! by registering the same id from `src/shell/contrib/`.

use std::rc::Rc;

use leptos::prelude::*;

use crate::model::permission_modes;

use super::host;
use super::rank::unique_match;
use super::registry::{Argument, Command, CommandRun, ShellHook, Shortcut, TopRegion, TopSlot};

fn run(f: impl Fn(&str) -> Result<(), String> + 'static) -> CommandRun {
    Rc::new(f)
}

fn command(
    id: &'static str,
    section: &'static str,
    section_order: i32,
    label: &'static str,
    detail: &'static str,
    shortcut: &'static str,
    keywords: &'static [&'static str],
    order: i32,
    argument: Option<Argument>,
    perform: CommandRun,
) -> Command {
    Command {
        id,
        section,
        section_order,
        label,
        detail,
        shortcut,
        keywords,
        argument,
        order,
        run: perform,
    }
}

pub fn install(reg: &mut ShellHook) {
    reg.shortcut(Shortcut {
        id: "palette",
        keys: "Ctrl+K",
        group: "Palette",
        group_order: 0,
        label: "Open or close the command palette",
        order: 0,
    });
    reg.shortcut(Shortcut {
        id: "help",
        keys: "?",
        group: "Palette",
        group_order: 0,
        label: "Keyboard shortcuts, when you are not typing",
        order: 1,
    });
    reg.shortcut(Shortcut {
        id: "back",
        keys: "Alt+Left",
        group: "Navigation",
        group_order: 1,
        label: "Back to the previous session",
        order: 0,
    });
    reg.shortcut(Shortcut {
        id: "forward",
        keys: "Alt+Right",
        group: "Navigation",
        group_order: 1,
        label: "Forward to the session you came back from",
        order: 1,
    });
    reg.shortcut(Shortcut {
        id: "focus",
        keys: "Alt+\\",
        group: "Navigation",
        group_order: 1,
        label: "Focus mode hides the session list and side panel",
        order: 2,
    });
    reg.shortcut(Shortcut {
        id: "close",
        keys: "Esc",
        group: "Palette",
        group_order: 0,
        label: "Close the palette, shortcuts, or an open menu",
        order: 2,
    });
    reg.shortcut(Shortcut {
        id: "palette-move",
        keys: "Up / Down",
        group: "Palette",
        group_order: 0,
        label: "Move through palette results",
        order: 3,
    });
    reg.shortcut(Shortcut {
        id: "palette-run",
        keys: "Enter",
        group: "Palette",
        group_order: 0,
        label: "Run the highlighted command",
        order: 4,
    });

    reg.command(command(
        "new",
        "Commands",
        0,
        "New session",
        "Open another live root",
        "",
        &["root"],
        10,
        None,
        run(|_| host::new_session()),
    ));
    reg.command(command(
        "open-session",
        "Commands",
        0,
        "Open session…",
        "Jump to a live or saved session by title or id",
        "",
        &["switch", "resume", "goto"],
        20,
        Some(Argument {
            placeholder: "Session title or id",
            required: true,
        }),
        run(|args| {
            let id = unique_match(&host::sessions()?, args)?;
            host::open_session(&id)
        }),
    ));
    reg.command(command(
        "activate-workspace",
        "Commands",
        0,
        "Activate workspace…",
        "Record a workspace as active. It does not move the live engine",
        "",
        &["folder", "project"],
        30,
        Some(Argument {
            placeholder: "Workspace name",
            required: true,
        }),
        run(|args| {
            let id = unique_match(&host::workspaces()?, args)?;
            host::activate_workspace(&id)
        }),
    ));
    reg.command(command(
        "back",
        "Commands",
        0,
        "Back",
        "Return to the previous session",
        "Alt+Left",
        &[],
        40,
        None,
        run(|_| {
            if !host::can_back() {
                return Err("Nothing to go back to.".into());
            }
            host::travel(true)
        }),
    ));
    reg.command(command(
        "forward",
        "Commands",
        0,
        "Forward",
        "Return to the session you left with Back",
        "Alt+Right",
        &[],
        50,
        None,
        run(|_| {
            if !host::can_forward() {
                return Err("Nothing to go forward to.".into());
            }
            host::travel(false)
        }),
    ));
    reg.command(command(
        "focus",
        "Commands",
        0,
        "Toggle focus mode",
        "Hide the session list and the side panel",
        "Alt+\\",
        &["zen"],
        60,
        None,
        run(|_| host::toggle_focus()),
    ));
    reg.command(command(
        "stop-all",
        "Commands",
        0,
        "Stop all",
        "Interrupt running delegated children in this session",
        "",
        &["interrupt"],
        70,
        None,
        run(|_| host::stop_all()),
    ));
    reg.command(command(
        "shortcuts",
        "Commands",
        0,
        "Keyboard shortcuts",
        "Show the shortcut overlay",
        "?",
        &["help", "keys"],
        80,
        None,
        run(|_| {
            host::open_help();
            Ok(())
        }),
    ));
    reg.command(command(
        "chat",
        "Apps",
        10,
        "Chat",
        "Sessions and the transcript",
        "",
        &[],
        10,
        None,
        run(|_| host::set_view("chat")),
    ));
    reg.command(command(
        "daemon",
        "Apps",
        10,
        "Daemon",
        "This gateway, its token, and workspaces",
        "",
        &["settings"],
        20,
        None,
        run(|_| host::set_view("daemon")),
    ));
    reg.command(command(
        "steer",
        "Settings",
        20,
        "Steer",
        "Enter adds your message to the current turn without stopping it",
        "",
        &[],
        10,
        None,
        run(|_| host::set_enter("steer")),
    ));
    reg.command(command(
        "queue",
        "Settings",
        20,
        "Queue",
        "Enter waits until the turn finishes",
        "",
        &[],
        20,
        None,
        run(|_| host::set_enter("queue")),
    ));
    for (index, mode) in permission_modes().iter().enumerate() {
        let id = mode_command_id(mode.id);
        let wire = mode.id;
        reg.command(command(
            id,
            "Settings",
            20,
            mode_label(mode.id),
            mode.detail,
            "",
            &[],
            30 + index as i32,
            None,
            run(move |_| host::set_perm(wire)),
        ));
    }
    for (index, (id, label, detail)) in THEMES.iter().copied().enumerate() {
        let theme = id;
        reg.command(command(
            theme_command_id(id),
            "Theme",
            30,
            label,
            detail,
            "",
            &[],
            index as i32,
            None,
            run(move |_| host::set_theme(theme)),
        ));
    }
    reg.command(command(
        "scheme-dark",
        "Theme",
        30,
        "Scheme: Dark",
        "Dark hexes from the TUI token pair",
        "",
        &[],
        20,
        None,
        run(|_| host::set_scheme("dark")),
    ));
    reg.command(command(
        "scheme-light",
        "Theme",
        30,
        "Scheme: Light",
        "Light hexes from the TUI token pair",
        "",
        &[],
        21,
        None,
        run(|_| host::set_scheme("light")),
    ));

    reg.slot(TopSlot {
        id: "gateway",
        region: TopRegion::Trailing,
        order: 80,
        title: "Gateway",
        overflow_label: "Gateway",
        collapse_below_px: 720,
        render: Rc::new(gateway_chip),
    });
    reg.slot(TopSlot {
        id: "view",
        region: TopRegion::Trailing,
        order: 90,
        title: "View",
        overflow_label: "View",
        collapse_below_px: 0,
        render: Rc::new(view_switcher),
    });
}

fn mode_command_id(id: &str) -> &'static str {
    match id {
        "default" => "mode-default",
        "plan" => "mode-plan",
        "soft-approve" => "mode-soft",
        "accept-edits" => "mode-edits",
        "yolo" => "mode-yolo",
        _ => "mode-default",
    }
}

fn mode_label(id: &str) -> &'static str {
    match id {
        "default" => "Approval: Default",
        "plan" => "Approval: Plan",
        "soft-approve" => "Approval: Soft",
        "accept-edits" => "Approval: Edits",
        "yolo" => "Approval: Yolo",
        _ => "Approval: Default",
    }
}

fn theme_command_id(id: &str) -> &'static str {
    match id {
        "strike" => "theme-strike",
        "nord" => "theme-nord",
        "dracula" => "theme-dracula",
        "gruvbox" => "theme-gruvbox",
        "monokai" => "theme-monokai",
        "tokyo-night" => "theme-tokyo-night",
        "catppuccin" => "theme-catppuccin",
        _ => "theme-strike",
    }
}

const THEMES: &[(&str, &str, &str)] = &[
    (
        "strike",
        "Theme: Strike",
        "Stock TUI palette. Royal purple, cyan working state",
    ),
    (
        "nord",
        "Theme: Nord",
        "Bundled TUI theme. Missing surfaces stay Strike",
    ),
    (
        "dracula",
        "Theme: Dracula",
        "Bundled TUI theme. Missing surfaces stay Strike",
    ),
    (
        "gruvbox",
        "Theme: Gruvbox",
        "Bundled TUI theme. Missing surfaces stay Strike",
    ),
    (
        "monokai",
        "Theme: Monokai",
        "Bundled TUI theme. Missing surfaces stay Strike",
    ),
    (
        "tokyo-night",
        "Theme: Tokyo Night",
        "Bundled TUI theme. Missing surfaces stay Strike",
    ),
    (
        "catppuccin",
        "Theme: Catppuccin",
        "Bundled TUI theme. Missing surfaces stay Strike",
    ),
];

fn gateway_class(state: &str) -> &'static str {
    match state {
        "connected" => "chip ok",
        "unreachable" => "chip bad",
        _ => "chip",
    }
}

fn gateway_label(state: &str) -> &'static str {
    match state {
        "connected" => "Connected",
        "unreachable" => "Unreachable",
        _ => "Checking",
    }
}

fn gateway_chip() -> AnyView {
    view! {
        <span class=move || gateway_class(&host::gateway()) title="This daemon">
            {move || gateway_label(&host::gateway())}
        </span>
    }
    .into_any()
}

fn view_switcher() -> AnyView {
    view! {
        <div class="segmented" role="tablist" aria-label="View">
            <button
                class=move || if host::view() == "chat" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = host::set_view("chat");
                }
            >
                "Chat"
            </button>
            <button
                class=move || if host::view() == "daemon" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = host::set_view("daemon");
                }
            >
                "Daemon"
            </button>
        </div>
    }
    .into_any()
}
