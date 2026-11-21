//! Open file… (#1396): a palette command whose second step lists
//! workspace files from the daemon's search as you type, plus the
//! Ctrl/Cmd+P shortcut listing. Rows and the open action live in
//! `crate::quick_open` (only the page owns base, token and the
//! document signals); the Ctrl/Cmd+P key itself is bound in `main.rs`
//! beside the other page-owned chords. Rows are query-driven, so they
//! stay in this command's own step and never displace root commands.

use std::rc::Rc;

use crate::shell::{Argument, Command, Group, ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "open-file",
            Group::Navigate,
            "Open file…",
            "Open a workspace file in the viewer by name",
            Rc::new(crate::quick_open::open),
        )
        .shortcut("Ctrl+P")
        .keywords(&["file", "quick open", "goto", "find file", "path"])
        .order(25)
        .argument(Argument {
            placeholder: "Type part of a file name",
            required: true,
        }),
    );
    reg.query_choices("open-file", crate::quick_open::rows);
    reg.shortcut(Shortcut {
        id: "quick-open",
        keys: "Ctrl+P",
        group: "Navigation",
        group_order: 1,
        label: "Open a workspace file by name",
        order: 10,
    });
}
