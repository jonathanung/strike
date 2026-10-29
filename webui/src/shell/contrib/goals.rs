//! Goals and Autopilot (#1284, #1593). Palette-only view. Alt+G stays,
//! bound in `main.rs`. Live progress stays in chat and the status chip.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "goals",
            Group::Navigate,
            "Goals",
            "Run a goal until its check passes or a guard stops it",
            Rc::new(|_| crate::shell::host::set_view("goals")),
        )
        .shortcut("Alt+G")
        .keywords(&["autopilot", "loop"])
        .order(30),
    );
    reg.shortcut(Shortcut {
        id: "goals",
        keys: "Alt+G",
        group: "Navigation",
        group_order: 1,
        label: "Open Goals",
        order: 4,
    });
}
