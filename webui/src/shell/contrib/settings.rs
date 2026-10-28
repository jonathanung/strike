//! Settings (#1280): a Menu row, a palette command, and the Ctrl/Cmd+,
//! shortcut listing. The page lives in `crate::settings` and is rendered
//! by `main.rs` when the view is `settings`. The header stays three zones.
//! `?` stays the shell shortcut overlay.

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Command, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "settings",
        section: "Settings",
        section_order: 20,
        label: "Settings",
        detail: "Theme, type size, shortcuts, and the files this daemon reads",
        shortcut: "Ctrl+,",
        keywords: &["theme", "font", "density", "preferences"],
        argument: None,
        order: 10,
        run: Rc::new(|_| crate::shell::host::set_view("settings")),
    });
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
