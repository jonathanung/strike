//! Apps (#1283, #1593). Palette-only view. Queue deep links are their
//! own commands so "issues" and "pull requests" land on that filter.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

fn queue(filter: &'static str) -> crate::shell::CommandRun {
    Rc::new(move |_| {
        crate::shell::host::set_view("apps")?;
        crate::park::dispatch_or_park("apps.queue", filter)
    })
}

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "apps",
            Group::Navigate,
            "Apps",
            "Queues such as GitHub triage",
            Rc::new(|_| crate::shell::host::set_view("apps")),
        )
        .keywords(&["github", "triage", "issues"])
        .order(20),
    );
    reg.command(
        Command::new(
            "apps.issues",
            Group::Navigate,
            "Apps: issues queue",
            "Open the issues filter",
            queue("open-issues"),
        )
        .keywords(&["triage", "issues", "queue"])
        .order(21),
    );
    reg.command(
        Command::new(
            "apps.prs",
            Group::Navigate,
            "Apps: pull requests queue",
            "Open the pull requests filter",
            queue("open-prs"),
        )
        .keywords(&["triage", "pull requests", "queue"])
        .order(22),
    );
}
