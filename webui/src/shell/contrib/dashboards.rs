//! Dashboards (#1628): palette entry for agent-authored project pages.
//! The page lives in `crate::dashboards`.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "dashboards",
            Group::Navigate,
            "Dashboards",
            "Project pages the agent writes; figures measured by the daemon",
            Rc::new(|_| crate::shell::host::set_view("dashboards")),
        )
        .keywords(&["standup", "roadmap", "kanban", "report", "pages"])
        .order(33),
    );
}
