//! Ideas inbox (#1323): palette entry for the page. The page and its
//! open-count badge live in `crate::ideas`.

use std::rc::Rc;

use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "ideas",
            Group::Navigate,
            "Ideas",
            "Suggestions for new work you can accept, snooze or dismiss",
            Rc::new(|_| crate::shell::host::set_view("ideas")),
        )
        .keywords(&["suggestions", "inbox", "feed", "proactive"])
        .order(32),
    );
}
