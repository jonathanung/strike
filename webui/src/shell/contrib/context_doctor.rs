//! Context doctor (#1537): one palette command that opens the read-only
//! context breakdown for the open session. The performer bound in
//! `main.rs` opens the dialog and sends `inspect.prompt`; the model
//! meter's "Where is my context going?" button dispatches the same id.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "context-doctor",
        group: Group::Session,
        label: "Context doctor",
        detail: "What fills this session's context, layer by layer",
        shortcut: "",
        keywords: &["context", "tokens", "prompt", "layers", "inspect", "window"],
        argument: None,
        order: 88,
        run: Rc::new(|args| crate::shell::dispatch("context-doctor", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
