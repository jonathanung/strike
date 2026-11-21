//! Debug mode (#1345): palette entry that starts a hypothesis-first debug
//! session on the open session. The panel (`crate::debug_mode_ui`) binds
//! the performer because it owns the live session and the op route.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "debug-mode.start",
        group: Group::Debug,
        label: "Start debug session",
        detail: "Hypotheses first: fix edits stay blocked until a hypothesis is confirmed with evidence or you override; ending sweeps tagged instrumentation",
        shortcut: "",
        keywords: &["debug", "hypothesis", "bug", "instrumentation", "reproduce"],
        argument: None,
        order: 10,
        run: Rc::new(|args| crate::shell::dispatch("debug-mode.start", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
