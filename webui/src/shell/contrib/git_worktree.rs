//! Git status and worktrees (#1270): palette commands for the Git and
//! Worktrees side-panel tabs, plus the Alt+T shortcut listing. The
//! panels themselves live in `crate::git` and are rendered by `main.rs`
//! when their side tab is active; this file only registers the commands
//! with the shell, following docs/webui-shell.md. The performers are
//! bound by `main.rs` in `App`, because only the page owns the side-tab
//! signal. The Alt+T key binding itself also stays in `main.rs`, like
//! Alt+G for Goals. Git has no Alt chord in this port: the PR's
//! original Alt+G now belongs to Goals (#1300) on main, and its
//! original Alt+W for Worktrees now belongs to the gateway switcher
//! (#1308), which landed first — so Worktrees takes the free Alt+T.

use std::rc::Rc;

use crate::shell::{Group, Command, ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "panel-git",
        group: Group::Git,
        label: "Git",
        detail: "Status, diff, commit, and branches",
        shortcut: "",
        keywords: &["branch", "commit", "status"],
        argument: None,
        order: 100,
        run: Rc::new(|args| crate::shell::dispatch("panel-git", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.command(Command {
        id: "panel-worktrees",
        group: Group::Git,
        label: "Worktrees",
        detail: "Session worktrees and child isolation",
        shortcut: "Alt+T",
        keywords: &["git", "isolation"],
        argument: None,
        order: 110,
        run: Rc::new(|args| crate::shell::dispatch("panel-worktrees", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.shortcut(Shortcut {
        id: "panel-worktrees",
        keys: "Alt+T",
        group: "Navigation",
        group_order: 1,
        label: "Open Worktrees",
        order: 7,
    });
}
