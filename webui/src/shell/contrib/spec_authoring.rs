//! Spec authoring (#1348). Palette-only page: requirements (EARS) →
//! design → tasks with phase gates, handed off to the Task runner.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "specs",
            Group::Navigate,
            "Specs",
            "Author a spec: requirements, design, tasks; hand off to the Task runner",
            Rc::new(|_| crate::shell::host::set_view("specs")),
        )
        // No "plan" keyword: it would outrank "Approval: Plan" on "plan".
        .keywords(&["spec", "requirements", "ears", "design"])
        .order(36),
    );
}
