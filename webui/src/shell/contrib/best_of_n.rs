//! Best-of-N (#1881): the "Run with N models…" palette command. The picker
//! and comparison cards live in `crate::best_of_n_card`, mounted in the chat
//! column; the command dispatches to the binding that component installs.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "best-of-n.open",
            Group::Model,
            "Run with N models…",
            "Send one prompt to 2–4 models in parallel, compare, then adopt one or keep none",
            Rc::new(|args| crate::shell::dispatch("best-of-n.open", args)),
        )
        .keywords(&["best", "compare", "models", "parallel", "council", "worktree"])
        .order(30),
    );
}
