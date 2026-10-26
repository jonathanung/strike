//! Durable storage (#1271): palette commands for the Memory, Knowledge,
//! and Artifacts side-panel tabs and for the session memory mode. The
//! panels themselves live in `crate::storage` and are rendered by
//! `main.rs` when their side tab is active; this file only registers the
//! commands with the shell, following docs/webui-shell.md. The performers
//! are bound by `main.rs` in `App`, because only the page owns the
//! side-tab and memory-mode signals.

use std::rc::Rc;

use crate::shell::{Command, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "panel-memory",
        section: "Commands",
        section_order: 0,
        label: "Memory",
        detail: "Inspect and edit durable memory",
        shortcut: "",
        keywords: &["storage", "remember"],
        argument: None,
        order: 70,
        run: Rc::new(|args| crate::shell::dispatch("panel-memory", args)),
    });
    reg.command(Command {
        id: "panel-knowledge",
        section: "Commands",
        section_order: 0,
        label: "Knowledge",
        detail: "Pinned paths the agent reads at session start",
        shortcut: "",
        keywords: &["storage", "pins"],
        argument: None,
        order: 80,
        run: Rc::new(|args| crate::shell::dispatch("panel-knowledge", args)),
    });
    reg.command(Command {
        id: "panel-artifacts",
        section: "Commands",
        section_order: 0,
        label: "Artifacts",
        detail: "Versioned findings, patches, and reports",
        shortcut: "",
        keywords: &["storage", "versions"],
        argument: None,
        order: 90,
        run: Rc::new(|args| crate::shell::dispatch("panel-artifacts", args)),
    });
    reg.command(Command {
        id: "memory-normal",
        section: "Settings",
        section_order: 20,
        label: "Memory: Normal",
        detail: "This session reads and writes memory",
        shortcut: "",
        keywords: &["storage", "mode"],
        argument: None,
        order: 60,
        run: Rc::new(|args| crate::shell::dispatch("memory-normal", args)),
    });
    reg.command(Command {
        id: "memory-incognito",
        section: "Settings",
        section_order: 20,
        label: "Memory: Incognito",
        detail: "This session reads memory and learns nothing",
        shortcut: "",
        keywords: &["storage", "mode"],
        argument: None,
        order: 70,
        run: Rc::new(|args| crate::shell::dispatch("memory-incognito", args)),
    });
    reg.command(Command {
        id: "memory-temporary",
        section: "Settings",
        section_order: 20,
        label: "Memory: Temporary",
        detail: "This session has no memory",
        shortcut: "",
        keywords: &["storage", "mode"],
        argument: None,
        order: 80,
        run: Rc::new(|args| crate::shell::dispatch("memory-temporary", args)),
    });
}
