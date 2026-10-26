//! Steer / queue (#1268): palette command to interrupt the running turn.
//! The composer, queue list, and split send stay in `main.rs`. This file
//! only registers the command; the page binds `stop-turn`.

use std::rc::Rc;

use crate::shell::{Command, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "stop-turn",
        section: "Commands",
        section_order: 0,
        label: "Stop turn",
        detail: "Interrupt the running turn in this session",
        shortcut: "Esc",
        keywords: &["interrupt", "cancel", "steer"],
        argument: None,
        order: 72,
        run: Rc::new(|_| crate::shell::dispatch("stop-turn", "")),
    });
}
