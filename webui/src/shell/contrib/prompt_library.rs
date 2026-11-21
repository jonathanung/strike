//! Prompt library (#1357): palette entries. The actions are bound by
//! `prompt_library::PromptLibrary` while the cockpit is mounted.

use std::rc::Rc;

use crate::prompt_library::{choices, INSERT_ID, MANAGE_ID, SAVE_ID};
use crate::shell::{Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            SAVE_ID,
            Group::Session,
            "Save draft as prompt",
            "Name the composer draft and keep it in this browser's prompt library",
            Rc::new(|_args| crate::shell::dispatch(SAVE_ID, "")),
        )
        .keywords(&["prompt", "library", "save", "template"])
        .order(65),
    );
    reg.command(
        Command::new(
            INSERT_ID,
            Group::Session,
            "Insert saved prompt…",
            "Pick a saved prompt; {{blanks}} are filled in before it reaches the composer",
            Rc::new(|args| crate::shell::dispatch(INSERT_ID, args)),
        )
        .keywords(&["prompt", "library", "insert", "template", "snippet"])
        .choices(choices)
        .order(66),
    );
    reg.command(
        Command::new(
            MANAGE_ID,
            Group::Session,
            "Prompt library",
            "Search, edit and delete saved prompts",
            Rc::new(|_args| crate::shell::dispatch(MANAGE_ID, "")),
        )
        .keywords(&["prompts", "library", "manage", "templates"])
        .order(67),
    );
}
