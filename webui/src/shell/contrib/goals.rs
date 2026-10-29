//! Goals and Autopilot (#1284): a top-bar switch, a palette command, and
//! the Alt+G shortcut listing. The pane itself lives in `crate::goals` and
//! is rendered by `main.rs` when the view is `goals`; this file only
//! registers it with the shell, following docs/webui-shell.md. The Alt+G
//! key binding itself stays in `main.rs` next to the host bridge, because
//! only the page owns the view signal.

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Group, Command, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "goals",
        group: Group::Navigate,
        label: "Goals",
        detail: "Run a goal until its check passes or a guard stops it",
        shortcut: "Alt+G",
        keywords: &["autopilot", "loop"],
        argument: None,
        order: 30,
        run: Rc::new(|_| crate::shell::host::set_view("goals")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.slot(TopSlot {
        id: "goals",
        region: TopRegion::Trailing,
        order: 85,
        title: "Goals",
        overflow_label: "Goals",
        // 0: the view switch stays in the bar at every width, as it did
        // inside the old segmented control — the smoke test clicks it.
        collapse_below_px: 0,
        render: Rc::new(goals_switch),
    });
    reg.shortcut(Shortcut {
        id: "goals",
        keys: "Alt+G",
        group: "Navigation",
        group_order: 1,
        label: "Open Goals",
        order: 4,
    });
}

fn goals_switch() -> AnyView {
    view! {
        <div class="segmented" role="tablist" aria-label="Goals">
            <button
                data-view="goals"
                class=move || if crate::shell::host::view() == "goals" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = crate::shell::host::set_view("goals");
                }
            >
                "Goals"
            </button>
        </div>
    }
    .into_any()
}
