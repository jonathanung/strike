//! Scheduler (#1276, #1593): palette commands and the Alt+C shortcut.
//! The page lives in `crate::schedule`. The view is palette-only: there
//! is no Menu slot. Alt+C stays, bound in `main.rs`.

use std::rc::Rc;

use crate::shell::{Argument, Command, Group, ShellHook, Shortcut};

pub fn install(reg: &mut ShellHook) {
    reg.command(
        Command::new(
            "schedule",
            Group::Navigate,
            "Schedule",
            "Cron, interval, one-shot, and heartbeat runs",
            Rc::new(|_| crate::shell::host::set_view("schedule")),
        )
        .shortcut("Alt+C")
        .keywords(&["cron", "clock", "heartbeat", "recurring"])
        .order(60),
    );
    reg.command(
        Command::new(
            "schedule.run",
            Group::Navigate,
            "Run schedule now…",
            "Fire one saved schedule",
            Rc::new(|args| {
                crate::shell::host::set_view("schedule")?;
                crate::park::dispatch_or_park("schedule.run", args)
            }),
        )
        .keywords(&["run", "fire", "cron", "now"])
        .order(61)
        .argument(Argument {
            placeholder: "Schedule",
            required: true,
        })
        .choices(crate::schedule::run_choices),
    );
    reg.command(
        Command::new(
            "schedule.hold",
            Group::Navigate,
            "Pause or resume schedule…",
            "Hold a schedule, or let it fire again",
            Rc::new(|args| {
                crate::shell::host::set_view("schedule")?;
                crate::park::dispatch_or_park("schedule.hold", args)
            }),
        )
        .keywords(&["pause", "resume", "hold"])
        .order(62)
        .argument(Argument {
            placeholder: "Schedule",
            required: true,
        })
        .choices(crate::schedule::hold_choices),
    );
    reg.shortcut(Shortcut {
        id: "schedule",
        keys: "Alt+C",
        group: "Navigation",
        group_order: 1,
        label: "Open Schedule",
        order: 7,
    });
}
