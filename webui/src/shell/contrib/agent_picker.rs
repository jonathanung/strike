//! Agent picker (#1563): the palette command behind the composer's Agent
//! chip. Lists the selectable agents from bootstrap and posts
//! `select.agent` through the page's picker (`crate::agent_picker`).
//! Hidden when the daemon reported no agents.

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
        .keywords(&["agent", "persona", "switch", "build", "plan"])
        .order(11)
        .argument(Argument {
            placeholder: "Filter agents",
            required: true,
        })
        .visible(crate::agent_picker::has_agents)
        .choices(|| {
            crate::agent_picker::choices(
                &crate::agent_picker::agents(),
                &crate::agent_picker::current(),
            )
        }),
    );
}
