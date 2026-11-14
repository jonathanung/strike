//! Find in transcript (#1342): a palette command and the Ctrl+F /
//! Cmd+F shortcut listing. The find bar itself lives in
//! `crate::transcript_find` and is rendered by `main.rs` over the
//! transcript, because only the page owns the transcript's lines and
//! the find signals; this file only registers it with the shell, like
//! the model menu. The Ctrl+F key binding itself also stays in
//! `main.rs`, next to the other page-owned chords (Alt+G / Alt+M).

use std::rc::Rc;

use crate::shell::{Group, Command, ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "transcript-find.open",
        group: Group::Session,
        label: "Find in transcript",
        detail: "Search the open session's messages, tool titles and folded tool output, with match count and next/previous",
        shortcut: "Ctrl+F",
        keywords: &["find", "search", "transcript"],
        argument: None,
        order: 55,
        run: Rc::new(|args| crate::shell::dispatch("transcript-find.open", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.shortcut(Shortcut {
        id: "transcript-find",
        keys: "Ctrl+F",
        group: "Session",
        group_order: 2,
        label: "Find in the open session's transcript",
        order: 2,
    });
}
