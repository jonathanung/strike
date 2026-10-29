//! Scheduler (#1276): a top-bar switch, a palette command, and the
//! Alt+C shortcut listing. The page itself lives in `crate::schedule`
//! and is rendered by `main.rs` when the view is `schedule`; this file
//! only registers it with the shell, following docs/webui-shell.md.
//! The Alt+C key binding itself stays in `main.rs` next to the host
//! bridge, because only the page owns the view signal.
//!
//! Shortcut: Alt+C, not the Alt+S the PR originally shipped — side
//! chat landed first and owns Alt+S on main (see
//! `src/shell/builtin.rs`). Alt+G/M/N/U/A/W are likewise taken (goals,
//! model menu, notifications, usage, audit, gateway switcher), so
//! Schedule takes the free Alt+C (C for clock).

use std::rc::Rc;

use leptos::prelude::*;

use crate::shell::{Group, Command, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "schedule",
        group: Group::Navigate,
        label: "Schedule",
        detail: "Cron, interval, one-shot, and heartbeat runs",
        shortcut: "Alt+C",
        keywords: &["cron", "clock", "heartbeat", "recurring"],
        argument: None,
        order: 60,
        run: Rc::new(|_| crate::shell::host::set_view("schedule")),
        visible: None,
        enabled: None,
        confirm: None,
        choices: None,
    });
    reg.slot(TopSlot {
        id: "schedule",
        region: TopRegion::Trailing,
        order: 87,
        title: "Schedule",
        overflow_label: "Schedule",
        // 0: the view switch stays in the bar at every width, as it did
        // inside the old segmented control — the smoke test clicks it.
        collapse_below_px: 0,
        render: Rc::new(schedule_switch),
    });
    reg.shortcut(Shortcut {
        id: "schedule",
        keys: "Alt+C",
        group: "Navigation",
        group_order: 1,
        label: "Open Schedule",
        order: 7,
    });
}

fn schedule_switch() -> AnyView {
    view! {
        <div class="segmented" role="tablist" aria-label="Schedule">
            <button
                data-view="schedule"
                class=move || if crate::shell::host::view() == "schedule" { "seg on" } else { "seg" }
                on:click=move |_| {
                    let _ = crate::shell::host::set_view("schedule");
                }
            >
                "Schedule"
            </button>
        </div>
    }
    .into_any()
}
