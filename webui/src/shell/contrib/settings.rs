//! Settings (#1280): a Menu row, a palette command, and the Ctrl/Cmd+,
//! shortcut listing. The page lives in `crate::settings` and is rendered
//! by `main.rs` when the view is `settings`. The header stays three zones.
//! `?` stays the shell shortcut overlay.

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Command, ConfirmSpec, Group, ShellHook, Shortcut, TopRegion, TopSlot};

fn font_command(reg: &mut ShellHook, id: &'static str, label: &'static str, value: &'static str, order: i32) {
    reg.command(
        Command::new(
            id,
            Group::Theme,
            label,
            "Type size for this browser",
            Rc::new(move |_| crate::shell::dispatch("settings.font", value)),
        )
        .keywords(&["font", "text size", "type", "zoom"])
        .order(order),
    );
}

fn density_command(reg: &mut ShellHook, id: &'static str, label: &'static str, value: &'static str, order: i32) {
    reg.command(
        Command::new(
            id,
            Group::Theme,
            label,
            "Density for this browser",
            Rc::new(move |_| crate::shell::dispatch("settings.density", value)),
        )
        .keywords(&["density", "compact", "spacing", "layout"])
        .order(order),
    );
}

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "settings",
        group: Group::Settings,
        label: "Settings",
        detail: "Appearance: theme, scheme, type size, and density",
        shortcut: "Ctrl+,",
        keywords: &["theme", "font", "density", "preferences", "appearance"],
        argument: None,
        order: 10,
        run: Rc::new(|_| crate::shell::host::set_view("settings")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    font_command(reg, "font-sm", "Type size: Small", "sm", 30);
    font_command(reg, "font-md", "Type size: Default", "md", 31);
    font_command(reg, "font-lg", "Type size: Large", "lg", 32);
    density_command(reg, "density-comfortable", "Density: Comfortable", "comfortable", 33);
    density_command(reg, "density-compact", "Density: Compact", "compact", 34);
    reg.command(
        Command::new(
            "settings.files",
            Group::Settings,
            "Open Settings: Data locations",
            "Paths this daemon reads",
            Rc::new(|_| {
                crate::shell::host::set_view("settings")?;
                crate::park::dispatch_or_park("settings.files", "")
            }),
        )
        .keywords(&["files", "paths", "data locations", "config files", "local files"])
        .order(20),
    );
    reg.command(
        Command::new(
            "settings.export-browser",
            Group::Settings,
            "Export browser settings…",
            "Download strike-browser.json without the token",
            Rc::new(|_| crate::shell::dispatch("settings.export-browser", "")),
        )
        .keywords(&["export", "download", "backup", "browser data"])
        .order(21),
    );
    reg.command(
        Command::new(
            "settings.export-daemon",
            Group::Settings,
            "Export daemon settings…",
            "Download the daemon settings dials",
            Rc::new(|_| crate::shell::dispatch("settings.export-daemon", "")),
        )
        .keywords(&["export", "download", "backup", "daemon settings", "config"])
        .order(22),
    );
    reg.command(
        Command::new(
            "settings.clear",
            Group::Settings,
            "Clear browser data…",
            "Remove strike.* keys in this browser",
            Rc::new(|_| crate::shell::dispatch("settings.clear", "")),
        )
        .keywords(&["clear", "reset browser", "delete local data", "sign out"])
        .order(23)
        .confirm(ConfirmSpec {
            title: "Clear browser data?",
            body: "Removes every strike.* key in this browser, including the gateway token. Daemon files stay. The HttpOnly cookie from /?token= stays until the browser drops it.",
            confirm_label: "Clear browser data",
        }),
    );
    reg.command(
        Command::new(
            "settings.daemon-theme",
            Group::Theme,
            "Use daemon theme in this browser",
            "Copy the daemon theme id into this browser",
            Rc::new(|_| {
                crate::shell::host::set_view("settings")?;
                crate::park::dispatch_or_park("settings.use-daemon-theme", "")
            }),
        )
        .keywords(&["daemon theme", "sync theme", "config theme", "adopt"])
        .order(24)
        .visible(crate::settings::daemon_theme_mismatch),
    );
    reg.command(
        Command::new(
            "permissions.rules",
            Group::Settings,
            "Edit permission rules…",
            "Allow and deny rules, and the audit list",
            Rc::new(|_| {
                crate::shell::host::set_view("chat")?;
                crate::park::dispatch_or_park("permissions.rules", "")
            }),
        )
        .keywords(&["rules", "permissions", "allow", "deny", "audit", "approval"])
        .order(25),
    );
    reg.slot(TopSlot {
        id: "settings",
        region: TopRegion::Trailing,
        order: 95,
        title: "Settings",
        overflow_label: "Settings",
        // The three-zone header puts Leading and Trailing slots in the
        // Menu and the phone sheet. This is not a bar control.
        collapse_below_px: 0,
        render: Rc::new(settings_button),
    });
    reg.shortcut(Shortcut {
        id: "settings",
        keys: "Ctrl+,",
        group: "Navigation",
        group_order: 1,
        label: "Open Settings",
        order: 7,
    });
}

fn settings_button() -> AnyView {
    view! {
        <button
            type="button"
            class="nav-menu-item"
            data-action="open-settings"
            aria-label="Settings"
            title="Settings (Ctrl+,)"
            on:click=move |_| {
                let _ = crate::shell::host::set_view("settings");
            }
        >
            <span class="name">"Settings"</span>
            <kbd>"Ctrl+,"</kbd>
        </button>
    }
    .into_any()
}
