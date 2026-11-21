//! Search in files (#1406): a palette command whose argument is the text
//! to find. The search box and results live in `crate::search_in_files`,
//! rendered by `main.rs` in the Files pane; the page binds
//! `search-in-files.run` to open that pane, fill the box and search.

use std::rc::Rc;

use crate::shell::{Argument, Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "search-in-files.run",
        group: Group::Navigate,
        label: "Search in files",
        detail: "Find text across the workspace's files; open a match at its line",
        shortcut: "",
        keywords: &["grep", "find", "search", "files", "workspace", "text"],
        argument: Some(Argument {
            placeholder: "Text to find in workspace files",
            required: true,
        }),
        order: 25,
        run: Rc::new(|args| crate::shell::dispatch("search-in-files.run", args)),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
}
