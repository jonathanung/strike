//! Notification centre (#1264): bell in the top bar, a palette command,
//! and the Alt+N shortcut listing. The bell component itself lives in
//! `crate::notify`; this file only registers it with the shell, following
//! docs/webui-shell.md. The component binds `notifications.open` at setup
//! so the palette command can open the centre.

use std::rc::Rc;

use crate::shell::{Command, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "notifications.open",
        section: "Apps",
        section_order: 10,
        label: "Notifications",
        detail: "Approvals, finished work, and the gateway",
        shortcut: "Alt+N",
        keywords: &["bell", "inbox", "alerts"],
        argument: None,
        order: 30,
        run: Rc::new(|args| crate::shell::dispatch("notifications.open", args)),
    });
    reg.slot(TopSlot {
        id: "notifications",
        region: TopRegion::Trailing,
        order: 40,
        title: "Notifications",
        overflow_label: "Notifications",
        // 0: the bell stays in the bar at every width, as it did before the
        // shell move — the centre is also the smoke test's entry point.
        collapse_below_px: 0,
        render: Rc::new(|| crate::notify::bell_view()),
    });
    reg.shortcut(Shortcut {
        id: "notifications",
        keys: "Alt+N",
        group: "Session",
        group_order: 2,
        label: "Open notifications",
        order: 0,
    });
}
