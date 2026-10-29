//! Durable storage (#1271): palette commands for the Memory, Knowledge,
//! and Artifacts side-panel tabs and for the session memory mode. The
//! panels themselves live in `crate::storage` and are rendered by
//! `main.rs` when their side tab is active; this file only registers the
//! commands with the shell, following docs/webui-shell.md. The performers
//! are bound by `main.rs` in `App`, because only the page owns the
//! side-tab and memory-mode signals.

use std::rc::Rc;

use crate::shell::{Group, Command, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "panel-memory",
        group: Group::Session,
        label: "Memory",
        detail: "Inspect and edit durable memory",
        shortcut: "",
        keywords: &["storage", "remember"],
        argument: None,
        order: 40,
        run: Rc::new(|args| crate::shell::dispatch("panel-memory", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "panel-knowledge",
        group: Group::Session,
        label: "Knowledge",
        detail: "Pinned paths the agent reads at session start",
        shortcut: "",
        keywords: &["storage", "pins"],
        argument: None,
        order: 80,
        run: Rc::new(|args| crate::shell::dispatch("panel-knowledge", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "panel-artifacts",
        group: Group::Session,
        label: "Artifacts",
        detail: "Versioned findings, patches, and reports",
        shortcut: "",
        keywords: &["storage", "versions"],
        argument: None,
        order: 90,
        run: Rc::new(|args| crate::shell::dispatch("panel-artifacts", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "panel-terminal",
        group: Group::Session,
        label: "Terminal",
        detail: "Open the shell panel",
        shortcut: "",
        keywords: &["shell", "pty", "console"],
        argument: None,
        order: 95,
        run: Rc::new(|args| crate::shell::dispatch("panel-terminal", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "terminal.kill-on-end",
        group: Group::Session,
        label: "Close new shells when the session ends",
        detail: "Toggle whether a new shell dies with the session",
        shortcut: "",
        keywords: &["terminal", "pty", "shell", "preference"],
        argument: None,
        order: 96,
        run: Rc::new(|args| {
            let _ = crate::shell::dispatch("panel-terminal", "");
            crate::park::dispatch_or_park("terminal.kill-on-end", args)
        }),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "memory-normal",
        group: Group::Settings,
        label: "Memory: Normal",
        detail: "This session reads and writes memory",
        shortcut: "",
        keywords: &["storage", "mode"],
        argument: None,
        order: 60,
        run: Rc::new(|args| crate::shell::dispatch("memory-normal", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "memory-incognito",
        group: Group::Settings,
        label: "Memory: Incognito",
        detail: "This session reads memory and learns nothing",
        shortcut: "",
        keywords: &["storage", "mode"],
        argument: None,
        order: 70,
        run: Rc::new(|args| crate::shell::dispatch("memory-incognito", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "memory-temporary",
        group: Group::Settings,
        label: "Memory: Temporary",
        detail: "This session has no memory",
        shortcut: "",
        keywords: &["storage", "mode"],
        argument: None,
        order: 80,
        run: Rc::new(|args| crate::shell::dispatch("memory-temporary", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
