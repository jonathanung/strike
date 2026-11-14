//! Commands and slots the shell itself ships. Another stream replaces one
//! by registering the same id from `src/shell/contrib/`.

use std::rc::Rc;

use leptos::prelude::*;

use crate::model::{autonomies, permission_modes};

use super::host;
use super::rank::unique_match;
use super::registry::{
    Argument, Choice, Command, CommandRun, Group, ShellHook, Shortcut, TopRegion, TopSlot,
};

fn run(f: impl Fn(&str) -> Result<(), String> + 'static) -> CommandRun {
    Rc::new(f)
}

fn command(
    id: &'static str,
    group: Group,
    label: &'static str,
    detail: &'static str,
    shortcut: &'static str,
    keywords: &'static [&'static str],
    order: i32,
    argument: Option<Argument>,
    perform: CommandRun,
) -> Command {
    let cmd = Command::new(id, group, label, detail, perform)
        .shortcut(shortcut)
        .keywords(keywords)
        .order(order);
    match argument {
        Some(argument) => cmd.argument(argument),
        None => cmd,
    }
}

fn choices_from(items: Vec<(String, String)>) -> Vec<Choice> {
    items
        .into_iter()
        .map(|(id, label)| Choice {
            id,
            label,
            detail: String::new(),
        })
        .collect()
}

