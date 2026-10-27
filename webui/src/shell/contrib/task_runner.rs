//! Task runner (#1277): a top-bar switch and a palette command. The board
//! itself lives in `crate::task_runner` and is rendered by `main.rs` when
//! the view is `tasks`; this file only registers it with the shell,
//! following docs/webui-shell.md (same pattern as the goals switch).

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Command, ShellHook, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "tasks",
        section: "Apps",
        section_order: 10,
        label: "Task runner",
        detail: "Open a checklist, run each section, review the checkpoint",
        shortcut: "",
        // Not "plan": the cockpit UAT pins the palette query "plan" to
        // Approval: Plan, and an exact keyword match here outranks it.
        keywords: &["tasks", "checklist", "checkpoint"],
        argument: None,
        order: 35,
        run: Rc::new(|_| crate::shell::host::set_view("tasks")),
    });
    reg.slot(TopSlot {
        id: "task-runner",
        region: TopRegion::Trailing,
        order: 87,
        title: "Tasks",
        overflow_label: "Tasks",
        // 0: the view switch stays in the bar at every width, as it did
        // inside the old segmented control — the smoke test clicks it.
        collapse_below_px: 0,
        render: Rc::new(tasks_switch),
    });
}

fn tasks_switch() -> AnyView {
    view! {
        <div class="segmented" role="tablist" aria-label="Tasks">
            <button
                data-view="tasks"
                class=move || if crate::shell::host::view() == "tasks" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = crate::shell::host::set_view("tasks");
                }
            >
                "Tasks"
            </button>
        </div>
    }
    .into_any()
}
