//! Workflows view (#1501). Palette-only page: catalog, document, and a
//! draft editor with daemon Review / Validate / Save draft (inactive).

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "workflows",
            Group::Navigate,
            "Workflows",
            "Browse workflows, read their phases, review and save drafts",
            Rc::new(|_| crate::shell::host::set_view("workflows")),
        )
        .keywords(&["workflow", "phases", "draft", "authoring"])
        .order(35),
    );
}
