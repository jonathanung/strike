//! Customize (#1281): a Menu row next to Settings, plus a palette command.
//! The header stays three zones. Trailing slots render in the Menu and the
//! phone sheet, not the bar.

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Command, ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "customize",
        section: "Apps",
        section_order: 10,
        label: "Customize",
        detail: "Skills, agents, MCP servers, plugins, hooks, and rules",
        shortcut: "",
        keywords: &["skills", "agents", "mcp", "hooks", "plugins", "roster"],
        argument: None,
        order: 36,
        run: Rc::new(|_| crate::shell::host::set_view("customize")),
    });
    reg.slot(TopSlot {
        id: "customize",
        region: TopRegion::Trailing,
        order: 96,
        title: "Customize",
        overflow_label: "Customize",
        collapse_below_px: 0,
        render: Rc::new(customize_button),
    });
}

fn customize_button() -> AnyView {
    view! {
        <button
            type="button"
            class="nav-menu-item"
            data-nav="customize"
            data-view="customize"
            aria-label="Customize"
            on:click=move |_| {
                let _ = crate::shell::host::set_view("customize");
            }
        >
            <span class="name">"Customize"</span>
        </button>
    }
    .into_any()
}
