//! Model menu (#1259): a palette command and the Alt+M shortcut listing.
//! The menu itself is a composer control (`crate::model_menu_view`) and
//! stays in `main.rs` with the composer, per docs/webui-shell.md — the
//! composer is not a top-bar slot, so this file registers no slot. The
//! command dispatches to a binding the page installs in `main.rs`,
//! because only the page owns the menu's open signal; the Alt+M key
//! binding itself also stays in `main.rs`, like Alt+G for Goals.

use std::rc::Rc;

use crate::shell::{Argument, Choice, Command, Group, ShellHook, Shortcut};

fn default_model_choices() -> Vec<Choice> {
    crate::shell::host::model_choices()
        .into_iter()
        .map(|choice| {
            let name = choice
                .label
                .strip_prefix("Switch model: ")
                .unwrap_or(choice.label.as_str())
                .to_string();
            Choice {
                id: choice.id,
                label: format!("Set default model: {name}"),
                detail: choice.detail,
            }
        })
        .collect()
}

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
    reg.command(
        Command::new(
            "model.effort",
            Group::Model,
            "Set reasoning effort…",
            "Set the effort for this session",
            Rc::new(|args| crate::shell::host::set_effort(args)),
        )
        .keywords(&["effort", "reasoning", "thinking", "fast"])
        .order(20)
        .argument(Argument {
            placeholder: "Effort",
            required: true,
        })
        .choices(crate::shell::host::effort_choices),
    );
    reg.command(
        Command::new(
            "model.effort-default",
            Group::Model,
            "Save effort as default",
            "Store the current effort as the global default",
            Rc::new(|_| crate::shell::host::save_effort_default()),
        )
        .keywords(&["effort", "default", "reasoning"])
        .order(21),
    );
    reg.command(
        Command::new(
            "model.default",
            Group::Model,
            "Set default model…",
            "Save a catalog model as the global default",
            Rc::new(|args| crate::shell::host::set_default_model(args)),
        )
        .shortcut("Ctrl+D")
        .keywords(&["default", "model", "save", "global"])
        .order(22)
        .argument(Argument {
            placeholder: "Model",
            required: true,
        })
        .choices(default_model_choices),
    );
    reg.shortcut(Shortcut {
        id: "model-menu",
        keys: "Alt+M",
        group: "Session",
        group_order: 2,
        label: "Open the model menu",
        order: 1,
    });
}
