//! Session delete (#1358): one palette command that resolves a session
//! by title or id — the same unique-match the builtin `open-session`
//! uses — and then dispatches the resolved id. The performer bound in
//! `main.rs` only opens the confirm dialog; the palette itself never
//! issues the DELETE, and an ambiguous or unknown argument stays in the
//! palette as an alert with no dialog and no request.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "session-delete",
        group: Group::Session,
        label: "Delete session…",
        detail: "Permanently delete the cursor session, or the open one",
        shortcut: "",
        keywords: &["delete", "remove", "destroy"],
        argument: None,
        order: 85,
        run: Rc::new(|args| crate::shell::dispatch("session-delete", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
