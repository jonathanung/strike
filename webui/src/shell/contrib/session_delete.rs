//! Session delete (#1358): one palette command that resolves a session
//! by title or id — the same unique-match the builtin `open-session`
//! uses — and then dispatches the resolved id. The performer bound in
//! `main.rs` only opens the confirm dialog; the palette itself never
//! issues the DELETE, and an ambiguous or unknown argument stays in the
//! palette as an alert with no dialog and no request.

use std::rc::Rc;

use crate::shell::rank::unique_match;
use crate::shell::{Group, Argument, Command, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "session-delete",
        group: Group::Session,
        label: "Delete session\u{2026}",
        detail: "Permanently delete a session and its durable log",
        shortcut: "",
        keywords: &["remove", "erase", "trash"],
        argument: Some(Argument {
            placeholder: "Session title or id",
            required: true,
        }),
        order: 85,
        run: Rc::new(|args| {
            let id = unique_match(&crate::shell::host::sessions()?, args)?;
            crate::shell::dispatch("session-delete", &id)
        }),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
