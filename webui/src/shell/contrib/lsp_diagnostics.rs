//! LSP diagnostics (#1404): a palette command that opens the side-panel
//! diagnostics view. The view and its data live in `crate::diagnostics`,
//! rendered by `main.rs` (only the page owns the side tab and the
//! document viewer); the page binds `lsp-diagnostics.open`.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "lsp-diagnostics.open",
        group: Group::Navigate,
        label: "Code diagnostics",
        detail: "Errors and warnings the language servers report, grouped by file",
        shortcut: "",
        keywords: &["lsp", "problems", "errors", "warnings", "diagnostics", "language server"],
        argument: None,
        order: 26,
        run: Rc::new(|args| crate::shell::dispatch("lsp-diagnostics.open", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
