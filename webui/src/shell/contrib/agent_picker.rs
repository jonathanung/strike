//! Agent picker (#1563): the palette prompt behind the composer's Agent
//! chip. Lists the selectable agents from bootstrap and posts
//! `select.agent` through the page's picker (`crate::agent_picker`).
//!
//! Two commands:
//! - `switch-agent` is the prompt that holds the agent list. It is hidden,
//!   so a typed root query never surfaces agent names as rows. The
//!   built-in `plan` agent otherwise outranked "Set approval mode: Plan"
//!   for the query "plan". The composer chip opens it directly with
//!   `show_palette_at`.
//! - `agent.switch` is the visible launcher ("Switch agent…") that opens
//!   that prompt. It is shown only when the daemon reported agents.

use std::rc::Rc;

use crate::shell::{Argument, Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "switch-agent",
            Group::Model,
            "Switch agent",
            "Set the agent the open session runs",
            Rc::new(|args| crate::agent_picker::pick(args)),
        )
        .order(11)
        .argument(Argument {
            placeholder: "Filter agents",
            required: true,
        })
        .visible(|| false)
        .choices(|| {
            crate::agent_picker::choices(
                &crate::agent_picker::agents(),
                &crate::agent_picker::current(),
            )
        }),
    );
    reg.command(
        Command::new(
            "agent.switch",
            Group::Model,
            "Switch agent…",
            "Set the agent the open session runs",
            Rc::new(|_| {
                // The palette closes after a command runs; reopen on the
                // prompt once that close has happened.
                leptos::task::spawn_local(async {
                    crate::shell::show_palette_at("switch-agent");
                });
                Ok(())
            }),
        )
        .keywords(&["agent", "persona", "switch"])
        .order(11)
        .visible(crate::agent_picker::has_agents),
    );
}
