//! Conversation layout (#1285): a top-bar layout switch and the two
//! palette commands, following docs/webui-shell.md. The layout state,
//! the drawers, and the composer stay in `crate::chat_mode` / `main.rs`;
//! this file only registers them with the shell. The toggle itself is
//! rendered by `crate::chat_mode::layout_view` through the feature's
//! late-bound `LayoutHost`, because only the page owns the signal.

use std::rc::Rc;

use crate::shell::{Group, Command, ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "layout-cockpit",
        group: Group::Settings,
        label: "Layout: Cockpit",
        detail: "Sessions, transcript, and the side panel",
        shortcut: "",
        keywords: &["layout", "cockpit", "columns"],
        argument: None,
        order: 40,
        run: Rc::new(|_| crate::chat_mode::host_set_layout("cockpit")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "layout-conversation",
        group: Group::Settings,
        label: "Layout: Conversation",
        detail: "One column. Sessions and the side panel open as drawers",
        shortcut: "",
        keywords: &["layout", "conversation", "chat", "column"],
        argument: None,
        order: 41,
        run: Rc::new(|_| crate::chat_mode::host_set_layout("conversation")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.slot(TopSlot {
        id: "layout",
        region: TopRegion::Trailing,
        order: 86,
        title: "Layout",
        overflow_label: "Layout",
        // 0: the layout switch stays in the bar at every width, as it did
        // inside the old header — the smoke test clicks it.
        collapse_below_px: 0,
        render: Rc::new(|| crate::chat_mode::layout_view()),
    });
}
