//! Session rewind (#1336): the palette entry. The transcript binds
//! `transcript.rewind` (it owns the cursor message and its turn
//! keep-count); the preview dialog lives in `crate::rewind`.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "transcript-rewind",
        group: Group::Session,
        label: "Rewind to here\u{2026}",
        detail: "Preview, then restore the conversation and/or code to before the cursor turn (or the latest)",
        shortcut: "",
        keywords: &["rewind", "undo", "restore", "checkpoint", "revert"],
        argument: None,
        order: 58,
        run: Rc::new(|args| crate::shell::dispatch("transcript.rewind", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