fn resolve_id(items: &[(String, String)], args: &str) -> Result<String, String> {
    if items.iter().any(|(id, _)| id == args) {
        return Ok(args.to_string());
    }
    unique_match(items, args)
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
        id: "palette-slash",
        keys: "/",
        group: "Palette",
        group_order: 0,
        label: "Open the command palette when you are not typing",
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
        id: "side-chat-toggle",
        keys: "Alt+S",
        group: "Navigation",
        group_order: 1,
        label: "Open or close the side chat drawer",
        order: 3,
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
        Group::Session,
        "New session",
        "Open another live root",
        "",
        &["root"],
        10,
        None,
        run(|_| host::new_session()),
    ));
    reg.command(
        command(
            "open-session",
            Group::Session,
            "Open session…",
            "Jump to a live or saved session by title or id",
            "",
            &["switch", "resume", "goto"],
            20,
            Some(Argument {
                placeholder: "Filter sessions",
                required: true,
            }),
            run(|args| {
                let sessions = host::sessions()?;
                let id = resolve_id(&sessions, args)?;
                host::open_session(&id)
            }),
        )
        .choices(|| choices_from(host::sessions().unwrap_or_default())),
    );
    reg.command(
        command(
            "activate-workspace",
            Group::Navigate,
            "Switch workspace…",
            "Record a workspace as active. It does not move the live engine",
            "",
            &["folder", "project", "switch"],
            30,
            Some(Argument {
                placeholder: "Filter workspaces",
                required: true,
            }),
            run(|args| {
                let workspaces = host::workspaces()?;
                let id = resolve_id(&workspaces, args)?;
                host::activate_workspace(&id)
            }),
        )
        .choices(|| choices_from(host::workspaces().unwrap_or_default())),
    );
    reg.command(
        command(
            "back",
            Group::Navigate,
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
        )
        .enabled(|| host::can_back()),
    );
    reg.command(
        command(
            "forward",
            Group::Navigate,
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
        )
        .enabled(|| host::can_forward()),
    );
    reg.command(command(
        "focus",
        Group::Navigate,
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
        Group::Session,
        "Stop all",
        "Interrupt running delegated children in this session",
        "",
        &["interrupt"],
        70,
        None,
        run(|_| host::stop_all()),
    ));
    reg.command(command(
        "side-chat",
        Group::Session,
        "Side chat",
        "Ask about this session without steering it or approving a tool",
        "Alt+S",
        &["sidechat", "ask"],
        75,
        None,
        run(|_| host::open_side_chat()),
    ));
    reg.command(command(
        "shortcuts",
        Group::Debug,
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
        Group::Navigate,
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
        Group::Navigate,
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
        Group::Settings,
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
        Group::Settings,
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
            Group::Settings,
            mode_label(mode.id),
            mode.detail,
            "",
            &[],
            30 + index as i32,
            None,
            run(move |_| host::set_perm(wire)),
        ));
    }
    // Autonomy (#1572) sits beside the approval modes: same Settings
    // group, same direct-run shape. `skip-all` still confirms — that
    // discipline lives in the page's set_autonomy, not the palette row.
    for (index, mode) in autonomies().iter().enumerate() {
        let id = autonomy_command_id(mode.id);
        let wire = mode.id;
        reg.command(command(
            id,
            Group::Settings,
            autonomy_label(mode.id),
            mode.detail,
            "",
            &["autonomy", "exit gate", "phase"],
            40 + index as i32,
            None,
            run(move |_| host::set_autonomy(wire)),
        ));
    }
    for (index, (id, label, detail)) in THEMES.iter().copied().enumerate() {
        let theme = id;
        reg.command(command(
            theme_command_id(id),
            Group::Theme,
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
        Group::Theme,
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
        Group::Theme,
        "Scheme: Light",
        "Light hexes from the TUI token pair",
        "",
        &[],
        21,
        None,
        run(|_| host::set_scheme("light")),
    ));
    reg.command(command(
        "density-compact",
        Group::Theme,
        "Density: Compact",
        "Tighter rows and padding",
        "",
        &["density", "compact"],
        30,
        None,
        run(|_| {
            crate::appearance::set_density("compact");
            Ok(())
        }),
    ));
    reg.command(command(
        "density-comfortable",
        Group::Theme,
        "Density: Comfortable",
        "The default row height",
        "",
        &["density", "comfortable"],
        31,
        None,
        run(|_| {
            crate::appearance::set_density("comfortable");
            Ok(())
        }),
    ));
    reg.command(command(
        "session-rename",
        Group::Session,
        "Rename session…",
        "Set the title of the open session",
        "",
        &["title", "rename"],
        80,
        Some(Argument {
            placeholder: "New title",
            required: true,
        }),
        run(|args| crate::shell::dispatch("session.rename", args)),
    ));
    reg.command(command(
        "session-fork",
        Group::Session,
        "Fork session",
        "Branch the open session into a new root",
        "",
        &["fork", "branch"],
        81,
        None,
        run(|_| crate::shell::dispatch("session.fork", "")),
    ));
    reg.command(command(
        "session-export",
        Group::Session,
        "Export session",
        "Download the redacted timeline JSON",
        "",
        &["export", "timeline", "download"],
        82,
        None,
        run(|_| crate::shell::dispatch("session.export", "")),
    ));
    reg.command(command(
        "session-export-md",
        Group::Session,
        "Export session markdown",
        "Download the visible transcript as markdown",
        "",
        &["export", "markdown"],
        83,
        None,
        run(|_| crate::shell::dispatch("session.export-md", "")),
    ));
    reg.command(command(
        "diagnostics",
        Group::Debug,
        "Diagnostics",
        "Copy version, gateway, and session into the clipboard",
        "",
        &["debug", "version", "copy"],
        10,
        None,
        run(|_| {
            let text = format!(
                "strike {}\ngateway {}\nsession {}",
                host::version_label(),
                host::gateway(),
                host::session_name()
            );
            if let Some(win) = web_sys::window() {
                let _ = win.navigator().clipboard().write_text(&text);
            }
            Ok(())
        }),
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

fn autonomy_command_id(id: &str) -> &'static str {
    match id {
        "supervised" => "autonomy-supervised",
        "agent" => "autonomy-agent",
        "checks" => "autonomy-checks",
        "skip-all" => "autonomy-skip-all",
        _ => "autonomy-supervised",
    }
}

fn autonomy_label(id: &str) -> &'static str {
    match id {
        "supervised" => "Autonomy: Supervised",
        "agent" => "Autonomy: Agent",
        "checks" => "Autonomy: Checks",
        "skip-all" => "Autonomy: Skip-all",
        _ => "Autonomy: Supervised",
    }
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
