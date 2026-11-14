//! Task runner (#1277, #1593). Palette-only view. Refresh is a palette
//! command; New, Run, Approve, Retry, and Skip stay on the board.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "tasks",
            Group::Navigate,
            "Task runner",
            "Open a checklist, run each section, review the checkpoint",
            Rc::new(|_| crate::shell::host::set_view("tasks")),
        )
        // No "plan" keyword: an exact keyword match outranks the fuzzy
        // label match for "Approval: Plan".
        .keywords(&["tasks", "checklist", "checkpoint"])
        .order(35),
    );
    reg.command(
        Command::new(
            "tasks.refresh",
            Group::Navigate,
            "Refresh task board…",
            "Reload task plans from the daemon",
            Rc::new(|_| {
                crate::shell::host::set_view("tasks")?;
                crate::park::dispatch_or_park("tasks.refresh", "")
            }),
        )
        .keywords(&["refresh", "reload", "tasks"])
        .order(36),
    );
}
