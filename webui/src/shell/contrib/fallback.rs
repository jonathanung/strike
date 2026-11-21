//! Model fallback (#1350): the palette command behind the composer's
//! Fallback chip. Choices are "No fallback" plus the model catalog the
//! page installed; anything else is refused before it reaches the daemon.
//! The chip (`crate::fallback::FallbackChip`) binds the dispatch, because
//! only the page knows the active session.

use std::rc::Rc;

use crate::shell::{Argument, Choice, Command, Group, ShellHook};

const NONE: &str = "none";

fn fallback_choices() -> Vec<Choice> {
    let mut rows = vec![Choice {
        id: NONE.into(),
        label: "No fallback".into(),
        detail: "A failed turn fails; no other model answers".into(),
    }];
    rows.extend(crate::shell::host::model_choices().into_iter().map(|choice| {
        let name = choice
            .label
            .strip_prefix("Switch model: ")
            .unwrap_or(choice.label.as_str())
            .to_string();
        Choice {
            id: choice.id,
            label: format!("Fallback: {name}"),
            detail: "Answers a turn when the session model is unavailable".into(),
        }
    }));
    rows
}

fn run(args: &str) -> Result<(), String> {
    if args.trim() == NONE {
        return crate::shell::dispatch(crate::fallback::COMMAND, "");
    }
    let catalog: Vec<(String, String)> = crate::shell::host::model_choices()
        .into_iter()
        .filter_map(|choice| {
            let (provider, id) = choice.id.split_once('\t')?;
            Some((provider.to_string(), id.to_string()))
        })
        .collect();
    let (provider, id) = crate::model_menu::resolve_model_pick(args, &catalog)?;
    crate::shell::dispatch(crate::fallback::COMMAND, &format!("{provider}\t{id}"))
}

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            crate::fallback::COMMAND,
            Group::Model,
            "Set fallback model…",
            "A second model that answers a turn when the session model is unavailable",
            Rc::new(run),
        )
        .keywords(&["fallback", "failover", "backup model", "outage", "rate limit"])
        .order(22)
        .argument(Argument {
            placeholder: "Fallback model",
            required: true,
        })
        .choices(fallback_choices),
    );
}
