//! Model menu (#1259): a palette command and the Alt+M shortcut listing.
//! The menu itself is a composer control (`crate::model_menu_view`) and
//! stays in `main.rs` with the composer, per docs/webui-shell.md — the
//! composer is not a top-bar slot, so this file registers no slot. The
//! command dispatches to a binding the page installs in `main.rs`,
//! because only the page owns the menu's open signal; the Alt+M key
//! binding itself also stays in `main.rs`, like Alt+G for Goals.

use std::rc::Rc;

use crate::shell::{Argument, Command, Group, ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "switch-model",
        group: Group::Model,
        label: "Switch model…",
        detail: "Set the model for the open session",
        shortcut: "",
        keywords: &["model", "switch", "provider"],
        argument: Some(Argument {
            placeholder: "Filter models",
            required: true,
        }),
        order: 10,
        run: Rc::new(|args| crate::shell::host::pick_model(args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: Some(Rc::new(crate::shell::host::model_choices)),
    });
    reg.command(Command {
        id: "model-menu.open",
        group: Group::Model,
        label: "Model menu",
        detail: "Pick the session model and effort, see the context window",
        shortcut: "Alt+M",
        keywords: &["model", "effort", "context"],
        argument: None,
        order: 50,
        run: Rc::new(|args| crate::shell::dispatch("model-menu.open", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.shortcut(Shortcut {
        id: "model-menu",
        keys: "Alt+M",
        group: "Session",
        group_order: 2,
        label: "Open the model menu",
        order: 1,
    });
}
