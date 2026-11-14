//! Customize (#1281): a palette command plus a trailing slot. There is no
//! header Menu since #1590; trailing slots stay mounted (clipped) and the
//! palette is the launcher. The phone sheet still lists the slot.

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Command, Group, ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "customize",
        group: Group::Customize,
        label: "Customize",
        detail: "Skills, agents, MCP servers, plugins, hooks, and rules",
        shortcut: "",
        keywords: &["skills", "agents", "mcp", "hooks", "plugins", "roster"],
        argument: None,
        order: 36,
        run: Rc::new(|_| crate::shell::host::set_view("customize")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
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
