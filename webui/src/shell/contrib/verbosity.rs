//! Response style (#1369): the palette command behind the composer's
//! Style chip. The chip (`crate::verbosity::VerbosityChip`) binds the
//! dispatch, because only the page knows the active session.

use std::rc::Rc;

use crate::shell::{Argument, Choice, Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            crate::verbosity::COMMAND,
            Group::Model,
            "Set response style…",
            "How much the agent says in this session (effort is how hard it thinks)",
            Rc::new(|args| crate::shell::dispatch(crate::verbosity::COMMAND, args)),
        )
        .keywords(&["verbosity", "style", "concise", "detailed", "brief", "length"])
        .order(25)
        .argument(Argument {
            placeholder: "Style",
            required: true,
        })
        .choices(|| {
            vec![
                Choice {
                    id: "default".into(),
                    label: "Style: Default".into(),
                    detail: "Send nothing; the model answers at its own length".into(),
                },
                Choice {
                    id: "concise".into(),
                    label: "Style: Concise".into(),
                    detail: "Short answers, sent as a session instruction".into(),
                },
                Choice {
                    id: "detailed".into(),
                    label: "Style: Detailed".into(),
                    detail: "Fuller explanations, sent as a session instruction".into(),
                },
            ]
        }),
    );
}
