//! Global transcript search (#1355): the palette entry. The overlay lives
//! in `crate::global_search` and is mounted by `main.rs`, which binds
//! `search.global` (it owns opening sessions and the find-in-transcript
//! handoff). An optional argument is the query, so the keyboard path is
//! palette → "Search all sessions" → type → Enter.

use std::rc::Rc;

use crate::shell::{Argument, Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "search-global",
        group: Group::Session,
        label: "Search all sessions\u{2026}",
        detail: "Find text in every session's messages, newest first, and jump to the match",
        shortcut: "",
        keywords: &["search", "find", "global", "transcript", "history", "grep"],
        argument: Some(Argument {
            placeholder: "Text to find (optional)",
            required: false,
        }),
        order: 56,
        run: Rc::new(|args| crate::shell::dispatch("search.global", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
