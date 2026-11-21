//! Workflow run controls (#1574): "Start workflow…" lists the daemon's
//! catalog (`GET /v1/workflows`) and "Stop workflow" stops the active
//! one. Both dispatch to performers bound in `main.rs`, which own the
//! live root, the confirms and the `workflow.start` / `workflow.stop`
//! ops. The palette never changes the displayed workflow state.

use std::rc::Rc;

use crate::shell::{Argument, Command, Group, ShellHook};

pub fn install(reg: &mut ShellHook) {
    // The catalog lists only after "Start workflow…" is opened: a root
    // query like "plan" must not surface (and one-Enter start)
    // plan-implement, nor shadow "Approval: Plan".
    crate::shell::choices_in_argument_step_only("workflow-start");
    reg.command(
        Command::new(
            "workflow-start",
            Group::Session,
            "Start workflow…",
            "Start a catalog workflow on the open session (replaces an active one after a confirm)",
            Rc::new(|args| crate::shell::dispatch("workflow-start", args)),
        )
        .keywords(&["workflow", "start", "run", "phase"])
        .order(60)
        .argument(Argument {
            placeholder: "Workflow",
            required: true,
        })
        .choices(crate::workflow_run::choices),
    );
    reg.command(
        Command::new(
            "workflow-stop",
            Group::Session,
            "Stop workflow",
            "Stop the open session's active workflow (asks first)",
            Rc::new(|args| crate::shell::dispatch("workflow-stop", args)),
        )
        .keywords(&["workflow", "stop", "phase", "cancel"])
        .order(61)
        .visible(crate::workflow_run::active),
    );
}
