//! Gateway switcher (#1265): a top-bar slot, a palette command, and the
//! Alt+W shortcut listing. The switcher component itself lives in
//! `crate::gateway` and is rendered through its late-bound `GatewayHost`,
//! because only the page owns the base/token signals it drives; this file
//! only registers it with the shell, following docs/webui-shell.md.
//!
//! Shortcut: Alt+W, not the Alt+G the PR originally shipped — Goals
//! (#1284, PR #1300) landed first and owns Alt+G on main. Alt+S/M/N/U/A
//! are likewise taken (side chat, model menu, notifications, usage,
//! audit), so the switcher takes the free Alt+W.
//!
//! Slot id is `gateway-switcher`, deliberately NOT `gateway`: the
//! `gateway` slot is the status aggregate chip owned by #1263
//! (`contrib/status.rs`), and registering the same id would replace it.

use std::rc::Rc;

use crate::shell::{Command, ShellHook, Shortcut, TopRegion, TopSlot};

pub fn install(reg: &mut ShellHook) {
    reg.command(Command {
        id: "gateways.open",
        section: "Apps",
        section_order: 10,
        label: "Gateways",
        detail: "Switch between this daemon and saved gateways",
        shortcut: "Alt+W",
        keywords: &["gateway", "ssh", "connect", "daemon", "remote"],
        argument: None,
        order: 50,
        run: Rc::new(|args| crate::shell::dispatch("gateways.open", args)),
    });
    reg.slot(TopSlot {
        id: "gateway-switcher",
        region: TopRegion::Trailing,
        order: 82,
        title: "Gateways",
        overflow_label: "Gateways",
        // 0: the switcher button stays in the bar at every width, as it
        // did in the pre-shell header — the smoke test clicks it, and the
        // panel itself is fixed-position at phone widths.
        collapse_below_px: 0,
        render: Rc::new(|| crate::gateway::switcher_view()),
    });
    reg.shortcut(Shortcut {
        id: "gateway-switcher",
        keys: "Alt+W",
        group: "Navigation",
        group_order: 1,
        label: "Open the gateway switcher",
        order: 5,
    });
}
