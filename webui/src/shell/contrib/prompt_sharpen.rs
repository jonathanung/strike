//! Prompt sharpen (#1385): the palette entry and shortcut row. Alt+P is
//! bound by the composer textarea in `main.rs`; the action itself is bound
//! by `prompt_sharpen::SharpenBar` while the composer is mounted.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            crate::prompt_sharpen::COMMAND_ID,
            Group::Session,
            "Sharpen prompt",
            "Rewrite the composer draft with the session model, then accept or reject",
            Rc::new(|args| crate::shell::dispatch(crate::prompt_sharpen::COMMAND_ID, args)),
        )
        .shortcut("Alt+P")
        .keywords(&["sharpen", "rewrite", "improve", "refine", "prompt"])
        .order(64),
    );
    reg.shortcut(Shortcut {
        id: "prompt-sharpen",
        keys: "Alt+P",
        group: "Session",
        group_order: 2,
        label: "Sharpen the composer draft (preview, then accept or reject)",
        order: 5,
    });
}
