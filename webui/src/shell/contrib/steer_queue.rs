//! Steer / queue (#1268): palette command to interrupt the running turn.
//! The composer, queue list, and split send stay in `main.rs`. This file
//! only registers the command; the page binds `stop-turn`.

use std::rc::Rc;

use crate::shell::{Command, ConfirmSpec, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "queue.clear",
        group: Group::Settings,
        label: "Clear queue",
        detail: "Drop every message waiting in the composer queue",
        shortcut: "",
        keywords: &["queue", "clear", "cancel"],
        argument: None,
        order: 73,
        run: Rc::new(|_| crate::shell::dispatch("queue.clear", "")),
        visible: None,
        enabled: None,
        confirm: Some(ConfirmSpec {
            title: "Clear the queue?",
            body: "Every waiting message is dropped. Nothing already sent is recalled.",
            confirm_label: "Clear queue",
        }),
        choices: None,
    });
    reg.command(Command {
        id: "stop-turn",
        group: Group::Session,
        label: "Stop turn",
        detail: "Interrupt the running turn in this session",
        shortcut: "Esc",
        keywords: &["interrupt", "cancel", "steer"],
        argument: None,
        order: 72,
        run: Rc::new(|_| crate::shell::dispatch("stop-turn", "")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
