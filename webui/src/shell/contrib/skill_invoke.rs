//! Composer skill invocation (#1594). Management editors arrive with
//! the Customize page. This command is the palette door for the catalog
//! the composer already uses. It does not create or edit skill files.

use std::rc::Rc;

use crate::shell::{Argument, Choice, Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "skill.use",
            Group::Customize,
            "Use a skill…",
            "Insert a $skill into the composer",
            Rc::new(|args| {
                crate::shell::host::set_view("chat")?;
                crate::park::dispatch_or_park("skill.use", args)
            }),
        )
        .keywords(&["skill", "skills", "slash", "extensions"])
        .order(10)
        .argument(Argument {
            placeholder: "Skill",
            required: true,
        })
        .choices(skill_choices),
    );
}

fn skill_choices() -> Vec<Choice> {
    crate::composer_triggers::palette_skills()
        .into_iter()
        .map(|(name, detail)| Choice {
            id: name.clone(),
            label: format!("Use a skill: {name}"),
            detail,
        })
        .collect()
}
